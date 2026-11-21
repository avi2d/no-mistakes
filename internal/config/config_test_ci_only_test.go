package config

import "testing"

func TestEffectiveRepoConfig_PushedBranchCannotSwitchTestCIMode(t *testing.T) {
	for _, tc := range []struct {
		name, global, trusted, pushed string
		want                          bool
	}{
		{name: "neither source opts in"},
		{name: "pushed true cannot defer local tests", pushed: "true"},
		{name: "pushed false cannot turn local tests back on", trusted: "true", pushed: "false", want: true},
		{name: "global true cannot be turned off by a repository", global: "true", trusted: "false", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			yamlFor := func(value string) []byte {
				if value == "" {
					return []byte("{}")
				}
				return []byte("test:\n  ci_only: " + value + "\n")
			}
			global, err := LoadGlobalFromBytes(yamlFor(tc.global))
			if err != nil {
				t.Fatal(err)
			}
			loadRepo := func(value string) *RepoConfig {
				t.Helper()
				repo, err := LoadRepoFromBytes(yamlFor(value))
				if err != nil {
					t.Fatal(err)
				}
				return repo
			}
			pushed := loadRepo(tc.pushed)
			for _, allow := range []bool{false, true} {
				trusted := loadRepo(tc.trusted)
				got := Merge(global, EffectiveRepoConfig(pushed, trusted, allow)).Test.CIOnly
				if got != tc.want {
					t.Errorf("allow_repo_commands=%t: ci_only=%t, want %t", allow, got, tc.want)
				}
				if tc.trusted == "" {
					got = Merge(global, EffectiveRepoConfig(pushed, nil, allow)).Test.CIOnly
					if got != tc.want {
						t.Errorf("no trusted file, allow_repo_commands=%t: ci_only=%t, want %t", allow, got, tc.want)
					}
				}
			}
		})
	}
}
