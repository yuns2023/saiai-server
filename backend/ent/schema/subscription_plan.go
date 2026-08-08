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

// SubscriptionPlan is an administrator-managed payment product. Payment
// orders persist the selected group, duration, and price so later plan edits
// cannot change an existing order's fulfillment contract.
type SubscriptionPlan struct {
	ent.Schema
}

func (SubscriptionPlan) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "subscription_plans"}}
}

func (SubscriptionPlan) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("group_id"),
		field.String("name").MaxLen(100).NotEmpty(),
		field.String("description").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.Float("price").SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}),
		field.Float("original_price").SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}).Optional().Nillable(),
		field.String("currency").MaxLen(3).Default("CNY"),
		field.Int("validity_days").Default(30),
		field.String("validity_unit").MaxLen(10).Default("day"),
		field.String("features").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.String("product_name").MaxLen(100).Default(""),
		field.Bool("for_sale").Default(true),
		field.Int("sort_order").Default(0),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (SubscriptionPlan) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("group_id"),
		index.Fields("for_sale", "sort_order"),
	}
}
