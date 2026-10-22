package config

import (
	"fmt"
	"strings"
)

const (
	freeModelReviewStep   = "review"
	freeModelCommitStep   = "commit"
	freeModelDocumentStep = "document"
	freeModelAdvisoryMode = "advisory"
)

type MinimaxExtraReview struct {
	Enabled       bool     `yaml:"enabled"`
	Model         string   `yaml:"model"`
	FallbackModel string   `yaml:"fallback_model"`
	Mode          string   `yaml:"mode"`
	Steps         []string `yaml:"steps"`
	Repos         []string `yaml:"repos"`
}

type BeamDrafts struct {
	Enabled       bool     `yaml:"enabled"`
	Model         string   `yaml:"model"`
	FallbackModel string   `yaml:"fallback_model"`
	Steps         []string `yaml:"steps"`
	EmptyRetries  int      `yaml:"empty_retries"`
	ApplyCheck    bool     `yaml:"apply_check"`
	Repos         []string `yaml:"repos"`
}

type minimaxExtraReviewRaw struct {
	Enabled       bool     `yaml:"enabled"`
	Model         string   `yaml:"model"`
	FallbackModel string   `yaml:"fallback_model"`
	Mode          string   `yaml:"mode"`
	Steps         []string `yaml:"steps"`
	Repos         []string `yaml:"repos"`
}

type beamDraftsRaw struct {
	Enabled       bool     `yaml:"enabled"`
	Model         string   `yaml:"model"`
	FallbackModel string   `yaml:"fallback_model"`
	Steps         []string `yaml:"steps"`
	EmptyRetries  *int     `yaml:"empty_retries"`
	ApplyCheck    *bool    `yaml:"apply_check"`
	Repos         []string `yaml:"repos"`
}

func derefMinimaxRaw(raw *minimaxExtraReviewRaw) minimaxExtraReviewRaw {
	if raw == nil {
		return minimaxExtraReviewRaw{}
	}
	return *raw
}

func derefBeamRaw(raw *beamDraftsRaw) beamDraftsRaw {
	if raw == nil {
		return beamDraftsRaw{}
	}
	return *raw
}

func parseMinimaxExtraReview(present bool, raw minimaxExtraReviewRaw) (MinimaxExtraReview, error) {
	if !present {
		return MinimaxExtraReview{}, nil
	}
	setting := MinimaxExtraReview{
		Enabled:       raw.Enabled,
		Model:         strings.TrimSpace(raw.Model),
		FallbackModel: strings.TrimSpace(raw.FallbackModel),
		Mode:          strings.TrimSpace(raw.Mode),
		Steps:         append([]string(nil), raw.Steps...),
		Repos:         append([]string(nil), raw.Repos...),
	}
	if setting.Model == "" {
		return MinimaxExtraReview{}, fmt.Errorf("minimax_extra_review.model must name the extra reviewer model")
	}
	if setting.FallbackModel == "" {
		return MinimaxExtraReview{}, fmt.Errorf("minimax_extra_review.fallback_model must name the fallback model")
	}
	if setting.Mode != freeModelAdvisoryMode {
		return MinimaxExtraReview{}, fmt.Errorf("minimax_extra_review.mode must be %q, got %q", freeModelAdvisoryMode, raw.Mode)
	}
	if len(setting.Steps) != 1 || setting.Steps[0] != freeModelReviewStep {
		return MinimaxExtraReview{}, fmt.Errorf("minimax_extra_review.steps must be [%s], got %q", freeModelReviewStep, raw.Steps)
	}
	if err := checkFreeModelRepos("minimax_extra_review", setting.Repos); err != nil {
		return MinimaxExtraReview{}, err
	}
	if setting.Enabled && len(setting.Repos) == 0 {
		return MinimaxExtraReview{}, fmt.Errorf("minimax_extra_review.enabled needs at least one repos entry, otherwise it allows nothing")
	}
	return setting, nil
}

func parseBeamDrafts(present bool, raw beamDraftsRaw) (BeamDrafts, error) {
	if !present {
		return BeamDrafts{}, nil
	}
	setting := BeamDrafts{
		Enabled:       raw.Enabled,
		Model:         strings.TrimSpace(raw.Model),
		FallbackModel: strings.TrimSpace(raw.FallbackModel),
		Steps:         append([]string(nil), raw.Steps...),
		Repos:         append([]string(nil), raw.Repos...),
		EmptyRetries:  1,
		ApplyCheck:    true,
	}
	if raw.EmptyRetries != nil {
		setting.EmptyRetries = *raw.EmptyRetries
	}
	if raw.ApplyCheck != nil {
		setting.ApplyCheck = *raw.ApplyCheck
	}
	if setting.Model == "" {
		return BeamDrafts{}, fmt.Errorf("beam_drafts.model must name the drafting model")
	}
	if setting.FallbackModel == "" {
		return BeamDrafts{}, fmt.Errorf("beam_drafts.fallback_model must name the fallback model")
	}
	wantSteps := []string{freeModelCommitStep, freeModelDocumentStep}
	if len(setting.Steps) != len(wantSteps) {
		return BeamDrafts{}, fmt.Errorf("beam_drafts.steps must be %q, got %q", wantSteps, raw.Steps)
	}
	for i, step := range wantSteps {
		if setting.Steps[i] != step {
			return BeamDrafts{}, fmt.Errorf("beam_drafts.steps must be %q, got %q", wantSteps, raw.Steps)
		}
	}
	if setting.EmptyRetries != 1 {
		return BeamDrafts{}, fmt.Errorf("beam_drafts.empty_retries must be 1, got %d", setting.EmptyRetries)
	}
	if !setting.ApplyCheck {
		return BeamDrafts{}, fmt.Errorf("beam_drafts.apply_check must stay true: a Beam edit is only used after the mechanical patch check")
	}
	if err := checkFreeModelRepos("beam_drafts", setting.Repos); err != nil {
		return BeamDrafts{}, err
	}
	if setting.Enabled && len(setting.Repos) == 0 {
		return BeamDrafts{}, fmt.Errorf("beam_drafts.enabled needs at least one repos entry, otherwise it allows nothing")
	}
	return setting, nil
}

func checkFreeModelRepos(key string, repos []string) error {
	for i, repo := range repos {
		if strings.TrimSpace(repo) == "" {
			return fmt.Errorf("%s.repos[%d] must name a repository", key, i)
		}
	}
	return nil
}

func (m MinimaxExtraReview) Allows(remoteURL string) bool {
	return m.Enabled && freeModelRepoAllowed(m.Repos, remoteURL)
}

func (b BeamDrafts) Allows(remoteURL string) bool {
	return b.Enabled && freeModelRepoAllowed(b.Repos, remoteURL)
}

func freeModelRepoAllowed(repos []string, remoteURL string) bool {
	name := freeModelRepoName(remoteURL)
	if name == "" {
		return false
	}
	for _, repo := range repos {
		if strings.TrimSpace(repo) == name {
			return true
		}
	}
	return false
}

func freeModelRepoName(remoteURL string) string {
	rest := strings.TrimSpace(remoteURL)
	rest = strings.TrimSuffix(rest, "/")
	if i := strings.LastIndex(rest, ":"); i >= 0 && !strings.Contains(rest[i:], "/") {
		rest = rest[i+1:]
	} else if i := strings.LastIndex(rest, "/"); i >= 0 {
		rest = rest[i+1:]
	}
	return strings.TrimSuffix(rest, ".git")
}
