package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// CryptoPayScanCursor is a scan watermark. Updates are compare-and-swap on version.
type CryptoPayScanCursor struct {
	ent.Schema
}

func (CryptoPayScanCursor) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "crypto_pay_scan_cursors"}}
}

func (CryptoPayScanCursor) Fields() []ent.Field {
	return []ent.Field{
		field.String("scope_key").MaxLen(200).Unique(),
		field.String("network").MaxLen(32),
		field.String("position").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.Int64("version").Default(0),
		field.Time("last_scanned_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("last_attempt_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("last_error").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}
