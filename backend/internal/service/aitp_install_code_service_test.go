//go:build unit

package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// ---- fakes -----------------------------------------------------------------

type installSettingsFake struct {
	mu      sync.Mutex
	enabled bool
	baseURL string
	err     error
}

func (f *installSettingsFake) GetAITPInstallCodeSettings(context.Context) (AITPInstallCodeSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return AITPInstallCodeSettings{}, f.err
	}
	return AITPInstallCodeSettings{Enabled: f.enabled, APIBaseURL: f.baseURL}, nil
}

type installKeysFake struct {
	mu   sync.Mutex
	keys map[int64]*APIKey
	err  error
}

func (f *installKeysFake) GetByID(_ context.Context, id int64) (*APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	k, ok := f.keys[id]
	if !ok {
		return nil, ErrAPIKeyNotFound
	}
	cp := *k
	return &cp, nil
}

func (f *installKeysFake) update(id int64, fn func(*APIKey)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.keys[id])
}

type installUsersFake struct {
	mu    sync.Mutex
	users map[int64]*User
	err   error
}

func (f *installUsersFake) GetByID(_ context.Context, id int64) (*User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	u, ok := f.users[id]
	if !ok {
		return nil, ErrUserNotFound
	}
	cp := *u
	return &cp, nil
}

type memInstallEntry struct {
	data      []byte
	expiresAt time.Time
}

// memInstallStore mimics the Redis store: JSON round trip, TTL, atomic consume.
type memInstallStore struct {
	mu         sync.Mutex
	now        func() time.Time
	codes      map[string]memInstallEntry
	counters   map[string]int64
	collisions int // the next N saves report a taken code
	saveErr    error
	consumeErr error
	countErr   error
	consumes   int
}

func newMemInstallStore(now func() time.Time) *memInstallStore {
	return &memInstallStore{now: now, codes: map[string]memInstallEntry{}, counters: map[string]int64{}}
}

func (s *memInstallStore) Save(_ context.Context, code string, rec *AITPInstallCodeRecord, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return false, s.saveErr
	}
	if s.collisions > 0 {
		s.collisions--
		return false, nil
	}
	if e, ok := s.codes[code]; ok && s.now().Before(e.expiresAt) {
		return false, nil
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return false, err
	}
	s.codes[code] = memInstallEntry{data: data, expiresAt: s.now().Add(ttl)}
	return true, nil
}

