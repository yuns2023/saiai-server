package admin

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
)

func TestNormalizeAPIEndpoints(t *testing.T) {
	valid, err := normalizeAPIEndpoints([]dto.APIEndpoint{
		{ID: "default", Name: "Default", URL: "https://default.example.com/"},
		{ID: "vmiss_cn", Name: "VMISS China", URL: "https://edge.example.com", Enabled: true},
	})
	if err != nil {
		t.Fatalf("normalize valid endpoints: %v", err)
	}
	if len(valid) != 2 || valid[0].URL != "https://default.example.com" {
		t.Fatalf("unexpected normalized endpoints: %#v", valid)
	}

	cases := []struct {
		name  string
		items []dto.APIEndpoint
	}{
		{"http", []dto.APIEndpoint{{ID: "a", Name: "A", URL: "http://a.example.com"}}},
		{"credentials", []dto.APIEndpoint{{ID: "a", Name: "A", URL: "https://user:pass@a.example.com"}}},
		{"query", []dto.APIEndpoint{{ID: "a", Name: "A", URL: "https://a.example.com/path?x=1"}}},
		{"duplicate id", []dto.APIEndpoint{{ID: "a", Name: "A", URL: "https://a.example.com"}, {ID: "a", Name: "B", URL: "https://b.example.com"}}},
		{"duplicate url", []dto.APIEndpoint{{ID: "a", Name: "A", URL: "https://a.example.com/"}, {ID: "b", Name: "B", URL: "https://a.example.com"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := normalizeAPIEndpoints(tc.items); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}
