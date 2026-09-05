package schemadigest

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// What the digest keeps and what it drops is the whole claim being tested by
// backend/tools/schemastudy, so these pin the reduction itself rather than any
// caller of it.

func TestFrom_ReducesToWhatAModelNeedsToWriteAQuery(t *testing.T) {
	got := From([]SourceTable{{
		Schema:     "public",
		Name:       "orders",
		Comment:    "customer orders",
		ApproxRows: 4210,
		Columns: []SourceColumn{
			{Name: "id", Type: "bigint"},
			{Name: "user_id", Type: "bigint"},
			{Name: "email", Type: "text", Masked: true},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []SourceForeignKey{
			{Columns: []string{"user_id"}, RefTable: "public.users", RefColumns: []string{"id"}},
		},
	}}, false)

	require.Len(t, got.Tables, 1)
	tbl := got.Tables[0]
	assert.Equal(t, "public.orders", tbl.Table, "the digest names a relation the way an allowlist does")
	assert.Equal(t, int64(4210), tbl.ApproxRows)
	assert.Equal(t, "customer orders", tbl.Comment)
	assert.Equal(t, []string{"id"}, tbl.PrimaryKey)
	assert.Equal(t, []string{"user_id -> public.users(id)"}, tbl.ForeignKeys,
		"the arrow form says the same thing as a nested object in a fraction of the tokens")
	assert.Equal(t, []Column{
		{Name: "id", Type: "bigint"},
		{Name: "user_id", Type: "bigint"},
		{Name: "email", Type: "text", Masked: true},
	}, tbl.Columns)
	assert.Equal(t, 1, got.TableCount)
	assert.False(t, got.Truncated)
}

func TestFrom_MultiColumnForeignKey(t *testing.T) {
	got := From([]SourceTable{{
		Schema: "public", Name: "line_items",
		ForeignKeys: []SourceForeignKey{{
			Columns:    []string{"tenant_id", "order_id"},
			RefTable:   "public.orders",
			RefColumns: []string{"tenant_id", "id"},
		}},
	}}, false)

	require.Len(t, got.Tables, 1)
	assert.Equal(t, []string{"tenant_id, order_id -> public.orders(tenant_id, id)"}, got.Tables[0].ForeignKeys)
}

func TestFrom_CapsTablesAndSaysSo(t *testing.T) {
	src := make([]SourceTable, 0, MaxTables+10)
	for i := 1; i <= MaxTables+10; i++ {
		src = append(src, SourceTable{Schema: "public", Name: fmt.Sprintf("t%04d", i)})
	}

	got := From(src, false)

	assert.Len(t, got.Tables, MaxTables)
	assert.Equal(t, MaxTables, got.TableCount, "table_count counts what was returned, not what exists")
	assert.True(t, got.Truncated, "a capped list that does not say it was capped reads as the whole schema")
}

// A caller that already withheld tables keeps saying so even when the digest's
// own cap did not bite. Losing that would tell the reader a partial database is
// the whole one.
func TestFrom_CarriesTheCallersTruncation(t *testing.T) {
	got := From([]SourceTable{}, true)

	assert.Empty(t, got.Tables)
	assert.Equal(t, 0, got.TableCount)
	assert.True(t, got.Truncated)
}

func TestFrom_OrdersTablesStably(t *testing.T) {
	got := From([]SourceTable{
		{Schema: "public", Name: "users"},
		{Schema: "analytics", Name: "events"},
		{Schema: "public", Name: "orders"},
	}, false)

	assert.Equal(t,
		[]string{"analytics.events", "public.orders", "public.users"},
		[]string{got.Tables[0].Table, got.Tables[1].Table, got.Tables[2].Table})
}

// The digest is a payload a model is charged for, so the fields it drops have to
// stay out of the serialized form, not merely out of the struct.
func TestFrom_SerializesWithoutTheDroppedFields(t *testing.T) {
	got := From([]SourceTable{{
		Schema: "public", Name: "t",
		Columns: []SourceColumn{{Name: "c", Type: "text"}},
	}}, false)

	payload, err := json.Marshal(got)
	require.NoError(t, err)
	for _, dropped := range []string{"nullable", "default", "indexes", "cursor"} {
		assert.NotContains(t, string(payload), dropped,
			"%q belongs to the full catalog, not to the digest", dropped)
	}
}

func TestQualify(t *testing.T) {
	assert.Equal(t, "public.users", Qualify("public", "users"))
	assert.Equal(t, "users", Qualify("", "users"), "an unqualified relation must not gain a leading dot")
}
