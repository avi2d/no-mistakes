package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiAgent_BridgedModelsUseThePromptInlinedSchema(t *testing.T) {
	for name, extra := range map[string][]string{
		"provider flag":   {"--provider", "claude-bridge", "--model", "claude-bridge/claude-opus-5-5"},
		"provider inline": {"--provider=claude-bridge"},
		"model flag":      {"--model", "claude-bridge/claude-opus-5-5"},
		"model inline":    {"--model=claude-bridge/claude-opus-5-5"},
	} {
		t.Run(name, func(t *testing.T) {
			cwd := t.TempDir()
			bin := writePiOutputFixture(t, piStrictVersion, piOutputEvents(`{"ok":true}`), piTextEvents(`{"ok":true}`), "")
			var logged []LifecycleEvent
			result, err := (&piAgent{bin: bin, extraArgs: extra}).Run(context.Background(), RunOpts{
				Prompt: "review", CWD: cwd,
				JSONSchema:  json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`),
				OnLifecycle: piOutputPathMessages(&logged),
			})
			if err != nil {
				t.Fatal(err)
			}
			if string(result.Output) != `{"ok":true}` {
				t.Fatalf("output: %s", result.Output)
			}
			argv, err := os.ReadFile(filepath.Join(cwd, "pi-argv.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(argv), "--extension") {
				t.Fatalf("bridged model was given the output extension: %q", argv)
			}
			if len(logged) != 1 || !strings.Contains(logged[0].Message, "prompt-inlined schema") || !strings.Contains(logged[0].Message, "claude-bridge") {
				t.Fatalf("step log must name the prompt path and the bridge: %+v", logged)
			}
		})
	}
}

func TestPiBridgedProvider(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"provider", []string{"--provider", "claude-bridge"}, true},
		{"provider inline", []string{"--provider=claude-bridge"}, true},
		{"model", []string{"--model", "claude-bridge/claude-opus-5-5"}, true},
		{"model inline", []string{"--model=claude-bridge/claude-opus-5-5"}, true},
		{"other provider", []string{"--provider", "openai-codex"}, false},
		{"other model", []string{"--model", "openai-codex/gpt-6.1-sol"}, false},
		{"provider wins", []string{"--provider", "openai-codex", "--model", "claude-bridge/x"}, false},
		{"model fallback", []string{"--model", "claude-bridge/x", "--thinking", "high"}, true},
		{"value not flag", []string{"--system-prompt", "claude-bridge"}, false},
		{"model value skipped", []string{"--system-prompt", "--model", "--model", "m"}, false},
		{"dangling provider", []string{"--provider"}, false},
		{"dangling model", []string{"--model"}, false},
		{"none", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := piBridgedProvider(tc.args); got != tc.want {
				t.Errorf("args %q = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
