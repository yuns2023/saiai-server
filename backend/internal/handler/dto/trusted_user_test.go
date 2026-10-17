package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestTrustedUserIsAdminOnly(t *testing.T) {
	user := &service.User{ID: 7, IsTrusted: true}
	admin, err := json.Marshal(UserFromServiceAdmin(user))
	require.NoError(t, err)
	require.Contains(t, string(admin), `"is_trusted":true`)
	profile, err := json.Marshal(UserFromService(user))
	require.NoError(t, err)
	require.NotContains(t, string(profile), "is_trusted")
}
