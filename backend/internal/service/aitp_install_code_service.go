package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// One-time AITP install codes (setting aitp_install_code_enabled, default off).
//
// The Key page's one-line install command embeds the plaintext API key and is long. With
// the setting on, the page can ask for a short one-time code instead and show
// `curl -fsSL -X POST <origin>/i/<CODE> | bash`; redeeming the code (POST /i/:code, no
// auth) returns the install script with the key filled in.
//
//   - A code is 12 Crockford base32 characters (60 bits) from crypto/rand, stored in Redis
//     under sha256(code) for AITPInstallCodeTTL as {user_id, api_key_id, shell, created_at}.
//     Issuing is limited per user; older codes stay valid until they expire.
//   - Redemption consumes the code atomically first, then re-loads the setting, the key and
//     the user; only the ids come from creation time. A consumed code is never put back, also
//     not when a later check or the database fails (fail closed).
//   - The installer root is the configured api_base_url only (never request headers) and
//     must pass a strict ASCII allowlist; the key must be [A-Za-z0-9_-]{1,256}. Both are
//     still quoted for the target shell.
//   - Every redemption outcome is a script for the target shell. Failures only print a
//     message and fail; PowerShell throws (an `exit` would close the user's window).
const (
	AITPInstallCodeLength   = 12
	AITPInstallCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	AITPInstallCodeTTL      = 10 * time.Minute

	// AITPInstallCodeIssueLimit codes per user per AITPInstallCodeIssueWindow.
	AITPInstallCodeIssueLimit  = 10
	AITPInstallCodeIssueWindow = 10 * time.Minute
	// AITPInstallCodeRedeemLimit redemption attempts per client IP per AITPInstallCodeRedeemWindow.
	AITPInstallCodeRedeemLimit  = 30
	AITPInstallCodeRedeemWindow = 10 * time.Minute

	// AITPInstallerBase serves setup.sh / setup.ps1. Keep equal to INSTALLER_BASE in
	// frontend-aog/src/lib/connect.ts.
	AITPInstallerBase = "https://aitp.lol"

	aitpInstallCodeSaveAttempts = 5
)

// AITPInstallShell is the script a code redeems to. The Key page's CMD tab runs the
// PowerShell script, so "cmd" is stored as powershell.
type AITPInstallShell string

const (
	AITPInstallShellUnix       AITPInstallShell = "unix"
	AITPInstallShellPowerShell AITPInstallShell = "powershell"
)

// ParseAITPInstallShell maps the Key page's shell tab (unix, powershell, cmd) to a script kind.
func ParseAITPInstallShell(raw string) (AITPInstallShell, bool) {
	switch raw {
	case "unix":
		return AITPInstallShellUnix, true
	case "powershell", "cmd":
		return AITPInstallShellPowerShell, true
	default:
		return "", false
	}
}

var (
	ErrAITPInstallCodeDisabled       = infraerrors.Forbidden("AITP_INSTALL_CODE_DISABLED", "install codes are not enabled on this site")
	ErrAITPInstallCodeBaseURLInvalid = infraerrors.ServiceUnavailable("AITP_INSTALL_CODE_BASE_URL_INVALID", "the site's API base URL is missing or invalid, so install codes are unavailable")
	ErrAITPInstallCodeShellInvalid   = infraerrors.BadRequest("AITP_INSTALL_CODE_SHELL_INVALID", "shell must be unix, powershell or cmd")
	ErrAITPInstallCodeKeyUnusable    = infraerrors.Conflict("AITP_INSTALL_CODE_KEY_UNUSABLE", "this API key is disabled or expired")
	ErrAITPInstallCodeKeyFormat      = infraerrors.BadRequest("AITP_INSTALL_CODE_KEY_FORMAT", "this API key cannot be used in an install command")
	ErrAITPInstallCodeRateLimited    = infraerrors.TooManyRequests("AITP_INSTALL_CODE_RATE_LIMITED", "too many install commands generated, try again in a few minutes")
	ErrAITPInstallCodeUnavailable    = infraerrors.ServiceUnavailable("AITP_INSTALL_CODE_UNAVAILABLE", "install codes are temporarily unavailable")
)

// AITPInstallCodeRecord is what a code points to. Nothing else from creation time is trusted.
type AITPInstallCodeRecord struct {
	UserID    int64            `json:"user_id"`
	APIKeyID  int64            `json:"api_key_id"`
	Shell     AITPInstallShell `json:"shell"`
	CreatedAt time.Time        `json:"created_at"`
}

