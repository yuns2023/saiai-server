package admin

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type rateCaptureAdminService struct {
	*stubAdminService
	calls   int
	entries []service.GroupRateMultiplierInput
}

func (s *rateCaptureAdminService) BatchSetGroupRateMultipliers(_ context.Context, _ int64, entries []service.GroupRateMultiplierInput) error {
	s.calls++
	s.entries = entries
	return nil
}

// A missing or null rate_multiplier is refused (it used to decode as 0,
// a free rate); an explicit 0 is a valid value and is saved.
func TestBatchSetGroupRateMultipliersRequiresExplicitRate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &rateCaptureAdminService{stubAdminService: newStubAdminService()}
	h := NewGroupHandler(svc, nil, nil, nil)
	router := gin.New()
	router.PUT("/groups/:id/rate-multipliers", h.BatchSetGroupRateMultipliers)

	for name, body := range map[string]string{
		"missing": `{"entries":[{"user_id":5,"rate_multiplier":1.5},{"user_id":6}]}`,
		"null":    `{"entries":[{"user_id":6,"rate_multiplier":null}]}`,
	} {
		rec := doJSON(router, http.MethodPut, "/groups/3/rate-multipliers", body)
		require.Equal(t, http.StatusBadRequest, rec.Code, name)
		require.Contains(t, rec.Body.String(), "rate_multiplier is required", name)
		require.Zero(t, svc.calls, name)
	}

	rec := doJSON(router, http.MethodPut, "/groups/3/rate-multipliers", `{"entries":[{"user_id":5,"rate_multiplier":0},{"user_id":6,"rate_multiplier":1.25}]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, svc.calls)
	require.Equal(t, []service.GroupRateMultiplierInput{{UserID: 5, RateMultiplier: 0}, {UserID: 6, RateMultiplier: 1.25}}, svc.entries)

	rec = doJSON(router, http.MethodPut, "/groups/3/rate-multipliers", `{"entries":[]}`)
	require.Equal(t, http.StatusOK, rec.Code, "an empty list clears the group's overrides as before")
	require.Equal(t, 2, svc.calls)
}
