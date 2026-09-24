package cli

import (
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/gate"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// abandonedSubmission is the private mirror head a fresh submission on branch
// may replace without containment proof: the submitted head of the branch's
// latest run, once that run is terminal and never reached publication. It is
// empty whenever any run on the branch may have published without a record.
func abandonedSubmission(d *db.DB, repoID, branch string) (gate.AbandonedSubmission, error) {
	runs, err := d.GetRunsByRepo(repoID)
	if err != nil {
		return gate.AbandonedSubmission{}, err
	}
	var latest *db.Run
	var publications []string
	for _, run := range runs {
		if strings.TrimPrefix(run.Branch, "refs/heads/") != branch {
			continue
		}
		if latest == nil {
			latest = run
		}
		published, recorded, err := recordedPublication(d, run)
		if err != nil {
			return gate.AbandonedSubmission{}, err
		}
		if !recorded {
			return gate.AbandonedSubmission{}, nil
		}
		if published != "" {
			publications = append(publications, published)
		}
	}
	if latest == nil || !latest.Status.Terminal() || latest.LastPushedSHA != nil || deref(latest.SubmittedHeadSHA) == "" {
		return gate.AbandonedSubmission{}, nil
	}
	return gate.AbandonedSubmission{Head: *latest.SubmittedHeadSHA, Publications: publications}, nil
}

// recordedPublication returns the head run last published, and whether that
// record is complete. The upstream push lands before the database records it,
// so a push step that started and did not complete may have published a head
// no record names.
func recordedPublication(d *db.DB, run *db.Run) (string, bool, error) {
	if run.PushActive {
		return "", false, nil
	}
	steps, err := d.GetStepsByRun(run.ID)
	if err != nil {
		return "", false, err
	}
	published := strings.TrimSpace(deref(run.LastPushedSHA))
	for _, step := range steps {
		if step.StepName != types.StepPush {
			continue
		}
		switch step.Status {
		case types.StepStatusPending, types.StepStatusSkipped:
			return published, true, nil
		case types.StepStatusCompleted:
			return published, published != "", nil
		default:
			return "", false, nil
		}
	}
	return published, true, nil
}
