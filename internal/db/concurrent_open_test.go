package db

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentOpensOfAFreshDatabaseNeverFailAsLocked(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		path := filepath.Join(t.TempDir(), "state.db")
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for opener := 0; opener < 4; opener++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				database, err := Open(path)
				if err != nil {
					errs <- err
					return
				}
				_ = database.Close()
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
}
