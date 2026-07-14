// Package extracerts loads additional trusted CA certificates for outbound HTTPS.
package extracerts

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	EnvExtraCAFile   = "SAIAI_EXTRA_CA_FILE"
	EnvExtraCADir    = "SAIAI_EXTRA_CA_DIR"
	EnvReClaudeCADir = "SAIAI_RECLAUDE_CA_DIR"
	reClaudeDirName  = ".reclaude"
	maxCertScanDepth = 3
	maxCertFileBytes = 4 * 1024 * 1024
)

// Options controls certificate loading. Empty options load only the system pool.
type Options struct {
	BasePool *x509.CertPool
	Files    []string
	Dirs     []string
}

// Result is the result of loading extra certificates.
type Result struct {
	Pool      *x509.CertPool
	CertCount int
	Warnings  []error
}

var (
	defaultOnce   sync.Once
	defaultResult Result
)

// TLSClientConfig returns a TLS config with additional CA certificates loaded.
// It returns nil when no extra certificates were loaded so callers keep Go's
// default system trust behavior.
func TLSClientConfig() *tls.Config {
	pool, count := RootCAs()
	if count == 0 {
		return nil
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// RootCAs returns the process-wide CA pool plus the number of extra certs added.
func RootCAs() (*x509.CertPool, int) {
	defaultOnce.Do(func() {
		defaultResult = Load(DefaultOptions())
		for _, warning := range defaultResult.Warnings {
			slog.Warn("extra_ca_load_warning", "error", warning)
		}
		if defaultResult.CertCount > 0 {
			slog.Info("extra_ca_loaded", "cert_count", defaultResult.CertCount)
		}
	})
	if defaultResult.CertCount == 0 {
		return nil, 0
	}
	return defaultResult.Pool, defaultResult.CertCount
}

// DefaultOptions builds the standard production search path:
// explicit env paths first, then common service/container .reclaude paths.
func DefaultOptions() Options {
	opts := Options{
		Files: splitPathList(os.Getenv(EnvExtraCAFile)),
		Dirs:  splitPathList(os.Getenv(EnvExtraCADir)),
	}
	if reClaudeDir := strings.TrimSpace(os.Getenv(EnvReClaudeCADir)); reClaudeDir != "" {
		opts.Dirs = append(opts.Dirs, splitPathList(reClaudeDir)...)
	} else if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		opts.Dirs = append(opts.Dirs, filepath.Join(home, reClaudeDirName))
	}
	opts.Dirs = append(opts.Dirs,
		// Production containers often run as root but os.UserHomeDir can be
		// unavailable in minimal images. Keep these explicit fallbacks so the
		// mounted/copied ReClaude CA is still picked up.
		filepath.Join(string(filepath.Separator), "root", reClaudeDirName),
		filepath.Join(string(filepath.Separator), "app", reClaudeDirName),
	)
	opts.Files = uniquePaths(opts.Files)
	opts.Dirs = uniquePaths(opts.Dirs)
	return opts
}

// Load creates a CA pool and appends certificates found in files and directories.
func Load(opts Options) Result {
	pool := opts.BasePool
	if pool != nil {
		pool = pool.Clone()
	} else {
		var err error
		pool, err = x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
	}

	result := Result{Pool: pool}
	seen := make(map[[32]byte]struct{})
	for _, file := range opts.Files {
		count, warnings := loadCertFile(pool, file, seen)
		result.CertCount += count
		result.Warnings = append(result.Warnings, warnings...)
	}
	for _, dir := range opts.Dirs {
		count, warnings := loadCertDir(pool, dir, seen)
		result.CertCount += count
		result.Warnings = append(result.Warnings, warnings...)
	}
	return result
}

func splitPathList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := filepath.SplitList(raw)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func loadCertDir(pool *x509.CertPool, dir string, seen map[[32]byte]struct{}) (int, []error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return 0, nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, []error{fmt.Errorf("stat extra CA dir %s: %w", dir, err)}
	}
	if !info.IsDir() {
		return 0, []error{fmt.Errorf("extra CA dir is not a directory: %s", dir)}
	}

	files, warnings := collectCandidateCertFiles(dir, 0)
	sort.Strings(files)
	total := 0
	for _, file := range files {
		count, fileWarnings := loadCertFile(pool, file, seen)
		total += count
		warnings = append(warnings, fileWarnings...)
	}
	return total, warnings
}

func collectCandidateCertFiles(dir string, depth int) ([]string, []error) {
	if depth > maxCertScanDepth {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []error{fmt.Errorf("read extra CA dir %s: %w", dir, err)}
	}

	var files []string
	var warnings []error
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			nested, nestedWarnings := collectCandidateCertFiles(path, depth+1)
			files = append(files, nested...)
			warnings = append(warnings, nestedWarnings...)
			continue
		}
		info, err := entry.Info()
		if err != nil {
			warnings = append(warnings, fmt.Errorf("stat extra CA candidate %s: %w", path, err))
			continue
		}
		if info.Mode().IsRegular() {
			files = append(files, path)
		}
	}
	return files, warnings
}

func uniquePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		cleaned := filepath.Clean(path)
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	return out
}

func loadCertFile(pool *x509.CertPool, path string, seen map[[32]byte]struct{}) (int, []error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, []error{fmt.Errorf("extra CA file not found: %s", path)}
		}
		return 0, []error{fmt.Errorf("stat extra CA file %s: %w", path, err)}
	}
	if !info.Mode().IsRegular() {
		return 0, nil
	}
	if info.Size() > maxCertFileBytes {
		return 0, []error{fmt.Errorf("extra CA file too large: %s", path)}
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, []error{fmt.Errorf("read extra CA file %s: %w", path, err)}
	}
	return appendCertsFromPEM(pool, raw, path, seen)
}

func appendCertsFromPEM(pool *x509.CertPool, raw []byte, source string, seen map[[32]byte]struct{}) (int, []error) {
	count := 0
	var warnings []error
	for {
		block, rest := pem.Decode(raw)
		if block == nil {
			break
		}
		raw = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			warnings = append(warnings, fmt.Errorf("parse certificate in %s: %w", source, err))
			continue
		}
		sum := sha256.Sum256(block.Bytes)
		if _, ok := seen[sum]; ok {
			continue
		}
		seen[sum] = struct{}{}
		pool.AddCert(cert)
		count++
	}
	return count, warnings
}
