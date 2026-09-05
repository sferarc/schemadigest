// Package schemadigest reads a PostgreSQL catalog and reduces it to a compact
// summary meant to be handed to a language model.
//
// It is two things that can be used separately.
//
// [Read] is a catalog reader. It pulls tables, columns, primary keys and
// foreign keys over a plain pgx connection in four queries, and returns them as
// [SourceTable] values. Nothing about it is opinionated: it is the catalog SQL,
// with the joins that are easy to get wrong already gotten right (see
// [IndexesQuery] for the two that were).
//
// [From] is the opinionated half. It reduces those rows to a [Digest]: per
// table the qualified name, an approximate row count, the comment, the primary
// key, foreign keys rendered as "col -> ref_table(ref_col)", and per column
// only name, type and a masked flag. Nullability, defaults, column comments and
// indexes are dropped, on the theory that a model writing its first query
// against a schema needs the shape and the joins and can ask for the rest.
//
// # What is measured and what is not
//
// The reduction exists because a compact representation is believed to be a
// better thing to put in a model's context than a raw information_schema dump
// or a DDL dump: more accurate SQL, at fewer tokens.
//
// That belief is not yet measured, and this package does not claim it. The
// harness that would settle it is written and has not been run. What is
// measured, on two fixtures, is the size of each representation, and it does
// not support the "fewer tokens" half cleanly: the digest is 4359 bytes against
// 4464 for a charitable nine-column information_schema dump on one schema, and
// 4098 against 4048 on the other, where the digest is larger. Bytes are not
// tokens and JSON tokenizes differently from pipe-delimited text, so the token
// counts may not look like the byte counts. Until somebody runs it, treat the
// reduction as one opinionated representation among several rather than as an
// established improvement.
//
// # No policy, no filtering
//
// [Read] describes every relation the connected role can see. It enforces
// nothing. A caller that needs to withhold relations or mask columns has to do
// that itself, before or after the reduction, which is why [SourceColumn]
// carries a Masked flag this package never sets.
//
// PgBeam, which this package was written for, does exactly that: its proxy
// interleaves allowlist, denylist, honeytoken and mask decisions with its own
// catalog scan and then calls [From] on the result, so the two paths differ in
// what they are allowed to see and in nothing else.
//
// # Stability
//
// The JSON tags on [Digest], [Table] and [Column] are a wire contract. They are
// what a consumer's stored output and a model's learned expectations both key
// off, so they change only with a major version.
//
// One wart worth knowing before you serialize: encoding/json escapes HTML by
// default, so the ">" in every foreign-key arrow ships as the six-character
// sequence \u003e rather than as the one character it renders. That is what
// json.Marshal produces, and PgBeam ships it, so it is also what the size
// numbers above measure. A caller that wants the character can encode through a
// json.Encoder with SetEscapeHTML(false).
package schemadigest
