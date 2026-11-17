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

// RedeemCodePurchase links a user who bought a balance redeem code with their
// balance to that code (migration 132). Rows are never deleted: a revoked
// purchase keeps status=revoked and revoked_at.
type RedeemCodePurchase struct {
	ent.Schema
}

func (RedeemCodePurchase) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "redeem_code_purchases"}}
}

func (RedeemCodePurchase) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("buyer_user_id"),
		field.Int64("redeem_code_id").Unique(),
		field.Other("amount", decimal.Decimal{}).SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)", dialect.SQLite: "numeric"}),
		field.String("request_id").MaxLen(64).NotEmpty(),
		// active | revoked
		field.String("status").MaxLen(16),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("revoked_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (RedeemCodePurchase) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("buyer_user_id", "request_id").Unique(),
		index.Fields("buyer_user_id", "created_at"),
	}
}
