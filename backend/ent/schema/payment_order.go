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

// PaymentOrder stores the authoritative state of a native SAIAI payment.
// Provider credentials are never stored in plaintext; the per-order snapshot
// is encrypted so callbacks remain verifiable after provider configuration changes.
type PaymentOrder struct {
	ent.Schema
}

func (PaymentOrder) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "payment_orders"}}
}

func (PaymentOrder) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		field.String("user_email").MaxLen(255),
		field.String("user_name").MaxLen(100).Default(""),
		field.Float("amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}),
		field.Float("pay_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}),
		field.String("currency").MaxLen(3).Default("CNY"),
		field.Float("balance_credit_rate").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default(1),
		field.Float("fee_rate").SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).Default(0),
		field.String("recharge_code").MaxLen(32).Unique(),
		field.String("order_type").MaxLen(20).Default("balance"),
		field.Int64("plan_id").Optional().Nillable(),
		field.Int64("subscription_group_id").Optional().Nillable(),
		field.Int("subscription_days").Optional().Nillable(),
		field.String("out_trade_no").MaxLen(64).Unique(),
		field.String("payment_type").MaxLen(30),
		field.String("provider_key").MaxLen(30),
		field.Int64("provider_instance_id"),
		field.String("provider_snapshot_encrypted").SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("status").MaxLen(30).Default("PENDING"),
		field.String("payment_trade_no").MaxLen(128).Default(""),
		field.String("pay_url").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("qr_code").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Time("expires_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("paid_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("completed_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("failed_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("failed_reason").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("refund_mode").MaxLen(20).Default(""),
		field.Float("refund_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}).Default(0),
		field.String("refund_reason").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("refund_external_reference").Optional().Nillable().MaxLen(200),
		field.String("refund_requested_by").MaxLen(100).Default(""),
		field.Time("refund_requested_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("refund_provider_call_started_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("refunded_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("refund_id").MaxLen(200).Default(""),
		field.Bool("refund_entitlement_reversed").Default(false),
		field.String("refund_entitlement_snapshot").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.Bool("refund_force").Default(false),
		field.String("refund_error").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("client_ip").MaxLen(64).Default(""),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (PaymentOrder) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id"),
		index.Fields("order_type"),
		index.Fields("plan_id"),
		index.Fields("status"),
		index.Fields("expires_at"),
		index.Fields("created_at"),
		index.Fields("provider_instance_id", "status"),
	}
}
