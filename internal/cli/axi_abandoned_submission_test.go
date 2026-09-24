package cli

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/gate"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestAbandonedSubmissionComesOnlyFromTheToolsOwnUnpublishedRun(t *testing.T) {
	t.Parallel()
	const branch = "feature/drop"
	submitted := strings.Repeat("1", 40)
	published := strings.Repeat("2", 40)

	type fixture struct {
		d    *db.DB
		repo *db.Repo
	}
	run := func(t *testing.T, f fixture, runBranch, head string, status types.RunStatus, push types.StepStatus) *db.Run {
		t.Helper()
		r, err := f.d.InsertRun(f.repo.ID, runBranch, head, strings.Repeat("0", 40))
		if err != nil {
			t.Fatal(err)
		}
		step, err := f.d.InsertStepResult(r.ID, types.StepPush)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.d.UpdateStepStatus(step.ID, push); err != nil {
			t.Fatal(err)
		}
		if err := f.d.UpdateRunStatus(r.ID, status); err != nil {
			t.Fatal(err)
		}
		return r
	}
	publish := func(t *testing.T, f fixture, r *db.Run, head string) {
		t.Helper()
		if err := f.d.UpdateRunPublication(r.ID, db.PushBinding{HeadSHA: head, TargetKind: "upstream", TargetFingerprint: "fp", Ref: "refs/heads/" + branch}); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name    string
		history func(t *testing.T, f fixture)
		want    gate.AbandonedSubmission
	}{
		{
			name: "a run aborted before push",
			history: func(t *testing.T, f fixture) {
				run(t, f, branch, submitted, types.RunCancelled, types.StepStatusPending)
			},
			want: gate.AbandonedSubmission{Head: submitted},
		},
		{
			name: "a run that failed before push",
			history: func(t *testing.T, f fixture) {
				run(t, f, branch, submitted, types.RunFailed, types.StepStatusPending)
			},
			want: gate.AbandonedSubmission{Head: submitted},
		},
		{
			name: "an earlier run's publication is carried as content still needing proof",
			history: func(t *testing.T, f fixture) {
				earlier := run(t, f, branch, published, types.RunCompleted, types.StepStatusCompleted)
				publish(t, f, earlier, published)
				run(t, f, branch, submitted, types.RunCancelled, types.StepStatusPending)
			},
			want: gate.AbandonedSubmission{Head: submitted, Publications: []string{published}},
		},
		{
			name: "another branch's runs are not this branch's history",
			history: func(t *testing.T, f fixture) {
				run(t, f, branch, submitted, types.RunCancelled, types.StepStatusPending)
				other := run(t, f, "feature/other", published, types.RunCompleted, types.StepStatusCompleted)
				publish(t, f, other, published)
			},
			want: gate.AbandonedSubmission{Head: submitted},
		},
		{
			name: "no run on the branch",
			history: func(t *testing.T, f fixture) {
				run(t, f, "feature/other", submitted, types.RunCancelled, types.StepStatusPending)
			},
		},
		{
			name: "the latest run is still active",
			history: func(t *testing.T, f fixture) {
				run(t, f, branch, submitted, types.RunRunning, types.StepStatusPending)
			},
		},
		{
			name: "the latest run published",
			history: func(t *testing.T, f fixture) {
				latest := run(t, f, branch, submitted, types.RunCancelled, types.StepStatusCompleted)
				publish(t, f, latest, submitted)
			},
		},
		{
			name: "the latest run's publication was reset for revalidation",
			history: func(t *testing.T, f fixture) {
				latest := run(t, f, branch, submitted, types.RunCancelled, types.StepStatusPending)
				publish(t, f, latest, published)
			},
		},
		{
			name: "the latest run was aborted during push",
			history: func(t *testing.T, f fixture) {
				run(t, f, branch, submitted, types.RunCancelled, types.StepStatusFailed)
			},
		},
		{
			name: "the latest run still holds the push lease",
			history: func(t *testing.T, f fixture) {
				latest := run(t, f, branch, submitted, types.RunFailed, types.StepStatusPending)
				if err := f.d.SetRunPushActive(latest.ID, true); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "an earlier run may have published without a record",
			history: func(t *testing.T, f fixture) {
				run(t, f, branch, published, types.RunFailed, types.StepStatusFailed)
				run(t, f, branch, submitted, types.RunCancelled, types.StepStatusPending)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := db.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { d.Close() })
			repo, err := d.InsertRepo(t.TempDir(), "https://example.com/repo.git", "main")
			if err != nil {
				t.Fatal(err)
			}
			tc.history(t, fixture{d: d, repo: repo})
			got, err := abandonedSubmission(d, repo.ID, branch)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("abandoned submission = %+v, want %+v", got, tc.want)
			}
		})
	}
}
