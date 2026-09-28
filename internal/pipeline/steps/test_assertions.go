package steps

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/git"
)

func isTestAssertionLine(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	for _, prefix := range []string{"//", "#", "*", "<!--"} {
		if strings.HasPrefix(trimmed, prefix) {
			return false
		}
	}
	for _, marker := range []string{
		"expect(", "assert.", "require.", "t.Error", "t.Fatal", "t.Fail(",
		"Fail(", "Expect(", ".Should", "So(",
		"assert(", "ok(", "equal(", "deepEqual(", "strictEqual(",
		"toBe(", "toEqual(", "toContain(", "toThrow(", "toMatch(",
		"assert ", "assertEqual(", "assertTrue(", "assertFalse(",
		"pytest.raises", "pytest.fail(",
	} {
		if strings.Contains(trimmed, marker) {
			return true
		}
	}
	return false
}

func changedTestAssertions(diff string) []string {
	var out []string
	file := ""
	oldLine := 0
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			path := strings.TrimPrefix(line, "+++ ")
			path = strings.TrimPrefix(path, "b/")
			file = path
		case strings.HasPrefix(line, "@@ "):
			oldLine = parseOldStart(line)
		case strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "diff --git"):
		case strings.HasPrefix(line, "-"):
			oldLine++
			if file != "" && isTestAssertionLine(line[1:]) {
				out = append(out, fmt.Sprintf("%s:%d: %s", file, oldLine, strings.TrimSpace(line[1:])))
			}
		case strings.HasPrefix(line, "+"):
		default:
			if strings.HasPrefix(line, " ") && oldLine > 0 {
				oldLine++
			}
		}
	}
	return out
}

func parseOldStart(hunk string) int {
	rest := strings.TrimPrefix(hunk, "@@ -")
	end := strings.IndexAny(rest, " ,")
	if end < 0 {
		return 0
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0
	}
	return n - 1
}

// A single revision shows committed fix rounds and uncommitted worktree edits
// together, while a two-revision range would miss the latter.
func testAssertionChangesSection(ctx context.Context, workDir, from string) string {
	from = strings.TrimSpace(from)
	if from == "" {
		return ""
	}
	diff, err := git.Run(ctx, workDir, "diff", "--no-renames", "-U0", from)
	if err != nil || strings.TrimSpace(diff) == "" {
		return ""
	}
	changed := changedTestAssertions(diff)
	if len(changed) == 0 {
		return ""
	}
	const maxListed = 50
	listed := changed
	truncated := ""
	if len(changed) > maxListed {
		listed = changed[:maxListed]
		truncated = fmt.Sprintf("\n- (%d further changed assertions omitted)", len(changed)-maxListed)
	}
	return "\nChanged test assertions in fix rounds:\n- " +
		strings.Join(listed, "\n- ") + truncated +
		"\nJudge each one against the stated intent: an assertion the intent changed is legitimate, one that contradicts it is a finding.\n"
}