func (s *memInstallStore) Consume(_ context.Context, code string) (*AITPInstallCodeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consumes++
	if s.consumeErr != nil {
		return nil, s.consumeErr
	}
	e, ok := s.codes[code]
	if !ok {
		return nil, nil
	}
	delete(s.codes, code)
	if !s.now().Before(e.expiresAt) {
		return nil, nil
	}
	var rec AITPInstallCodeRecord
	if err := json.Unmarshal(e.data, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *memInstallStore) count(key string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.countErr != nil {
		return 0, s.countErr
	}
	s.counters[key]++
	return s.counters[key], nil
}

func (s *memInstallStore) CountIssued(_ context.Context, userID int64, _ time.Duration) (int64, error) {
	return s.count("issued:" + strconv.FormatInt(userID, 10))
}

func (s *memInstallStore) CountRedeemAttempt(_ context.Context, clientIP string, _ time.Duration) (int64, error) {
	return s.count("ip:" + clientIP)
}

func (s *memInstallStore) has(code string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.codes[code]
	return ok
}

type installFixture struct {
	svc      *AITPInstallCodeService
	settings *installSettingsFake
	keys     *installKeysFake
	users    *installUsersFake
	store    *memInstallStore
	now      time.Time
	clockMu  sync.Mutex
}

const (
	installTestUser  = int64(7)
	installTestOther = int64(8)
	installTestKeyID = int64(41)
	installTestRoot  = "https://ccyou.dev"
)

// Built at run time so secret scanners do not flag a fake key.
var installTestKey = "sk-" + strings.Repeat("0123456789abcdef", 4)

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	f := &installFixture{now: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	clock := func() time.Time {
		f.clockMu.Lock()
		defer f.clockMu.Unlock()
		return f.now
	}
	f.settings = &installSettingsFake{enabled: true, baseURL: "https://ccyou.dev/v1"}
	f.keys = &installKeysFake{keys: map[int64]*APIKey{
		installTestKeyID: {ID: installTestKeyID, UserID: installTestUser, Key: installTestKey, Status: StatusActive},
		42:               {ID: 42, UserID: installTestOther, Key: "sk-other", Status: StatusActive},
	}}
	f.users = &installUsersFake{users: map[int64]*User{
		installTestUser:  {ID: installTestUser, Status: StatusActive},
		installTestOther: {ID: installTestOther, Status: StatusActive},
	}}
	f.store = newMemInstallStore(clock)
	f.svc = &AITPInstallCodeService{
		settings: f.settings,
		apiKeys:  f.keys,
		users:    f.users,
		store:    f.store,
		now:      clock,
		random:   rand.Reader,
	}
	return f
}

func (f *installFixture) advance(d time.Duration) {
	f.clockMu.Lock()
	defer f.clockMu.Unlock()
	f.now = f.now.Add(d)
}

func (f *installFixture) issue(t *testing.T, shell string) string {
	t.Helper()
	issued, err := f.svc.Create(context.Background(), installTestUser, installTestKeyID, shell)
	require.NoError(t, err)
	return issued.Code
}

func (f *installFixture) redeem(code string) AITPInstallRedeemResult {
	return f.svc.Redeem(context.Background(), code, "203.0.113.9", "curl/8.5.0")
}

func requireAppErr(t *testing.T, err error, want error) {
	t.Helper()
	require.Error(t, err)
	require.ErrorIs(t, err, want)
}

// ---- code format -------------------------------------------------------------

func TestAITPInstallCode_GeneratedFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		code, err := generateAITPInstallCode(rand.Reader)
		require.NoError(t, err)
		require.Len(t, code, AITPInstallCodeLength)
		for _, c := range code {
			require.Contains(t, AITPInstallCodeAlphabet, string(c))
		}
		norm, ok := NormalizeAITPInstallCode(code)
		require.True(t, ok)
		require.Equal(t, code, norm)
		require.False(t, seen[code], "60-bit codes must not repeat in a small sample")
		seen[code] = true
	}
	require.Len(t, AITPInstallCodeAlphabet, 32)
	for _, c := range "ILOU" {
		require.NotContains(t, AITPInstallCodeAlphabet, string(c), "Crockford base32 has no I, L, O, U")
	}

	// Every byte maps through its low 5 bits, so the 32 symbols are equally likely.
	code, err := generateAITPInstallCode(bytes.NewReader([]byte{0, 31, 32, 255, 1, 10, 17, 18, 21, 26, 27, 30}))
	require.NoError(t, err)
	require.Equal(t, "0Z0Z1AHJNTVY", code)

	_, err = generateAITPInstallCode(bytes.NewReader([]byte{1, 2, 3}))
	require.Error(t, err, "a short random read must not produce a code")
}

func TestNormalizeAITPInstallCode(t *testing.T) {
	for raw, want := range map[string]string{
		"0123456789AB": "0123456789AB",
		"abcdefghjkmn": "ABCDEFGHJKMN",
		"pqrstvwxyz01": "PQRSTVWXYZ01",
	} {
		got, ok := NormalizeAITPInstallCode(raw)
		require.True(t, ok, raw)
		require.Equal(t, want, got)
	}
	for _, raw := range []string{
		"", "0123456789A", "0123456789ABC", "0123456789AI", "0123456789AL", "0123456789AO", "0123456789AU",
		"0123456789A ", " 0123456789A", "0123456789A\n", "0123456789A-", "0123456789A'", "0123456789A\x00",
		"0123456789Aſ"[:12], "0123456789ſ", // ſ upper-cases to S in Unicode; bytes must be ASCII
		"%30123456789A", "../012345678",
	} {
		_, ok := NormalizeAITPInstallCode(raw)
		require.False(t, ok, "%q", raw)
	}
}

// ---- root / key validation and quoting ----------------------------------------

