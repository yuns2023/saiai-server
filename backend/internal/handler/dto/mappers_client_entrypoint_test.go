package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// The Claude entrypoint warning is admin-only.
func TestUsageLogClientEntrypointIsAdminOnly(t *testing.T) {
	log := &service.UsageLog{ID: 1, ClientEntrypoint: &service.ClientEntrypointFlag{UA: "t3-code", Body: "t3-code", BodyStatus: "valid", Reason: "ua_not_allowed", GroupID: 2, Policy: "observe"}}
	admin, err := json.Marshal(UsageLogFromServiceAdmin(log))
	require.NoError(t, err)
	require.Contains(t, string(admin), `"client_entrypoint":{"ua":"t3-code","body":"t3-code","body_status":"valid","reason":"ua_not_allowed","group_id":2,"policy":"observe"}`)
	user, err := json.Marshal(UsageLogFromService(log))
	require.NoError(t, err)
	require.NotContains(t, string(user), "client_entrypoint")
}
