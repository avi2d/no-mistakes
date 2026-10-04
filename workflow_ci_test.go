package main

import (
	"slices"
	"strings"
	"testing"
)

func TestCIWorkflowRunsTestsOnAllSupportedDesktopPlatforms(t *testing.T) {
	wf := loadCIWorkflowDoc(t)
	want := map[string]string{
		"test-linux":         "ubuntu-latest",
		"test-macos":         "macos-latest",
		"test-windows-core":  "windows-latest",
		"test-windows-git":   "windows-latest",
		"test-windows-steps": "windows-latest",
	}
	for name, runner := range want {
		if got := ciJob(t, wf, name).RunsOn; got != runner {
			t.Errorf("%s runs-on = %v, want %s", name, got, runner)
		}
	}
	for name, job := range wf.Jobs {
		if _, ok := want[name]; !ok && strings.HasPrefix(name, "test-") && name != "test-gate" {
			t.Errorf("unexpected test job %s on %v; add it to the platform and shard contracts", name, job.RunsOn)
		}
	}
}

func TestCIWorkflowUsesRaceTestsOnUnixRunners(t *testing.T) {
	wf := loadCIWorkflowDoc(t)
	linux := workflowCommandsMatching(ciJob(t, wf, "test-linux").Steps, func(wfStep) bool { return true })
	var raceTests int
	for _, command := range linux {
		if command.name == "go" && slices.Equal(command.args, []string{"test", "-race", "./..."}) {
			raceTests++
		}
	}
	if raceTests != 1 {
		t.Fatalf("Linux go test -race ./... commands = %d, want 1; normalized commands: %#v", raceTests, linux)
	}

	_, mac := namedStep(t, ciJob(t, wf, "test-macos"), "Test on macOS")
	var macTests []workflowCommand
	for _, command := range workflowCommandsMatching([]wfStep{mac}, func(wfStep) bool { return true }) {
		if command.name == "go" && len(command.args) > 0 && command.args[0] == "test" {
			macTests = append(macTests, command)
		}
	}
	if len(macTests) != 1 || !macTests[0].hasArg("-race") {
		t.Fatalf("macOS shards must run one go test -race command, got %#v", macTests)
	}
}
