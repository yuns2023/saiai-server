package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/gin-gonic/gin"
)

// Per-site client brand.
//
// One Gateway binary serves several sites, each with its own settings table.
// A few client-facing texts name the SAIAI brand or tell the user to re-run
// the SAIAI client setup. A site that ships another client sets
// client_brand_name and/or client_setup_command:
//
//   - both empty: every text is byte-identical to the original constant,
//     which stays the fallback (and the test baseline);
//   - brand set: the brand replaces SAIAI; without a command the setup step
//     is worded neutrally ("re-run your client setup" / "重新运行客户端初始化");
//   - command set: the setup step names the command; without a brand the
//     brand is dropped or replaced by a neutral word.
//
// Whenever either is set, no text names SAIAI. Error codes, status codes,
// headers and other fields never change.
//
// The value is read once per request (middleware.ClientBrand puts it in the
// request context) so a stream or WebSocket session uses one snapshot and
// its per-frame code never reads settings.

const (
	// MaxClientBrandNameRunes caps client_brand_name. An ASCII brand of this
	// length still fits the branded WebSocket close reason in the protocol's
	// 123-byte limit; longer UTF-8 encodings fall back to a neutral reason.
	MaxClientBrandNameRunes = 32
	// MaxClientSetupCommandRunes caps client_setup_command.
	MaxClientSetupCommandRunes = 64

	clientBrandCacheTTL         = 60 * time.Second
	clientBrandErrorTTL         = 5 * time.Second
	clientBrandFirstLoadTimeout = time.Second
	clientBrandRefreshTimeout   = 5 * time.Second
	clientBrandSingleflightKey  = "client_brand"
)

// ClientBrand is an immutable snapshot of a site's client brand settings.
// The zero value reproduces the original SAIAI texts.
type ClientBrand struct {
	name         string
	setupCommand string
}

// NewClientBrand validates both settings and returns the snapshot they
// describe. Surrounding white space is trimmed; empty means unset.
func NewClientBrand(name, setupCommand string) (ClientBrand, error) {
	normalizedName, err := NormalizeClientBrandName(name)
	if err != nil {
		return ClientBrand{}, err
	}
	normalizedCommand, err := NormalizeClientSetupCommand(setupCommand)
	if err != nil {
		return ClientBrand{}, err
	}
	return ClientBrand{name: normalizedName, setupCommand: normalizedCommand}, nil
}

// NormalizeClientBrandName trims and validates client_brand_name.
func NormalizeClientBrandName(value string) (string, error) {
	return normalizeClientBrandField(value, MaxClientBrandNameRunes, "INVALID_CLIENT_BRAND_NAME", "client_brand_name")
}

// NormalizeClientSetupCommand trims and validates client_setup_command.
func NormalizeClientSetupCommand(value string) (string, error) {
	return normalizeClientBrandField(value, MaxClientSetupCommandRunes, "INVALID_CLIENT_SETUP_COMMAND", "client_setup_command")
}

// normalizeClientBrandField keeps the values safe for every place they are
// spliced into: JSON built with strconv.Quote (no control characters), single
// line SSE/WebSocket frames (no line breaks), and the client-safe upstream
// message, which must never name the restricted upstream identity itself.
// It also rejects SAIAI, so a customized site's texts never name it.
func normalizeClientBrandField(value string, maxRunes int, code, field string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if !utf8.ValidString(value) {
		return "", infraerrors.BadRequest(code, field+" must be valid UTF-8")
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return "", infraerrors.BadRequest(code, fmt.Sprintf("%s must be at most %d characters", field, maxRunes))
	}
	for _, r := range value {
		if !unicode.IsPrint(r) {
			return "", infraerrors.BadRequest(code, field+" must not contain control characters, line breaks or special spaces")
		}
	}
	if containsRestrictedUpstreamIdentity(value) {
		return "", infraerrors.BadRequest(code, field+" contains a reserved word")
	}
	// A customized site never names SAIAI; leaving both settings empty is how
	// a site keeps the original SAIAI wording.
	if strings.Contains(strings.ToLower(value), "saiai") {
		return "", infraerrors.BadRequest(code, field+" must not contain SAIAI; leave both fields empty to keep the original SAIAI wording")
	}
	return value, nil
}

