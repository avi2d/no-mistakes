package steps

import (
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// A replay, an amend or a squash keeps a commit's author and author date, so
// a new commit carrying a branch commit's authorship is that commit rewritten,
// and it must keep one of the messages the branch recorded. A commit dropped
// from the branch has no such rewrite and is left to the other checks. base
// is the rebase target, or "" when the round did not rebase or could not
// resolve it; it, every remote-tracking ref and the last fetch hold upstream
// commits, which the round did not write.
func refuseRewordedBranchCommits(sctx *pipeline.StepContext, stepName types.StepName, base string) error {
	recorded := strings.TrimSpace(sctx.Run.HeadSHA)
	if recorded == "" {
		return nil
	}
	head, err := stepGitHeadSHA(sctx)
	if err != nil {
		return fmt.Errorf("resolve head for commit message gate: %w", err)
	}
	if head == recorded {
		return nil
	}
	appended, err := headDescendsFrom(sctx, recorded, head)
	if err != nil {
		return fmt.Errorf("inspect %s commits for commit message gate: %w", stepName, err)
	}
	if appended {
		return nil
	}
	forkPoint, err := stepGitRun(sctx, "merge-base", recorded, head)
	if err != nil {
		return fmt.Errorf("find the fork point for commit message gate: %w", err)
	}
	branch, err := authoredCommits(sctx, recorded, "^"+forkPoint)
	if err != nil {
		return err
	}
	authorship, messages := map[string]bool{}, map[string]bool{}
	for _, c := range branch {
		authorship[c.authorship] = true
		messages[c.message] = true
	}
	revs := []string{head, "^" + recorded, "^" + forkPoint}
	if base = strings.TrimSpace(base); base != "" {
		revs = append(revs, "^"+base)
	}
	if fetched, err := stepGitRun(sctx, "rev-parse", "--verify", "--quiet", "FETCH_HEAD^{commit}"); err == nil && fetched != "" {
		revs = append(revs, "^"+fetched)
	}
	rewritten, err := authoredCommits(sctx, append(revs, "--no-merges", "--not", "--remotes")...)
	if err != nil {
		return err
	}
	for _, c := range rewritten {
		if !authorship[c.authorship] || messages[c.message] {
			continue
		}
		cause := fmt.Errorf("%s round rewrote the commit message of a branch commit as %s: a commit's subject and footer belong to its author, so the round may change only the code, and its result was discarded and the worktree restored to %s", stepName, shortObjectID(c.sha), recorded)
		if rebaseInProgress(sctx.Ctx, sctx.WorkDir) {
			_, _ = stepGitRun(sctx, "rebase", "--abort")
		}
		return restorePreMergeHead(sctx.Ctx, sctx, recorded, cause)
	}
	return nil
}

type authoredCommit struct {
	sha, authorship, message string
}

func authoredCommits(sctx *pipeline.StepContext, revs ...string) ([]authoredCommit, error) {
	out, err := stepGitRun(sctx, append([]string{"log", "--date=raw", "--format=%H%x00%an%x00%ae%x00%ad"}, revs...)...)
	if err != nil {
		return nil, fmt.Errorf("list commits for commit message gate: %w", err)
	}
	var commits []authoredCommit
	for _, line := range strings.Split(out, "\n") {
		sha, authorship, found := strings.Cut(line, "\x00")
		if !found {
			continue
		}
		message, err := commitMessage(sctx, sha)
		if err != nil {
			return nil, err
		}
		commits = append(commits, authoredCommit{sha: sha, authorship: authorship, message: message})
	}
	return commits, nil
}
