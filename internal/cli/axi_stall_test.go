package cli

import (
	"bytes"
	"strings"
	"testing"

	toon "github.com/toon-format/toon-go"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

type stalledStatusDoc struct {
	Run struct {
		StalledAgents []struct {
			Step      string `toon:"step"`
			Agent     string `toon:"agent"`
			SilentFor string `toon:"silent_for"`
			AgentPID  string `toon:"agent_pid"`
		} `toon:"stalled_agents"`
	} `toon:"run"`
	Help []string `toon:"help"`
}

func TestAxiStatusNamesAStalledAgentAndHowLongItHasBeenSilent(t *testing.T) {
	restore := nowUnix
	nowUnix = func() int64 { return 1_000_000 }
	defer func() { nowUnix = restore }()

	repoDir, _, database, repo := setupAxiQueryRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/stall")
	chdir(t, repoDir)

	r, err := database.InsertRun(repo.ID, "feature/stall", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateRunStatus(r.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	step, err := database.InsertStepResult(r.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.StartStep(step.ID); err != nil {
		t.Fatal(err)
	}
	pid := 4242
	if err := database.SetStepAgentActivity(step.ID, "pi started pid=4242", &pid); err != nil {
		t.Fatal(err)
	}
	if err := database.SetStepStall(step.ID, "pi", 1_000_000-(46*60+12)); err != nil {
		t.Fatal(err)
	}

	out := axiStatusOutput(t, "")
	var doc stalledStatusDoc
	if err := toon.UnmarshalString(out, &doc); err != nil {
		t.Fatalf("decode axi status: %v\n%s", err, out)
	}
	if len(doc.Run.StalledAgents) != 1 {
		t.Fatalf("stalled_agents = %+v, want the review agent\n%s", doc.Run.StalledAgents, out)
	}
	got := doc.Run.StalledAgents[0]
	if got.Step != "review" || got.Agent != "pi" || got.SilentFor != "46m12s" || got.AgentPID != "4242" {
		t.Fatalf("stalled agent = %+v, want review/pi/46m12s/4242\n%s", got, out)
	}
	if !strings.Contains(strings.Join(doc.Help, "\n"), "pi has produced no output in review for 46m12s") {
		t.Fatalf("help = %q, want the stall explained\n%s", doc.Help, out)
	}

	if err := database.ClearStepStall(step.ID); err != nil {
		t.Fatal(err)
	}
	if out := axiStatusOutput(t, ""); strings.Contains(out, "stalled_agents") || strings.Contains(out, "no output") {
		t.Fatalf("status still reports a stall after the agent spoke:\n%s", out)
	}
}

func TestRunObjectIgnoresAStallOnAStepThatIsNoLongerActive(t *testing.T) {
	rv := runView{
		ID:      "run-1",
		Branch:  "feature/x",
		Status:  string(types.RunFailed),
		HeadSHA: "abcdef1234567890",
		Steps: []stepView{{
			Name:   "review",
			Status: string(types.StepStatusFailed),
			Stall:  &ipc.AgentStall{Agent: "pi", SilentSince: 1},
		}},
	}
	if out := axiDoc(runObjectField(rv)); strings.Contains(out, "stalled_agents") {
		t.Fatalf("a finished step rendered as stalled:\n%s", out)
	}
}

func TestDriveProgressAnnouncesEachStallOnce(t *testing.T) {
	restore := nowUnix
	nowUnix = func() int64 { return 1_000_000 }
	defer func() { nowUnix = restore }()

	var out bytes.Buffer
	pp := &progressPrinter{w: &out, seen: map[string]string{}}
	running := func(stall *ipc.AgentStall) *ipc.RunInfo {
		return &ipc.RunInfo{ID: "run-1", Status: types.RunRunning, Steps: []ipc.StepResultInfo{{
			StepName: types.StepReview,
			Status:   types.StepStatusRunning,
			Stall:    stall,
		}}}
	}
	stall := &ipc.AgentStall{Agent: "pi", SilentSince: 1_000_000 - 600}

	pp.update(running(stall))
	pp.update(running(stall))
	if got := strings.Count(out.String(), "pi has produced no output for 10m0s"); got != 1 {
		t.Fatalf("stall announced %d times, want once:\n%s", got, out.String())
	}
	pp.update(running(nil))
	pp.update(running(&ipc.AgentStall{Agent: "pi", SilentSince: 1_000_000 - 660}))
	if got := strings.Count(out.String(), "pi has produced no output for"); got != 2 {
		t.Fatalf("a second stall was announced %d times in total, want 2:\n%s", got, out.String())
	}
}
