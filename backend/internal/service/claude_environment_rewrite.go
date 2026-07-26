package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claudebilling"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	claudeEnvironmentHeading = "# Environment"
	claudeRuntimeHeading     = "# Runtime Context"
	claudeRuntimeIntro       = "This session is running with these runtime details"
)

var claudeEnvironmentFieldTemplates = map[string]string{
	"Primary working directory": "Active working folder is %s",
	"Is a git repository":       "Git repository status is %s",
	"Platform":                  "Runtime platform is %s",
	"Shell":                     "Command shell is %s",
	"OS Version":                "Operating system build is %s",
}

type claudeEnvironmentLine struct {
	body string
	eol  string
}

// rewriteClaudeEnvironmentBlock rewrites only the first # Environment section
// in one system text block. Values are preserved; only the heading and labels
// are changed.
func rewriteClaudeEnvironmentBlock(text string) (string, bool) {
	lines := splitClaudeEnvironmentLines(text)
	start := -1
	for i := range lines {
		if strings.TrimSpace(lines[i].body) == claudeEnvironmentHeading {
			start = i
			break
		}
	}
	if start < 0 {
		return text, false
	}

	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimLeft(lines[i].body, " \t"), "# ") {
			end = i
			break
		}
	}

	changed := false
	lines[start].body = claudeRuntimeHeading
	changed = true

	for i := start + 1; i < end; i++ {
		next, ok := rewriteClaudeEnvironmentLine(lines[i].body)
		if ok && next != lines[i].body {
			lines[i].body = next
			changed = true
		}
	}

	if !changed {
		return text, false
	}
	return joinClaudeEnvironmentLines(lines), true
}

// rewriteClaudeEnvironmentInBody rewrites matching text wherever it appears in
// the Anthropic system field. It deliberately does not depend on a fixed block
// index because Claude Code may move the environment block between releases.
func rewriteClaudeEnvironmentInBody(body []byte) ([]byte, bool) {
	system := gjson.GetBytes(body, "system")
	if !system.Exists() {
		return body, false
	}

	if system.Type == gjson.String {
		rewritten, changed := rewriteClaudeEnvironmentBlock(system.String())
		if !changed {
			return body, false
		}
		next, err := sjson.SetBytes(body, "system", rewritten)
		if err != nil {
			return body, false
		}
		return next, true
	}

	if !system.IsArray() {
		return body, false
	}

	out := body
	changed := false
	index := 0
	system.ForEach(func(_, item gjson.Result) bool {
		text := item.Get("text")
		if text.Type == gjson.String {
			rewritten, textChanged := rewriteClaudeEnvironmentBlock(text.String())
			if textChanged {
				path := fmt.Sprintf("system.%d.text", index)
				if next, err := sjson.SetBytes(out, path, rewritten); err == nil {
					out = next
					changed = true
				}
			}
		}
		index++
		return true
	})

	if !changed {
		return body, false
	}
	return out, true
}

// rewriteClaudeEnvironmentIfEnabled applies the selected group's switch and
// repairs a real non-placeholder CCH after changing the body. Missing CCH and
// cch=00000 retain their original compatibility shape.
func (s *GatewayService) rewriteClaudeEnvironmentIfEnabled(ctx context.Context, body []byte, oauthIdentity *oauthRequestIdentity) []byte {
	if !isClaudeEnvironmentRewriteEnabled(ctx) {
		return body
	}

	rewritten, changed := rewriteClaudeEnvironmentInBody(body)
	if !changed {
		return body
	}

	var seed uint64
	var mode claudebilling.CCHInputMode
	if oauthIdentity != nil {
		seed = oauthIdentity.BillingCCHSeed
		mode = oauthIdentity.BillingCCHMode
	}
	return rewriteClaudeBillingHeaderPreservingCCVersionWithMode(rewritten, seed, mode)
}

func isClaudeEnvironmentRewriteEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	group, ok := ctx.Value(ctxkey.Group).(*Group)
	if !ok || !IsGroupContextValid(group) || group.Platform != PlatformAnthropic {
		return false
	}
	return group.ClaudeEnvironmentRewrite
}

func rewriteClaudeEnvironmentLine(line string) (string, bool) {
	leadingLen := len(line) - len(strings.TrimLeft(line, " \t"))
	leading := line[:leadingLen]
	trimmed := line[leadingLen:]

	if trimmed == "You have been invoked in the following environment:" {
		return leading + claudeRuntimeIntro, true
	}
	if !strings.HasPrefix(trimmed, "- ") {
		return line, false
	}

	rest := trimmed[2:]
	if model, ok := parseClaudeEnvironmentModel(rest); ok {
		return leading + "- The active model is " + model + ".", true
	}

	colon := strings.Index(rest, ":")
	if colon < 0 {
		return line, false
	}
	name := strings.TrimSpace(rest[:colon])
	value := strings.TrimPrefix(rest[colon+1:], " ")

	template, ok := claudeEnvironmentFieldTemplates[name]
	if !ok {
		return line, false
	}
	return leading + "- " + fmt.Sprintf(template, value), true
}

func parseClaudeEnvironmentModel(rest string) (string, bool) {
	const prefix = "You are powered by the model "
	if !strings.HasPrefix(rest, prefix) || !strings.HasSuffix(rest, ".") {
		return "", false
	}
	model := strings.TrimSuffix(strings.TrimPrefix(rest, prefix), ".")
	if model == "" {
		return "", false
	}
	return model, true
}

func splitClaudeEnvironmentLines(text string) []claudeEnvironmentLine {
	if text == "" {
		return nil
	}
	var lines []claudeEnvironmentLine
	for len(text) > 0 {
		index := strings.IndexAny(text, "\r\n")
		if index < 0 {
			lines = append(lines, claudeEnvironmentLine{body: text})
			break
		}
		body := text[:index]
		eol := text[index : index+1]
		next := index + 1
		if text[index] == '\r' && next < len(text) && text[next] == '\n' {
			eol = "\r\n"
			next++
		}
		lines = append(lines, claudeEnvironmentLine{body: body, eol: eol})
		text = text[next:]
	}
	return lines
}

func joinClaudeEnvironmentLines(lines []claudeEnvironmentLine) string {
	var builder strings.Builder
	for _, line := range lines {
		_, _ = builder.WriteString(line.body)
		_, _ = builder.WriteString(line.eol)
	}
	return builder.String()
}
