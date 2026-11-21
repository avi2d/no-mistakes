package config

import "testing"

func TestReviewDeferNonErrorsDoesNotReadThePushedBranch(t *testing.T) {
	for _, tc := range []struct {
		name, global, pushed, trusted string
		want                          bool
	}{
		{name: "off by default"},
		{name: "pushed on stays off", pushed: "true"},
		{name: "trusted on beats pushed off", pushed: "false", trusted: "true", want: true},
		{name: "global on beats pushed off", global: "true", pushed: "false", want: true},
		{name: "trusted off beats global on", global: "true", pushed: "true", trusted: "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			yaml := func(value string) []byte {
				if value == "" {
					return []byte("{}")
				}
				return []byte("review:\n  defer_non_errors: " + value + "\n")
			}
			global, err := LoadGlobalFromBytes(yaml(tc.global))
			if err != nil {
				t.Fatal(err)
			}
			pushed, err := LoadRepoFromBytes(yaml(tc.pushed))
			if err != nil {
				t.Fatal(err)
			}
			trusted, err := LoadRepoFromBytes(yaml(tc.trusted))
			if err != nil {
				t.Fatal(err)
			}
			for _, allow := range []bool{false, true} {
				effective := Merge(global, EffectiveRepoConfig(pushed, trusted, allow))
				if effective.Review.DeferNonErrors != tc.want {
					t.Errorf("allow_repo_commands=%v: defer_non_errors=%v, want %v", allow, effective.Review.DeferNonErrors, tc.want)
				}
			}
		})
	}
}
