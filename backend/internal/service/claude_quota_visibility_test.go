//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClaudeQuotaVisibility(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*User, *Group, *Account)
		visible bool
	}{
		{"authorized OAuth", func(*User, *Group, *Account) {}, true},
		{"public group", func(_ *User, g *Group, _ *Account) { g.IsExclusive = false }, false},
		{"switch off", func(_ *User, g *Group, _ *Account) { g.ShowAccountQuota = false }, false},
		{"revoked grant", func(u *User, _ *Group, _ *Account) { u.AllowedGroups = nil }, false},
		{"inactive user", func(u *User, _ *Group, _ *Account) { u.Status = "disabled" }, false},
		{"inactive group", func(_ *User, g *Group, _ *Account) { g.Status = "disabled" }, false},
		{"API key account", func(_ *User, _ *Group, a *Account) { a.Type = AccountTypeAPIKey }, false},
		{"different account group", func(_ *User, _ *Group, a *Account) { a.GroupIDs = []int64{11} }, false},
		{"setup token", func(_ *User, _ *Group, a *Account) { a.Type = AccountTypeSetupToken }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &User{ID: 1, Status: StatusActive, AllowedGroups: []int64{10}}
			g := &Group{ID: 10, Status: StatusActive, IsExclusive: true, ShowAccountQuota: true}
			a := &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth, GroupIDs: []int64{10}}
			tc.change(u, g, a)
			svc := &GatewayService{userRepo: &hostedUsersStub{user: u}, groupRepo: &hostedGroupsStub{group: g}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			groupID := int64(10)
			c.Set("api_key", &APIKey{UserID: 1, GroupID: &groupID, Group: &Group{ID: 10, IsExclusive: true, ShowAccountQuota: true}})
			h := http.Header{}
			h.Set("anthropic-ratelimit-unified-5h-utilization", "0.42")
			h.Set("anthropic-ratelimit-unified-7d-utilization", "0.63")
			h.Set("anthropic-ratelimit-unified-5h-reset", "1900000000")
			h.Set("anthropic-ratelimit-unified-status", "allowed")
			h.Set("anthropic-ratelimit-unified-overage-utilization", "0.99")
			// Simulate a permissive administrator-configured generic header filter.
			for name, values := range h {
				c.Writer.Header()[name] = values
			}
			svc.writeClaudeQuotaHeaders(context.Background(), c, a, h)
			if tc.visible {
				require.Equal(t, "0.42", c.Writer.Header().Get("anthropic-ratelimit-unified-5h-utilization"))
				require.Equal(t, "0.63", c.Writer.Header().Get("anthropic-ratelimit-unified-7d-utilization"))
				require.Equal(t, "1900000000", c.Writer.Header().Get("anthropic-ratelimit-unified-5h-reset"))
			} else {
				require.Empty(t, c.Writer.Header().Get("anthropic-ratelimit-unified-5h-utilization"))
				require.Empty(t, c.Writer.Header().Get("anthropic-ratelimit-unified-7d-utilization"))
			}
			require.Empty(t, c.Writer.Header().Get("anthropic-ratelimit-unified-overage-utilization"))
		})
	}
}

func TestClaudeQuotaResponsePaths(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmtBool(stream), func(t *testing.T) {
			svc := newMinimalGatewayService()
			svc.userRepo = &hostedUsersStub{user: &User{ID: 1, Status: StatusActive, AllowedGroups: []int64{10}}}
			svc.groupRepo = &hostedGroupsStub{group: &Group{ID: 10, Status: StatusActive, IsExclusive: true, ShowAccountQuota: true}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			groupID := int64(10)
			c.Set("api_key", &APIKey{UserID: 1, GroupID: &groupID})
			a := &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth, GroupIDs: []int64{10}}
			body := `{"id":"msg_test","type":"message","model":"model","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`
			if stream {
				body = "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
			}
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
			resp.Header.Set("anthropic-ratelimit-unified-5h-utilization", "0.42")
			resp.Header.Set("anthropic-ratelimit-unified-7d-utilization", "0.63")
			if stream {
				_, err := svc.handleStreamingResponse(context.Background(), resp, c, a, time.Now(), "model", "model", false)
				require.NoError(t, err)
			} else {
				_, err := svc.handleNonStreamingResponse(context.Background(), resp, c, a, "model", "model")
				require.NoError(t, err)
			}
			require.Equal(t, "0.42", rec.Result().Header.Get("anthropic-ratelimit-unified-5h-utilization"))
			require.Equal(t, "0.63", rec.Result().Header.Get("anthropic-ratelimit-unified-7d-utilization"))
		})
	}
}
func fmtBool(stream bool) string {
	if stream {
		return "stream"
	}
	return "nonstream"
}
