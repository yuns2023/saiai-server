package schema

import (
	"fmt"

	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

func validateLoginOAuthProvider(value string) error {
	switch value {
	case "github", "google":
		return nil
	default:
		return fmt.Errorf("unsupported login oauth provider %q", value)
	}
}

// AuthIdentity binds one verified external OAuth subject to one local user.
// Provider access and refresh tokens are deliberately not persisted.
type AuthIdentity struct {
	ent.Schema
}

func (AuthIdentity) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "auth_identities"}}
}

func (AuthIdentity) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}}
}

func (AuthIdentity) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		field.String("provider").MaxLen(20).NotEmpty().Validate(validateLoginOAuthProvider),
		field.String("subject").MaxLen(255).NotEmpty(),
		field.String("verified_email").MaxLen(255).NotEmpty(),
		field.Time("verified_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (AuthIdentity) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("auth_identities").
			Field("user_id").
			Required().
			Unique().
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (AuthIdentity) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("provider", "subject").Unique(),
		index.Fields("user_id", "provider").Unique(),
		index.Fields("user_id"),
	}
}
