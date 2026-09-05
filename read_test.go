package schemadigest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRows serves canned rows through the pgx.Rows interface, so the assembly is
// testable without a database.
type fakeRows struct {
	rows [][]any
	i    int
	err  error
}

func (r *fakeRows) Close()                                       {}
func (r *fakeRows) Err() error                                   { return r.err }
func (r *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeRows) RawValues() [][]byte                          { return nil }
func (r *fakeRows) Conn() *pgx.Conn                              { return nil }
func (r *fakeRows) Next() bool                                   { r.i++; return r.i <= len(r.rows) }
func (r *fakeRows) Values() ([]any, error)                       { return r.rows[r.i-1], nil }

func (r *fakeRows) Scan(dest ...any) error {
	src := r.rows[r.i-1]
	if len(src) != len(dest) {
		return fmt.Errorf("scan: %d values into %d destinations", len(src), len(dest))
	}
	for i, d := range dest {
		switch p := d.(type) {
		case *string:
			*p = src[i].(string)
		case **string:
			if src[i] == nil {
				*p = nil
				continue
			}
			s := src[i].(string)
			*p = &s
		case *int64:
			*p = src[i].(int64)
		case *int:
			*p = src[i].(int)
		case *bool:
			*p = src[i].(bool)
		default:
			return fmt.Errorf("scan: unsupported destination %T", d)
		}
	}
	return nil
}

type fakeDB struct {
	tables  [][]any
	columns [][]any
	indexes [][]any
	fks     [][]any
	failOn  string
}

func (f *fakeDB) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	if f.failOn != "" && strings.Contains(sql, f.failOn) {
		return nil, errors.New("boom")
	}
	switch {
	case strings.Contains(sql, "GREATEST(c.reltuples, 0)"):
		return &fakeRows{rows: f.tables}, nil
	case strings.Contains(sql, "format_type(a.atttypid"):
		return &fakeRows{rows: f.columns}, nil
	case strings.Contains(sql, "pg_get_indexdef"):
		return &fakeRows{rows: f.indexes}, nil
	case strings.Contains(sql, "con.contype = 'f'"):
		return &fakeRows{rows: f.fks}, nil
	}
	return &fakeRows{}, nil
}

func fixture() *fakeDB {
	return &fakeDB{
		tables: [][]any{
			{"public", "orders", "customer orders", int64(4210)},
			{"public", "users", nil, int64(97)},
		},
		columns: [][]any{
			{"public", "orders", "id", "bigint", false, "nextval('s')", "the id"},
			{"public", "orders", "user_id", "bigint", true, "", ""},
			{"public", "users", "id", "bigint", false, "", ""},
			// A column on a relation the tables query did not return has nothing to
			// hang off and must be dropped rather than creating a table.
			{"public", "ghost", "id", "bigint", false, "", ""},
		},
		indexes: [][]any{
			{"public", "orders", "orders_pkey", true, true, "id", 1},
			{"public", "orders", "orders_user_idx", false, false, "user_id", 1},
			{"public", "users", "users_pkey", true, true, "id", 1},
		},
		fks: [][]any{
			{"public", "orders", "orders_user_fk", "public.users", "user_id", "id", 1},
		},
	}
}

func TestRead_AssemblesTheCatalogFactsTheDigestKeeps(t *testing.T) {
	got, truncated, err := Read(context.Background(), fixture())
	require.NoError(t, err)
	assert.False(t, truncated)
	require.Len(t, got, 2)

	orders := got[0]
	assert.Equal(t, "orders", orders.Name)
	assert.Equal(t, "customer orders", orders.Comment)
	assert.Equal(t, int64(4210), orders.ApproxRows)
	assert.Equal(t, []string{"id"}, orders.PrimaryKey, "the primary key comes off the index query")
	assert.Equal(t, []SourceColumn{{Name: "id", Type: "bigint"}, {Name: "user_id", Type: "bigint"}}, orders.Columns)
	require.Len(t, orders.ForeignKeys, 1)
	assert.Equal(t, "public.users", orders.ForeignKeys[0].RefTable)

	assert.Empty(t, got[1].Comment, "a NULL comment reads as no comment, not as a crash")
	assert.Empty(t, got[1].ForeignKeys)
}

// A read with no credential is unfiltered by definition, so nothing here may
// invent a relation the tables query did not return.
func TestRead_DropsAspectRowsForUnlistedRelations(t *testing.T) {
	got, _, err := Read(context.Background(), fixture())
	require.NoError(t, err)
	for _, tbl := range got {
		assert.NotEqual(t, "ghost", tbl.Name)
	}
}

func TestRead_ReportsTruncationPastTheReadLimit(t *testing.T) {
	db := &fakeDB{}
	for i := 0; i <= ReadLimit; i++ {
		db.tables = append(db.tables, []any{"public", fmt.Sprintf("t%05d", i), nil, int64(0)})
	}

	got, truncated, err := Read(context.Background(), db)
	require.NoError(t, err)
	assert.Len(t, got, ReadLimit, "the row past the limit only signals truncation")
	assert.True(t, truncated)
}

func TestRead_EmptyDatabaseIsNotAnError(t *testing.T) {
	got, truncated, err := Read(context.Background(), &fakeDB{})
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.False(t, truncated)
}

// Each per-aspect query is a separate round trip, and a failure in any of them
// has to surface. A digest silently missing its foreign keys is the worst
// possible outcome for a tool whose job is to tell a model how to join.
func TestRead_SurfacesAQueryFailure(t *testing.T) {
	for name, fragment := range map[string]string{
		"tables":       "GREATEST(c.reltuples, 0)",
		"columns":      "format_type(a.atttypid",
		"indexes":      "pg_get_indexdef",
		"foreign keys": "con.contype = 'f'",
	} {
		t.Run(name, func(t *testing.T) {
			db := fixture()
			db.failOn = fragment
			_, _, err := Read(context.Background(), db)
			require.Error(t, err)
		})
	}
}
