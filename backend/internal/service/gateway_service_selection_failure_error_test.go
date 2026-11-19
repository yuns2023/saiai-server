package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCollectSelectionFailureStatsTracksShortestModelLimit(t *testing.T) {
	svc := &GatewayService{}
	model := "claude-fable-5-2"
	limited := func(id int64, in time.Duration) Account {
		return Account{
			ID: id, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
			Extra: map[string]any{"model_rate_limits": map[string]any{
				anthropicFableRateLimitKey: map[string]any{"rate_limit_reset_at": time.Now().Add(in).UTC().Format(time.RFC3339)},
			}},
		}
	}
	accounts := []Account{
		limited(1, 50*time.Hour),
		limited(2, 3*time.Hour),
		{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true},
	}
	stats := svc.collectSelectionFailureStats(context.Background(), accounts, model, PlatformAnthropic, map[int64]struct{}{3: {}}, false)
	if stats.ModelRateLimited != 2 || stats.Excluded != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if stats.ModelRateLimitMinRemaining <= 2*time.Hour || stats.ModelRateLimitMinRemaining > 3*time.Hour {
		t.Fatalf("shortest model limit = %s, want about 3h", stats.ModelRateLimitMinRemaining)
	}

	none := svc.collectSelectionFailureStats(context.Background(), accounts[2:], model, PlatformAnthropic, map[int64]struct{}{3: {}}, false)
	if none.ModelRateLimited != 0 || none.ModelRateLimitMinRemaining != 0 {
		t.Fatalf("unexpected stats without limits: %+v", none)
	}
}

func TestSelectionFailureErrorKeepsTextAndSentinel(t *testing.T) {
	stats := selectionFailureStats{Total: 8, Excluded: 2, ModelRateLimited: 2, ModelRateLimitMinRemaining: 3 * time.Hour}
	err := error(newSelectionFailureError("claude-fable-5-2", stats))
	want := fmt.Sprintf("%s supporting model: %s (%s)", ErrNoAvailableAccounts, "claude-fable-5-2", summarizeSelectionFailureStats(stats))
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
	wrapped := fmt.Errorf("select: %w", err)
	if !errors.Is(wrapped, ErrNoAvailableAccounts) {
		t.Fatal("must still match ErrNoAvailableAccounts")
	}
	var got *SelectionFailureError
	if !errors.As(wrapped, &got) || got.ModelRateLimited != 2 || got.ModelRateLimitMinRemaining != 3*time.Hour {
		t.Fatalf("errors.As = %+v", got)
	}
	if errors.Unwrap(err) != ErrNoAvailableAccounts {
		t.Fatal("must unwrap to ErrNoAvailableAccounts like the former wrapper")
	}

	plain := error(newPlainSelectionFailureError("claude-fable-5-2", stats))
	if plain.Error() != ErrNoAvailableAccounts.Error() || !errors.Is(plain, ErrNoAvailableAccounts) {
		t.Fatalf("plain variant must keep the sentinel text, got %q", plain.Error())
	}
	if !errors.As(plain, &got) || got.ModelRateLimited != 2 {
		t.Fatal("plain variant must still carry the stats")
	}
}

func TestCollectSelectionFailureStatsKeepsSubSecondMinimum(t *testing.T) {
	svc := &GatewayService{}
	acc := func(id int64, in time.Duration) Account {
		return Account{
			ID: id, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
			Extra: map[string]any{"model_rate_limits": map[string]any{
				anthropicFableRateLimitKey: map[string]any{"rate_limit_reset_at": time.Now().Add(in).UTC().Format(time.RFC3339Nano)},
			}},
		}
	}
	stats := svc.collectSelectionFailureStats(context.Background(), []Account{acc(1, 130*time.Hour), acc(2, 900*time.Millisecond)}, "claude-fable-5-2", PlatformAnthropic, nil, false)
	if stats.ModelRateLimited != 2 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if stats.ModelRateLimitMinRemaining <= 0 || stats.ModelRateLimitMinRemaining >= time.Second {
		t.Fatalf("shortest wait = %s, want the sub-second one", stats.ModelRateLimitMinRemaining)
	}
}

func TestClaudeOAuthCarpoolDevicesFullModelLimitedMessage(t *testing.T) {
	msg := ClaudeOAuthCarpoolDevicesFullModelLimitedMessage("claude-fable-5-2", 129*time.Hour+53*time.Minute)
	for _, part := range []string{"No available Claude OAuth carpool account", "claude-fable-5-2", "usage limit is reached on some accounts", "earliest known reset in about 5d 10h", "could not admit this device", "switch to another model"} {
		if !strings.Contains(msg, part) {
			t.Fatalf("message %q lacks %q", msg, part)
		}
	}
	if strings.Contains(ClaudeOAuthCarpoolDevicesFullModelLimitedMessage("claude-fable-5-2", 0), "earliest reset") {
		t.Fatal("unknown wait must not print a reset time")
	}
	if !strings.Contains(ClaudeOAuthCarpoolDevicesFullModelLimitedMessage(" ", time.Hour), "serve this model") {
		t.Fatal("empty model needs a neutral noun")
	}
}

func TestFormatApproxWait(t *testing.T) {
	cases := map[time.Duration]string{
		10 * time.Second:               "1m",
		12 * time.Minute:               "12m",
		59*time.Minute + time.Second:   "1h",
		3*time.Hour + 20*time.Minute:   "3h 20m",
		23*time.Hour + 30*time.Minute:  "1d",
		48 * time.Hour:                 "2d",
		129*time.Hour + 53*time.Minute: "5d 10h",
	}
	for in, want := range cases {
		if got := formatApproxWait(in); got != want {
			t.Errorf("formatApproxWait(%s) = %q, want %q", in, got, want)
		}
	}
}
