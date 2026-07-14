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
	prompt := flag.String("prompt", "", "Prompt text to use directly")
	version := flag.String("version", "", "Claude Code base version, for example 2.1.80")
	suffixOnly := flag.Bool("suffix-only", false, "Print only the computed 3-hex suffix")
	fullOnly := flag.Bool("full-only", false, "Print only the full cc_version string")
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
	if resolvedPrompt == "" {
		if len(body) == 0 {
			failf("either -prompt or -in is required")
		}
		resolvedPrompt, err = extractFirstUserText(body)
		if err != nil {
			failf("failed to extract first user text: %v", err)
		}
	}

	resolvedVersion := *version
	foundVersion := ""
	foundSuffix := ""
	if len(body) > 0 {
		foundVersion, foundSuffix = extractCCVersionFromBody(body)
		if resolvedVersion == "" {
			resolvedVersion = foundVersion
		}
	}
	if resolvedVersion == "" {
		failf("version is required; pass -version or provide a body containing x-anthropic-billing-header")
	}

	picked := pickCCVersionChars(resolvedPrompt)
	suffix := computeCCVersionSuffix(resolvedPrompt, resolvedVersion)
	full := resolvedVersion + "." + suffix

	if *suffixOnly {
		fmt.Println(suffix)
		return
	}
	if *fullOnly {
		fmt.Println(full)
		return
	}

	if foundVersion != "" {
		fmt.Printf("FOUND_CC_VERSION=%s", foundVersion)
		if foundSuffix != "" {
			fmt.Printf(".%s", foundSuffix)
		}
		fmt.Println()
	}
	fmt.Printf("VERSION=%s\n", resolvedVersion)
	fmt.Printf("PICKED_CHARS=%s\n", picked)
	fmt.Printf("SUFFIX=%s\n", suffix)
	fmt.Printf("CC_VERSION=%s\n", full)
}

func readBody(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func extractFirstUserText(body []byte) (string, error) {
	return claudebilling.ExtractFirstUserText(body)
}

func extractCCVersionFromBody(body []byte) (version string, suffix string) {
	return claudebilling.ExtractCCVersionFromBody(body)
}

func pickCCVersionChars(prompt string) string {
	return claudebilling.PickCCVersionChars(prompt)
}

func computeCCVersionSuffix(prompt, version string) string {
	return claudebilling.ComputeCCVersionSuffix(prompt, version)
}

func failf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
