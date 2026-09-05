# schemadigest

[![Go Reference](https://pkg.go.dev/badge/github.com/sferarc/schemadigest.svg)](https://pkg.go.dev/github.com/sferarc/schemadigest)

Read a PostgreSQL catalog and reduce it to a compact summary you can put in a language model's context.

```bash
go get github.com/sferarc/schemadigest
```

## The problem

Something has to tell the model what the database looks like. The obvious answers are a `pg_dump --schema-only`, which spends most of its tokens on syntax the model does not need, or a dump of `information_schema.columns`, which has 44 columns and no idea which of them matter.

This package is the third answer: read the catalog, keep the shape and the joins, drop the rest.

```go
conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
if err != nil {
    return err
}
defer conn.Close(ctx)

tables, truncated, err := schemadigest.Read(ctx, conn)
if err != nil {
    return err
}

digest := schemadigest.From(tables, truncated)
out, err := json.Marshal(digest)
```

```json
{
  "tables": [
    {
      "table": "public.orders",
      "approx_rows": 48219,
      "comment": "one row per placed order",
      "columns": [
        { "name": "id", "type": "bigint" },
        { "name": "customer_id", "type": "bigint" },
        { "name": "total_cents", "type": "integer" }
      ],
      "primary_key": ["id"],
      "foreign_keys": ["customer_id -> public.customers(id)"]
    }
  ],
  "table_count": 1
}
```

## Two halves, usable separately

`Read` is a catalog reader. Four queries over a `pgx` connection for tables, columns, primary keys and foreign keys. Nothing opinionated about it, and the joins that are easy to get wrong are already right. Two of them were wrong here first:

- The index query read only `relkind` `'r'` and `'p'`, so every materialized view came back with its columns and comment intact and an empty index list, which is indistinguishable from a materialized view that genuinely has no index. A heavily used one usually does have one, because `REFRESH MATERIALIZED VIEW CONCURRENTLY` requires it.
- An index key can be an expression rather than a column, and PostgreSQL records that as a zero in `pg_index.indkey`. There is no `pg_attribute` row with `attnum` 0, so an inner join on that column deleted the row: an index on `lower(email)` disappeared, and an index on `(tenant_id, lower(email))` came back as a one-column index on `tenant_id`, which looks exactly like a genuine one-column index.

If you only want the catalog reader, `Read` and the four `*Query` functions are the whole of it.

`From` is the opinionated half. Per table it keeps the qualified name, an approximate row count (from `pg_class.reltuples`, so no sequential scan), the comment, the primary key, foreign keys rendered as `col -> ref_table(ref_col)`, and per column only name, type and a masked flag. Nullability, defaults, column comments and indexes are dropped.

The arrow form for foreign keys rather than nested objects is deliberate: the reader is a model deciding how to join, and the arrow says the same thing in a fraction of the tokens.

## What is measured, and what is not

The reduction exists because a compact representation is believed to be better context than a raw dump: more accurate SQL, at fewer tokens.

**That belief is not measured, and this package does not claim it.** The harness that would settle it is written and has not been run.

What is measured, on two fixtures, is the size of each representation:

| schema | digest | `information_schema` (9 columns) | DDL    |
| ------ | ------ | -------------------------------- | ------ |
| shop   | 4359 B | 4464 B                           | 6775 B |
| saas   | 4098 B | 4048 B                           | 5963 B |

The digest is **larger** than a charitable `information_schema` dump on one of the two. Bytes are not tokens and JSON tokenizes differently from pipe-delimited text, so the token counts may not look like this. But "at fewer tokens" is not currently a safe thing to say, and this README is not going to say it.

Treat the reduction as one opinionated representation among several. If you measure it against yours, the result is worth more than this paragraph.

## It filters nothing

`Read` describes every relation the connected role can see. There is no allowlist, no denylist and no masking, on purpose: that is enforcement, and enforcement belongs where you can be sure it runs, not in a formatting library.

`SourceColumn` carries a `Masked` flag this package never sets, for callers that do their own filtering and want the model told that a column exists but is unreadable. That matters more than it looks: a tool that treats a masked column as nonexistent makes a model read its own correct query as a typo.

Point `Read` at a role that is allowed to see what you are willing to describe.

## One wart

`encoding/json` escapes HTML by default, so the `>` in every foreign-key arrow serializes as `\u003e`. That is what `json.Marshal` produces and what the size numbers above measure. If you want the character, encode through a `json.Encoder` with `SetEscapeHTML(false)`.

## Where this comes from

Built for [PgBeam](https://pgbeam.com) and used in production there, where it produces the schema half of the briefing an agent receives at the start of a session.

## Contributing

Issues and pull requests are welcome here.

```bash
go test ./...
```

The catalog queries are the part most likely to be wrong for a schema shape nobody here has run it against. If `Read` describes your database incorrectly, an issue carrying the relevant `pg_class`, `pg_index` or `pg_constraint` rows is worth more than a patch, because the fix usually belongs in the query rather than in the caller.

## License

Apache-2.0. See [LICENSE](LICENSE).
