package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJevModerationDefaultAndStrictScores(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		valid      bool
	}{
		{"valid", `{"model":"jev-1.13.0","answers":{"Jailbreak":{"type":"noul","noul":0.9},"PII":{"type":"noul","noul":0.1},"Non-violent Illegal Acts":{"type":"noul","noul":0.2},"Unethical Acts":{"type":"noul","noul":0}}}`, 200, true},
		{"missing answers", `{"model":"jev-1.13.0","answers":{}}`, 200, false},
		{"unexpected model", `{"model":"jev-latest","answers":{}}`, 200, false},
		{"provider error must not echo text", `sensitive prompt or secret`, 401, false},
		{"oversized", strings.Repeat("x", inputModerationMaxResponseBytes+1), 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer synthetic-key", r.Header.Get("Authorization"))
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.Len(t, body, 3)
				require.Equal(t, jevModerationModel, body["model"])
				require.Equal(t, "synthetic input", body["state"])
				require.Len(t, body["questions"], 4)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			cfg := &config.Config{}
			cfg.Gateway.InputModeration.APIKey = "synthetic-key"
			cfg.Gateway.InputModeration.Endpoint = server.URL
			client, ok := NewInputModerationClient(cfg).(*jevModerationClient)
			require.True(t, ok)
			client.client.Transport = server.Client().Transport
			result, err := client.Classify(context.Background(), "synthetic input")
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, .9, result.Scores["Jailbreak"])
			} else {
				require.Error(t, err)
				require.Nil(t, result)
				require.NotContains(t, err.Error(), "sensitive")
			}
		})
	}
}

func TestJevModerationNoCredentialNoRequest(t *testing.T) {
	client := NewInputModerationClient(&config.Config{})
	_, err := client.Classify(context.Background(), "synthetic input")
	require.ErrorContains(t, err, "key is not configured")
}

func TestJevRegistryPreservesFrozenLegacyRules(t *testing.T) {
	var frozen map[string]string
	require.NoError(t, json.Unmarshal(jevModerationRules, &frozen))
	current, err := service.JevRulesForPolicy(nil)
	require.NoError(t, err)
	require.Equal(t, frozen, current)
}
func TestJevExpandedRequestUsesOnlySelectedRulesAndStrictScores(t *testing.T) {
	p := &service.InputModerationPolicy{RulesVersion: service.JevExpandedRulesVersion, AuditCategories: []string{"sexual/minors", "political/manipulation"}}
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Questions map[string]any `json:"questions"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				require.Len(t, request.Questions, 2)
				require.Contains(t, request.Questions, "sexual/minors")
				require.Contains(t, request.Questions, "political/manipulation")
				answers := map[string]any{"sexual/minors": map[string]any{"type": "noul", "noul": .1}}
				if !missing {
					answers["political/manipulation"] = map[string]any{"type": "noul", "noul": .2}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers})
			}))
			defer server.Close()
			cfg := &config.Config{}
			cfg.Gateway.InputModeration.APIKey = "TEST_ONLY"
			cfg.Gateway.InputModeration.Endpoint = server.URL
			c := newJevModerationClient(cfg)
			c.client.Transport = server.Client().Transport
			result, err := c.ClassifyWithPolicy(context.Background(), "TEST_ONLY", p)
			if missing {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Len(t, result.Scores, 2)
			}
		})
	}
}
