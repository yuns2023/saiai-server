package main

import (
	"archive/zip"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claudebilling"
	"github.com/tidwall/gjson"
)

var (
	methodPattern          = regexp.MustCompile(`(?m)^Method: ([^\n]+)$`)
	hostPattern            = regexp.MustCompile(`(?m)^Host: ([^\n]+)$`)
	filePattern            = regexp.MustCompile(`(?m)^File: ([^\n]+)$`)
	requestBodySizePattern = regexp.MustCompile(`(?m)^Request-Body-Size: ([0-9]+)$`)
	requestBodyPattern     = regexp.MustCompile(`(?s)Request-Body:<<--EOF-[^\n]+\n(.*?)\n--EOF-[^\n]*`)
)

type requestEntry struct {
	Source   string
	Index    int
	Method   string
	Host     string
	File     string
	Body     []byte
	BodySize int
}

type analysisResult struct {
	Model             string
	Deferred          bool
	HasBilling        bool
	FoundCCVersion    string
	ComputedCCVersion string
	CCVersionMatch    bool
	CCHSeed           uint64
	CCHInputMode      claudebilling.CCHInputMode
	FoundCCH          string
	ComputedCCH       string
	CCHMatch          bool
	Err               error
}

func main() {
	inPath := flag.String("in", "", "Charles .trace or .chlz input path")
	format := flag.String("format", "auto", "Input format: auto, trace, or chlz")
	outDir := flag.String("outdir", "", "Optional output directory for extracted raw request bodies")
	includeNoBilling := flag.Bool("include-no-billing", false, "Include /v1/messages requests that do not contain x-anthropic-billing-header")
	flag.Parse()

	if *inPath == "" {
		failf("-in is required")
	}

	entries, detectedFormat, err := loadEntries(*inPath, *format)
	if err != nil {
		failf("failed to load entries: %v", err)
	}
	if len(entries) == 0 {
		failf("no POST /v1/messages entries found")
	}

	if *outDir != "" {
		if err := os.MkdirAll(*outDir, 0o755); err != nil {
			failf("failed to create outdir: %v", err)
		}
	}

	shown := 0
	skippedNoBilling := 0
	fmt.Printf("FORMAT=%s\n", detectedFormat)
	fmt.Printf("TOTAL=%d\n", len(entries))

	for _, entry := range entries {
		result := analyzeEntry(entry.Body)
		if !result.HasBilling && !*includeNoBilling {
			skippedNoBilling++
			continue
		}
		shown++

		fmt.Printf("\n[%d] %s#%d %s bytes=%d", shown, entry.Source, entry.Index, entry.File, entry.BodySize)
		if result.Model != "" {
			fmt.Printf(" model=%s", result.Model)
		}
		if result.Deferred {
			fmt.Printf(" deferred=true")
		}
		fmt.Println()

		if *outDir != "" {
			outPath := filepath.Join(*outDir, fmt.Sprintf("%s-%d.json", entry.Source, entry.Index))
			if err := os.WriteFile(outPath, entry.Body, 0o644); err != nil {
				failf("failed to write %s: %v", outPath, err)
			}
			fmt.Printf("  out=%s\n", outPath)
		}

		if !result.HasBilling {
			fmt.Printf("  billing=false\n")
			continue
		}

		if result.Err != nil {
			fmt.Printf("  error=%v\n", result.Err)
			continue
		}

		fmt.Printf("  cc_version found=%s computed=%s match=%t\n", result.FoundCCVersion, result.ComputedCCVersion, result.CCVersionMatch)
		fmt.Printf("  cch found=%s computed=%s seed=0x%016x mode=%s match=%t\n", result.FoundCCH, result.ComputedCCH, result.CCHSeed, result.CCHInputMode, result.CCHMatch)
	}

	fmt.Printf("\nSHOWN=%d\n", shown)
	fmt.Printf("SKIPPED_NO_BILLING=%d\n", skippedNoBilling)
}

func loadEntries(path string, requestedFormat string) ([]requestEntry, string, error) {
	format, err := detectFormat(path, requestedFormat)
	if err != nil {
		return nil, "", err
	}
	switch format {
	case "trace":
		entries, err := parseTrace(path)
		return entries, format, err
	case "chlz":
		entries, err := parseCHLZ(path)
		return entries, format, err
	default:
		return nil, "", fmt.Errorf("unsupported format %q", format)
	}
}

func detectFormat(path string, requested string) (string, error) {
	if requested != "" && requested != "auto" {
		switch requested {
		case "trace", "chlz":
			return requested, nil
		default:
			return "", fmt.Errorf("unsupported format %q", requested)
		}
	}

	switch strings.ToLower(filepath.Ext(path)) {
	case ".trace":
		return "trace", nil
	case ".chlz":
		return "chlz", nil
	}

	header := make([]byte, 64)
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = f.Close()
	}()
	n, err := f.Read(header)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	header = header[:n]
	switch {
	case bytes.HasPrefix(header, []byte("HTTP-Trace-Version:")):
		return "trace", nil
	case bytes.HasPrefix(header, []byte("PK\x03\x04")):
		return "chlz", nil
	default:
		return "", fmt.Errorf("could not detect input format for %s", path)
	}
}