func TestAITPInstallRoot(t *testing.T) {
	for raw, want := range map[string]string{
		"https://ccyou.dev":                 "https://ccyou.dev",
		"https://ccyou.dev/":                "https://ccyou.dev",
		"https://ccyou.dev/v1":              "https://ccyou.dev",
		"https://ccyou.dev/v1/":             "https://ccyou.dev",
		"  https://ccyou.dev/v1 \n":         "https://ccyou.dev",
		"https://gw.example.com/claude/v1":  "https://gw.example.com/claude",
		"http://127.0.0.1:8080":             "http://127.0.0.1:8080",
		"https://[2001:db8::1]:8443/a_b~c.": "https://[2001:db8::1]:8443/a_b~c.",
		"https://x.example:65535":           "https://x.example:65535",
		"https://ccyou.dev/\u00a0":          "https://ccyou.dev", // surrounding Unicode space is trimmed like JS trim()
	} {
		got, ok := AITPInstallRoot(raw)
		require.True(t, ok, "%q", raw)
		require.Equal(t, want, got, raw)
	}
	for _, raw := range []string{
		"", "   ", "/v1", "ccyou.dev", "//ccyou.dev", "ftp://ccyou.dev", "javascript:alert(1)", "HTTPS://ccyou.dev",
		"https://user:pw@ccyou.dev", "https://user@ccyou.dev", "https://ccyou.dev\\@evil.example",
		"https://ccyou.dev?x=1", "https://ccyou.dev/?x=1", "https://ccyou.dev#frag", "https://ccyou.dev/a#b",
		"https://ccyou.dev/a b", "https://ccyou dev", "https://ccyou.dev/\tx",
		"https://ccyou.dev/a'b", `https://ccyou.dev/a"b`, "https://ccyou.dev/$(id)", "https://ccyou.dev/${HOME}",
		"https://ccyou.dev/`id`", "https://ccyou.dev/a;b", "https://ccyou.dev/a&b", "https://ccyou.dev/a|b",
		"https://ccyou.dev/a%27b", "https://ccyou.dev/a%b", "https://ccyou.dev/a\\b", "https://ccyou.dev/a!b",
		"https://ccyou.dev/a\rb", "https://ccyou.dev/a\nb", "https://ccyou.dev/a\x00b", "https://ccyou.dev\x00",
		"https://ccyou.dev/a‘b", "https://ccyou.dev/a’b", "https://ccyou.dev/a‚b", "https://ccyou.dev/a‛b",
		"https://bücher.example", "https://ccyou.dev/ü", "https://ccyou.dev/a\u00a0b", "https://ccyou.dev/a\u2028b",
		"https://ccyou.dev:0", "https://ccyou.dev:65536", "https://ccyou.dev:99999", "https://ccyou.dev:123456",
		"https://ccyou.dev:", "https://[::1", "https://[::1]x",
	} {
		got, ok := AITPInstallRoot(raw)
		require.False(t, ok, "%q accepted as %q", raw, got)
	}
}

func TestValidAITPInstallKey(t *testing.T) {
	require.True(t, ValidAITPInstallKey(installTestKey))
	require.True(t, ValidAITPInstallKey("custom_KEY-01"))
	require.True(t, ValidAITPInstallKey(strings.Repeat("a", 256)))
	for _, key := range []string{
		"", strings.Repeat("a", 257), "sk-a'b", `sk-a"b`, "sk-$x", "sk-`x`", "sk-a b", "sk-a\rb", "sk-a\nb",
		"sk-a\x00b", "sk-a‘b", "sk-a’b", "sk-a‚b", "sk-a‛b", "sk-ü", "sk-a;b", "sk-a%b", "sk-a\\b",
	} {
		require.False(t, ValidAITPInstallKey(key), "%q", key)
	}
}

func TestAITPInstallQuoting(t *testing.T) {
	require.Equal(t, `'plain'`, aitpShellQuote("plain"))
	require.Equal(t, `'a'\''b'`, aitpShellQuote("a'b"))
	require.Equal(t, `'$(id) `+"`x`"+` "q"'`, aitpShellQuote("$(id) `x` \"q\""), "everything but ' is literal in sh single quotes")
	require.Equal(t, `'a''b'`, aitpPowerShellQuote("a'b"))
	require.Equal(t, `'$x `+"`x`"+` "q"'`, aitpPowerShellQuote("$x `x` \"q\""), "everything but quotes is literal in PowerShell single quotes")
	// PowerShell also ends a single-quoted string at U+2018..U+201B; each is doubled.
	require.Equal(t, "'a‘‘b’’c‚‚d‛‛e'", aitpPowerShellQuote("a‘b’c‚d‛e"))
}

