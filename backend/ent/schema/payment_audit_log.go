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

// PaymentAuditLog is an append-only record of payment state transitions.
type PaymentAuditLog struct {
	ent.Schema
}

func (PaymentAuditLog) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "payment_audit_logs"}}
}

func (PaymentAuditLog) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("order_id"),
		field.String("action").MaxLen(50),
		field.String("detail").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.String("operator").MaxLen(100).Default("system"),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (PaymentAuditLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_id", "created_at"),
		index.Fields("action"),
	}
}
