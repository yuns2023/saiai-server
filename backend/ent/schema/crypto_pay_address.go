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

// CryptoPayAddress is a user's fixed deposit address on one network. Rows are
// never reassigned or deleted.
type CryptoPayAddress struct {
	ent.Schema
}

func (CryptoPayAddress) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "crypto_pay_addresses"}}
}

func (CryptoPayAddress) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		field.String("network").MaxLen(32),
		field.String("address").MaxLen(64),
		field.Int64("keyset_id"),
		field.Int("keyset_version"),
		field.Int64("derivation_index"),
		field.String("derivation_path").MaxLen(100),
		field.Int64("start_block").Optional().Nillable(),
		field.Time("last_order_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (CryptoPayAddress) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "network").Unique(),
		index.Fields("network", "address").Unique(),
		index.Fields("keyset_id", "derivation_index").Unique(),
		index.Fields("network", "last_order_at"),
	}
}