func TestAITPInstallScripts_ExactText(t *testing.T) {
	sh := renderAITPInstallScript(AITPInstallShellUnix, installTestRoot, installTestKey)
	require.Equal(t, "aitp_install() {\n"+
		"  set -eu\n"+
		"  tmp=$(mktemp)\n"+
		"  trap 'rm -f \"$tmp\"' EXIT\n"+
		"  curl -fsSL https://aitp.lol/setup.sh -o \"$tmp\"\n"+
		"  bash \"$tmp\" --base-url 'https://ccyou.dev' --api-key '"+installTestKey+"'\n"+
		"}\n"+
		"aitp_install\n", sh)

	ps := renderAITPInstallScript(AITPInstallShellPowerShell, installTestRoot, installTestKey)
	require.Equal(t, "& ([scriptblock]::Create((irm https://aitp.lol/setup.ps1))) -BaseUrl 'https://ccyou.dev' -ApiKey '"+installTestKey+"'\n", ps)

	psErr := renderAITPInstallErrorScript(AITPInstallShellPowerShell, aitpInstallMessageInvalid)
	require.True(t, strings.HasPrefix(psErr, "throw '"), psErr)
	require.NotContains(t, strings.ToLower(psErr), "exit", "exit would close the user's PowerShell window")
	require.Contains(t, psErr, "安装码无效")
	require.Contains(t, psErr, "generate a new one on the Key page")

	shErr := renderAITPInstallErrorScript(AITPInstallShellUnix, aitpInstallMessageUnavailable)
	require.Equal(t, "echo '"+aitpInstallMessageUnavailable.zh+"' >&2\necho '"+aitpInstallMessageUnavailable.en+"' >&2\nexit 1\n", shErr)

	for _, msg := range []aitpInstallMessage{aitpInstallMessageInvalid, aitpInstallMessageUnusable, aitpInstallMessageUnavailable} {
		for _, text := range []string{msg.zh, msg.en} {
			require.NotContains(t, text, "'")
			require.False(t, strings.ContainsAny(text, "‘’‚‛\\\n"), text)
		}
	}
}

// The installer host is one constant on each side; the Key page's full command and the
// redeemed script must download from the same place.
func TestAITPInstallerBaseMatchesKeyPage(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "frontend-aog", "src", "lib", "connect.ts"))
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("frontend-aog sources not present")
	}
	require.NoError(t, err)
	require.Contains(t, string(src), "const INSTALLER_BASE = '"+AITPInstallerBase+"'")
}

func TestParseAITPInstallShell(t *testing.T) {
	for raw, want := range map[string]AITPInstallShell{"unix": AITPInstallShellUnix, "powershell": AITPInstallShellPowerShell, "cmd": AITPInstallShellPowerShell} {
		got, ok := ParseAITPInstallShell(raw)
		require.True(t, ok)
		require.Equal(t, want, got)
	}
	for _, raw := range []string{"", "bash", "Unix", "pwsh", "zsh"} {
		_, ok := ParseAITPInstallShell(raw)
		require.False(t, ok, raw)
	}
	require.Equal(t, AITPInstallShellPowerShell, AITPInstallShellForUserAgent("Mozilla/5.0 (Windows NT; Windows NT 10.0; zh-CN) WindowsPowerShell/5.1.26100.1"))
	require.Equal(t, AITPInstallShellPowerShell, AITPInstallShellForUserAgent("Mozilla/5.0 (Windows NT 10.0) PowerShell/7.4.6"))
	require.Equal(t, AITPInstallShellUnix, AITPInstallShellForUserAgent("curl/8.5.0"))
	require.Equal(t, AITPInstallShellUnix, AITPInstallShellForUserAgent(""))
}

// ---- create ------------------------------------------------------------------

