package eval

import (
	"sync"
	"testing"
)

func TestConcurrentOpensOfAFreshRegistryNeverFailTheMigrationAsLocked(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		root := t.TempDir()
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for opener := 0; opener < 4; opener++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				store, err := Open(root)
				if err != nil {
					errs <- err
					return
				}
				_ = store.Close()
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
}
