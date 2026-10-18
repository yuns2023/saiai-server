package admin

import (
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseAccountStatsRange(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		query    string
		valid    bool
		duration time.Duration
	}{
		{"?days=30&timezone=UTC", true, 30 * 24 * time.Hour},
		{"?days=1&timezone=UTC", true, 24 * time.Hour},
		{"?start_time=2026-10-17T18:13:00Z&end_time=2026-10-18T18:13:00Z&timezone=Asia%2FShanghai", true, 24 * time.Hour},
		{"?start_time=2026-10-17T18:13:00Z", false, 0},
		{"?start_time=invalid&end_time=invalid", false, 0},
		{"?start_time=2026-10-18T18:13:00Z&end_time=2026-10-17T18:13:00Z", false, 0},
		{"?start_time=2026-01-01T00:00:00Z&end_time=2026-10-18T00:00:00Z", false, 0},
		{"?days=91", false, 0}, {"?days=0", false, 0}, {"?timezone=invalid", false, 0},
	} {
		t.Run(tc.query, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("GET", "/stats"+tc.query, nil)
			start, end, err := parseAccountStatsRange(c, now)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			if tc.valid && end.Sub(start) != tc.duration {
				t.Fatalf("unexpected duration: %v", end.Sub(start))
			}
		})
	}
}