// AITPInstallCodeStore keeps codes and the two rate-limit counters (Redis).
type AITPInstallCodeStore interface {
	// Save stores rec under code for ttl unless the code is taken (false, nil then).
	Save(ctx context.Context, code string, rec *AITPInstallCodeRecord, ttl time.Duration) (bool, error)
	// Consume atomically reads and deletes the record; (nil, nil) when there is none.
	Consume(ctx context.Context, code string) (*AITPInstallCodeRecord, error)
	// CountIssued / CountRedeemAttempt add one to a fixed-window counter and return the new value.
	CountIssued(ctx context.Context, userID int64, window time.Duration) (int64, error)
	CountRedeemAttempt(ctx context.Context, clientIP string, window time.Duration) (int64, error)
}

// AITPInstallCodeSettings are the settings a code depends on, read uncached.
type AITPInstallCodeSettings struct {
	Enabled    bool
	APIBaseURL string
}

// GetAITPInstallCodeSettings reads the switch and api_base_url; callers fail closed on error.
func (s *SettingService) GetAITPInstallCodeSettings(ctx context.Context) (AITPInstallCodeSettings, error) {
	values, err := s.settingRepo.GetMultiple(ctx, []string{SettingKeyAITPInstallCodeEnabled, SettingKeyAPIBaseURL})
	if err != nil {
		return AITPInstallCodeSettings{}, fmt.Errorf("get install code settings: %w", err)
	}
	return AITPInstallCodeSettings{
		Enabled:    values[SettingKeyAITPInstallCodeEnabled] == "true",
		APIBaseURL: values[SettingKeyAPIBaseURL],
	}, nil
}

type aitpInstallCodeSettingsReader interface {
	GetAITPInstallCodeSettings(ctx context.Context) (AITPInstallCodeSettings, error)
}

type aitpInstallCodeKeyReader interface {
	GetByID(ctx context.Context, id int64) (*APIKey, error)
}

type aitpInstallCodeUserReader interface {
	GetByID(ctx context.Context, id int64) (*User, error)
}

// AITPInstallCodeService issues and redeems one-time install codes.
type AITPInstallCodeService struct {
	settings aitpInstallCodeSettingsReader
	apiKeys  aitpInstallCodeKeyReader
	users    aitpInstallCodeUserReader
	store    AITPInstallCodeStore
	now      func() time.Time
	random   io.Reader
}

// NewAITPInstallCodeService wires the service. Keys are read through the repository so a
// soft-deleted key is never found.
func NewAITPInstallCodeService(settingService *SettingService, apiKeyRepo APIKeyRepository, userRepo UserRepository, store AITPInstallCodeStore) *AITPInstallCodeService {
	return &AITPInstallCodeService{
		settings: settingService,
		apiKeys:  apiKeyRepo,
		users:    userRepo,
		store:    store,
		now:      time.Now,
		random:   rand.Reader,
	}
}

