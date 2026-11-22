package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestRenderFindings_AskUserFindingMustNotHideWhatItsDecisionAuthorizes(t *testing.T) {
	raw := `{"findings":[
		{"id":"f1","severity":"error","file":"a.go","line":1,"description":"drop the branch","action":"ask-user","question_action":"delete"},
		{"id":"f2","severity":"warning","file":"b.go","line":2,"description":"silent question","action":"ask-user"},
		{"id":"f3","severity":"warning","file":"c.go","line":3,"description":"invalid question","action":"ask-user","question_action":"deploy"},
		{"id":"f4","severity":"warning","file":"d.go","line":4,"description":"unclassified"}
	],"summary":"4 issues"}`

	got := stripANSI(renderFindings(raw, 120))

	for _, want := range []string{
		"[question action: delete] drop the branch",
		"[question action: unknown] silent question",
		"[question action: unknown] invalid question",
		"[question action: unknown] unclassified",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered findings missing %q, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[question action: none]") {
		t.Errorf("a missing or invalid question_action must render as unknown, never none, got:\n%s", got)
	}
}

func TestRenderFindings_NonAskUserFindingMustNotShowAQuestionAction(t *testing.T) {
	raw := `{"findings":[
		{"id":"f1","severity":"error","file":"a.go","line":1,"description":"fix me","action":"auto-fix","question_action":"delete"},
		{"id":"f2","severity":"info","file":"b.go","line":2,"description":"note","action":"no-op"}
	],"summary":"2 issues"}`

	got := stripANSI(renderFindings(raw, 120))

	if strings.Contains(got, "question action") {
		t.Errorf("only ask-user findings carry a question action, got:\n%s", got)
	}
	for _, want := range []string{"fix me", "note"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered findings missing description %q, got:\n%s", want, got)
		}
	}
}

func TestRenderFindings_UnfocusedAskUserFindingMustNotLeaveItsQuestionActionUndimmed(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI)
	raw := `{"findings":[
		{"id":"f1","severity":"error","file":"a.go","line":1,"description":"first issue","action":"ask-user","question_action":"merge"},
		{"id":"f2","severity":"warning","file":"b.go","line":2,"description":"second issue","action":"ask-user","question_action":"push"}
	],"summary":"2 issues"}`
	selected := map[string]bool{"f1": true, "f2": true}
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(ansiBrightBlack))

	content, _ := renderFindingsWithSelection(raw, 80, 0, selected, 0)

	if !strings.Contains(content, dimStyle.Render("        [question action: push] second issue")) {
		t.Errorf("unfocused ask-user description, label included, should be dim-styled, got:\n%q", content)
	}
	if strings.Contains(content, dimStyle.Render("        [question action: merge] first issue")) {
		t.Errorf("focused ask-user description should not be dim-styled, got:\n%q", content)
	}
	if !strings.Contains(stripANSI(content), "        [question action: merge] first issue") {
		t.Errorf("focused ask-user description should render with its label, got:\n%s", stripANSI(content))
	}
}
