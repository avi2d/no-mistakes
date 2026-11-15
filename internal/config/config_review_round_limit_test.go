package config

import (
	"strings"
	"testing"
)

func TestReviewMaxFixRounds_TrustedOnlyAndDefaultsUnbounded(t *testing.T) {
	pushed, err := LoadRepoFromBytes([]byte("review:\n  max_fix_rounds: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := LoadRepoFromBytes([]byte("review:\n  max_fix_rounds: 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, allow := range []bool{false, true} {
		for _, tc := range []struct {
			trusted *RepoConfig
			want    int
		}{{nil, 0}, {&RepoConfig{}, 0}, {trusted, 2}} {
			cfg := Merge(&GlobalConfig{}, EffectiveRepoConfig(pushed, tc.trusted, allow))
			if cfg.Review.MaxFixRounds != tc.want {
				t.Errorf("allow_repo_commands=%v: max_fix_rounds=%d, want %d", allow, cfg.Review.MaxFixRounds, tc.want)
			}
		}
	}
}

func TestReviewMaxFixRounds_Validation(t *testing.T) {
	for _, value := range []string{"-1", "1.5", "many"} {
		_, err := LoadRepoFromBytes([]byte("review:\n  max_fix_rounds: " + value + "\n"))
		if err == nil {
			t.Errorf("accepted invalid max_fix_rounds %q", value)
		}
	}
	_, err := LoadRepoFromBytes([]byte("review:\n  max_fix_rounds: -1\n"))
	if err == nil || !strings.Contains(err.Error(), "review.max_fix_rounds") {
		t.Fatalf("negative limit error should identify the key, got %v", err)
	}
}

func TestReviewMaxFixRounds_GlobalDefaultAppliesOnlyWhenTrustedRepoLeavesItUnset(t *testing.T) {
	global, err := LoadGlobalFromBytes([]byte("review:\n  max_fix_rounds: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	pushed, err := LoadRepoFromBytes([]byte("review:\n  max_fix_rounds: 7\n"))
	if err != nil {
		t.Fatal(err)
	}
	load := func(yaml string) *RepoConfig {
		t.Helper()
		repo, err := LoadRepoFromBytes([]byte(yaml))
		if err != nil {
			t.Fatal(err)
		}
		return repo
	}
	for _, tc := range []struct {
		name    string
		trusted *RepoConfig
		want    int
	}{
		{"no trusted config", nil, 1},
		{"trusted config without the key", load("review:\n  conversation: false\n"), 1},
		{"trusted raises it", load("review:\n  max_fix_rounds: 3\n"), 3},
		{"trusted explicit zero stays unlimited", load("review:\n  max_fix_rounds: 0\n"), 0},
	} {
		for _, allow := range []bool{false, true} {
			cfg := Merge(global, EffectiveRepoConfig(pushed, tc.trusted, allow))
			if cfg.Review.MaxFixRounds != tc.want {
				t.Errorf("%s, allow_repo_commands=%v: max_fix_rounds=%d, want %d", tc.name, allow, cfg.Review.MaxFixRounds, tc.want)
			}
		}
	}
}

func TestReviewMaxFixRounds_GlobalRejectsNegativeAndPerRepositoryOverride(t *testing.T) {
	for _, value := range []string{"-1", "1.5"} {
		if _, err := LoadGlobalFromBytes([]byte("review:\n  max_fix_rounds: " + value + "\n")); err == nil {
			t.Errorf("global config accepted invalid max_fix_rounds %q", value)
		}
	}
	_, err := LoadGlobalFromBytes([]byte("repository_overrides:\n  https://github.com/acme/app:\n    review:\n      max_fix_rounds: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "review.max_fix_rounds") {
		t.Fatalf("repository_overrides max_fix_rounds should fail naming the key, got %v", err)
	}
}
