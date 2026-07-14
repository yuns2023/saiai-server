// Package billingreject 落盘 carpool billing 严格校验失败时的请求现场，
// 用于离线分析客户端 cc_version / cch fingerprint 算法漂移。
//
// 设计目标：trace-on-error，比 SAIAI_HTTP_TRACE 全量抓包窄一个量级 ——
// 只在 validateCarpoolBillingIntegrity 决定 reject 时落盘，量小、隐私边界小、
// 可常驻生产。未启用（SAIAI_BILLING_REJECT_CAPTURE 未设）时所有调用走零开销
// no-op 快路径。
//
// 落盘文件：${SAIAI_BILLING_REJECT_DIR}/<unix_ts>-<request_id>.json，
// 0600 权限，目录 0700。文件结构包含 reason / detail / sanitized headers /
// base64 body，body 超 SAIAI_BILLING_REJECT_MAX_BODY_KB 截断并标记。
//
// Ring buffer：每次 capture 后扫目录按 mtime 排序，超过
// SAIAI_BILLING_REJECT_KEEP（默认 200）即删最老者。reject 是低频事件，
// 目录扫描开销可接受；用 mtime 排序而非进程内索引，重启不丢状态。
package billingreject

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	envEnabled    = "SAIAI_BILLING_REJECT_CAPTURE"
	envDir        = "SAIAI_BILLING_REJECT_DIR"
	envKeep       = "SAIAI_BILLING_REJECT_KEEP"
	envMaxBodyKB  = "SAIAI_BILLING_REJECT_MAX_BODY_KB"
	defaultDir    = "/app/data/billing-rejects"
	defaultKeep   = 200
	defaultMaxKB  = 512
	tmpFilePrefix = ".billing-reject-"
	tmpFileSuffix = ".tmp"
	finalSuffix   = ".json"
)

// 黑名单：凭证类 header 不落盘。其余（含 X-Stainless-* / x-anthropic-billing-header /
// User-Agent / Content-Type / x-request-id 等）保留以供 fingerprint 离线复算。
// 当前 saiai gateway 实际入站只见 Authorization / x-api-key / x-anthropic-* 几个，
// 其余 (X-Auth-Token / Cf-Access-Jwt-Assertion 等) 是边界条件防御性补充。
var sensitiveHeaders = map[string]struct{}{
	"authorization":                 {},
	"proxy-authorization":           {},
	"cookie":                        {},
	"set-cookie":                    {},
	"x-api-key":                     {},
	"api-key":                       {},
	"x-auth-token":                  {},
	"x-session-token":               {},
	"x-anthropic-token":             {},
	"x-anthropic-api-key":           {},
	"x-claude-access-token":         {},
	"x-forwarded-access-token":      {},
	"cf-access-jwt-assertion":       {},
	"x-amzn-remapped-authorization": {},
}

var (
	defaultOnce sync.Once
	defaultInst *Dumper
)

// Dumper 是 reject 现场落盘器。未启用时 Enabled() 返回 false，所有 Capture 走 no-op。
type Dumper struct {
	enabled      bool
	dir          string
	keep         int
	maxBodyBytes int

	mu sync.Mutex
}

type captureRecord struct {
	CapturedAt        string              `json:"captured_at"`
	UnixTs            int64               `json:"unix_ts"`
	RequestID         string              `json:"request_id"`
	AccountID         int64               `json:"account_id"`
	SubReason         string              `json:"sub_reason"`
	Detail            string              `json:"detail,omitempty"`
	Headers           map[string][]string `json:"headers"`
	BodyEncoding      string              `json:"body_encoding"`
	BodyBase64        string              `json:"body_base64"`
	BodyBytes         int                 `json:"body_bytes"`
	BodyCapturedBytes int                 `json:"body_captured_bytes"`
	BodyTruncated     bool                `json:"body_truncated"`
}

// Default 返回进程单例 Dumper。首次调用时读取环境变量决定是否启用。
func Default() *Dumper {
	defaultOnce.Do(func() {
		defaultInst = newFromEnv()
	})
	return defaultInst
}