func TestAITPInstallCodeCreate_StoresIdsOnly(t *testing.T) {
	f := newInstallFixture(t)
	issued, err := f.svc.Create(context.Background(), installTestUser, installTestKeyID, "cmd")
	require.NoError(t, err)
	_, ok := NormalizeAITPInstallCode(issued.Code)
	require.True(t, ok)
	require.Equal(t, f.now.Add(10*time.Minute), issued.ExpiresAt)

	f.store.mu.Lock()
	entry := f.store.codes[issued.Code]
	f.store.mu.Unlock()
	require.NotContains(t, string(entry.data), installTestKey, "the key is never stored with the code")
	var rec map[string]any
	require.NoError(t, json.Unmarshal(entry.data, &rec))
	require.Equal(t, float64(installTestUser), rec["user_id"])
	require.Equal(t, float64(installTestKeyID), rec["api_key_id"])
	require.Equal(t, "powershell", rec["shell"], "cmd runs the PowerShell script")
	require.Contains(t, rec, "created_at")
	require.Equal(t, f.now.Add(AITPInstallCodeTTL), entry.expiresAt)
}

func TestAITPInstallCodeCreate_Refusals(t *testing.T) {
	ctx := context.Background()
	f := newInstallFixture(t)

	_, err := f.svc.Create(ctx, installTestUser, installTestKeyID, "zsh")
	requireAppErr(t, err, ErrAITPInstallCodeShellInvalid)

	f.settings.enabled = false
	_, err = f.svc.Create(ctx, installTestUser, installTestKeyID, "unix")
	requireAppErr(t, err, ErrAITPInstallCodeDisabled)
	f.settings.enabled = true

	for _, bad := range []string{"", "ccyou.dev", "https://ccyou.dev/?q", "https://u@ccyou.dev"} {
		f.settings.baseURL = bad
		_, err = f.svc.Create(ctx, installTestUser, installTestKeyID, "unix")
		requireAppErr(t, err, ErrAITPInstallCodeBaseURLInvalid)
	}
	f.settings.baseURL = installTestRoot

	_, err = f.svc.Create(ctx, installTestUser, 999, "unix")
	requireAppErr(t, err, ErrAPIKeyNotFound)
	// Another user's key id answers exactly like a missing key.
	_, err = f.svc.Create(ctx, installTestUser, 42, "unix")
	requireAppErr(t, err, ErrAPIKeyNotFound)

	f.keys.update(installTestKeyID, func(k *APIKey) { k.Status = StatusAPIKeyDisabled })
	_, err = f.svc.Create(ctx, installTestUser, installTestKeyID, "unix")
	requireAppErr(t, err, ErrAITPInstallCodeKeyUnusable)
	f.keys.update(installTestKeyID, func(k *APIKey) { k.Status = StatusAPIKeyQuotaExhausted })
	_, err = f.svc.Create(ctx, installTestUser, installTestKeyID, "unix")
	requireAppErr(t, err, ErrAITPInstallCodeKeyUnusable)
	past := time.Now().Add(-time.Minute) // APIKey.IsExpired reads the real clock
	f.keys.update(installTestKeyID, func(k *APIKey) { k.Status = StatusActive; k.ExpiresAt = &past })
	_, err = f.svc.Create(ctx, installTestUser, installTestKeyID, "unix")
	requireAppErr(t, err, ErrAITPInstallCodeKeyUnusable)
	f.keys.update(installTestKeyID, func(k *APIKey) { k.ExpiresAt = nil; k.Key = "sk-it's" })
	_, err = f.svc.Create(ctx, installTestUser, installTestKeyID, "unix")
	requireAppErr(t, err, ErrAITPInstallCodeKeyFormat)
	f.keys.update(installTestKeyID, func(k *APIKey) { k.Key = installTestKey })

	f.users.users[installTestUser].Status = StatusDisabled
	_, err = f.svc.Create(ctx, installTestUser, installTestKeyID, "unix")
	requireAppErr(t, err, ErrUserNotActive)
	f.users.users[installTestUser].Status = StatusActive

	require.Empty(t, f.store.codes, "no refused request stores a code")
	require.Empty(t, f.store.counters, "refused requests do not use up the issuance limit")
}

