package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// OAuthRegistrationSession is a one-time, server-side registration handoff.
// It never contains a target user ID and therefore cannot authorize account binding.
type OAuthRegistrationSession struct {
	ent.Schema
}

func (OAuthRegistrationSession) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "oauth_registration_sessions"}}
}

func (OAuthRegistrationSession) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}}
}

func (OAuthRegistrationSession) Fields() []ent.Field {
	return []ent.Field{
		field.String("token_hash").MaxLen(64).NotEmpty().Unique(),
		field.String("provider").MaxLen(20).NotEmpty().Validate(validateLoginOAuthProvider),
		field.String("subject").MaxLen(255).NotEmpty(),
		field.String("verified_email").MaxLen(255).NotEmpty(),
		field.String("username").MaxLen(100).Default(""),
		field.Time("expires_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (OAuthRegistrationSession) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("expires_at"),
		index.Fields("provider", "subject"),
	}
}
