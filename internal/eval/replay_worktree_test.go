package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRestoreCaseStartsNoBackgroundGitMaintenance(t *testing.T) {
	ctx := context.Background()
	p, sourceDB, run, _, _ := setupCapturedRun(t, ctx)
	defer sourceDB.Close()
	store, err := Open(p.EvalDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cases, err := Capture(ctx, store, p, sourceDB, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 {
		t.Fatalf("captured cases = %d, want 1", len(cases))
	}

	tracePath := filepath.Join(t.TempDir(), "trace2.json")
	t.Setenv("GIT_TRACE2_EVENT", tracePath)
	workDir, err := restoreCase(ctx, store, cases[0], t.TempDir())
	if err != nil {
		t.Fatalf("restore case: %v", err)
	}
	t.Setenv("GIT_TRACE2_EVENT", "")
	if _, err := os.Stat(filepath.Join(workDir, "main.go")); err != nil {
		t.Fatalf("restored worktree is missing the reviewed file: %v", err)
	}

	trace, err := os.Open(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	defer trace.Close()
	var childStarts int
	scanner := bufio.NewScanner(trace)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		var event struct {
			Event string   `json:"event"`
			Argv  []string `json:"argv"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("parse trace2 event %q: %v", scanner.Text(), err)
		}
		if event.Event != "child_start" {
			continue
		}
		childStarts++
		if slices.Contains(event.Argv, "maintenance") || slices.Contains(event.Argv, "gc") {
			t.Fatalf("restore started git maintenance in the sandbox gate, which can prune the entry worktree add has not locked yet: %q", event.Argv)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if childStarts == 0 {
		t.Fatal("trace2 recorded no child processes; the trace cannot show whether maintenance started")
	}
}
