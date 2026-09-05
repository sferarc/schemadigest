package schemadigest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
)

// Reduce catalog rows to the digest. The rows normally come from [Read], but
// [From] is a pure function, so a caller that already has the catalog (or is
// writing a test) can hand it whatever it has.
func ExampleFrom() {
	tables := []SourceTable{{
		Schema:     "public",
		Name:       "orders",
		Comment:    "one row per placed order",
		ApproxRows: 48219,
		PrimaryKey: []string{"id"},
		Columns: []SourceColumn{
			{Name: "id", Type: "bigint"},
			{Name: "customer_id", Type: "bigint"},
			{Name: "email", Type: "text", Masked: true},
			{Name: "total_cents", Type: "integer"},
		},
		ForeignKeys: []SourceForeignKey{{
			Columns:    []string{"customer_id"},
			RefTable:   "public.customers",
			RefColumns: []string{"id"},
		}},
	}}

	digest := From(tables, false)

	out, err := json.MarshalIndent(digest, "", "  ")
	if err != nil {
		panic(err)
	}
	fmt.Println(string(out))
	// Output:
	// {
	//   "tables": [
	//     {
	//       "table": "public.orders",
	//       "approx_rows": 48219,
	//       "comment": "one row per placed order",
	//       "columns": [
	//         {
	//           "name": "id",
	//           "type": "bigint"
	//         },
	//         {
	//           "name": "customer_id",
	//           "type": "bigint"
	//         },
	//         {
	//           "name": "email",
	//           "type": "text",
	//           "masked": true
	//         },
	//         {
	//           "name": "total_cents",
	//           "type": "integer"
	//         }
	//       ],
	//       "primary_key": [
	//         "id"
	//       ],
	//       "foreign_keys": [
	//         "customer_id -\u003e public.customers(id)"
	//       ]
	//     }
	//   ],
	//   "table_count": 1
	// }
}

// The foreign-key arrow comes back as the escape sequence \u003e under
// json.Marshal, because encoding/json escapes HTML by default. That is what
// PgBeam ships, so it is the default here too. A caller that wants the
// character encodes through a json.Encoder instead.
func ExampleDigest_escaping() {
	digest := From([]SourceTable{{
		Schema: "public",
		Name:   "orders",
		ForeignKeys: []SourceForeignKey{{
			Columns:    []string{"customer_id"},
			RefTable:   "public.customers",
			RefColumns: []string{"id"},
		}},
	}}, false)

	escaped, err := json.Marshal(digest.Tables[0].ForeignKeys)
	if err != nil {
		panic(err)
	}
	fmt.Printf("json.Marshal:  %s\n", escaped)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(digest.Tables[0].ForeignKeys); err != nil {
		panic(err)
	}
	fmt.Printf("json.Encoder:  %s", buf.String())

	// Output:
	// json.Marshal:  ["customer_id -\u003e public.customers(id)"]
	// json.Encoder:  ["customer_id -> public.customers(id)"]
}

// Read the catalog over a plain connection and reduce it in one pass. This is
// the whole library from a consumer's point of view.
//
// The connection wants a role that is allowed to see what you are willing to
// describe: Read filters nothing.
func ExampleRead() {
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		panic(err)
	}
	defer conn.Close(ctx)

	tables, truncated, err := Read(ctx, conn)
	if err != nil {
		panic(err)
	}

	digest := From(tables, truncated)
	if digest.Truncated {
		fmt.Printf("describing the first %d relations\n", digest.TableCount)
	}

	out, err := json.Marshal(digest)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(out))
}
