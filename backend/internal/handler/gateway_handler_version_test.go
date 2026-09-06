package handler

import (
	"strings"
	"testing"
)

func TestClaudeCodeNativeDowngradeCommand(t *testing.T) {
	command := claudeCodeNativeDowngradeInstruction("2.1.258")
	if !strings.Contains(command, "curl -fsSL https://claude.ai/install.sh | bash -s 2.1.258") {
		t.Fatalf("native downgrade command = %q", command)
	}
	if !strings.Contains(command, "export DISABLE_AUTOUPDATER=1") {
		t.Fatalf("native downgrade command must disable auto-updater: %q", command)
	}
	if strings.Contains(command, "npm install") {
		t.Fatalf("native downgrade command must not suggest npm: %q", command)
	}
}
