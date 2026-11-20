package repository

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type claudeDevicePlatformUserLookup struct {
	client *dbent.Client
}

// NewClaudeDevicePlatformUserLookup resolves user ids for the admin device
// platform list (one batch query per page).
func NewClaudeDevicePlatformUserLookup(client *dbent.Client) service.ClaudeDevicePlatformUserLookup {
	return &claudeDevicePlatformUserLookup{client: client}
}

func (r *claudeDevicePlatformUserLookup) LookupClaudeDevicePlatformUsers(ctx context.Context, ids []int64) (map[int64]service.ClaudeDevicePlatformUserName, error) {
	out := make(map[int64]service.ClaudeDevicePlatformUserName, len(ids))
	if r == nil || r.client == nil || len(ids) == 0 {
		return out, nil
	}
	users, err := r.client.User.Query().
		Where(dbuser.IDIn(ids...)).
		Select(dbuser.FieldID, dbuser.FieldUsername, dbuser.FieldEmail).
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		out[u.ID] = service.ClaudeDevicePlatformUserName{Username: u.Username, Email: u.Email}
	}
	return out, nil
}
