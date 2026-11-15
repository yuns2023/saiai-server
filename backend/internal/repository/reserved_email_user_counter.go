package repository

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type reservedEmailUserCounter struct {
	client *dbent.Client
}

// NewReservedEmailUserCounter counts users in a reserved synthetic-email domain (case-insensitive).
func NewReservedEmailUserCounter(client *dbent.Client) service.ReservedEmailUserCounter {
	return &reservedEmailUserCounter{client: client}
}

func (r *reservedEmailUserCounter) CountUsersWithEmailDomain(ctx context.Context, domain string) (int, error) {
	// ContainsFold over-counts only addresses that embed the domain elsewhere; that is the
	// conservative direction for a "refuse to enable" check.
	return r.client.User.Query().Where(dbuser.EmailContainsFold(domain)).Count(ctx)
}
