package schemadigest

import (
	"fmt"
	"sort"
	"strings"
)

// MaxTables bounds the digest. A briefing is one fetch by definition, so it
// cannot page; past this many tables the digest reports itself truncated and the
// caller points at whatever does page. The value is well under the catalog page
// size (200) because the proxy's payload also carries a whole permissions report
// and is meant to be read in full at the start of a session.
const MaxTables = 50

// SourceTable is one relation as read from the catalog, before reduction. It
// carries only the facts the digest can use: a caller that has richer catalog
// rows (the proxy does) drops the rest on the way in, so this type never grows
// a field the digest would have to decide whether to keep.
type SourceTable struct {
	Schema      string
	Name        string
	Comment     string
	ApproxRows  int64
	Columns     []SourceColumn
	PrimaryKey  []string
	ForeignKeys []SourceForeignKey
}

// SourceColumn is one column as read from the catalog.
type SourceColumn struct {
	Name string
	Type string
	// Masked is set by the proxy when the credential's policy masks this column.
	// The standalone generator has no policy and never sets it.
	Masked bool
}

// SourceForeignKey is one outbound foreign-key reference as read from the
// catalog, before it is rendered into the arrow form.
type SourceForeignKey struct {
	Columns    []string
	RefTable   string
	RefColumns []string
}

// Digest is the compact schema summary. The JSON tags are the wire contract of
// the MCP briefing tool's `schema` object and of the standalone generator's
// output, which are the same bytes for the same input.
type Digest struct {
	Tables     []Table `json:"tables"`
	TableCount int     `json:"table_count"`
	// Truncated is true when tables were withheld, either because the caller's
	// catalog page filled or because the digest hit MaxTables.
	Truncated bool `json:"truncated,omitempty"`
	// Unavailable carries why the schema could not be read at all. The proxy sets
	// it on a briefing whose other halves still answered, because the loopback
	// session is where the kill-switch is enforced and an agent that gets an empty
	// table list needs to be told the difference between "empty" and "refused".
	// The standalone generator fails instead of emitting this.
	Unavailable string `json:"unavailable,omitempty"`
}

// Table is one table in the digest.
type Table struct {
	// Table is schema-qualified, matching how a policy allowlist and mask rules
	// name a relation.
	Table      string   `json:"table"`
	ApproxRows int64    `json:"approx_rows"`
	Comment    string   `json:"comment,omitempty"`
	Columns    []Column `json:"columns"`
	PrimaryKey []string `json:"primary_key,omitempty"`
	// ForeignKeys are rendered as "col -> ref_table(ref_col)" rather than as
	// nested objects: this payload is read by a model deciding how to join, and
	// the arrow form says the same thing in a fraction of the tokens.
	ForeignKeys []string `json:"foreign_keys,omitempty"`
}

// Column is a column reduced to what a model needs to write a first query
// against it.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Masked is worth its bytes because validate_sql treats a masked column as
	// nonexistent, so an agent that does not know a column is masked reads that
	// verdict as a typo.
	Masked bool `json:"masked,omitempty"`
}

// From reduces catalog rows to the digest. Pure, so the shape of the summary is
// testable without a database.
//
// truncated says the CALLER already withheld tables (a catalog page that filled,
// a policy filter that emptied one). It is carried through rather than
// recomputed, and From sets it as well when its own cap bites.
//
// It deliberately produces no resume cursor. A caller's cursor advances over the
// whole raw page it read, which is a longer prefix than the one this digest
// keeps, so handing that cursor on would silently skip every table in between.
// Reporting truncation and naming a tool that pages is right; offering a resume
// point the digest cannot honour is not.
func From(tables []SourceTable, truncated bool) Digest {
	sorted := make([]SourceTable, len(tables))
	copy(sorted, tables)
	// Stable schema.table order, so two digests of an unchanged database read
	// identically and the cap always drops the same tail.
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Schema != sorted[j].Schema {
			return sorted[i].Schema < sorted[j].Schema
		}
		return sorted[i].Name < sorted[j].Name
	})

	out := Digest{Truncated: truncated}
	if len(sorted) > MaxTables {
		sorted = sorted[:MaxTables]
		out.Truncated = true
	}

	out.Tables = make([]Table, 0, len(sorted))
	for _, tbl := range sorted {
		out.Tables = append(out.Tables, tableFrom(tbl))
	}
	out.TableCount = len(out.Tables)
	return out
}

// tableFrom reduces one catalog table to its digest form.
func tableFrom(tbl SourceTable) Table {
	out := Table{
		Table:      Qualify(tbl.Schema, tbl.Name),
		ApproxRows: tbl.ApproxRows,
		Comment:    tbl.Comment,
		PrimaryKey: tbl.PrimaryKey,
		Columns:    make([]Column, 0, len(tbl.Columns)),
	}
	for _, col := range tbl.Columns {
		out.Columns = append(out.Columns, Column{
			Name:   col.Name,
			Type:   col.Type,
			Masked: col.Masked,
		})
	}
	for _, fk := range tbl.ForeignKeys {
		out.ForeignKeys = append(out.ForeignKeys, fmt.Sprintf("%s -> %s(%s)",
			strings.Join(fk.Columns, ", "), fk.RefTable, strings.Join(fk.RefColumns, ", ")))
	}
	return out
}

// Qualify renders a schema-qualified relation name. A relation with no schema
// (which the catalog queries never produce, but a hand-built source table can)
// keeps its bare name rather than gaining a leading dot.
func Qualify(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}
