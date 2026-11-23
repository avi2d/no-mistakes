package testscript

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestWriteFileRejectsExecRacingWriteWithETXTBSY(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture")
	started := make(chan struct{})
	done := make(chan error, 1)
	syscall.ForkLock.Lock()
	go func() {
		close(started)
		done <- WriteFile(path, []byte("#!/bin/sh\nprintf fixture-ok"), 0o755)
	}()
	<-started
	select {
	case err := <-done:
		syscall.ForkLock.Unlock()
		t.Fatalf("write completed during fork: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		syscall.ForkLock.Unlock()
		t.Fatalf("fixture became visible during fork: %v", err)
	}
	syscall.ForkLock.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(path).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "fixture-ok" {
		t.Fatalf("output = %q, want fixture-ok", out)
	}
}
