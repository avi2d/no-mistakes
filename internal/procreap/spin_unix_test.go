//go:build unix

package procreap

import (
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseCPUTime(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"00:00:00", 0},
		{"00:00:01", time.Second},
		{"01:02:03", 3723 * time.Second},
		{"1-02:03:04", 26*time.Hour + 3*time.Minute + 4*time.Second},
		{"0:00.07", 70 * time.Millisecond},
		{"00:00:01.5", 1500 * time.Millisecond},
	}
	for _, tc := range cases {
		if got, err := parseCPUTime(tc.in); err != nil || got != tc.want {
			t.Errorf("parseCPUTime(%q) = (%v, %v), want (%v, nil)", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "nope", "00:00:0x", "00:00:01.x"} {
		if got, err := parseCPUTime(bad); err == nil {
			t.Errorf("parseCPUTime(%q) = %v, want an error", bad, got)
		}
	}
}

func TestParseProcessCPUTable(t *testing.T) {
	t.Parallel()
	out := "    123    100    100 S   00:00:05 00:00:01 /sbin/foo --bar  baz\n" +
		"    124    123    100 R   00:00:03 00:00:02.5 spin\n" +
		"    125    123    100 R      00:01 00:01 00:01 echoed\n" +
		"garbage line\n"
	states := parseProcessCPUTable(out)
	if len(states) != 3 {
		t.Fatalf("parsed %d states, want 3", len(states))
	}
	if states[2].Command != "00:01 echoed" {
		t.Errorf("command = %q, want the full command when a clock repeats the wall age", states[2].Command)
	}
	if states[0].Command != "/sbin/foo --bar  baz" {
		t.Errorf("command = %q, want embedded whitespace preserved", states[0].Command)
	}
	if states[1].Total != 2500*time.Millisecond {
		t.Errorf("fractional CPU = %v, want 2.5s", states[1].Total)
	}
}

func startSpinner(t *testing.T, script string) (root int, cleanup func()) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start spinner: %v", err)
	}
	root = cmd.Process.Pid
	return root, func() {
		pgid, err := syscall.Getpgid(root)
		if err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
		_ = cmd.Wait()
	}
}

func processGone(pid int) bool {
	return syscall.Kill(pid, 0) != nil
}

func TestKillSpinningLeaf_KillsHotLeaf(t *testing.T) {
	root, cleanup := startSpinner(t, "while :; do :; done & wait")
	defer cleanup()
	time.Sleep(300 * time.Millisecond)
	victim, err := KillSpinningLeaf(root, 0.4, 3*time.Second, 3*time.Second, time.Now())
	if err != nil {
		t.Fatalf("KillSpinningLeaf: %v", err)
	}
	if victim == nil {
		t.Fatal("a leaf spinning a core was not killed")
	}
	if !strings.Contains(victim.Command, "while") {
		t.Errorf("victim command = %q, want the spinning probe", victim.Command)
	}
	if victim.CPUFraction < 0.4 {
		t.Errorf("victim fraction = %v, want at least the bound", victim.CPUFraction)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !processGone(victim.PID) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !processGone(victim.PID) {
		t.Errorf("victim pid=%d still alive after the kill", victim.PID)
	}
}

func TestKillSpinningLeaf_IdleTreeKillsNothing(t *testing.T) {
	root, cleanup := startSpinner(t, "sleep 30 & wait")
	defer cleanup()
	time.Sleep(200 * time.Millisecond)
	if victim, err := KillSpinningLeaf(root, 0.4, 300*time.Millisecond, time.Second, time.Now()); err != nil || victim != nil {
		t.Fatalf("idle tree: victim=%+v err=%v, want nothing killed", victim, err)
	}
}
