package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestAxiReviewGate_DoesNotHideDeferralOrDropNamedFollowUpsOnApproval(t *testing.T) {
	var responded atomic.Bool
	calls := make(chan ipc.RespondParams, 1)
	fx := newAxiTimeoutFixture(t, axiTimeoutOpts{
		respond: func(_ context.Context, raw json.RawMessage) (interface{}, error) {
			var params ipc.RespondParams
			if err := json.Unmarshal(raw, &params); err != nil {
				return nil, err
			}
			calls <- params
			responded.Store(true)
			return &ipc.RespondResult{OK: true, FollowUps: []string{"R1", "R2"}}, nil
		},
	})
	parked := func(deferral string) *ipc.RunInfo {
		run := fx.awaiting()
		raw := `{"step_name":"review","status":"fix_review","findings_json":"{\"findings\":[{\"id\":\"R1\",\"severity\":\"warning\",\"description\":\"Improve naming\"},{\"id\":\"R2\",\"severity\":\"info\",\"description\":\"Add an example\"}]}"` + deferral + `}`
		if err := json.Unmarshal([]byte(raw), &run.Steps[0]); err != nil {
			t.Fatal(err)
		}
		return run
	}

	for _, tc := range []struct {
		name, wire, want string
	}{
		{"unknown", "", ""},
		{"does not apply", `,"defer_non_errors":false`, "defer_non_errors: false"},
		{"applies", `,"defer_non_errors":true`, "defer_non_errors: true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := parked(tc.wire)
			fx.setGetRun(func(context.Context, int) (*ipc.RunInfo, error) { return gate, nil })
			out, err := executeCmd("axi", "run", "--wait", "3s")
			if err != nil || !strings.Contains(out, "gate:") {
				t.Fatalf("axi run did not return the Review gate: %v\n%s", err, out)
			}
			if tc.want == "" && strings.Contains(out, "defer_non_errors:") || tc.want != "" && !strings.Contains(out, tc.want) {
				t.Fatalf("gate deferral field should be %q:\n%s", tc.want, out)
			}
		})
	}

	gate := parked(`,"defer_non_errors":true`)
	fx.setGetRun(func(context.Context, int) (*ipc.RunInfo, error) {
		if responded.Load() {
			return fx.completed(), nil
		}
		return gate, nil
	})
	out, err := executeCmd("axi", "respond", "--action", "approve", "--follow-ups", "R1,R2", "--wait", "3s")
	if err != nil {
		t.Fatalf("axi respond: %v\n%s", err, out)
	}
	select {
	case params := <-calls:
		if params.Action != types.ActionApprove || params.Step != types.StepReview || !slices.Equal(params.FindingIDs, []string{"R1", "R2"}) {
			t.Fatalf("approval request did not name the follow-ups: %+v", params)
		}
	default:
		t.Fatal("approval did not reach the daemon")
	}
	for _, want := range []string{"recorded:", "sent_to_fixer[0]", "follow_ups[2]: R1,R2", "outcome: passed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("approval reply missing %q:\n%s", want, out)
		}
	}
}
