package cli

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
)

func TestAxiRespond_ReportsSkippedFixRoundRatherThanSilentApproval(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fixed []string
		want  string
	}{
		{name: "only follow-ups", want: "skipped, no selected error; 2 findings recorded as follow-ups"},
		{name: "error also selected", fixed: []string{"R3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var responded atomic.Bool
			fx := newAxiTimeoutFixture(t, axiTimeoutOpts{
				respond: func(context.Context, json.RawMessage) (interface{}, error) {
					responded.Store(true)
					return &ipc.RespondResult{OK: true, Fixed: tc.fixed, FollowUps: []string{"R1", "R2"}}, nil
				},
			})
			fx.setGetRun(func(context.Context, int) (*ipc.RunInfo, error) {
				if responded.Load() {
					return fx.completed(), nil
				}
				return fx.awaiting(), nil
			})
			out, err := executeCmd("axi", "respond", "--action", "fix", "--findings", "R1,R2", "--wait", "3s")
			if err != nil {
				t.Fatalf("respond: %v\n%s", err, out)
			}
			if tc.want != "" && (!strings.Contains(out, "fix_round:") || !strings.Contains(out, tc.want)) {
				t.Fatalf("output missing %q:\n%s", tc.want, out)
			}
			if tc.want == "" && strings.Contains(out, "fix_round:") {
				t.Fatalf("reported a skipped fix round despite a selected error:\n%s", out)
			}
		})
	}
}
