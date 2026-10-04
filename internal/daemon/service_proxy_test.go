package daemon

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/paths"
)

var sandboxProxyEnv = [][2]string{
	{"HTTP_PROXY", "http://127.0.0.1:57649"},
	{"HTTPS_PROXY", "http://127.0.0.1:57649"},
	{"ALL_PROXY", "http://127.0.0.1:57649"},
	{"NO_PROXY", "github.com,githubusercontent.com,localhost,127.0.0.1,::1"},
	{"http_proxy", "http://127.0.0.1:57649"},
	{"https_proxy", "http://127.0.0.1:57649"},
	{"all_proxy", "http://127.0.0.1:57649"},
	{"no_proxy", "github.com,githubusercontent.com,localhost,127.0.0.1,::1"},
}

func exportSandboxProxy(t *testing.T) {
	t.Helper()
	for _, kv := range sandboxProxyEnv {
		t.Setenv(kv[0], kv[1])
	}
}

func unsetProxyEnv(t *testing.T) {
	t.Helper()
	for _, kv := range sandboxProxyEnv {
		t.Setenv(kv[0], "")
		if err := os.Unsetenv(kv[0]); err != nil {
			t.Fatal(err)
		}
	}
}

func assertCarriesNoProxy(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range sandboxProxyEnv {
		if strings.Contains(string(data), kv[0]) {
			t.Fatalf("service definition carries %s:\n%s", kv[0], data)
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("service definition mode = %o, want 0644", got)
	}
}

func stubProxyServiceRuntime(t *testing.T, goos string) (p *paths.Paths, home string) {
	t.Helper()
	p = paths.WithRoot(filepath.Join(t.TempDir(), "nm-home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	home = t.TempDir()
	t.Cleanup(stubServiceRuntime(t))
	runtimeGOOS = goos
	serviceUserHomeDir = func() (string, error) { return home, nil }
	serviceCurrentUser = func() (*user.User, error) { return &user.User{Uid: "501"}, nil }
	serviceExecutablePath = func() (string, error) { return "/usr/local/bin/no-mistakes", nil }
	serviceCommandRunner = func(string, ...string) ([]byte, error) { return nil, nil }
	return p, home
}

func TestInstallLaunchAgentNeverBakesTheInstallingShellsProxy(t *testing.T) {
	p, _ := stubProxyServiceRuntime(t, "darwin")
	exportSandboxProxy(t)

	if _, err := installManagedServiceWithExecutable(p, "/usr/local/bin/no-mistakes"); err != nil {
		t.Fatal(err)
	}

	assertCarriesNoProxy(t, launchAgentPath(p))
}

func TestInstallSystemdUnitNeverBakesTheInstallingShellsProxy(t *testing.T) {
	p, _ := stubProxyServiceRuntime(t, "linux")
	exportSandboxProxy(t)

	if _, err := installManagedServiceWithExecutable(p, "/usr/local/bin/no-mistakes"); err != nil {
		t.Fatal(err)
	}

	assertCarriesNoProxy(t, systemdUserServicePath(p))
}

func TestStartDropsAProxyAnEarlierInstallBakedIntoTheLaunchAgent(t *testing.T) {
	p, home := stubProxyServiceRuntime(t, "darwin")
	unsetProxyEnv(t)

	plistPath := launchAgentPath(p)
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	var baked strings.Builder
	for _, kv := range sandboxProxyEnv {
		baked.WriteString("    <key>" + kv[0] + "</key>\n    <string>" + kv[1] + "</string>\n")
	}
	clean := renderLaunchAgent("/usr/local/bin/no-mistakes", p, home)
	stale := strings.Replace(clean, "    <key>PATH</key>", baked.String()+"    <key>PATH</key>", 1)
	if stale == clean {
		t.Fatal("could not splice a baked proxy into the rendered plist")
	}
	if err := os.WriteFile(plistPath, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}

	running := true
	serviceCommandRunner = func(name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		if strings.Contains(command, "launchctl bootout ") {
			running = false
		}
		if strings.Contains(command, "launchctl kickstart ") {
			running = true
		}
		return nil, nil
	}
	daemonHealthCheck = func(*paths.Paths) (bool, error) { return running, nil }

	if err := Start(p); err != nil {
		t.Fatalf("Start must refresh a launch agent that carries a baked proxy: %v", err)
	}

	assertCarriesNoProxy(t, plistPath)
}

func TestInstallSystemdUnitDropsAProxyAnEarlierInstallBaked(t *testing.T) {
	p, home := stubProxyServiceRuntime(t, "linux")
	unsetProxyEnv(t)

	unitPath := systemdUserServicePath(p)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := renderSystemdUnit("/usr/local/bin/no-mistakes", p, home)
	homeLine := systemdEnvironmentLine("HOME", home)
	var baked strings.Builder
	baked.WriteString(homeLine)
	for _, kv := range sandboxProxyEnv {
		baked.WriteString("\n" + systemdEnvironmentLine(kv[0], kv[1]))
	}
	stale := strings.Replace(clean, homeLine, baked.String(), 1)
	if stale == clean {
		t.Fatal("could not splice a baked proxy into the rendered unit")
	}
	if err := os.WriteFile(unitPath, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := installManagedServiceWithExecutable(p, "/usr/local/bin/no-mistakes"); err != nil {
		t.Fatal(err)
	}

	assertCarriesNoProxy(t, unitPath)
}