func TestAITPInstallCodeCreate_InfrastructureFailuresFailClosed(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	cases := map[string]func(f *installFixture){
		"settings": func(f *installFixture) { f.settings.err = boom },
		"key":      func(f *installFixture) { f.keys.err = boom },
		"user":     func(f *installFixture) { f.users.err = boom },
		"counter":  func(f *installFixture) { f.store.countErr = boom },
		"save":     func(f *installFixture) { f.store.saveErr = boom },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			f := newInstallFixture(t)
			breakIt(f)
			_, err := f.svc.Create(ctx, installTestUser, installTestKeyID, "unix")
			requireAppErr(t, err, ErrAITPInstallCodeUnavailable)
			require.Empty(t, f.store.codes)
		})
	}
}

func TestAITPInstallCodeCreate_RetriesCollisions(t *testing.T) {
	f := newInstallFixture(t)
	f.store.collisions = aitpInstallCodeSaveAttempts - 1
	f.issue(t, "unix")

	f.store.collisions = aitpInstallCodeSaveAttempts
	_, err := f.svc.Create(context.Background(), installTestUser, installTestKeyID, "unix")
	requireAppErr(t, err, ErrAITPInstallCodeUnavailable)
}

func TestAITPInstallCodeCreate_PerUserLimitKeepsOlderCodes(t *testing.T) {
	f := newInstallFixture(t)
	codes := make([]string, 0, AITPInstallCodeIssueLimit)
	for i := 0; i < AITPInstallCodeIssueLimit; i++ {
		codes = append(codes, f.issue(t, "unix"))
	}
	_, err := f.svc.Create(context.Background(), installTestUser, installTestKeyID, "unix")
	requireAppErr(t, err, ErrAITPInstallCodeRateLimited)

	// Every earlier code is still valid until it expires.
	for _, code := range codes {
		require.Equal(t, AITPInstallRedeemOK, f.redeem(code).Outcome)
	}
}

// ---- redeem ------------------------------------------------------------------

func TestAITPInstallCodeRedeem_ScriptsPerShell(t *testing.T) {
	f := newInstallFixture(t)

	res := f.redeem(f.issue(t, "unix"))
	require.Equal(t, AITPInstallRedeemOK, res.Outcome)
	require.Equal(t, renderAITPInstallScript(AITPInstallShellUnix, installTestRoot, installTestKey), res.Script)
	require.Equal(t, installTestUser, res.UserID)
	require.Equal(t, installTestKeyID, res.APIKeyID)

	// The stored shell wins over the User-Agent.
	code := f.issue(t, "powershell")
	res = f.svc.Redeem(context.Background(), strings.ToLower(code), "203.0.113.9", "curl/8.5.0")
	require.Equal(t, AITPInstallRedeemOK, res.Outcome, "codes are case-insensitive")
	require.Equal(t, renderAITPInstallScript(AITPInstallShellPowerShell, installTestRoot, installTestKey), res.Script)

	res = f.redeem(f.issue(t, "cmd"))
	require.Equal(t, AITPInstallRedeemOK, res.Outcome)
	require.Equal(t, AITPInstallShellPowerShell, res.Shell)
}

func TestAITPInstallCodeRedeem_UsesTheCurrentKeyAndRoot(t *testing.T) {
	f := newInstallFixture(t)
	code := f.issue(t, "unix")
	f.keys.update(installTestKeyID, func(k *APIKey) { k.Key = "sk-rotated" })
	f.settings.baseURL = "https://new.example/gw/v1"
	res := f.redeem(code)
	require.Equal(t, AITPInstallRedeemOK, res.Outcome)
	require.Contains(t, res.Script, "--base-url 'https://new.example/gw' --api-key 'sk-rotated'")
	require.NotContains(t, res.Script, installTestKey)
}

func TestAITPInstallCodeRedeem_InvalidCodes(t *testing.T) {
	f := newInstallFixture(t)
	for _, raw := range []string{"", "short", "0123456789AI", "0123456789ABC", "../../etc/pa", "0123456789A\n"} {
		res := f.redeem(raw)
		require.Equal(t, AITPInstallRedeemInvalid, res.Outcome, raw)
		require.Equal(t, "malformed", res.Detail)
		require.Equal(t, renderAITPInstallErrorScript(AITPInstallShellUnix, aitpInstallMessageInvalid), res.Script)
	}
	require.Zero(t, f.store.consumes, "malformed codes never reach Redis")
	require.Empty(t, f.store.counters, "malformed codes never reach Redis")

	res := f.redeem("0123456789AB")
	require.Equal(t, AITPInstallRedeemInvalid, res.Outcome)
	require.Equal(t, "not_found", res.Detail)
	require.Zero(t, res.UserID)

	// Unknown code from PowerShell: the error script is PowerShell (throw, never exit).
	res = f.svc.Redeem(context.Background(), "0123456789AB", "203.0.113.9", "Mozilla/5.0 WindowsPowerShell/5.1")
	require.Equal(t, renderAITPInstallErrorScript(AITPInstallShellPowerShell, aitpInstallMessageInvalid), res.Script)
}

