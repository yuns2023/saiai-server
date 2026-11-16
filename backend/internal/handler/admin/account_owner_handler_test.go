package admin

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestParseOwnerUserIDBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	parse := func(body string) (*int64, error) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/accounts/1/owner", strings.NewReader(body))
		return parseOwnerUserIDBody(c)
	}

	id, err := parse(`{"owner_user_id": 5}`)
	require.NoError(t, err)
	require.NotNil(t, id)
	require.Equal(t, int64(5), *id)

	id, err = parse(`{"owner_user_id": null}`)
	require.NoError(t, err)
	require.Nil(t, id, "explicit null clears")

	id, err = parse("{\"owner_user_id\": 7}\n  ")
	require.NoError(t, err)
	require.Equal(t, int64(7), *id, "trailing whitespace is fine")

	for _, body := range []string{
		``, `{}`, `null`, `[]`, `{"owner": 5}`,
		`{"owner_user_id": 0}`, `{"owner_user_id": -1}`, `{"owner_user_id": "5"}`,
		`{"owner_user_id": 5.0}`, `{"owner_user_id": 5e0}`, `{"owner_user_id": 99999999999999999999}`,
		`{"owner_user_id": true}`, `{"owner_user_id": {}}`,
		`{"owner_user_id": 1}{"owner_user_id": null}`, `{"owner_user_id": 1} 2`, `{"owner_user_id": 1}]`,
	} {
		_, err := parse(body)
		require.ErrorIs(t, err, service.ErrHostedOwnerInvalidOwnerID, body)
	}
}

func TestListAccountsByOwnerFiltersInMemory(t *testing.T) {
	accounts := []service.Account{
		{ID: 1, Name: "Alpha", Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken, Status: service.StatusActive, GroupIDs: []int64{7}},
		{ID: 2, Name: "beta", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive},
		{ID: 3, Name: "Gamma", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusError, GroupIDs: []int64{8}},
	}
	filter := func(platform, kind, status, search string, group int64, page, size int) ([]int64, int64) {
		out, total := filterOwnedAccounts(accounts, page, size, platform, kind, status, search, group)
		ids := make([]int64, 0, len(out))
		for _, a := range out {
			ids = append(ids, a.ID)
		}
		return ids, total
	}
	ids, total := filter("", "", "", "", 0, 1, 20)
	require.Equal(t, []int64{3, 2, 1}, ids, "newest first, like the regular list")
	require.Equal(t, int64(3), total)
	ids, _ = filter(service.PlatformAnthropic, "", "", "", 0, 1, 20)
	require.Equal(t, []int64{3, 1}, ids)
	ids, _ = filter("", "", service.StatusError, "", 0, 1, 20)
	require.Equal(t, []int64{3}, ids)
	future, past := time.Now().Add(time.Hour), time.Now().Add(-time.Hour)
	accounts[0].RateLimitResetAt = &future
	accounts[1].RateLimitResetAt = &past
	accounts[2].TempUnschedulableUntil = &future
	ids, _ = filter("", "", "rate_limited", "", 0, 1, 20)
	require.Equal(t, []int64{1}, ids, "computed from reset time, like the repository filter")
	ids, _ = filter("", "", "temp_unschedulable", "", 0, 1, 20)
	require.Equal(t, []int64{3}, ids)
	accounts[0].RateLimitResetAt, accounts[1].RateLimitResetAt, accounts[2].TempUnschedulableUntil = nil, nil, nil
	ids, _ = filter("", "", "", "ALP", 0, 1, 20)
	require.Equal(t, []int64{1}, ids)
	ids, _ = filter("", "", "", "", 8, 1, 20)
	require.Equal(t, []int64{3}, ids)
	ids, _ = filter("", "", "", "", service.AccountListGroupUngrouped, 1, 20)
	require.Equal(t, []int64{2}, ids)
	ids, total = filter("", "", "", "", 0, 2, 2)
	require.Equal(t, []int64{1}, ids)
	require.Equal(t, int64(3), total)
	ids, total = filter("", "", "", "", 0, 9, 2)
	require.Empty(t, ids)
	require.Equal(t, int64(3), total)
	ids, total = filter("", "", "", "", 0, math.MaxInt, 20)
	require.Empty(t, ids, "huge page values must not overflow into a panic")
	require.Equal(t, int64(3), total)
	ids, _ = filter("", "", "", "", 0, 1, math.MaxInt)
	require.Equal(t, []int64{3, 2, 1}, ids)
}
