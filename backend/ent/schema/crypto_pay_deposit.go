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

// CryptoPayDeposit is one on-chain inflow to a deposit address, keyed by
// (network, tx_hash, event_key). Status: observed|finalized|credited|held|orphaned.
type CryptoPayDeposit struct {
	ent.Schema
}

func (CryptoPayDeposit) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "crypto_pay_deposits"}}
}

func (CryptoPayDeposit) Fields() []ent.Field {
	return []ent.Field{
		field.String("network").MaxLen(32),
		field.String("tx_hash").MaxLen(128),
		field.String("event_key").MaxLen(128),
		field.Int64("address_id"),
		field.Int64("user_id"),
		field.String("address").MaxLen(64),
		field.String("asset_symbol").MaxLen(16),
		field.String("asset_contract").MaxLen(64),
		field.String("token_account").MaxLen(64).Default(""),
		field.String("from_address").MaxLen(64).Default(""),
		field.Other("units", decimal.Decimal{}).SchemaType(map[string]string{dialect.Postgres: "numeric(78,0)", dialect.SQLite: "numeric"}),
		field.Int16("decimals"),
		field.Other("amount", decimal.Decimal{}).SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)", dialect.SQLite: "numeric"}),
		field.Int64("block_number"),
		field.String("block_hash").MaxLen(128).Default(""),
		field.Time("block_time").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("status").MaxLen(16).Default("observed"),
		field.String("hold_reason").MaxLen(64).Default(""),
		field.Time("observed_at").Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("finalized_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("credited_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("orphaned_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (CryptoPayDeposit) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("network", "tx_hash", "event_key").Unique(),
		index.Fields("user_id", "created_at"),
		index.Fields("address_id", "block_time"),
		index.Fields("status", "id"),
	}
}