// AITPInstallCodeIssued is the create response. It contains the code: never log or trace it.
type AITPInstallCodeIssued struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Create issues a code for one of the caller's keys.
func (s *AITPInstallCodeService) Create(ctx context.Context, userID, apiKeyID int64, rawShell string) (*AITPInstallCodeIssued, error) {
	shell, ok := ParseAITPInstallShell(rawShell)
	if !ok {
		return nil, ErrAITPInstallCodeShellInvalid
	}
	cfg, err := s.settings.GetAITPInstallCodeSettings(ctx)
	if err != nil {
		return nil, ErrAITPInstallCodeUnavailable.WithCause(err)
	}
	if !cfg.Enabled {
		return nil, ErrAITPInstallCodeDisabled
	}
	if _, ok := AITPInstallRoot(cfg.APIBaseURL); !ok {
		return nil, ErrAITPInstallCodeBaseURLInvalid
	}

	key, err := s.apiKeys.GetByID(ctx, apiKeyID)
	if err != nil {
		if errors.Is(err, ErrAPIKeyNotFound) {
			return nil, ErrAPIKeyNotFound
		}
		return nil, ErrAITPInstallCodeUnavailable.WithCause(err)
	}
	if key.UserID != userID {
		// Same answer as a missing key: no oracle for other users' key ids.
		return nil, ErrAPIKeyNotFound
	}
	if !key.IsActive() || key.IsExpired() {
		return nil, ErrAITPInstallCodeKeyUnusable
	}
	if !ValidAITPInstallKey(key.Key) {
		return nil, ErrAITPInstallCodeKeyFormat
	}
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrUserNotActive
		}
		return nil, ErrAITPInstallCodeUnavailable.WithCause(err)
	}
	if !user.IsActive() {
		return nil, ErrUserNotActive
	}

	issued, err := s.store.CountIssued(ctx, userID, AITPInstallCodeIssueWindow)
	if err != nil {
		return nil, ErrAITPInstallCodeUnavailable.WithCause(err)
	}
	if issued > AITPInstallCodeIssueLimit {
		return nil, ErrAITPInstallCodeRateLimited
	}

	now := s.now()
	rec := &AITPInstallCodeRecord{UserID: userID, APIKeyID: key.ID, Shell: shell, CreatedAt: now.UTC()}
	for attempt := 0; attempt < aitpInstallCodeSaveAttempts; attempt++ {
		code, err := generateAITPInstallCode(s.random)
		if err != nil {
			return nil, ErrAITPInstallCodeUnavailable.WithCause(err)
		}
		saved, err := s.store.Save(ctx, code, rec, AITPInstallCodeTTL)
		if err != nil {
			return nil, ErrAITPInstallCodeUnavailable.WithCause(err)
		}
		if saved {
			return &AITPInstallCodeIssued{Code: code, ExpiresAt: now.Add(AITPInstallCodeTTL).UTC()}, nil
		}
	}
	return nil, ErrAITPInstallCodeUnavailable.WithCause(errors.New("install code collisions exhausted"))
}

// AITPInstallRedeemOutcome classifies a redemption for the audit log.
type AITPInstallRedeemOutcome string

const (
	AITPInstallRedeemOK AITPInstallRedeemOutcome = "ok"
	// AITPInstallRedeemInvalid: malformed, unknown, already used or expired.
	AITPInstallRedeemInvalid AITPInstallRedeemOutcome = "invalid"
	// AITPInstallRedeemRateLimited: too many attempts from the client IP (same script as invalid).
	AITPInstallRedeemRateLimited AITPInstallRedeemOutcome = "rate_limited"
	// AITPInstallRedeemUnusable: consumed, but the setting, key or user no longer allow it.
	AITPInstallRedeemUnusable AITPInstallRedeemOutcome = "unusable"
	// AITPInstallRedeemUnavailable: Redis or the database failed; the code is not put back.
	AITPInstallRedeemUnavailable AITPInstallRedeemOutcome = "unavailable"
)

// AITPInstallRedeemResult is always a script. Script may contain the plaintext key: never
// log or trace it. Detail, UserID and APIKeyID are safe for the audit log.
type AITPInstallRedeemResult struct {
	Script   string
	Outcome  AITPInstallRedeemOutcome
	Detail   string
	Shell    AITPInstallShell
	UserID   int64
	APIKeyID int64
}

type aitpInstallMessage struct{ zh, en string }

var (
	aitpInstallMessageInvalid = aitpInstallMessage{
		zh: "安装码无效、已过期或已使用，请回到网站的 Key 页面重新生成。",
		en: "The install code is invalid, expired or already used; generate a new one on the Key page.",
	}
	aitpInstallMessageUnusable = aitpInstallMessage{
		zh: "安装码已失效：对应的 Key 或网站设置已变化，请回到网站的 Key 页面检查后重新生成。",
		en: "The install code can no longer be used because the key or the site settings changed; check the Key page and generate a new one.",
	}
	aitpInstallMessageUnavailable = aitpInstallMessage{
		zh: "服务暂时不可用，请稍后回到网站的 Key 页面重新生成安装命令。",
		en: "The service is temporarily unavailable; generate a new install command on the Key page later.",
	}
)

