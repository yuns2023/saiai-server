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

// PaymentProviderInstance is an administrator-managed payment merchant.
// ConfigEncrypted is always AES-GCM ciphertext produced by SecretEncryptor.
type PaymentProviderInstance struct {
	ent.Schema
}

func (PaymentProviderInstance) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "payment_provider_instances"}}
}

func (PaymentProviderInstance) Fields() []ent.Field {
	return []ent.Field{
		field.String("provider_key").MaxLen(30).NotEmpty(),
		field.String("name").MaxLen(100).NotEmpty(),
		field.String("config_encrypted").SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("supported_types").MaxLen(200).Default(""),
		field.Float("balance_credit_rate").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default(1),
		field.Bool("enabled").Default(false),
		field.Int("sort_order").Default(0),
		field.String("limits").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (PaymentProviderInstance) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("provider_key"),
		index.Fields("enabled", "sort_order"),
	}
}
