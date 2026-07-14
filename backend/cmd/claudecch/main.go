package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claudebilling"
)

type cchMatch = claudebilling.CCHMatch

func main() {
	inPath := flag.String("in", "-", "Request body input path, or - for stdin")
	outPath := flag.String("out", "-", "Output path when -replace is used, or - for stdout")
	version := flag.String("version", "", "Claude Code base version; defaults to cc_version found in the input body")
	cchOnly := flag.Bool("cch-only", false, "Print only the computed cch")
	replace := flag.Bool("replace", false, "Replace cch in the input body and write the updated body")
	flag.Parse()

	body, err := readBody(*inPath)
	if err != nil {
		failf("failed to read input: %v", err)
	}

	normalized, match, err := normalizeBodyForCCH(body)
	if err != nil {
		failf("failed to normalize request body: %v", err)
	}

	resolvedVersion := *version
	if resolvedVersion == "" {
		resolvedVersion, _ = claudebilling.ExtractCCVersionFromBody(body)
	}
	seed, mode := claudebilling.CCHProfileForCCVersion(resolvedVersion)
	sum, cch := claudebilling.ComputeCCHWithProfile(normalized, seed, mode)

	if *replace {
		replaced := replaceCCH(normalized, match, cch)
		if err := writeBody(*outPath, replaced); err != nil {
			failf("failed to write output body: %v", err)
		}
		return
	}

	if *cchOnly {
		fmt.Println(cch)
		return
	}

	fmt.Printf("FOUND_CCH=%s\n", match.Value)
	fmt.Printf("NORMALIZED=%t\n", match.Value != "00000")
	if resolvedVersion != "" {
		fmt.Printf("CC_VERSION=%s\n", resolvedVersion)
	}
	fmt.Printf("CCH_SEED=0x%016x\n", seed)
	fmt.Printf("CCH_INPUT_MODE=%s\n", mode)
	fmt.Printf("XXH64=0x%016x\n", sum)
	fmt.Printf("CCH=%s\n", cch)
}

func readBody(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func writeBody(path string, body []byte) error {
	if path == "-" {
		_, err := os.Stdout.Write(body)
		return err
	}
	return os.WriteFile(path, body, 0o644)
}

func normalizeBodyForCCH(body []byte) ([]byte, cchMatch, error) {
	return claudebilling.NormalizeBodyForCCH(body)
}

func computeCCH(normalizedBody []byte) (uint64, string) {
	return claudebilling.ComputeCCH(normalizedBody)
}

func computeCCHWithSeed(normalizedBody []byte, seed uint64) (uint64, string) {
	return claudebilling.ComputeCCHWithSeed(normalizedBody, seed)
}

func replaceCCH(normalizedBody []byte, match cchMatch, cch string) []byte {
	return claudebilling.ReplaceCCH(normalizedBody, match, cch)
}

func failf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
