package steps

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/safeurl"
)

func restampReplayedCommits(sctx *pipeline.StepContext, base string) error {
	// normalizeAgentHead cannot cover a replay: it needs the recorded head as an
	// ancestor, and a rebase keeps each author but stamps the committer of
	// whoever ran it. Trees, messages and author dates are kept, so a pure
	// replay stays patch-identical.
	recorded := strings.TrimSpace(sctx.Run.HeadSHA)
	if recorded == "" {
		return nil
	}
	head, err := stepGitHeadSHA(sctx)
	if err != nil {
		return fmt.Errorf("resolve head for identity gate: %w", err)
	}
	if head == recorded {
		return nil
	}
	resolvedBase, err := stepGitRun(sctx, "rev-parse", "--verify", strings.TrimSpace(base)+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve rebase target for identity gate: %w", err)
	}
	name, email, err := pipelineCommitIdentity(sctx, recorded)
	if err != nil {
		return err
	}
	pipelineIdent := name + "\x00" + email
	// An author the branch never carried made its commit during the replay.
	known, recordedCommits, err := recordedBranch(sctx, recorded, resolvedBase)
	if err != nil {
		return err
	}
	known[pipelineIdent] = true

	chain, chainErr := linearChain(sctx, resolvedBase, head)
	if chainErr != nil {
		out, err := stepGitRun(sctx, "log", "--format=%an%x00%ae%x00%cn%x00%ce", resolvedBase+".."+head, "^"+recorded)
		if err != nil {
			return fmt.Errorf("inspect replayed commits for identity gate: %w", err)
		}
		for _, line := range strings.Split(out, "\n") {
			if line == "" {
				continue
			}
			fields := strings.Split(line, "\x00")
			if len(fields) != 4 || !known[fields[0]+"\x00"+fields[1]] || fields[2]+"\x00"+fields[3] != pipelineIdent {
				return fmt.Errorf("refusing to adopt %s: it carries a foreign identity but is not a linear chain on %s (%v), which needs an operator re-author", shortObjectID(head), shortObjectID(resolvedBase), chainErr)
			}
		}
		return nil
	}

	parent, restamped := resolvedBase, 0
	for _, sha := range chain {
		info, err := stepGitRun(sctx, "show", "-s", "--date=raw", "--format=%T%x00%an%x00%ae%x00%ad%x00%cn%x00%ce", sha)
		if err != nil {
			return fmt.Errorf("read replayed commit %s: %w", shortObjectID(sha), err)
		}
		fields := strings.Split(info, "\x00")
		if len(fields) != 6 {
			return fmt.Errorf("read replayed commit %s: unexpected git output", shortObjectID(sha))
		}
		tree, authorName, authorEmail, authorDate := fields[0], fields[1], fields[2], fields[3]
		authorKnown := known[authorName+"\x00"+authorEmail]
		// A commit the rebase left in place is already published as it is.
		unchanged := recordedCommits[sha] || authorKnown && fields[4]+"\x00"+fields[5] == pipelineIdent
		if restamped == 0 && unchanged {
			parent = sha
			continue
		}
		if !authorKnown {
			authorName, authorEmail = name, email
		}
		message, err := commitMessage(sctx, sha)
		if err != nil {
			return err
		}
		env := []string{
			"GIT_AUTHOR_NAME=" + authorName,
			"GIT_AUTHOR_EMAIL=" + authorEmail,
			"GIT_AUTHOR_DATE=" + authorDate,
			"GIT_COMMITTER_NAME=" + name,
			"GIT_COMMITTER_EMAIL=" + email,
		}
		if parent, err = commitTreeWithMessage(sctx, env, tree, parent, message); err != nil {
			return fmt.Errorf("restamp replayed commit %s: %w", shortObjectID(sha), err)
		}
		restamped++
	}
	if restamped == 0 {
		return nil
	}
	if _, err := stepGitRun(sctx, "update-ref", "-m", "no-mistakes: restamp replayed commits with the pipeline identity", "HEAD", parent, head); err != nil {
		return fmt.Errorf("move head to restamped replay: %w", err)
	}
	sctx.Log(fmt.Sprintf("restamped %d replayed commit(s) with the pipeline identity", restamped))
	return nil
}

func recordedBranch(sctx *pipeline.StepContext, recorded, base string) (authors, commits map[string]bool, err error) {
	forkPoint, err := stepGitRun(sctx, "merge-base", recorded, base)
	if err != nil {
		return nil, nil, fmt.Errorf("find the branch's fork point for identity gate: %w", err)
	}
	out, err := stepGitRun(sctx, "log", "--format=%H%x00%an%x00%ae", forkPoint+".."+recorded)
	if err != nil {
		return nil, nil, fmt.Errorf("list branch authors for identity gate: %w", err)
	}
	authors, commits = map[string]bool{}, map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		sha, author, found := strings.Cut(line, "\x00")
		if found {
			commits[sha] = true
			authors[author] = true
		}
	}
	return authors, commits, nil
}

func commitMessage(sctx *pipeline.StepContext, sha string) (string, error) {
	// A pretty format appends its own terminator, and trimming that would also
	// eat the message's own trailing whitespace.
	raw, err := stepGitRunRaw(sctx, "cat-file", "commit", sha)
	if err != nil {
		return "", fmt.Errorf("read commit %s: %w", shortObjectID(sha), err)
	}
	_, message, _ := strings.Cut(raw, "\n\n")
	return message, nil
}

func commitTreeWithMessage(sctx *pipeline.StepContext, env []string, tree, parent, message string) (string, error) {
	args := []string{"commit-tree", tree, "-p", parent, "-F", "-"}
	cmd := stepCmd(sctx, "git", args...)
	cmd.Env = append(git.NonInteractiveEnvFrom(cmd.Env, sctx.WorkDir), env...)
	cmd.Stdin = strings.NewReader(message)
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, safeurl.RedactText(stderr))
	}
	return strings.TrimSpace(string(out)), nil
}
