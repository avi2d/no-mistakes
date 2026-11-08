package procreap

import (
	"testing"
	"time"
)

func TestSelectSpinningLeaf(t *testing.T) {
	t.Parallel()
	const agent = 100
	const wall = 10 * time.Second
	state := func(pid, ppid int, stat string, total time.Duration, command string) cpuState {
		return cpuState{PID: pid, PPID: ppid, PGID: 7, Stat: stat, Elapsed: time.Minute, Total: total, Command: command}
	}
	cases := []struct {
		name   string
		before []cpuState
		after  []cpuState
		want   int
		wantOK bool
	}{
		{
			"hot leaf picked",
			[]cpuState{state(agent, 1, "S", 0, "agent"), state(101, agent, "R", 0, "spin probe")},
			[]cpuState{state(agent, 1, "S", time.Second, "agent"), state(101, agent, "R", 9*time.Second, "spin probe")},
			101, true,
		},
		{
			"below bound ignored",
			[]cpuState{state(agent, 1, "S", 0, "agent"), state(101, agent, "S", 0, "sleep 30")},
			[]cpuState{state(agent, 1, "S", time.Second, "agent"), state(101, agent, "S", 4*time.Second, "sleep 30")},
			0, false,
		},
		{
			"hot parent is not a leaf",
			[]cpuState{state(agent, 1, "S", 0, "agent"), state(101, agent, "R", 0, "sh -c"), state(102, 101, "S", 0, "sleep 30")},
			[]cpuState{state(agent, 1, "S", time.Second, "agent"), state(101, agent, "R", 9*time.Second, "sh -c"), state(102, 101, "S", 0, "sleep 30")},
			0, false,
		},
		{
			"a burning root is the turn itself, not a leaf",
			[]cpuState{state(agent, 1, "S", 0, "agent"), state(101, agent, "S", 0, "sleep 30")},
			[]cpuState{state(agent, 1, "S", 9*time.Second, "agent"), state(101, agent, "S", 0, "sleep 30")},
			0, false,
		},
		{
			"leaf gone in the second sample ignored",
			[]cpuState{state(agent, 1, "S", 0, "agent"), state(101, agent, "R", 0, "spin")},
			[]cpuState{state(agent, 1, "S", time.Second, "agent")},
			0, false,
		},
		{
			"zombie leaf ignored",
			[]cpuState{state(agent, 1, "S", 0, "agent"), state(101, agent, "R", 0, "spin")},
			[]cpuState{state(agent, 1, "S", time.Second, "agent"), state(101, agent, "Z", 9*time.Second, "spin")},
			0, false,
		},
		{
			"backwards clock ignored",
			[]cpuState{state(agent, 1, "S", 0, "agent"), state(101, agent, "R", 5*time.Second, "spin")},
			[]cpuState{state(agent, 1, "S", time.Second, "agent"), state(101, agent, "R", 3*time.Second, "spin")},
			0, false,
		},
		{
			"hottest leaf wins",
			[]cpuState{state(agent, 1, "S", 0, "agent"), state(101, agent, "R", 0, "warm"), state(102, agent, "R", 0, "hot")},
			[]cpuState{state(agent, 1, "S", time.Second, "agent"), state(101, agent, "R", 6*time.Second, "warm"), state(102, agent, "R", 9*time.Second, "hot")},
			102, true,
		},
	}
	for _, tc := range cases {
		got, frac, cmd, ok := selectSpinningLeaf(agent, indexCPUStates(tc.before), indexCPUStates(tc.after), wall, 0.5)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("%s: selectSpinningLeaf = (%d, %v), want (%d, %v)", tc.name, got, ok, tc.want, tc.wantOK)
		}
		if ok && cmd == "" {
			t.Errorf("%s: selected leaf without its command line", tc.name)
		}
		if ok && frac < 0.5 {
			t.Errorf("%s: selected fraction %v below the bound", tc.name, frac)
		}
	}
	if _, _, _, ok := selectSpinningLeaf(agent, map[int]cpuState{}, map[int]cpuState{}, 0, 0.5); ok {
		t.Error("zero wall must select nothing")
	}
}

func TestRootAgeMatches(t *testing.T) {
	t.Parallel()
	if !rootAgeMatches(10*time.Minute, 10*time.Minute+time.Second) {
		t.Error("a matching age must verify")
	}
	if rootAgeMatches(time.Minute, 10*time.Minute) {
		t.Error("a younger table entry is a reused PID and must not verify")
	}
	if rootAgeMatches(10*time.Minute, time.Minute) {
		t.Error("an older table entry cannot be the launch and must not verify")
	}
}

func TestKillSpinningLeaf_UnknownRootKillsNothing(t *testing.T) {
	t.Parallel()
	old := listCPUStatesFunc
	listCPUStatesFunc = func() ([]cpuState, error) {
		return []cpuState{{PID: 7, PPID: 1, Stat: "S", Elapsed: time.Minute}}, nil
	}
	defer func() { listCPUStatesFunc = old }()
	if victim, err := KillSpinningLeaf(999999, 0.5, time.Millisecond, time.Millisecond, time.Time{}); err != nil || victim != nil {
		t.Fatalf("unknown root: victim=%+v err=%v, want nothing killed", victim, err)
	}
}

func TestKillSpinningLeaf_StaleRootKillsNothing(t *testing.T) {
	t.Parallel()
	oldList, oldSignal := listCPUStatesFunc, signalProcessFunc
	calls := 0
	listCPUStatesFunc = func() ([]cpuState, error) {
		calls++
		leaf := cpuState{PID: 101, PPID: 100, PGID: 7, Stat: "R", Elapsed: time.Minute, Total: time.Duration(calls) * time.Hour, Command: "spin"}
		return []cpuState{{PID: 100, PPID: 1, PGID: 7, Stat: "S", Elapsed: 5 * time.Second}, leaf}, nil
	}
	signalProcessFunc = func(pid int, sig procSignal) error {
		t.Errorf("reused PID %d must never be signalled", pid)
		return nil
	}
	defer func() { listCPUStatesFunc, signalProcessFunc = oldList, oldSignal }()
	if victim, err := KillSpinningLeaf(100, 0.5, time.Millisecond, time.Millisecond, time.Now().Add(-time.Hour)); err != nil || victim != nil {
		t.Fatalf("reused PID: victim=%+v err=%v, want nothing killed", victim, err)
	}
}
