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

// CryptoPayCredit is the balance ledger: exactly one row per credited deposit.
type CryptoPayCredit struct {
	ent.Schema
}

func (CryptoPayCredit) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "crypto_pay_credits"}}
}

func (CryptoPayCredit) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("deposit_id").Unique(),
		field.Int64("user_id"),
		field.Other("amount", decimal.Decimal{}).SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)"}),
		field.Other("balance_after", decimal.NullDecimal{}).Optional().SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)"}),
		field.String("operator").MaxLen(100).Default("system"),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (CryptoPayCredit) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "created_at"),
	}
}
