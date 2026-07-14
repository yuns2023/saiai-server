package billingreject

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestDumper(t *testing.T, keep, maxBodyKB int) *Dumper {
	t.Helper()
	return &Dumper{
		enabled:      true,
		dir:          t.TempDir(),
		keep:         keep,
		maxBodyBytes: maxBodyKB * 1024,
	}
}

// TestEnabledOffNoOp 未启用时 Capture 不应落盘任何文件。
func TestEnabledOffNoOp(t *testing.T) {
	d := &Dumper{dir: t.TempDir()}
	d.Capture("req-1", 117, "billing_cch_mismatch", "x", http.Header{"User-Agent": {"claude-cli/2.1.119"}}, []byte(`{"a":1}`))

	entries, err := os.ReadDir(d.dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no files, got %d", len(entries))
	}
}

// TestCaptureWritesFile Capture 写出包含 reason/headers/body 的 JSON 文件。
func TestCaptureWritesFile(t *testing.T) {
	d := newTestDumper(t, 10, 64)
	headers := http.Header{
		"User-Agent":     {"claude-cli/2.1.119"},
		"Authorization":  {"Bearer secret"}, // 应被过滤
		"X-Stainless-OS": {"Linux"},
	}
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	d.Capture("req-abc", 117, "billing_cc_version_mismatch", "observed=462 version=2.1.119", headers, body)

	files := listJSONFiles(t, d.dir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	rec := readRecord(t, filepath.Join(d.dir, files[0]))
	if rec.RequestID != "req-abc" {
		t.Errorf("request_id mismatch: %q", rec.RequestID)
	}
	if rec.AccountID != 117 {
		t.Errorf("account_id mismatch: %d", rec.AccountID)
	}
	if rec.SubReason != "billing_cc_version_mismatch" {
		t.Errorf("sub_reason mismatch: %q", rec.SubReason)
	}
	if rec.Detail != "observed=462 version=2.1.119" {
		t.Errorf("detail mismatch: %q", rec.Detail)
	}
	if _, ok := rec.Headers["Authorization"]; ok {
		t.Errorf("Authorization should be stripped: %v", rec.Headers)
	}
	if got := rec.Headers["User-Agent"]; len(got) == 0 || got[0] != "claude-cli/2.1.119" {
		t.Errorf("User-Agent missing: %v", rec.Headers)
	}
	if got := rec.Headers["X-Stainless-Os"]; len(got) == 0 {
		// http.Header canonicalizes to X-Stainless-Os
		if got := rec.Headers["X-Stainless-OS"]; len(got) == 0 {
			t.Errorf("X-Stainless-OS missing: %v", rec.Headers)
		}
	}
	if rec.BodyTruncated {
		t.Errorf("expected not truncated, body_bytes=%d max=%d", rec.BodyBytes, d.maxBodyBytes)
	}
	if rec.BodyBytes != len(body) {
		t.Errorf("body_bytes mismatch: got %d want %d", rec.BodyBytes, len(body))
	}
	decoded, err := base64.StdEncoding.DecodeString(rec.BodyBase64)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if string(decoded) != string(body) {
		t.Errorf("body roundtrip mismatch: %q vs %q", decoded, body)
	}
}

// TestBodyTruncation 大 body 应截断并标记 BodyTruncated=true。
func TestBodyTruncation(t *testing.T) {
	d := newTestDumper(t, 10, 1) // 1 KB 上限
	body := make([]byte, 4*1024)
	for i := range body {
		body[i] = 'x'
	}
	d.Capture("req-big", 0, "billing_cch_mismatch", "", http.Header{}, body)

	files := listJSONFiles(t, d.dir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	rec := readRecord(t, filepath.Join(d.dir, files[0]))
	if !rec.BodyTruncated {
		t.Fatal("expected BodyTruncated=true")
	}
	if rec.BodyBytes != len(body) {
		t.Errorf("body_bytes should reflect original length: got %d want %d", rec.BodyBytes, len(body))
	}
	if rec.BodyCapturedBytes != 1024 {
		t.Errorf("captured bytes should equal cap: got %d want 1024", rec.BodyCapturedBytes)
	}
}

// TestRingBufferPrune 写入超过 keep 后最老者被删除。
func TestRingBufferPrune(t *testing.T) {
	d := newTestDumper(t, 3, 8)
	for i := 0; i < 6; i++ {
		// 强制不同 mtime —— 文件名前缀也含 unix_ts，但秒级精度可能撞，我们额外
		// sleep 1ms 让 ModTime 严格单调。
		d.Capture("req-"+itoa(i), int64(i), "billing_cch_mismatch", "", http.Header{}, []byte("body"))
		time.Sleep(20 * time.Millisecond)
	}

	files := listJSONFiles(t, d.dir)
	if len(files) != 3 {
		t.Fatalf("expected 3 files after prune, got %d: %v", len(files), files)
	}

	// 剩下的应是最后 3 条 (req-3, req-4, req-5)
	keepSet := map[string]bool{"req-3": false, "req-4": false, "req-5": false}
	for _, name := range files {
		rec := readRecord(t, filepath.Join(d.dir, name))
		if _, ok := keepSet[rec.RequestID]; !ok {
			t.Errorf("unexpected request_id retained: %q", rec.RequestID)
		}
		keepSet[rec.RequestID] = true
	}
	for k, found := range keepSet {
		if !found {
			t.Errorf("expected to find %q in retained files", k)
		}
	}
}

// TestSanitizeFilename 路径分隔符 / 控制字符不应进入文件名。
func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"abc-123":         "abc-123",
		"":                "unknown",
		"   ":             "unknown",
		"foo/bar":         "foo_bar",
		"a:b\\c":          "a_b_c",
		"日本語":             "___",
		"pre.dot_under-1": "pre.dot_under-1",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSensitiveHeaderStripping 各种凭证 header 大小写都要过滤。
func TestSensitiveHeaderStripping(t *testing.T) {
	in := http.Header{
		"Authorization":       {"Bearer x"},
		"Cookie":              {"sid=1"},
		"X-Api-Key":           {"k"},
		"x-api-key":           {"k2"}, // 同 key 混合大小写
		"Proxy-Authorization": {"y"},
		"X-Anthropic-Token":   {"t"},
		"User-Agent":          {"keep"},
	}
	out := sanitizeHeaders(in)
	for _, k := range []string{"Authorization", "Cookie", "X-Api-Key", "x-api-key", "Proxy-Authorization", "X-Anthropic-Token"} {
		if _, ok := out[k]; ok {
			t.Errorf("sensitive header %q should be stripped", k)
		}
	}
	if _, ok := out["User-Agent"]; !ok {
		t.Error("User-Agent should be retained")
	}
}

// TestConcurrentCapture 多 goroutine 同时写入不应 panic 也不应丢失文件，最终
// 落盘数 ≤ keep。验证 mu 保护了 write+prune 串行化。
func TestConcurrentCapture(t *testing.T) {
	d := newTestDumper(t, 50, 8)
	const writers = 16
	const perWriter = 20
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				d.Capture(
					"req-"+itoa(w)+"-"+itoa(i),
					int64(w),
					"billing_cch_mismatch",
					"concurrent",
					http.Header{"User-Agent": {"claude-cli/2.1.119"}},
					[]byte(`{"x":1}`),
				)
			}
		}(w)
	}
	wg.Wait()

	files := listJSONFiles(t, d.dir)
	if len(files) > d.keep {
		t.Fatalf("ring buffer breached: got %d files, keep=%d", len(files), d.keep)
	}
	if len(files) == 0 {
		t.Fatal("no files written under concurrent load")
	}
	// Sanity: every retained file must parse cleanly (no torn writes from rename race).
	for _, name := range files {
		_ = readRecord(t, filepath.Join(d.dir, name))
	}
}

func listJSONFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, e.Name())
		}
	}
	return out
}

func readRecord(t *testing.T, path string) captureRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var rec captureRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return rec
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
