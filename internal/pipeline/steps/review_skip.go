package steps

import (
	"context"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/git"
)

const docsAndSchedulesOnlyReason = "only docs and CI schedule lines changed"

var docExtensions = map[string]bool{
	".md":       true,
	".markdown": true,
	".rst":      true,
	".adoc":     true,
}

// Agents read these as instructions, so a change to one is a change to behavior.
var agentInstructionFiles = map[string]bool{
	"agents.md": true,
	"claude.md": true,
	"gemini.md": true,
	"skill.md":  true,
}

var (
	cronValueLine = regexp.MustCompile(`^( *(?:- +)?cron: +)('[^']*'|"[^"\\]*") *(?:#.*)?$`)
	scheduleKey   = regexp.MustCompile(`^ *schedule: *(?:#.*)?$`)
	onKey         = regexp.MustCompile(`^(?:on|'on'|"on"): *(?:#.*)?$`)
	hunkHeader    = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
)

const (
	regularFileMode = "100644"
	absentFileMode  = "000000"
)

type changedEntry struct {
	oldMode, newMode string
	status           byte
	path             string
}

// docsAndSchedulesOnly reports whether every change between base and head is a
// doc file or a quoted cron value under a GitHub workflow's on.schedule. Any
// doubt, including any git error, answers false so the review runs.
func docsAndSchedulesOnly(ctx context.Context, workDir, base, head string) bool {
	raw, err := git.RunRaw(ctx, workDir, "diff", "--raw", "-z", "--no-renames", "--no-abbrev", base+".."+head)
	if err != nil {
		return false
	}
	entries, ok := parseRawDiff(string(raw))
	if !ok || len(entries) == 0 {
		return false
	}
	for _, entry := range entries {
		switch {
		case isDocChange(entry):
		case isWorkflowFile(entry.path) && entry.status == 'M' && entry.oldMode == regularFileMode && entry.newMode == regularFileMode:
			if !cronValuesOnly(ctx, workDir, base, head, entry.path) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func parseRawDiff(raw string) ([]changedEntry, bool) {
	fields := strings.Split(raw, "\x00")
	if fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}
	if len(fields)%2 != 0 {
		return nil, false
	}
	entries := make([]changedEntry, 0, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		meta := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		if len(meta) != 5 || len(meta[4]) != 1 {
			return nil, false
		}
		entries = append(entries, changedEntry{oldMode: meta[0], newMode: meta[1], status: meta[4][0], path: fields[i+1]})
	}
	return entries, true
}

func isDocChange(entry changedEntry) bool {
	if !isDocPath(entry.path) {
		return false
	}
	switch entry.status {
	case 'A':
		return entry.oldMode == absentFileMode && entry.newMode == regularFileMode
	case 'D':
		return entry.oldMode == regularFileMode && entry.newMode == absentFileMode
	case 'M':
		return entry.oldMode == regularFileMode && entry.newMode == regularFileMode
	}
	return false
}

func isDocPath(file string) bool {
	base := path.Base(file)
	if !docExtensions[strings.ToLower(path.Ext(base))] || agentInstructionFiles[strings.ToLower(base)] {
		return false
	}
	for _, segment := range strings.Split(path.Dir(file), "/") {
		if segment != "." && strings.HasPrefix(segment, ".") || segment == "testdata" || segment == "skills" {
			return false
		}
	}
	return true
}

func isWorkflowFile(file string) bool {
	ext := path.Ext(file)
	return path.Dir(file) == ".github/workflows" && (ext == ".yml" || ext == ".yaml")
}

// cronValuesOnly holds when every hunk replaces cron lines one for one, each
// keeping its text up to the value, and each new line sits under on.schedule.
// Equal counts and prefixes leave the YAML structure of both sides identical,
// so checking the ancestry in the head file alone is enough.
func cronValuesOnly(ctx context.Context, workDir, base, head, file string) bool {
	diff, err := git.RunRaw(ctx, workDir, "diff", "-U0", "--no-color", "--no-ext-diff", "--no-textconv", base, head, "--", file)
	if err != nil {
		return false
	}
	content, err := git.RunRaw(ctx, workDir, "cat-file", "blob", head+":"+file)
	if err != nil {
		return false
	}
	headLines := strings.Split(string(content), "\n")
	hunks, ok := parseZeroContextHunks(string(diff))
	if !ok || len(hunks) == 0 {
		return false
	}
	for _, h := range hunks {
		if len(h.removed) == 0 || len(h.removed) != len(h.added) {
			return false
		}
		for i := range h.added {
			oldMatch := cronValueLine.FindStringSubmatch(h.removed[i])
			newMatch := cronValueLine.FindStringSubmatch(h.added[i])
			if oldMatch == nil || newMatch == nil || oldMatch[1] != newMatch[1] {
				return false
			}
			if !underOnSchedule(headLines, h.newStart-1+i) {
				return false
			}
		}
	}
	return true
}

type zeroContextHunk struct {
	newStart int
	removed  []string
	added    []string
}

func parseZeroContextHunks(diff string) ([]zeroContextHunk, bool) {
	var hunks []zeroContextHunk
	for _, line := range strings.Split(strings.TrimSuffix(diff, "\n"), "\n") {
		if m := hunkHeader.FindStringSubmatch(line); m != nil {
			start, err := strconv.Atoi(m[1])
			if err != nil {
				return nil, false
			}
			hunks = append(hunks, zeroContextHunk{newStart: start})
			continue
		}
		if len(hunks) == 0 {
			continue
		}
		current := &hunks[len(hunks)-1]
		switch {
		case strings.HasPrefix(line, "-"):
			current.removed = append(current.removed, line[1:])
		case strings.HasPrefix(line, "+"):
			current.added = append(current.added, line[1:])
		default:
			return nil, false
		}
	}
	return hunks, true
}

// underOnSchedule requires the line's parent to be schedule: and that key's
// parent to be a top-level on:.
func underOnSchedule(lines []string, index int) bool {
	if index < 0 || index >= len(lines) {
		return false
	}
	parent := yamlParent(lines, index)
	if parent < 0 || !scheduleKey.MatchString(lines[parent]) {
		return false
	}
	grandparent := yamlParent(lines, parent)
	return grandparent >= 0 && indentOf(lines[grandparent]) == 0 && onKey.MatchString(lines[grandparent])
}

// yamlParent finds the line owning the one at index. A sequence item may sit at
// its key's own indent, so an item also takes a same-indent non-item line as
// its parent.
func yamlParent(lines []string, index int) int {
	indent := indentOf(lines[index])
	item := strings.HasPrefix(strings.TrimLeft(lines[index], " "), "- ")
	for i := index - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		lineIndent := indentOf(lines[i])
		if lineIndent < indent || lineIndent == indent && item && !strings.HasPrefix(trimmed, "- ") {
			return i
		}
	}
	return -1
}

func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}
