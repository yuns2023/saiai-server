package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// CryptoPayKeyset is one imported watch-only key source: an EVM account xpub
// or an offline-signed Solana address pool. The table is written with raw SQL
// by the crypto pay service (migration 127 is authoritative).
type CryptoPayKeyset struct {
	ent.Schema
}

func (CryptoPayKeyset) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "crypto_pay_keysets"}}
}

func (CryptoPayKeyset) Fields() []ent.Field {
	return []ent.Field{
		field.String("network").MaxLen(32),
		field.String("family").MaxLen(16),
		field.Int("version"),
		field.String("xpub").SchemaType(map[string]string{dialect.Postgres: "text"}).Default("").Sensitive(),
		field.String("path_template").MaxLen(100),
		field.String("fingerprint").MaxLen(64),
		field.String("signer_pubkey").MaxLen(64).Default(""),
		field.Int64("next_index").Default(0),
		field.Int("pool_size").Default(0),
		field.Bool("active").Default(true),
		field.String("created_by").MaxLen(100).Default(""),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (CryptoPayKeyset) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("network", "version").Unique(),
		index.Fields("network", "fingerprint").Unique(),
	}
}