// clientBrandFromStored builds the snapshot from stored values. A value that
// fails validation (only possible when written outside the admin API) is
// ignored rather than spliced into client texts.
func clientBrandFromStored(name, setupCommand string) ClientBrand {
	var brand ClientBrand
	var err error
	if brand.name, err = NormalizeClientBrandName(name); err != nil {
		slog.Warn("ignoring invalid stored client brand setting", "key", SettingKeyClientBrandName, "error", err)
		brand.name = ""
	}
	if brand.setupCommand, err = NormalizeClientSetupCommand(setupCommand); err != nil {
		slog.Warn("ignoring invalid stored client brand setting", "key", SettingKeyClientSetupCommand, "error", err)
		brand.setupCommand = ""
	}
	return brand
}

// Customized reports whether either setting is set. When false, every
// branded text is the original constant.
func (b ClientBrand) Customized() bool { return b.name != "" || b.setupCommand != "" }

// Name is the configured brand ("" when unset).
func (b ClientBrand) Name() string { return b.name }

// SetupCommand is the configured setup command ("" when unset).
func (b ClientBrand) SetupCommand() string { return b.setupCommand }

// setupStepEN is the English re-initialization step of a customized brand.
func (b ClientBrand) setupStepEN() string {
	if b.setupCommand != "" {
		return "run `" + b.setupCommand + "`"
	}
	return "re-run your client setup"
}

// ClaudeCodeRequiredMessage replaces claudeOAuthRealClaudeCodeRequiredMessage.
func (b ClientBrand) ClaudeCodeRequiredMessage() string {
	if !b.Customized() {
		return claudeOAuthRealClaudeCodeRequiredMessage
	}
	return "Please use the official Claude Code client. If it is already installed, " + b.setupStepEN() +
		" and try again. Contact your administrator if the issue persists."
}

// DeviceAuthorizationUnavailableMessage replaces
// DeviceAuthorizationUnavailableClientMessage.
func (b ClientBrand) DeviceAuthorizationUnavailableMessage() string {
	switch {
	case !b.Customized():
		return DeviceAuthorizationUnavailableClientMessage
	case b.name != "":
		return b.name + " 服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。"
	default:
		return "服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。"
	}
}

// CodexUpstreamCapacityMessage replaces codexUpstreamCapacityMessage.
func (b ClientBrand) CodexUpstreamCapacityMessage() string {
	switch {
	case !b.Customized():
		return codexUpstreamCapacityMessage
	case b.name != "":
		return b.name + " upstream capacity is temporarily unavailable. Please retry later or contact your administrator. This is not your " +
			b.name + " balance or quota."
	default:
		return "Upstream capacity is temporarily unavailable. Please retry later or contact your administrator. This is not your account balance or quota."
	}
}

// BalanceRechargeSubject replaces paymentBalanceRechargeSubject.
func (b ClientBrand) BalanceRechargeSubject() string {
	switch {
	case !b.Customized():
		return paymentBalanceRechargeSubject
	case b.name != "":
		return b.name + " Balance Recharge"
	default:
		return "Balance Recharge"
	}
}

// TOTPIssuer replaces totpIssuer for new enrollments.
func (b ClientBrand) TOTPIssuer() string {
	switch {
	case !b.Customized():
		return totpIssuer
	case b.name != "":
		return b.name
	default:
		return totpNeutralIssuer
	}
}

type clientBrandContextKey struct{}

// WithClientBrand attaches the request's brand snapshot.
func WithClientBrand(ctx context.Context, brand ClientBrand) context.Context {
	return context.WithValue(ctx, clientBrandContextKey{}, brand)
}

