package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNixWorkflowBuildsAndChecksTheFlake(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(".github", "workflows", "nix.yml"))
	if err != nil {
		t.Fatalf("read Nix workflow: %v", err)
	}
	var wf wfDoc
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parse Nix workflow: %v", err)
	}
	job, ok := wf.Jobs["nix"]
	if !ok {
		t.Fatal("Nix workflow has no nix job")
	}
	if !slices.ContainsFunc(job.Steps, func(step wfStep) bool {
		return strings.HasPrefix(step.Uses, "DeterminateSystems/nix-installer-action@")
	}) {
		t.Fatal("nix job must install Nix")
	}
	var commands []string
	for _, command := range workflowCommandsMatching(job.Steps, func(wfStep) bool { return true }) {
		commands = append(commands, strings.Join(append([]string{command.name}, command.args...), " "))
	}
	for _, want := range []string{
		"nix flake check --all-systems --no-build",
		"set -o pipefail",
		"./result/bin/no-mistakes --version",
		"nix-build default.nix",
	} {
		if !slices.ContainsFunc(commands, func(command string) bool { return strings.HasPrefix(command, want) }) {
			t.Errorf("nix job must run %q; normalized commands: %q", want, commands)
		}
	}
}

func TestNixWorkflowRunsOnlyOnMainPushAndDispatch(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(".github", "workflows", "nix.yml"))
	if err != nil {
		t.Fatalf("read Nix workflow: %v", err)
	}
	var wf struct {
		On map[string]any `yaml:"on"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parse Nix workflow: %v", err)
	}
	if _, ok := wf.On["pull_request"]; ok {
		t.Error("Nix workflow must not start on pull_request")
	}
	push, _ := wf.On["push"].(map[string]any)
	if branches, _ := push["branches"].([]any); !slices.Equal(branches, []any{"main"}) {
		t.Errorf("Nix workflow push branches = %v, want [main]", branches)
	}
	if _, ok := wf.On["workflow_dispatch"]; !ok {
		t.Error("Nix workflow must allow workflow_dispatch")
	}
}