func newFromEnv() *Dumper {
	if strings.TrimSpace(os.Getenv(envEnabled)) != "1" {
		return &Dumper{}
	}

	d := &Dumper{
		enabled:      true,
		dir:          getenvDefault(envDir, defaultDir),
		keep:         getenvPositiveInt(envKeep, defaultKeep),
		maxBodyBytes: getenvPositiveInt(envMaxBodyKB, defaultMaxKB) * 1024,
	}
	log.Printf("billingreject: enabled dir=%s keep=%d max_body_kb=%d", d.dir, d.keep, d.maxBodyBytes/1024)
	return d
}

// Enabled 当且仅当 SAIAI_BILLING_REJECT_CAPTURE=1 时为 true。
func (d *Dumper) Enabled() bool {
	return d != nil && d.enabled
}

// Capture 同步落盘一条 reject 现场。失败仅 log warning，不会阻塞调用方。
func (d *Dumper) Capture(
	requestID string,
	accountID int64,
	subReason string,
	detail string,
	headers http.Header,
	body []byte,
) {
	if !d.Enabled() {
		return
	}

	now := time.Now().UTC()
	captured := body
	truncated := false
	if d.maxBodyBytes > 0 && len(captured) > d.maxBodyBytes {
		captured = captured[:d.maxBodyBytes]
		truncated = true
	}

	record := captureRecord{
		CapturedAt:        now.Format(time.RFC3339Nano),
		UnixTs:            now.Unix(),
		RequestID:         requestID,
		AccountID:         accountID,
		SubReason:         subReason,
		Detail:            detail,
		Headers:           sanitizeHeaders(headers),
		BodyEncoding:      "base64",
		BodyBase64:        base64.StdEncoding.EncodeToString(captured),
		BodyBytes:         len(body),
		BodyCapturedBytes: len(captured),
		BodyTruncated:     truncated,
	}

	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		log.Printf("billingreject: marshal failed request_id=%q err=%v", requestID, err)
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if err := os.MkdirAll(d.dir, 0o700); err != nil {
		log.Printf("billingreject: mkdir failed dir=%q err=%v", d.dir, err)
		return
	}

	finalName := fmt.Sprintf("%d-%s%s", now.Unix(), sanitizeFilename(requestID), finalSuffix)
	finalPath := filepath.Join(d.dir, finalName)

	if err := writeAtomic(d.dir, finalPath, payload); err != nil {
		log.Printf("billingreject: write failed path=%q request_id=%q err=%v", finalPath, requestID, err)
		return
	}

	if err := d.pruneLocked(); err != nil {
		log.Printf("billingreject: prune failed dir=%q err=%v", d.dir, err)
	}
}

// writeAtomic 通过 tmp + rename 原子写入，确保 prune 不会看到半成品文件。
func writeAtomic(dir, finalPath string, data []byte) error {
	tmp, err := os.CreateTemp(dir, tmpFilePrefix+"*"+tmpFileSuffix)
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()

	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// pruneLocked 删除超出 keep 的最老 .json 文件。调用方需持有 d.mu。
func (d *Dumper) pruneLocked() error {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return err
	}

	type candidate struct {
		name    string
		modTime time.Time
	}
	files := make([]candidate, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), finalSuffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, candidate{name: e.Name(), modTime: info.ModTime()})
	}

	if len(files) <= d.keep {
		return nil
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].name < files[j].name
		}
		return files[i].modTime.Before(files[j].modTime)
	})

	for _, f := range files[:len(files)-d.keep] {
		if err := os.Remove(filepath.Join(d.dir, f.name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func sanitizeHeaders(h http.Header) map[string][]string {
	if len(h) == 0 {
		return map[string][]string{}
	}
	out := make(map[string][]string, len(h))
	for k, v := range h {
		if _, drop := sensitiveHeaders[strings.ToLower(strings.TrimSpace(k))]; drop {
			continue
		}
		dup := make([]string, len(v))
		copy(dup, v)
		out[k] = dup
	}
	return out
}

// sanitizeFilename 只保留 [A-Za-z0-9._-]，其余替换为 _。空 ID fallback "unknown"。
func sanitizeFilename(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			_, _ = b.WriteRune(r)
		default:
			_ = b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func getenvDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func getenvPositiveInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		log.Printf("billingreject: invalid %s=%q, using fallback=%d", key, raw, fallback)
		return fallback
	}
	return n
}