// Redeem consumes a code and returns the install script, or an error script.
func (s *AITPInstallCodeService) Redeem(ctx context.Context, rawCode, clientIP, userAgent string) AITPInstallRedeemResult {
	shell := AITPInstallShellForUserAgent(userAgent)
	fail := func(outcome AITPInstallRedeemOutcome, detail string, msg aitpInstallMessage) AITPInstallRedeemResult {
		return AITPInstallRedeemResult{Script: renderAITPInstallErrorScript(shell, msg), Outcome: outcome, Detail: detail, Shell: shell}
	}

	code, ok := NormalizeAITPInstallCode(rawCode)
	if !ok {
		return fail(AITPInstallRedeemInvalid, "malformed", aitpInstallMessageInvalid)
	}
	if strings.TrimSpace(clientIP) == "" {
		clientIP = "unknown"
	}
	attempts, err := s.store.CountRedeemAttempt(ctx, clientIP, AITPInstallCodeRedeemWindow)
	if err != nil {
		return fail(AITPInstallRedeemUnavailable, "rate_limit_store", aitpInstallMessageUnavailable)
	}
	if attempts > AITPInstallCodeRedeemLimit {
		return fail(AITPInstallRedeemRateLimited, "client_ip", aitpInstallMessageInvalid)
	}

	rec, err := s.store.Consume(ctx, code)
	if err != nil {
		return fail(AITPInstallRedeemUnavailable, "code_store", aitpInstallMessageUnavailable)
	}
	if rec == nil {
		return fail(AITPInstallRedeemInvalid, "not_found", aitpInstallMessageInvalid)
	}

	// From here on the code is gone whatever happens.
	if stored, ok := ParseAITPInstallShell(string(rec.Shell)); ok {
		shell = stored
	}
	failRec := func(outcome AITPInstallRedeemOutcome, detail string, msg aitpInstallMessage) AITPInstallRedeemResult {
		res := fail(outcome, detail, msg)
		res.UserID, res.APIKeyID = rec.UserID, rec.APIKeyID
		return res
	}

	cfg, err := s.settings.GetAITPInstallCodeSettings(ctx)
	if err != nil {
		return failRec(AITPInstallRedeemUnavailable, "settings", aitpInstallMessageUnavailable)
	}
	if !cfg.Enabled {
		return failRec(AITPInstallRedeemUnusable, "disabled", aitpInstallMessageUnusable)
	}
	root, ok := AITPInstallRoot(cfg.APIBaseURL)
	if !ok {
		return failRec(AITPInstallRedeemUnusable, "base_url", aitpInstallMessageUnusable)
	}

	key, err := s.apiKeys.GetByID(ctx, rec.APIKeyID)
	if err != nil {
		if errors.Is(err, ErrAPIKeyNotFound) {
			return failRec(AITPInstallRedeemUnusable, "key_not_found", aitpInstallMessageUnusable)
		}
		return failRec(AITPInstallRedeemUnavailable, "key_lookup", aitpInstallMessageUnavailable)
	}
	if key.UserID != rec.UserID {
		return failRec(AITPInstallRedeemUnusable, "key_owner", aitpInstallMessageUnusable)
	}
	if !key.IsActive() || key.IsExpired() {
		return failRec(AITPInstallRedeemUnusable, "key_inactive", aitpInstallMessageUnusable)
	}
	if !ValidAITPInstallKey(key.Key) {
		return failRec(AITPInstallRedeemUnusable, "key_format", aitpInstallMessageUnusable)
	}

	user, err := s.users.GetByID(ctx, rec.UserID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return failRec(AITPInstallRedeemUnusable, "user_not_found", aitpInstallMessageUnusable)
		}
		return failRec(AITPInstallRedeemUnavailable, "user_lookup", aitpInstallMessageUnavailable)
	}
	if !user.IsActive() {
		return failRec(AITPInstallRedeemUnusable, "user_inactive", aitpInstallMessageUnusable)
	}

	return AITPInstallRedeemResult{
		Script:   renderAITPInstallScript(shell, root, key.Key),
		Outcome:  AITPInstallRedeemOK,
		Shell:    shell,
		UserID:   rec.UserID,
		APIKeyID: rec.APIKeyID,
	}
}

// AITPInstallShellForUserAgent picks the error-script shell when the code is not known:
// PowerShell's irm sends "...PowerShell/x.y", curl does not.
func AITPInstallShellForUserAgent(userAgent string) AITPInstallShell {
	if strings.Contains(strings.ToLower(userAgent), "powershell") {
		return AITPInstallShellPowerShell
	}
	return AITPInstallShellUnix
}

