package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/shopspring/decimal"
)

// CryptoPayOrder is a suggested amount with a countdown. It never owns funds:
// inflows belong to the user through the fixed deposit address.
type CryptoPayOrder struct {
	ent.Schema
}

func (CryptoPayOrder) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "crypto_pay_orders"}}
}

func (CryptoPayOrder) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		field.Int64("address_id"),
		field.String("network").MaxLen(32),
		field.String("asset").MaxLen(16),
		field.Other("amount", decimal.Decimal{}).SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)", dialect.SQLite: "numeric"}),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("expires_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (CryptoPayOrder) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "created_at"),
	}
}
