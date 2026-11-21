package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestPiToolFreeTurnDoesNotInheritExtensionsOrShellTools(t *testing.T) {
	workDir := t.TempDir()
	bin := writeFakePi(t, t.TempDir(), `#!/bin/sh
printf '%s\n' "$*" > pi-argv.txt
cat > /dev/null
printf '%s\n' '{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"{\"ok\":true}"}]}]}'
`, strings.Join([]string{
		"@echo off",
		"echo %* > pi-argv.txt",
		"more > nul",
		"echo {\"type\":\"agent_end\",\"messages\":[{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"{\\\"ok\\\":true}\"}]}]}",
	}, "\r\n"))
	pi, err := New(types.AgentPi, bin, []string{
		"--extension", "user-extension.ts", "--tools", "bash", "--model", "chosen-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	ag := WithSteering(pi, workDir)
	res, err := ag.Run(context.Background(), RunOpts{
		Prompt: "only rank existing evidence", NoTools: true, CWD: workDir,
		JSONSchema: json.RawMessage(`{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Output) != `{"ok":true}` {
		t.Fatalf("unexpected response: %s", res.Output)
	}
	argv, err := os.ReadFile(filepath.Join(workDir, "pi-argv.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := "--mode json --no-session --no-tools --no-extensions --no-mcp --no-skills --no-context-files --no-prompt-templates --model chosen-model"
	if got := strings.TrimSpace(string(argv)); got != want {
		t.Fatalf("Pi argv = %q, want %q", got, want)
	}
}

func TestAgentToolFreeTurnDoesNotLaunchAnUnverifiedAdapter(t *testing.T) {
	dir := t.TempDir()
	codex, err := New(types.AgentCodex, filepath.Join(dir, "missing-codex"), nil)
	if err != nil {
		t.Fatal(err)
	}
	claude, err := New(types.AgentClaude, filepath.Join(dir, "missing-claude"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ag := NewFallback([]Agent{WithSteering(codex, dir), WithSteering(claude, dir)})
	if _, err := ag.Run(context.Background(), RunOpts{NoTools: true, CWD: dir}); !errors.Is(err, ErrToolFreeAgentUnavailable) {
		t.Fatalf("want both adapters refused before launch, got %v", err)
	}
}