// NormalizeAITPInstallCode upper-cases ASCII letters and accepts exactly
// AITPInstallCodeLength characters of AITPInstallCodeAlphabet.
func NormalizeAITPInstallCode(raw string) (string, bool) {
	if len(raw) != AITPInstallCodeLength {
		return "", false
	}
	out := make([]byte, len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		if strings.IndexByte(AITPInstallCodeAlphabet, c) < 0 {
			return "", false
		}
		out[i] = c
	}
	return string(out), true
}

// generateAITPInstallCode draws each character from 5 random bits (256 is a multiple of 32,
// so every character is uniform).
func generateAITPInstallCode(r io.Reader) (string, error) {
	buf := make([]byte, AITPInstallCodeLength)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", fmt.Errorf("generate install code: %w", err)
	}
	for i, b := range buf {
		buf[i] = AITPInstallCodeAlphabet[b&31]
	}
	return string(buf), nil
}

// Same check as AITP's setup scripts, narrowed to an explicit path character set: an
// absolute http(s) URL, ASCII only, no userinfo, query, fragment, whitespace or quotes.
var aitpInstallRootPattern = regexp.MustCompile(`^https?://([A-Za-z0-9.-]+|\[[0-9A-Fa-f:.]+\])(:([0-9]{1,5}))?(/[A-Za-z0-9._~/-]*)?$`)

var aitpInstallKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// AITPInstallRoot normalizes api_base_url like the Key page's gatewayRoot (trim, drop
// trailing slashes and one trailing /v1) and accepts only an allowlisted URL. The request's
// Host / X-Forwarded-* headers are never a fallback.
func AITPInstallRoot(apiBaseURL string) (string, bool) {
	root := strings.TrimRight(strings.TrimSpace(apiBaseURL), "/")
	root = strings.TrimRight(strings.TrimSuffix(root, "/v1"), "/")
	m := aitpInstallRootPattern.FindStringSubmatch(root)
	if m == nil {
		return "", false
	}
	if port := m[3]; port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", false
		}
	}
	return root, true
}

// ValidAITPInstallKey: keys are "sk-" + hex; anything else is refused.
func ValidAITPInstallKey(key string) bool {
	return aitpInstallKeyPattern.MatchString(key)
}

// aitpShellQuote single-quotes a value for sh; an embedded single quote is closed,
// escaped with a backslash and reopened.
func aitpShellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// aitpPowerShellQuoteReplacer doubles every character PowerShell treats as a single quote
// (U+0027, U+2018, U+2019, U+201A, U+201B).
var aitpPowerShellQuoteReplacer = strings.NewReplacer(
	"'", "''",
	"‘", "‘‘",
	"’", "’’",
	"‚", "‚‚",
	"‛", "‛‛",
)

// aitpPowerShellQuote single-quotes for PowerShell, doubling every single-quote character
// (ASCII and the four typographic ones PowerShell also accepts). The allowlists already
// exclude them; this is defense in depth.
func aitpPowerShellQuote(v string) string {
	return "'" + aitpPowerShellQuoteReplacer.Replace(v) + "'"
}

func renderAITPInstallScript(shell AITPInstallShell, root, key string) string {
	if shell == AITPInstallShellPowerShell {
		return "& ([scriptblock]::Create((irm " + AITPInstallerBase + "/setup.ps1))) -BaseUrl " +
			aitpPowerShellQuote(root) + " -ApiKey " + aitpPowerShellQuote(key) + "\n"
	}
	// A function called on the last line: a truncated download runs nothing, and set -e
	// makes a failed setup.sh download fail the whole command.
	return "aitp_install() {\n" +
		"  set -eu\n" +
		"  tmp=$(mktemp)\n" +
		"  trap 'rm -f \"$tmp\"' EXIT\n" +
		"  curl -fsSL " + AITPInstallerBase + "/setup.sh -o \"$tmp\"\n" +
		"  bash \"$tmp\" --base-url " + aitpShellQuote(root) + " --api-key " + aitpShellQuote(key) + "\n" +
		"}\n" +
		"aitp_install\n"
}

func renderAITPInstallErrorScript(shell AITPInstallShell, msg aitpInstallMessage) string {
	if shell == AITPInstallShellPowerShell {
		return "throw " + aitpPowerShellQuote(msg.zh+" "+msg.en) + "\n"
	}
	return "echo " + aitpShellQuote(msg.zh) + " >&2\n" +
		"echo " + aitpShellQuote(msg.en) + " >&2\n" +
		"exit 1\n"
}
