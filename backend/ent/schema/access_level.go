package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AccessLevel defines a balance-driven user access and PAYG discount tier.
type AccessLevel struct {
	ent.Schema
}

func (AccessLevel) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "access_levels"}}
}

func (AccessLevel) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").MaxLen(100).NotEmpty(),
		field.Int("rank").NonNegative(),
		field.Float("balance_threshold").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0),
		field.Float("payg_discount_multiplier").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Default(1),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (AccessLevel) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("auto_users", User.Type),
		edge.To("manual_users", User.Type),
		edge.To("required_groups", Group.Type),
	}
}

func (AccessLevel) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("rank").Unique(),
		index.Fields("balance_threshold"),
	}
}