func TestAITPInstallCodeRedeem_SingleUse(t *testing.T) {
	f := newInstallFixture(t)
	code := f.issue(t, "unix")
	require.Equal(t, AITPInstallRedeemOK, f.redeem(code).Outcome)
	again := f.redeem(code)
	require.Equal(t, AITPInstallRedeemInvalid, again.Outcome)
	require.NotContains(t, again.Script, installTestKey)
}

func TestAITPInstallCodeRedeem_ConcurrentDoublePostHasOneWinner(t *testing.T) {
	f := newInstallFixture(t)
	code := f.issue(t, "unix")
	const n = 16
	results := make(chan AITPInstallRedeemResult, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- f.redeem(code)
		}()
	}
	wg.Wait()
	close(results)
	ok := 0
	for res := range results {
		if res.Outcome == AITPInstallRedeemOK {
			ok++
			continue
		}
		require.Equal(t, AITPInstallRedeemInvalid, res.Outcome)
		require.NotContains(t, res.Script, installTestKey)
	}
	require.Equal(t, 1, ok)
}

func TestAITPInstallCodeRedeem_Expired(t *testing.T) {
	f := newInstallFixture(t)
	code := f.issue(t, "unix")
	f.advance(AITPInstallCodeTTL + time.Second)
	res := f.redeem(code)
	require.Equal(t, AITPInstallRedeemInvalid, res.Outcome)
	require.NotContains(t, res.Script, installTestKey)
}

func TestAITPInstallCodeRedeem_RechecksEverythingAfterCreation(t *testing.T) {
	past := time.Now().Add(-time.Minute) // APIKey.IsExpired reads the real clock
	cases := map[string]struct {
		change func(f *installFixture)
		detail string
	}{
		"key disabled": {func(f *installFixture) {
			f.keys.update(installTestKeyID, func(k *APIKey) { k.Status = StatusAPIKeyDisabled })
		}, "key_inactive"},
		"key expired": {func(f *installFixture) { f.keys.update(installTestKeyID, func(k *APIKey) { k.ExpiresAt = &past }) }, "key_inactive"},
		"key deleted": {func(f *installFixture) { delete(f.keys.keys, installTestKeyID) }, "key_not_found"},
		"key owner moved": {func(f *installFixture) {
			f.keys.update(installTestKeyID, func(k *APIKey) { k.UserID = installTestOther })
		}, "key_owner"},
		"key bad format":   {func(f *installFixture) { f.keys.update(installTestKeyID, func(k *APIKey) { k.Key = "sk-'; rm -rf ~" }) }, "key_format"},
		"user disabled":    {func(f *installFixture) { f.users.users[installTestUser].Status = StatusDisabled }, "user_inactive"},
		"user deleted":     {func(f *installFixture) { delete(f.users.users, installTestUser) }, "user_not_found"},
		"setting off":      {func(f *installFixture) { f.settings.enabled = false }, "disabled"},
		"base url removed": {func(f *installFixture) { f.settings.baseURL = "" }, "base_url"},
		"base url unsafe":  {func(f *installFixture) { f.settings.baseURL = "https://ccyou.dev/'$(id)'" }, "base_url"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newInstallFixture(t)
			code := f.issue(t, "powershell")
			tc.change(f)
			res := f.redeem(code)
			require.Equal(t, AITPInstallRedeemUnusable, res.Outcome)
			require.Equal(t, tc.detail, res.Detail)
			require.Equal(t, renderAITPInstallErrorScript(AITPInstallShellPowerShell, aitpInstallMessageUnusable), res.Script)
			require.NotContains(t, res.Script, installTestKey)
			require.False(t, f.store.has(code), "a consumed code is never put back")
		})
	}
}

