package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claudebilling"
)

func main() {
	inPath := flag.String("in", "", "Request body input path, or - for stdin")
	outPath := flag.String("out", "-", "Output path when -replace-body is used, or - for stdout")
	prompt := flag.String("prompt", "", "Prompt text to use directly when no body extraction is desired")
	version := flag.String("version", "", "Claude Code base version, for example 2.1.80")
	entrypoint := flag.String("entrypoint", "", "Billing header entrypoint, defaults to extracted value or sdk-cli")
	headerOnly := flag.Bool("header-only", false, "Print only the computed billing header")
	replaceBody := flag.Bool("replace-body", false, "Rewrite the input body with corrected cc_version and cch")
	flag.Parse()

	var body []byte
	var err error
	if *inPath != "" {
		body, err = readBody(*inPath)
		if err != nil {
			failf("failed to read input: %v", err)
		}
	}

	resolvedPrompt := *prompt
	if resolvedPrompt == "" && len(body) > 0 {
		resolvedPrompt, err = claudebilling.ExtractFirstUserText(body)
		if err != nil {
			failf("failed to extract first user text: %v", err)
		}
	}
	if resolvedPrompt == "" {
		failf("prompt is required; pass -prompt or provide -in with a request body")
	}

	foundVersion := ""
	foundSuffix := ""
	if len(body) > 0 {
		foundVersion, foundSuffix = claudebilling.ExtractCCVersionFromBody(body)
	}
	resolvedVersion := *version
	if resolvedVersion == "" {
		resolvedVersion = foundVersion
	}
	if resolvedVersion == "" {
		failf("version is required; pass -version or provide a body containing x-anthropic-billing-header")
	}

	resolvedEntrypoint := *entrypoint
	if resolvedEntrypoint == "" && len(body) > 0 {
		if extracted, ok := claudebilling.ExtractCCEntrypointFromBody(body); ok {
			resolvedEntrypoint = extracted
		}
	}
	if resolvedEntrypoint == "" {
		resolvedEntrypoint = "sdk-cli"
	}

	suffix := claudebilling.ComputeCCVersionSuffix(resolvedPrompt, resolvedVersion)

	if len(body) == 0 {
		header := claudebilling.BuildHeader(resolvedVersion, suffix, resolvedEntrypoint, "00000")
		if *headerOnly {
			fmt.Println(header)
			return
		}
		fmt.Printf("VERSION=%s\n", resolvedVersion)
		fmt.Printf("PICKED_CHARS=%s\n", claudebilling.PickCCVersionChars(resolvedPrompt))
		fmt.Printf("SUFFIX=%s\n", suffix)
		fmt.Printf("CC_VERSION=%s.%s\n", resolvedVersion, suffix)
		fmt.Printf("ENTRYPOINT=%s\n", resolvedEntrypoint)
		fmt.Printf("CCH_UNAVAILABLE=true\n")
		fmt.Printf("BILLING_HEADER_WITH_PLACEHOLDER=%s\n", header)
		return
	}

	bodyWithVersion, err := claudebilling.ReplaceCCVersion(body, resolvedVersion, suffix)
	if err != nil {
		failf("failed to update cc_version in body: %v", err)
	}
	normalizedBody, match, err := claudebilling.NormalizeBodyForCCH(bodyWithVersion)
	if err != nil {
		failf("failed to normalize cch in body: %v", err)
	}
	seed, mode := claudebilling.CCHProfileForCCVersion(resolvedVersion)
	sum, cch := claudebilling.ComputeCCHWithProfile(normalizedBody, seed, mode)
	header := claudebilling.BuildHeader(resolvedVersion, suffix, resolvedEntrypoint, cch)

	if *replaceBody {
		finalBody := claudebilling.ReplaceCCH(normalizedBody, match, cch)
		if err := writeBody(*outPath, finalBody); err != nil {
			failf("failed to write output body: %v", err)
		}
		return
	}

	if *headerOnly {
		fmt.Println(header)
		return
	}

	if foundVersion != "" {
		fmt.Printf("FOUND_CC_VERSION=%s", foundVersion)
		if foundSuffix != "" {
			fmt.Printf(".%s", foundSuffix)
		}
		fmt.Println()
	}
	fmt.Printf("FOUND_CCH=%s\n", match.Value)
	fmt.Printf("NORMALIZED_CCH=%t\n", match.Value != "00000")
	fmt.Printf("VERSION=%s\n", resolvedVersion)
	fmt.Printf("PICKED_CHARS=%s\n", claudebilling.PickCCVersionChars(resolvedPrompt))
	fmt.Printf("SUFFIX=%s\n", suffix)
	fmt.Printf("CC_VERSION=%s.%s\n", resolvedVersion, suffix)
	fmt.Printf("ENTRYPOINT=%s\n", resolvedEntrypoint)
	fmt.Printf("CCH_SEED=0x%016x\n", seed)
	fmt.Printf("CCH_INPUT_MODE=%s\n", mode)
	fmt.Printf("XXH64=0x%016x\n", sum)
	fmt.Printf("CCH=%s\n", cch)
	fmt.Printf("BILLING_HEADER=%s\n", header)
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

func failf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
