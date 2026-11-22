//go:build unix

package steps

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestStrictPiFakeIsNotTextFileBusyWhileSiblingsFork(t *testing.T) {
	t.Parallel()
	for i := range 32 {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()
			cwd := t.TempDir()
			pi, err := agent.New(types.AgentPi, writeStrictPi(t, `{"summary":"ok"}`), nil)
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var lifecycle []agent.LifecycleEvent
			_, runErr := pi.Run(context.Background(), agent.RunOpts{Prompt: "fix", CWD: cwd, JSONSchema: commitSummarySchema, OnLifecycle: func(event agent.LifecycleEvent) {
				if event.Phase == agent.LifecyclePhaseActivity {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				lifecycle = append(lifecycle, event)
			}})
			mu.Lock()
			defer mu.Unlock()
			if _, err := os.Stat(filepath.Join(cwd, "pi-extension.ts")); err != nil || runErr != nil {
				t.Fatalf("fake pi 0.99.1 left the strict path: extension: %v; run error: %v; lifecycle: %+v", err, runErr, lifecycle)
			}
		})
	}
}
