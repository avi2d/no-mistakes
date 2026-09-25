package config

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPRClampTitle_OverLimitFitsWithSuffixCounted(t *testing.T) {
	t.Parallel()

	pr := PR{TitleMaxLength: 90}
	long := "feat(pipeline): add a squash-suffix-aware title budget with word-boundary shortening for long descriptions"
	got, err := pr.ClampTitle(long)
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(got)+prTitleSquashSuffixReserveChars > pr.TitleMaxLength {
		t.Fatalf("ClampTitle() = %q (%d chars), exceeds budget with suffix counted", got, utf8.RuneCountInString(got))
	}
	if !strings.HasPrefix(got, "feat(pipeline): ") {
		t.Fatalf("ClampTitle() cut the conventional prefix: %q", got)
	}
	if strings.HasSuffix(got, " ") {
		t.Fatalf("ClampTitle() left a trailing space: %q", got)
	}
}

func TestPRClampTitle_WithinLimitUntouched(t *testing.T) {
	t.Parallel()

	pr := PR{TitleMaxLength: 90}
	title := "fix(ci): retry transient workflow failures"
	got, err := pr.ClampTitle(title)
	if err != nil {
		t.Fatal(err)
	}
	if got != title {
		t.Fatalf("ClampTitle() = %q, want untouched %q", got, title)
	}
}

func TestPRClampTitle_OffByDefault(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 500)
	got, err := PR{}.ClampTitle(long)
	if err != nil {
		t.Fatal(err)
	}
	if got != long {
		t.Fatalf("ClampTitle() shortened without a configured limit")
	}
	if cfg := Merge(DefaultGlobalConfig(), &RepoConfig{}); cfg.PR.TitleMaxLength != 0 {
		t.Fatalf("default TitleMaxLength = %d, want off", cfg.PR.TitleMaxLength)
	}
}

func TestPRClampTitle_PrefixWithoutRoomFailsClosed(t *testing.T) {
	t.Parallel()

	pr := PR{TitleMaxLength: 12}
	if _, err := pr.ClampTitle("feat(pipeline): something far too long"); err == nil {
		t.Fatal("ClampTitle() cut the conventional prefix instead of failing")
	}
}

func TestPRClampTitle_TinyLimitFailsClosed(t *testing.T) {
	t.Parallel()

	pr := PR{TitleMaxLength: 5}
	if _, err := pr.ClampTitle("a title with no colon at all"); err == nil {
		t.Fatal("ClampTitle() accepted a limit with no room for any title")
	}
}

func TestLoadRepo_RejectsNonPositiveTitleMaxLength(t *testing.T) {
	t.Parallel()

	for _, data := range []string{
		"pr:\n  title_max_length: 0\n",
		"pr:\n  title_max_length: -5\n",
	} {
		if _, err := LoadRepoFromBytes([]byte(data)); err == nil {
			t.Fatalf("LoadRepoFromBytes() accepted %q", data)
		}
	}
}

func TestLoadGlobal_RejectsNonPositiveTitleMaxLength(t *testing.T) {
	t.Parallel()

	if _, err := LoadGlobalFromBytes([]byte("pr:\n  title_max_length: 0\n")); err == nil {
		t.Fatal("LoadGlobalFromBytes() accepted non-positive pr.title_max_length")
	}
}

func TestMerge_TitleMaxLengthRepoWinsOverGlobal(t *testing.T) {
	t.Parallel()

	repoLen, globalLen := 90, 100
	global := DefaultGlobalConfig()
	global.PR = GlobalPRRaw{TitleMaxLength: &globalLen}
	got := Merge(global, &RepoConfig{PR: PRRaw{TitleMaxLength: &repoLen}})
	if got.PR.TitleMaxLength != repoLen {
		t.Fatalf("TitleMaxLength = %d, want repo %d", got.PR.TitleMaxLength, repoLen)
	}
	got = Merge(global, &RepoConfig{})
	if got.PR.TitleMaxLength != globalLen {
		t.Fatalf("TitleMaxLength = %d, want global fallback %d", got.PR.TitleMaxLength, globalLen)
	}
}

func TestLoadRepo_ReadsTitleMaxLength(t *testing.T) {
	t.Parallel()

	cfg, err := LoadRepoFromBytes([]byte("pr:\n  title_max_length: 90\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PR.TitleMaxLength == nil || *cfg.PR.TitleMaxLength != 90 {
		t.Fatalf("pr.title_max_length = %v, want 90", cfg.PR.TitleMaxLength)
	}
	if merged := Merge(DefaultGlobalConfig(), cfg); merged.PR.TitleMaxLength != 90 {
		t.Fatalf("merged TitleMaxLength = %d, want 90", merged.PR.TitleMaxLength)
	}
}