// ClientBrandFromContext returns the request's brand snapshot; without one
// it returns the zero value (original texts).
func ClientBrandFromContext(ctx context.Context) ClientBrand {
	if ctx == nil {
		return ClientBrand{}
	}
	brand, _ := ctx.Value(clientBrandContextKey{}).(ClientBrand)
	return brand
}

// ClientBrandFromGin returns the brand snapshot of c's request.
func ClientBrandFromGin(c *gin.Context) ClientBrand {
	if c == nil || c.Request == nil {
		return ClientBrand{}
	}
	return ClientBrandFromContext(c.Request.Context())
}

type cachedClientBrand struct {
	brand     ClientBrand
	expiresAt int64 // unix nano
	loaded    bool  // false: placeholder after a read error with no last known good value
}

// ClientBrand returns this site's brand settings from a per-instance 60s
// cache. An expired value is served while one background refresh runs, so
// the request path never waits on the settings store after the first load
// (bounded by clientBrandFirstLoadTimeout). A read error keeps the last known
// good value; with none, the original texts are used and the read is retried
// after a short TTL.
func (s *SettingService) ClientBrand(ctx context.Context) ClientBrand {
	if s == nil || s.settingRepo == nil {
		return ClientBrand{}
	}
	if cached := s.clientBrandCache.Load(); cached != nil {
		if time.Now().UnixNano() >= cached.expiresAt && s.clientBrandRefreshing.CompareAndSwap(false, true) {
			go func() {
				defer s.clientBrandRefreshing.Store(false)
				s.loadClientBrand(context.WithoutCancel(ctx), clientBrandRefreshTimeout)
			}()
		}
		return cached.brand
	}
	return s.loadClientBrand(ctx, clientBrandFirstLoadTimeout)
}

// loadClientBrand reads the settings (singleflight) and publishes them unless
// an admin save was published meanwhile, in which case the saved value wins.
func (s *SettingService) loadClientBrand(ctx context.Context, timeout time.Duration) ClientBrand {
	result, _, _ := s.clientBrandSF.Do(clientBrandSingleflightKey, func() (any, error) {
		previous := s.clientBrandCache.Load()
		if previous != nil && time.Now().UnixNano() < previous.expiresAt {
			return previous.brand, nil
		}
		publish := func(next *cachedClientBrand) ClientBrand {
			if s.clientBrandCache.CompareAndSwap(previous, next) {
				return next.brand
			}
			if latest := s.clientBrandCache.Load(); latest != nil {
				return latest.brand
			}
			return next.brand
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		values, err := s.settingRepo.GetMultiple(dbCtx, []string{SettingKeyClientBrandName, SettingKeyClientSetupCommand})
		if err != nil {
			next := &cachedClientBrand{expiresAt: time.Now().Add(clientBrandErrorTTL).UnixNano()}
			if previous != nil && previous.loaded {
				next.brand, next.loaded = previous.brand, true
				slog.Warn("failed to refresh client brand settings, keeping last known value", "error", err)
			} else {
				slog.Error("failed to load client brand settings, using the original texts until they can be read", "error", err)
			}
			return publish(next), nil
		}
		return publish(&cachedClientBrand{
			brand:     clientBrandFromStored(values[SettingKeyClientBrandName], values[SettingKeyClientSetupCommand]),
			expiresAt: time.Now().Add(clientBrandCacheTTL).UnixNano(),
			loaded:    true,
		}), nil
	})
	brand, _ := result.(ClientBrand)
	return brand
}

// storeClientBrand publishes a value an administrator just saved.
func (s *SettingService) storeClientBrand(name, setupCommand string) {
	s.clientBrandSF.Forget(clientBrandSingleflightKey)
	s.clientBrandCache.Store(&cachedClientBrand{
		brand:     clientBrandFromStored(name, setupCommand),
		expiresAt: time.Now().Add(clientBrandCacheTTL).UnixNano(),
		loaded:    true,
	})
}
