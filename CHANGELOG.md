# schemadigest

## 0.1.0

First release.

`Read` pulls tables, columns, primary keys and foreign keys from a PostgreSQL catalog over a `pgx` connection. `From` reduces those rows to a compact digest meant for a language model's context.

Two catalog bugs are fixed in the queries this ships with, both of which produce a plausible wrong answer rather than an error:

- The index query covered only ordinary and partitioned tables, so a materialized view reported an empty index list, which is indistinguishable from one that has no index.
- An expression index key is a zero in `pg_index.indkey` and has no `pg_attribute` row, so an inner join dropped it. An index on `lower(email)` vanished, and an index on `(tenant_id, lower(email))` came back looking like a one-column index on `tenant_id`.

The claim that motivates the reduction (better SQL accuracy at fewer tokens than a raw dump) is not measured. See the README.
