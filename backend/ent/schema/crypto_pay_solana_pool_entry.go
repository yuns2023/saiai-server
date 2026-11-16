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

// CryptoPaySolanaPoolEntry is one pre-generated Solana deposit owner address.
type CryptoPaySolanaPoolEntry struct {
	ent.Schema
}

func (CryptoPaySolanaPoolEntry) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "crypto_pay_solana_pool_entries"}}
}

func (CryptoPaySolanaPoolEntry) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("keyset_id"),
		field.String("network").MaxLen(32),
		field.Int64("derivation_index"),
		field.String("address").MaxLen(64),
		field.Int64("assigned_user_id").Optional().Nillable(),
		field.Time("assigned_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (CryptoPaySolanaPoolEntry) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("keyset_id", "derivation_index").Unique(),
		index.Fields("network", "address").Unique(),
	}
}
