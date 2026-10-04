package cli

import (
	"net"
	"regexp"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/telemetry"
)

var proxyEnvNames = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"}

// startDoctorDaemon starts an in-process daemon, so the daemon's environment is
// this test process's environment.
func startDoctorDaemon(t *testing.T) {
	t.Helper()
	nmHome := makeSocketSafeTempDir(t)
	t.Setenv("NM_HOME", nmHome)
	p := paths.WithRoot(nmHome)
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	startTestDaemon(t, p, database)
}

func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, name := range proxyEnvNames {
		t.Setenv(name, "")
	}
}

// deadProxyAddress returns a loopback address that refuses connections.
func deadProxyAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func liveProxyAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return ln.Addr().String()
}

func doctorProxyLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "daemon proxy") {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestDoctorFailsWhenTheDaemonNamesAProxyNothingListensOn(t *testing.T) {
	restore := telemetry.SetDefaultForTesting(&telemetryRecorder{})
	defer restore()
	clearProxyEnv(t)
	dead := deadProxyAddress(t)
	live := liveProxyAddress(t)
	t.Setenv("HTTPS_PROXY", "http://user:secret@"+dead)
	t.Setenv("http_proxy", "http://"+live)
	startDoctorDaemon(t)

	out, err := executeCmd("doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	lines := strings.Join(doctorProxyLines(out), "\n")
	failLine := regexp.MustCompile(`✗.*daemon proxy.*HTTPS_PROXY names ` + regexp.QuoteMeta(dead) + `, where nothing listens`)
	if !failLine.MatchString(lines) {
		t.Fatalf("doctor did not fail on the dead HTTPS_PROXY %s:\n%s", dead, out)
	}
	if !regexp.MustCompile(`✓.*daemon proxy.*http_proxy ` + regexp.QuoteMeta(live) + ` is listening`).MatchString(lines) {
		t.Fatalf("doctor did not pass the listening http_proxy %s:\n%s", live, out)
	}
	if strings.Contains(out, "secret") {
		t.Fatalf("doctor printed the proxy credentials:\n%s", out)
	}
	if !strings.Contains(out, "some checks failed") {
		t.Fatalf("doctor did not report the failure:\n%s", out)
	}
}

func TestDoctorPassesADaemonWithoutAProxy(t *testing.T) {
	restore := telemetry.SetDefaultForTesting(&telemetryRecorder{})
	defer restore()
	clearProxyEnv(t)
	startDoctorDaemon(t)

	out, err := executeCmd("doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	lines := doctorProxyLines(out)
	if len(lines) != 1 || !strings.Contains(lines[0], "✓") || !strings.Contains(lines[0], "none set") {
		t.Fatalf("daemon proxy lines = %q, want one passing \"none set\"\n%s", lines, out)
	}
}

func TestProxyDialAddress(t *testing.T) {
	for value, want := range map[string]string{
		"http://127.0.0.1:57649":    "127.0.0.1:57649",
		"127.0.0.1:57649":           "127.0.0.1:57649",
		"http://user:pw@proxy.corp": "proxy.corp:80",
		"https://proxy.corp":        "proxy.corp:443",
		"socks5h://[::1]:1080":      "[::1]:1080",
		"socks5://proxy.corp":       "proxy.corp:1080",
	} {
		got, ok := proxyDialAddress(value)
		if !ok || got != want {
			t.Errorf("proxyDialAddress(%q) = %q, %v; want %q", value, got, ok, want)
		}
	}
	for _, value := range []string{"http://", "://:80"} {
		if got, ok := proxyDialAddress(value); ok {
			t.Errorf("proxyDialAddress(%q) = %q, want no address", value, got)
		}
	}
}
