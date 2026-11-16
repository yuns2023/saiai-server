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

// CryptoPayAuditLog records crypto pay actions in the same transaction as the action.
type CryptoPayAuditLog struct {
	ent.Schema
}

func (CryptoPayAuditLog) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "crypto_pay_audit_logs"}}
}

func (CryptoPayAuditLog) Fields() []ent.Field {
	return []ent.Field{
		field.String("action").MaxLen(50),
		field.Int64("deposit_id").Optional().Nillable(),
		field.Int64("user_id").Optional().Nillable(),
		field.Int64("keyset_id").Optional().Nillable(),
		field.String("operator").MaxLen(100).Default("system"),
		field.String("detail").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (CryptoPayAuditLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("deposit_id", "created_at"),
		index.Fields("action", "created_at"),
	}
}
