package daemon

import (
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/runsecret"
)

const stagedRunEnvLifetime = 5 * time.Minute

type runEnvKey struct{ repoID, branch, headSHA string }

type stagedVars struct {
	vars  map[string]string
	timer *time.Timer
}

// stagedRunEnv holds axi run's --run-env-file variables in memory only, from
// the stage call until the next run created for the same repository, branch
// and head claims them. Nothing here may reach the database, a log, the run
// folder or an error message.
type stagedRunEnv struct {
	mu      sync.Mutex
	pending map[runEnvKey]*stagedVars
}

func validateStagedRunEnv(key runEnvKey, vars map[string]string) error {
	if strings.TrimSpace(key.repoID) == "" || strings.TrimSpace(key.branch) == "" || strings.TrimSpace(key.headSHA) == "" {
		return fmt.Errorf("run env requires repo_id, branch and head_sha")
	}
	if len(vars) == 0 {
		return fmt.Errorf("run env names no variables")
	}
	if len(vars) > runsecret.MaxVariables {
		return fmt.Errorf("run env names more than %d variables", runsecret.MaxVariables)
	}
	size := 0
	for name, value := range vars {
		if err := runsecret.Validate(name, value); err != nil {
			return fmt.Errorf("run env: %w", err)
		}
		size += len(name) + len(value)
	}
	if size > runsecret.MaxFileBytes {
		return fmt.Errorf("run env exceeds %d bytes", runsecret.MaxFileBytes)
	}
	return nil
}

func (s *stagedRunEnv) stage(key runEnvKey, vars map[string]string) error {
	if err := validateStagedRunEnv(key, vars); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = make(map[runEnvKey]*stagedVars)
	}
	s.dropLocked(key)
	staged := &stagedVars{vars: maps.Clone(vars)}
	staged.timer = time.AfterFunc(stagedRunEnvLifetime, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.pending[key] == staged {
			delete(s.pending, key)
		}
	})
	s.pending[key] = staged
	return nil
}

func (s *stagedRunEnv) claim(key runEnvKey) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	staged := s.pending[key]
	if staged == nil {
		return nil
	}
	s.dropLocked(key)
	return staged.vars
}

func (s *stagedRunEnv) release(key runEnvKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropLocked(key)
}

func (s *stagedRunEnv) dropLocked(key runEnvKey) {
	if staged := s.pending[key]; staged != nil {
		staged.timer.Stop()
		delete(s.pending, key)
	}
}
