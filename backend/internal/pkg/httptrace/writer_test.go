package httptrace

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRotatedPath 确保 rotated 文件名产出 http-trace.1.jsonl 这种格式。
func TestRotatedPath(t *testing.T) {
	got := rotatedPath("/var/log/http-trace.jsonl", 3)
	if got != "/var/log/http-trace.3.jsonl" {
		t.Fatalf("got %q", got)
	}
}

// TestWriteRotate 构造一个小 maxBytes 的 Writer，连写若干条验证轮转。
func TestWriteRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trace.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	w := &Writer{
		enabled:  true,
		path:     path,
		maxBytes: 128, // 极小阈值，第二条就会触发轮转
		keep:     3,
		file:     f,
	}

	for i := 0; i < 5; i++ {
		w.WritePhase("trace-1", "inbound_request", map[string]any{"i": i, "pad": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"})
	}
	_ = w.file.Close()

	// 当前 jsonl 存在 + 至少 1 个 rotated 文件
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("current jsonl missing: %v", err)
	}
	if _, err := os.Stat(rotatedPath(path, 1)); err != nil {
		t.Fatalf("rotated .1.jsonl missing: %v", err)
	}
}

// TestEnabledOffNoWrite 验证 Enabled=false 时 WritePhase 不落盘（即使 file 非 nil）。
func TestEnabledOffNoWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "off.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	w := &Writer{enabled: false, file: f, path: path, maxBytes: 1 << 30, keep: 2}
	w.WritePhase("t", "inbound_request", map[string]any{"a": 1})
	_ = w.file.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("expected empty file when disabled, got size=%d", info.Size())
	}
}

func TestNewFromEnvUsesConfiguredPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "trace.jsonl")
	t.Setenv(envEnabled, "1")
	t.Setenv(envPath, path)
	t.Setenv(envMaxFileMB, "1")
	t.Setenv(envKeep, "2")

	w := newFromEnv()
	if !w.Enabled() {
		t.Fatal("expected configured trace writer to be enabled")
	}
	if w.path != path {
		t.Fatalf("trace path = %q, want %q", w.path, path)
	}
	if err := w.file.Close(); err != nil {
		t.Fatalf("close trace file: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat trace file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("trace file mode = %o, want 600", info.Mode().Perm())
	}
}
