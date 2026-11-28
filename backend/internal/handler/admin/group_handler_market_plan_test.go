package admin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// market_plan on a group update has presence semantics: absent = keep,
// null or "" = clear, a string = set (validated by the service).
func TestGroupHandlerMarketPlanPresence(t *testing.T) {
	router, svc, _ := newEntrypointGroupRouter()

	rec := doJSON(router, http.MethodPut, "/groups/1", `{"name":"t"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.False(t, svc.updated.MarketPlanSet, "absent keeps the stored plan")
	require.Nil(t, svc.updated.MarketPlan)

	for _, body := range []string{`{"market_plan":null}`, `{"market_plan":""}`, `{"market_plan":"  "}`, `{"market_tier":false,"market_plan":null}`} {
		rec = doJSON(router, http.MethodPut, "/groups/1", body)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.True(t, svc.updated.MarketPlanSet, body)
		require.Nil(t, svc.updated.MarketPlan, "%s clears", body)
	}

	rec = doJSON(router, http.MethodPut, "/groups/1", `{"market_tier":true,"market_plan":"max"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, svc.updated.MarketPlanSet)
	require.Equal(t, "max", *svc.updated.MarketPlan)

	rec = doJSON(router, http.MethodPut, "/groups/1", `{"market_plan":7}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "not a string")

	rec = doJSON(router, http.MethodPost, "/groups", `{"name":"tier","platform":"anthropic","market_tier":true,"market_plan":"pro"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "pro", *svc.created.MarketPlan)
	rec = doJSON(router, http.MethodPost, "/groups", `{"name":"plain","platform":"anthropic"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Nil(t, svc.created.MarketPlan)
}

func TestGroupDTOMarketPlanAdminOnly(t *testing.T) {
	plan := "max"
	g := &service.Group{ID: 3, Name: "T2", Platform: service.PlatformAnthropic, MarketTier: true, MarketPlan: &plan}
	admin, err := json.Marshal(dto.GroupFromServiceAdmin(g))
	require.NoError(t, err)
	require.Contains(t, string(admin), `"market_plan":"max"`)
	user, err := json.Marshal(dto.GroupFromService(g))
	require.NoError(t, err)
	require.NotContains(t, string(user), "market_plan", "users see the tier flag only")
	none, err := json.Marshal(dto.GroupFromServiceAdmin(&service.Group{ID: 4}))
	require.NoError(t, err)
	require.Contains(t, string(none), `"market_plan":null`)
}

func TestHostedApproveRequestMarketPlanJSON(t *testing.T) {
	var req hostedApproveRequest
	require.NoError(t, json.Unmarshal([]byte(`{"proxy_mode":"existing","proxy_id":3,"market_plan":"max"}`), &req))
	require.Equal(t, "max", req.MarketPlan)
	var omitted hostedApproveRequest
	require.NoError(t, json.Unmarshal([]byte(`{"proxy_mode":"existing","proxy_id":3}`), &omitted))
	require.Empty(t, omitted.MarketPlan)
}
