package schemadigest

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// ReadLimit bounds how many relations Read will pull in one pass. The digest
// itself keeps at most MaxTables, so this only decides how much of a very wide
// database is considered before the sort picks the first MaxTables of it. A
// database past this many user relations is reported truncated.
const ReadLimit = 2000

// Querier is the one method of *pgx.Conn the catalog read uses. It is an
// interface rather than the concrete connection so the assembly can be tested
// without a database.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Read pulls the catalog facts the digest needs over a plain connection, with no
// policy filtering of any kind: every user relation the connected role can see
// is returned.
//
// This is the standalone path. The proxy does NOT call it: its own read has to
// interleave the allowlist, denylist, honeytoken and mask decisions with the
// scan, which is an enforcement concern that has no business in a shared
// package. The two paths therefore assemble separately and reduce identically,
// and TestStandaloneReadMatchesTheBriefingDigest in the mcp package runs both
// over the same rows and asserts the digests are byte-identical, so the
// assembly cannot drift apart in silence.
//
// The bool reports that relations were withheld because ReadLimit filled.
func Read(ctx context.Context, q Querier) ([]SourceTable, bool, error) {
	tables, order, truncated, err := readTables(ctx, q)
	if err != nil {
		return nil, false, err
	}
	if len(order) == 0 {
		return []SourceTable{}, truncated, nil
	}
	if err := readColumns(ctx, q, tables); err != nil {
		return nil, false, err
	}
	if err := readPrimaryKeys(ctx, q, tables); err != nil {
		return nil, false, err
	}
	if err := readForeignKeys(ctx, q, tables); err != nil {
		return nil, false, err
	}

	out := make([]SourceTable, 0, len(order))
	for _, k := range order {
		out = append(out, *tables[k])
	}
	return out, truncated, nil
}

// relKey identifies a relation across the per-aspect queries.
type relKey struct{ schema, name string }

func readTables(ctx context.Context, q Querier) (map[relKey]*SourceTable, []relKey, bool, error) {
	rows, err := q.Query(ctx, TablesQuery(), true, "", "", ReadLimit+1)
	if err != nil {
		return nil, nil, false, err
	}
	defer rows.Close()

	tables := map[relKey]*SourceTable{}
	order := []relKey{}
	count := 0
	for rows.Next() {
		var schema, name string
		var comment *string
		var approx int64
		if err := rows.Scan(&schema, &name, &comment, &approx); err != nil {
			return nil, nil, false, err
		}
		count++
		// The query asks for one row past the limit; that row only signals
		// truncation and is not itself returned.
		if count > ReadLimit {
			break
		}
		k := relKey{schema, name}
		st := &SourceTable{Schema: schema, Name: name, ApproxRows: approx, Columns: []SourceColumn{}}
		if comment != nil {
			st.Comment = *comment
		}
		tables[k] = st
		order = append(order, k)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, err
	}
	return tables, order, count > ReadLimit, nil
}

func readColumns(ctx context.Context, q Querier, tables map[relKey]*SourceTable) error {
	rows, err := q.Query(ctx, ColumnsQuery())
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var schema, table, name, typ, def, comment string
		var nullable bool
		if err := rows.Scan(&schema, &table, &name, &typ, &nullable, &def, &comment); err != nil {
			return err
		}
		// Nullability, the default expression and the column comment are read
		// because the shared query reads them, and dropped here because the digest
		// does not carry them. Reading a narrower query instead would be a second
		// column query to keep in step with the first.
		st := tables[relKey{schema, table}]
		if st == nil {
			continue
		}
		st.Columns = append(st.Columns, SourceColumn{Name: name, Type: typ})
	}
	return rows.Err()
}

// readPrimaryKeys takes the primary key off the index query, which is where
// PostgreSQL keeps it, so the digest's primary_key and schema_catalog's agree by
// construction rather than by two queries happening to match.
func readPrimaryKeys(ctx context.Context, q Querier, tables map[relKey]*SourceTable) error {
	rows, err := q.Query(ctx, IndexesQuery())
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var schema, table, index, column string
		var unique, primary bool
		var ord int
		if err := rows.Scan(&schema, &table, &index, &unique, &primary, &column, &ord); err != nil {
			return err
		}
		if !primary {
			continue
		}
		st := tables[relKey{schema, table}]
		if st == nil {
			continue
		}
		st.PrimaryKey = append(st.PrimaryKey, column)
	}
	return rows.Err()
}

func readForeignKeys(ctx context.Context, q Querier, tables map[relKey]*SourceTable) error {
	rows, err := q.Query(ctx, ForeignKeysQuery())
	if err != nil {
		return err
	}
	defer rows.Close()

	type conKey struct{ schema, table, constraint string }
	order := map[relKey][]conKey{}
	acc := map[conKey]*SourceForeignKey{}
	for rows.Next() {
		var schema, table, constraint, refTable, column, refColumn string
		var ord int
		if err := rows.Scan(&schema, &table, &constraint, &refTable, &column, &refColumn, &ord); err != nil {
			return err
		}
		rk := relKey{schema, table}
		if tables[rk] == nil {
			continue
		}
		ck := conKey{schema, table, constraint}
		fk := acc[ck]
		if fk == nil {
			fk = &SourceForeignKey{RefTable: refTable}
			acc[ck] = fk
			order[rk] = append(order[rk], ck)
		}
		fk.Columns = append(fk.Columns, column)
		fk.RefColumns = append(fk.RefColumns, refColumn)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for rk, keys := range order {
		for _, ck := range keys {
			tables[rk].ForeignKeys = append(tables[rk].ForeignKeys, *acc[ck])
		}
	}
	return nil
}
