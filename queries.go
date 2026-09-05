package schemadigest

// The catalog SQL. It lives here rather than in the MCP package because two
// callers run it: the proxy's schema_catalog / briefing tools, and the
// standalone digest generator. One copy means a fix to a relkind filter or a
// join reaches both.

// Relkinds is the pg_class.relkind set the catalog treats as a relation worth
// listing: ordinary table, partitioned table, view, materialized view, foreign
// table. The tables query and the columns query both use it, so a relation that
// is listed always gets its columns.
const Relkinds = `'r','p','v','m','f'`

// IndexBearingRelkinds is the subset of Relkinds that can carry an index in
// PostgreSQL. A view and a foreign table cannot. A materialized view can, and a
// heavily used one usually does, because REFRESH MATERIALIZED VIEW CONCURRENTLY
// requires a unique index on it.
//
// This is a subset of Relkinds rather than its own list on purpose: reporting an
// index for a relation the catalog never listed would hang it off nothing, and
// leaving an index-bearing relkind out hides indexes that exist. The second is
// what happened. The index query read 'r' and 'p' only, so every materialized
// view came back with its columns, its comment and its approximate row count
// intact and an empty index list, which reads exactly like a materialized view
// that genuinely has no index. The agent then plans a sequential scan over a
// relation that has the index it needed, and schema_catalog is the tool it is
// told to prefer over spelunking information_schema, so nothing else in the
// session corrects the picture.
const IndexBearingRelkinds = `'r','p','m'`

// TablesQuery returns user tables/views visible to the session, ordered for
// stable keyset pagination.
//
// It takes four bind values, in order: a boolean saying this is the first page
// (which short-circuits the keyset guard), the cursor schema, the cursor name,
// and the row limit. Cursor values are bound, never interpolated (SEC-09), and
// must never contain a NUL byte: the proxy parse-guard rejects it and it is
// invalid against UTF-8 PostgreSQL.
//
// The keyset is applied as a row-tuple comparison, so paging is immune to
// concurrent DDL shifting OFFSETs. System and TOAST schemas are excluded.
// Approximate row counts come from pg_class.reltuples (a planner estimate; no
// sequential scan). Callers that page pass a limit one past the page size so
// truncation is detectable without a second round trip.
func TablesQuery() string {
	return `SELECT n.nspname AS schema,
       c.relname AS name,
       obj_description(c.oid, 'pg_class') AS comment,
       GREATEST(c.reltuples, 0)::bigint AS approx_rows
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN (` + Relkinds + `)
  AND n.nspname NOT IN ('pg_catalog','information_schema')
  AND n.nspname NOT LIKE 'pg_toast%'
  AND ($1::boolean OR (n.nspname, c.relname) > ($2, $3))
ORDER BY n.nspname, c.relname
LIMIT $4`
}

// ColumnsQuery returns every column of every user table in one pass, joined to
// its comment. Ordered by schema, table, ordinal so the caller can group in a
// single forward scan.
func ColumnsQuery() string {
	return `SELECT n.nspname AS schema,
       c.relname AS table_name,
       a.attname AS name,
       format_type(a.atttypid, a.atttypmod) AS type,
       NOT a.attnotnull AS nullable,
       COALESCE(pg_get_expr(d.adbin, d.adrelid), '') AS default_expr,
       COALESCE(col_description(c.oid, a.attnum), '') AS comment
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE c.relkind IN (` + Relkinds + `)
  AND a.attnum > 0
  AND NOT a.attisdropped
  AND n.nspname NOT IN ('pg_catalog','information_schema')
  AND n.nspname NOT LIKE 'pg_toast%'
ORDER BY n.nspname, c.relname, a.attnum`
}

// IndexesQuery returns indexes (with column lists, uniqueness and primary-key
// flag) for every index-bearing relation the catalog lists, in one pass. Which
// relkinds those are is IndexBearingRelkinds; the materialized view is the one
// that used to be missing from it.
//
// An index key can be an expression rather than a column, and PostgreSQL records
// that as a zero in `pg_index.indkey`. There is no `pg_attribute` row with
// attnum 0, so joining the two on that column INNER deleted the row: an index on
// `lower(email)` disappeared from the catalog entirely, and an index on
// `(tenant_id, lower(email))` came back as a one-column index on `tenant_id`,
// which is indistinguishable from a genuinely one-column index on the same
// table. Both directions cost the agent the same way the catalog costs it
// everywhere else: it plans a query against an index shape the database does not
// have, or it does not plan against an index the database does have.
//
// The join is LEFT so the key position survives, and the name falls back to
// `pg_get_indexdef` for that ordinal, which renders the expression text
// ("lower(email)"). A plain column still takes `attname`, so its spelling is
// unchanged: `pg_get_indexdef` would quote a mixed-case column ("MixedCase")
// where every other catalog column here is the bare identifier.
func IndexesQuery() string {
	return `SELECT n.nspname AS schema,
       c.relname AS table_name,
       ic.relname AS index_name,
       i.indisunique AS is_unique,
       i.indisprimary AS is_primary,
       COALESCE(a.attname, pg_get_indexdef(i.indexrelid, k.ord::int, true)) AS column_name,
       k.ord AS column_ord
FROM pg_index i
JOIN pg_class c ON c.oid = i.indrelid
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
LEFT JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
WHERE c.relkind IN (` + IndexBearingRelkinds + `)
  AND n.nspname NOT IN ('pg_catalog','information_schema')
  AND n.nspname NOT LIKE 'pg_toast%'
ORDER BY n.nspname, c.relname, ic.relname, k.ord`
}

// ForeignKeysQuery returns outbound foreign keys for all user tables in one
// pass, with local and referenced column lists in key order.
func ForeignKeysQuery() string {
	return `SELECT n.nspname AS schema,
       c.relname AS table_name,
       con.conname AS constraint_name,
       rn.nspname || '.' || rc.relname AS ref_table,
       col.attname AS column_name,
       refcol.attname AS ref_column,
       k.ord AS column_ord
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_class rc ON rc.oid = con.confrelid
JOIN pg_namespace rn ON rn.oid = rc.relnamespace
JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord) ON true
JOIN LATERAL unnest(con.confkey) WITH ORDINALITY AS rk(attnum, ord) ON rk.ord = k.ord
JOIN pg_attribute col ON col.attrelid = con.conrelid AND col.attnum = k.attnum
JOIN pg_attribute refcol ON refcol.attrelid = con.confrelid AND refcol.attnum = rk.attnum
WHERE con.contype = 'f'
  AND n.nspname NOT IN ('pg_catalog','information_schema')
  AND n.nspname NOT LIKE 'pg_toast%'
ORDER BY n.nspname, c.relname, con.conname, k.ord`
}