func TestAITPInstallCodeRedeem_InfrastructureFailuresFailClosed(t *testing.T) {
	boom := errors.New("boom")
	cases := map[string]func(f *installFixture){
		"redis consume": func(f *installFixture) { f.store.consumeErr = boom },
		"redis counter": func(f *installFixture) { f.store.countErr = boom },
		"settings db":   func(f *installFixture) { f.settings.err = boom },
		"key db":        func(f *installFixture) { f.keys.err = boom },
		"user db":       func(f *installFixture) { f.users.err = boom },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			f := newInstallFixture(t)
			code := f.issue(t, "unix")
			breakIt(f)
			res := f.redeem(code)
			require.Equal(t, AITPInstallRedeemUnavailable, res.Outcome)
			require.Equal(t, renderAITPInstallErrorScript(AITPInstallShellUnix, aitpInstallMessageUnavailable), res.Script)
			require.NotContains(t, res.Script, installTestKey)
		})
	}

	// A database failure after the consume does not put the code back.
	f := newInstallFixture(t)
	code := f.issue(t, "unix")
	f.keys.err = boom
	require.Equal(t, AITPInstallRedeemUnavailable, f.redeem(code).Outcome)
	f.keys.err = nil
	require.Equal(t, AITPInstallRedeemInvalid, f.redeem(code).Outcome)
}

func TestAITPInstallCodeRedeem_PerIPLimit(t *testing.T) {
	f := newInstallFixture(t)
	code := f.issue(t, "unix")
	ctx := context.Background()
	for i := 0; i < AITPInstallCodeRedeemLimit; i++ {
		res := f.svc.Redeem(ctx, "0000000000AB", "198.51.100.7", "curl/8")
		require.Equal(t, AITPInstallRedeemInvalid, res.Outcome)
	}
	res := f.svc.Redeem(ctx, code, "198.51.100.7", "curl/8")
	require.Equal(t, AITPInstallRedeemRateLimited, res.Outcome)
	require.Equal(t, renderAITPInstallErrorScript(AITPInstallShellUnix, aitpInstallMessageInvalid), res.Script, "rate limited looks like invalid")
	require.True(t, f.store.has(code), "a rate-limited attempt does not consume the code")

	// Another client IP is not affected.
	require.Equal(t, AITPInstallRedeemOK, f.svc.Redeem(ctx, code, "198.51.100.8", "curl/8").Outcome)
}

// ---- settings ----------------------------------------------------------------

type installSettingRepo struct {
	SettingRepository
	values map[string]string
	err    error
}

func (r *installSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := r.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func TestAITPInstallCodeSetting_PublicAndInjected(t *testing.T) {
	ctx := context.Background()
	for raw, want := range map[string]bool{"": false, "true": true, "false": false, "TRUE": false, "1": false, "yes": false} {
		values := map[string]string{SettingKeyAPIBaseURL: "https://ccyou.dev"}
		if raw != "" {
			values[SettingKeyAITPInstallCodeEnabled] = raw
		}
		svc := NewSettingService(&installSettingRepo{values: values}, &config.Config{})
		pub, err := svc.GetPublicSettings(ctx)
		require.NoError(t, err)
		require.Equal(t, want, pub.AITPInstallCodeEnabled, "%q", raw)

		injected, err := svc.GetPublicSettingsForInjection(ctx)
		require.NoError(t, err)
		data, err := json.Marshal(injected)
		require.NoError(t, err)
		if want {
			require.Contains(t, string(data), `"aitp_install_code_enabled":true`)
		} else {
			require.Contains(t, string(data), `"aitp_install_code_enabled":false`)
		}

		cfg, err := svc.GetAITPInstallCodeSettings(ctx)
		require.NoError(t, err)
		require.Equal(t, want, cfg.Enabled)
		require.Equal(t, "https://ccyou.dev", cfg.APIBaseURL)
	}

	svc := NewSettingService(&installSettingRepo{err: errors.New("db down")}, &config.Config{})
	_, err := svc.GetAITPInstallCodeSettings(ctx)
	require.Error(t, err)
}