func parseTrace(path string) ([]requestEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	parts := strings.Split(text, "\n\nMethod: ")
	var entries []requestEntry
	for i, part := range parts {
		if i == 0 {
			if !strings.HasPrefix(part, "Method: ") {
				continue
			}
		} else {
			part = "Method: " + part
		}

		method := firstMatch(methodPattern, part)
		host := firstMatch(hostPattern, part)
		file := firstMatch(filePattern, part)
		if method != "POST" || host != "api.anthropic.com" || !strings.HasPrefix(file, "/v1/messages") {
			continue
		}

		bodyText := firstMatch(requestBodyPattern, part)
		if bodyText == "" {
			continue
		}
		bodySize := atoi(firstMatch(requestBodySizePattern, part))

		entries = append(entries, requestEntry{
			Source:   "trace",
			Index:    i,
			Method:   method,
			Host:     host,
			File:     file,
			Body:     []byte(bodyText),
			BodySize: bodySize,
		})
	}
	return entries, nil
}

func parseCHLZ(path string) ([]requestEntry, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = zr.Close()
	}()

	fileMap := make(map[string]*zip.File, len(zr.File))
	indices := make([]int, 0, len(zr.File))
	for _, f := range zr.File {
		fileMap[f.Name] = f
		if strings.HasSuffix(f.Name, "-meta.json") {
			idxText := strings.TrimSuffix(f.Name, "-meta.json")
			if idx, err := strconv.Atoi(idxText); err == nil {
				indices = append(indices, idx)
			}
		}
	}
	sort.Ints(indices)

	var entries []requestEntry
	for _, idx := range indices {
		metaFile := fileMap[fmt.Sprintf("%d-meta.json", idx)]
		reqFile := fileMap[fmt.Sprintf("%d-req.json", idx)]
		if metaFile == nil || reqFile == nil {
			continue
		}
		metaBytes, err := readZipFile(metaFile)
		if err != nil {
			return nil, err
		}
		method := gjson.GetBytes(metaBytes, "method").String()
		host := gjson.GetBytes(metaBytes, "host").String()
		file := gjson.GetBytes(metaBytes, "path").String()
		if file == "" {
			file = gjson.GetBytes(metaBytes, "file").String()
		}
		if file == "" {
			file = gjson.GetBytes(metaBytes, "url").String()
		}
		if method != "POST" || host != "api.anthropic.com" || !strings.HasPrefix(file, "/v1/messages") {
			continue
		}
		body, err := readZipFile(reqFile)
		if err != nil {
			return nil, err
		}
		bodySize := int(gjson.GetBytes(metaBytes, "requestBodySize").Int())
		if bodySize == 0 {
			bodySize = len(body)
		}
		entries = append(entries, requestEntry{
			Source:   "chlz",
			Index:    idx,
			Method:   method,
			Host:     host,
			File:     file,
			Body:     body,
			BodySize: bodySize,
		})
	}
	return entries, nil
}

func analyzeEntry(body []byte) analysisResult {
	result := analysisResult{
		Model:    gjson.GetBytes(body, "model").String(),
		Deferred: bytes.Contains(body, []byte("<available-deferred-tools>")),
	}

	foundVersion, foundSuffix := claudebilling.ExtractCCVersionFromBody(body)
	normalized, match, err := claudebilling.NormalizeBodyForCCH(body)
	if foundVersion == "" || foundSuffix == "" || err != nil {
		return result
	}
	result.HasBilling = true

	prompt, err := claudebilling.ExtractFirstUserText(body)
	if err != nil {
		result.Err = err
		return result
	}

	computedSuffix := claudebilling.ComputeCCVersionSuffix(prompt, foundVersion)
	computedVersion := fmt.Sprintf("%s.%s", foundVersion, computedSuffix)
	result.FoundCCVersion = fmt.Sprintf("%s.%s", foundVersion, foundSuffix)
	result.ComputedCCVersion = computedVersion
	result.CCVersionMatch = result.FoundCCVersion == computedVersion

	seed, mode := claudebilling.CCHProfileForCCVersion(foundVersion)
	_, cch := claudebilling.ComputeCCHWithProfile(normalized, seed, mode)
	result.FoundCCH = match.Value
	result.CCHSeed = seed
	result.CCHInputMode = mode
	result.ComputedCCH = cch
	result.CCHMatch = result.FoundCCH == result.ComputedCCH
	return result
}

func readZipFile(file *zip.File) ([]byte, error) {
	rc, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rc.Close()
	}()
	return io.ReadAll(rc)
}

func firstMatch(pattern *regexp.Regexp, text string) string {
	match := pattern.FindStringSubmatch(text)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func failf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
