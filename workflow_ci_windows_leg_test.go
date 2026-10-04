package main

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// The workflow needs hosted runners, so these tests assert it through a typed
// workflow, `go list` package sets, and the test names `go test` would run.

// minBuildHeadroom covers a cold-cache compile on a Windows runner, which runs
// before go test's own -timeout clock starts.
const minBuildHeadroom = 10 * time.Minute

var windowsShardJobs = []string{"test-windows-core", "test-windows-git", "test-windows-steps"}

func loadCIWorkflowDoc(t *testing.T) *wfDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	var wf wfDoc
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parse CI workflow: %v", err)
	}
	for name, job := range wf.Jobs {
		job.name = name
	}
	return &wf
}

func ciJob(t *testing.T, wf *wfDoc, name string) *wfJob {
	t.Helper()
	job, ok := wf.Jobs[name]
	if !ok {
		t.Fatalf("CI workflow has no %s job", name)
	}
	return job
}

type ciShard struct {
	job      string
	name     string
	goos     string
	packages []string
	exclude  string
	run      string
	skip     string
	timeout  string
}

func ciShards(t *testing.T, wf *wfDoc, jobName, goos string) []ciShard {
	t.Helper()
	job := ciJob(t, wf, jobName)
	var shards []ciShard
	for _, row := range job.Strategy.Matrix.Include {
		shard := ciShard{
			job:      jobName,
			name:     row["shard"],
			goos:     goos,
			packages: strings.Fields(row["packages"]),
			exclude:  row["exclude"],
			run:      row["run"],
			skip:     row["skip"],
			timeout:  row["timeout"],
		}
		if shard.name == "" {
			t.Fatalf("%s matrix row %v has no shard name", jobName, row)
		}
		if (len(shard.packages) == 0) == (shard.exclude == "") {
			t.Fatalf("shard %s must set exactly one of packages or exclude, got %v", shard.name, row)
		}
		for _, filter := range []string{shard.run, shard.skip} {
			if strings.Contains(filter, "/") {
				t.Fatalf("shard %s filter %q reaches into subtests; split by top-level test name only", shard.name, filter)
			}
			if _, err := regexp.Compile(filter); err != nil {
				t.Fatalf("shard %s filter %q: %v", shard.name, filter, err)
			}
		}
		shards = append(shards, shard)
	}
	if len(shards) == 0 {
		t.Fatalf("%s has no shards", jobName)
	}
	return shards
}

func windowsCIShards(t *testing.T, wf *wfDoc) []ciShard {
	t.Helper()
	var shards []ciShard
	for _, job := range windowsShardJobs {
		shards = append(shards, ciShards(t, wf, job, "windows")...)
	}
	return shards
}

// selects mirrors go test's top-level -run and -skip matching for a filter
// without a slash.
func (s ciShard) selects(test string) bool {
	if s.run != "" && !regexp.MustCompile(s.run).MatchString(test) {
		return false
	}
	return s.skip == "" || !regexp.MustCompile(s.skip).MatchString(test)
}

type goTestPackage struct {
	ImportPath   string
	Dir          string
	TestGoFiles  []string
	XTestGoFiles []string
}

func goListTestPackages(t *testing.T, goos string, patterns ...string) []goTestPackage {
	t.Helper()
	cmd := exec.Command("go", append([]string{"list", "-json"}, patterns...)...)
	cmd.Env = append(os.Environ(), "GOOS="+goos)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("GOOS=%s go list %s: %v", goos, strings.Join(patterns, " "), err)
	}
	var packages []goTestPackage
	decoder := json.NewDecoder(strings.NewReader(string(out)))
	for {
		var pkg goTestPackage
		if err := decoder.Decode(&pkg); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		packages = append(packages, pkg)
	}
	return packages
}

func importPaths(packages []goTestPackage) []string {
	var paths []string
	for _, pkg := range packages {
		paths = append(paths, pkg.ImportPath)
	}
	slices.Sort(paths)
	return paths
}

func (s ciShard) resolvePackages(t *testing.T, all []goTestPackage) []string {
	t.Helper()
	if s.exclude == "" {
		return importPaths(goListTestPackages(t, s.goos, s.packages...))
	}
	exclude := regexp.MustCompile(s.exclude)
	var remainder []string
	for _, pkg := range importPaths(all) {
		if !exclude.MatchString(pkg) {
			remainder = append(remainder, pkg)
		}
	}
	return remainder
}

func goTestNames(t *testing.T, pkg goTestPackage) []string {
	t.Helper()
	var names []string
	fset := token.NewFileSet()
	for _, file := range append(append([]string{}, pkg.TestGoFiles...), pkg.XTestGoFiles...) {
		parsed, err := parser.ParseFile(fset, filepath.Join(pkg.Dir, file), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			name := fn.Name.Name
			switch {
			case name == "TestMain":
			case isGoTestName(name, "Test"), isGoTestName(name, "Fuzz"), isGoTestName(name, "Example"):
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	return names
}

func isGoTestName(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	if len(name) == len(prefix) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(name[len(prefix):])
	return !unicode.IsLower(r)
}

func shardCoverageGaps(t *testing.T, shards []ciShard, all []goTestPackage) []string {
	t.Helper()
	including := map[string][]ciShard{}
	for _, shard := range shards {
		for _, pkg := range shard.resolvePackages(t, all) {
			including[pkg] = append(including[pkg], shard)
		}
	}
	var gaps []string
	for _, pkg := range all {
		if len(including[pkg.ImportPath]) == 0 {
			gaps = append(gaps, pkg.ImportPath+" runs in no shard")
			continue
		}
		for _, test := range goTestNames(t, pkg) {
			var selecting []string
			for _, shard := range including[pkg.ImportPath] {
				if shard.selects(test) {
					selecting = append(selecting, shard.name)
				}
			}
			switch len(selecting) {
			case 0:
				gaps = append(gaps, pkg.ImportPath+"."+test+" runs in no shard")
			case 1:
			default:
				gaps = append(gaps, pkg.ImportPath+"."+test+" runs in more than one shard: "+strings.Join(selecting, ", "))
			}
		}
	}
	return gaps
}

func TestCIWorkflow_ShardsRunEveryTestExactlyOnce(t *testing.T) {
	t.Parallel()

	wf := loadCIWorkflowDoc(t)
	for _, leg := range []struct {
		goos   string
		shards []ciShard
	}{
		{"windows", windowsCIShards(t, wf)},
		{"darwin", ciShards(t, wf, "test-macos", "darwin")},
	} {
		all := goListTestPackages(t, leg.goos, "./...")
		if gaps := shardCoverageGaps(t, leg.shards, all); len(gaps) > 0 {
			t.Errorf("%s shards must run every test exactly once:\n%s", leg.goos, strings.Join(gaps, "\n"))
		}
	}
}

func TestCIWorkflow_ShardGroupsKeepTheirPackages(t *testing.T) {
	t.Parallel()

	wf := loadCIWorkflowDoc(t)
	all := goListTestPackages(t, "windows", "./...")
	groupPackages := func(job string) []string {
		var packages []string
		for _, shard := range ciShards(t, wf, job, "windows") {
			for _, pkg := range shard.resolvePackages(t, all) {
				if !slices.Contains(packages, pkg) {
					packages = append(packages, pkg)
				}
			}
		}
		slices.Sort(packages)
		return packages
	}

	if got, want := groupPackages("test-windows-steps"), importPaths(goListTestPackages(t, "windows", "./internal/pipeline/steps/...")); !slices.Equal(got, want) {
		t.Errorf("windows-steps shards run %v, want exactly ./internal/pipeline/steps/... %v", got, want)
	}
	git := groupPackages("test-windows-git")
	for _, pkg := range []string{"internal/git", "internal/branchsync"} {
		if !slices.Contains(git, "github.com/kunchenguid/no-mistakes/"+pkg) {
			t.Errorf("windows-git shards must run %s, the documented Windows wall floor", pkg)
		}
	}
	var remainders []string
	for _, shard := range windowsCIShards(t, wf) {
		if shard.exclude != "" {
			remainders = append(remainders, shard.job+"/"+shard.name)
		}
	}
	if len(remainders) != 1 || !strings.HasPrefix(remainders[0], "test-windows-core/") {
		t.Errorf("windows-core must carry the only Windows go-list remainder shard, got %v", remainders)
	}
}

func TestCIWorkflow_ShardGroupsKeepTheRequiredCheckNames(t *testing.T) {
	t.Parallel()

	wf := loadCIWorkflowDoc(t)
	if name := ciJob(t, wf, "test-linux").Name; name != "test (ubuntu-latest)" {
		t.Errorf("test-linux job name = %q, want the required check test (ubuntu-latest)", name)
	}
	gate := ciJob(t, wf, "test-gate")
	if gate.Name != "test (${{ matrix.group }})" {
		t.Errorf("test-gate name = %q, want test (${{ matrix.group }})", gate.Name)
	}
	if normalizeWorkflowCondition(gate.If) != "always()" {
		t.Errorf("test-gate must run with if: always() so a failed shard fails its group check, got %q", gate.If)
	}
	wantGroups := map[string]string{
		"macos-latest":  "test-macos",
		"windows-core":  "test-windows-core",
		"windows-git":   "test-windows-git",
		"windows-steps": "test-windows-steps",
	}
	gotGroups := map[string]string{}
	for _, row := range gate.Strategy.Matrix.Include {
		gotGroups[row["group"]] = row["job"]
	}
	if len(gotGroups) != len(wantGroups) {
		t.Fatalf("test-gate groups = %v, want %v", gotGroups, wantGroups)
	}
	needs := workflowNeeds(gate.Needs)
	for group, job := range wantGroups {
		if gotGroups[group] != job {
			t.Errorf("required check test (%s) gates on %q, want %q", group, gotGroups[group], job)
		}
		if !slices.Contains(needs, job) {
			t.Errorf("test-gate must need %s", job)
		}
	}
	steps := gate.Steps
	if len(steps) != 1 || steps[0].Env["JOB"] != "${{ matrix.job }}" || steps[0].Env["NEEDS"] != "${{ toJSON(needs) }}" ||
		!strings.Contains(steps[0].Run, `test "$result" = success`) {
		t.Errorf("test-gate must fail unless its own group's result is success, got %#v", steps)
	}
}

func workflowNeeds(needs any) []string {
	switch value := needs.(type) {
	case string:
		return []string{value}
	case []any:
		var out []string
		for _, item := range value {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func namedStep(t *testing.T, job *wfJob, name string) (int, wfStep) {
	t.Helper()
	for i, step := range job.Steps {
		if step.Name == name {
			return i, step
		}
	}
	t.Fatalf("%s has no %q step", job.name, name)
	return 0, wfStep{}
}

func TestCIWorkflow_WindowsTestStepRunsItsMatrixRow(t *testing.T) {
	t.Parallel()

	wf := loadCIWorkflowDoc(t)
	wantEnv := map[string]string{
		"NM_CI_PACKAGES": "${{ matrix.packages }}",
		"NM_CI_EXCLUDE":  "${{ matrix.exclude }}",
		"NM_CI_RUN":      "${{ matrix.run }}",
		"NM_CI_SKIP":     "${{ matrix.skip }}",
		"NM_CI_TIMEOUT":  "${{ matrix.timeout }}",
	}
	wantLines := []string{
		`$pkgs = go list ./... | Where-Object { $_ -notmatch $env:NM_CI_EXCLUDE }`,
		`$pkgs = -split $env:NM_CI_PACKAGES`,
		`if (-not $pkgs) { throw "shard resolved no packages" }`,
		`if ($env:NM_CI_RUN) { $filters += "-run=$env:NM_CI_RUN" }`,
		`if ($env:NM_CI_SKIP) { $filters += "-skip=$env:NM_CI_SKIP" }`,
		`go test -v "-timeout=$env:NM_CI_TIMEOUT" @filters @pkgs`,
	}
	for _, name := range windowsShardJobs {
		_, step := namedStep(t, ciJob(t, wf, name), "Test on Windows")
		if step.Shell != "pwsh" {
			t.Errorf("%s Windows tests must run with pwsh, got %q", name, step.Shell)
		}
		for key, value := range wantEnv {
			if step.Env[key] != value {
				t.Errorf("%s Windows test env %s = %q, want %q", name, key, step.Env[key], value)
			}
		}
		lines := strings.Split(step.Run, "\n")
		for i := range lines {
			lines[i] = strings.TrimSpace(lines[i])
		}
		for _, want := range wantLines {
			if !slices.Contains(lines, want) {
				t.Errorf("%s Windows test step must contain %q", name, want)
			}
		}
	}

	_, macStep := namedStep(t, ciJob(t, wf, "test-macos"), "Test on macOS")
	if macStep.Env["NM_CI_PACKAGES"] != "${{ matrix.packages }}" || macStep.Env["NM_CI_EXCLUDE"] != "${{ matrix.exclude }}" {
		t.Errorf("macOS test step must read its packages from the matrix row, got env %v", macStep.Env)
	}
	for _, want := range []string{`pkgs=$(go list ./... | grep -Ev "$NM_CI_EXCLUDE")`, `pkgs=$NM_CI_PACKAGES`, `go test -race $pkgs`} {
		if !strings.Contains(macStep.Run, want) {
			t.Errorf("macOS test step must contain %q", want)
		}
	}
	for _, shard := range ciShards(t, wf, "test-macos", "darwin") {
		if shard.run != "" || shard.skip != "" {
			t.Errorf("macOS shard %s sets a name filter the macOS step does not pass to go test", shard.name)
		}
	}
}

type workflowCommand struct {
	step int
	line int
	name string
	args []string
}

func normalizeWorkflowCondition(condition string) string {
	condition = strings.TrimSpace(condition)
	condition = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(condition, "${{"), "}}"))
	return strings.Join(strings.Fields(condition), " ")
}

func workflowCommandsMatching(steps []wfStep, include func(wfStep) bool) []workflowCommand {
	var commands []workflowCommand
	for stepIndex, step := range steps {
		if !include(step) {
			continue
		}
		for lineIndex, line := range strings.Split(step.Run, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) == 0 || strings.HasPrefix(fields[0], "$p") || strings.ContainsAny(fields[0], "{}()") {
				continue
			}
			commands = append(commands, workflowCommand{step: stepIndex, line: lineIndex, name: fields[0], args: fields[1:]})
		}
	}
	return commands
}

func findWorkflowCommandWithArg(commands []workflowCommand, name, arg string) (workflowCommand, bool) {
	for _, command := range commands {
		if strings.EqualFold(command.name, name) && command.hasArg(arg) {
			return command, true
		}
	}
	return workflowCommand{}, false
}

func (c workflowCommand) hasArg(want string) bool {
	for _, arg := range c.args {
		if strings.EqualFold(arg, want) {
			return true
		}
	}
	return false
}

func TestCIWorkflow_WindowsTestsRunWithScanExclusions(t *testing.T) {
	t.Parallel()

	wf := loadCIWorkflowDoc(t)
	for _, name := range windowsShardJobs {
		job := ciJob(t, wf, name)
		testStep, _ := namedStep(t, job, "Test on Windows")
		commands := workflowCommandsMatching(job.Steps, func(wfStep) bool { return true })
		for _, option := range []string{"-ExclusionPath", "-ExclusionProcess"} {
			command, ok := findWorkflowCommandWithArg(commands, "Add-MpPreference", option)
			if !ok {
				t.Fatalf("%s must apply Defender %s before tests", name, option)
			}
			if job.Steps[command.step].Shell != "pwsh" {
				t.Errorf("%s Defender exclusions must execute with pwsh, got %q", name, job.Steps[command.step].Shell)
			}
			if command.step >= testStep {
				t.Errorf("%s Defender exclusion at step %d must run before the tests at step %d", name, command.step, testStep)
			}
		}
	}
}

func TestCIWorkflow_WindowsHangSurfacesAsGoTimeoutNotJobCancellation(t *testing.T) {
	t.Parallel()

	wf := loadCIWorkflowDoc(t)
	for _, name := range windowsShardJobs {
		job := ciJob(t, wf, name)
		if job.TimeoutMinutes != 40 {
			t.Errorf("%s timeout-minutes = %d, want 40 so a wedged runner cannot burn a full six-hour budget", name, job.TimeoutMinutes)
		}
		jobTimeout := time.Duration(job.TimeoutMinutes) * time.Minute
		for _, shard := range ciShards(t, wf, name, "windows") {
			if slices.Contains(shard.packages, "./...") {
				t.Errorf("shard %s runs ./...; a hang in a late package would cancel the job before go test -timeout fires", shard.name)
			}
			goTimeout, err := time.ParseDuration(shard.timeout)
			if err != nil {
				t.Errorf("shard %s timeout %q: %v", shard.name, shard.timeout, err)
				continue
			}
			if goTimeout+minBuildHeadroom > jobTimeout {
				t.Errorf("shard %s go test -timeout %s leaves under %s of build headroom inside the %s job cap; the Go timeout must fire first so a hang produces a goroutine dump", shard.name, goTimeout, minBuildHeadroom, jobTimeout)
			}
		}
	}
}
