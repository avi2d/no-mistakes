package daemon

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/telemetry"
)

const serviceTelemetryKey = "NO_MISTAKES_TELEMETRY"

func TestDaemonStartupNeverReenablesTelemetryEitherSideOptedOutOf(t *testing.T) {
	for _, value := range []string{"0", "false", "off"} {
		for _, direction := range []string{"supplied_opt_out", "shell_opt_out"} {
			t.Run(value+"/"+direction, func(t *testing.T) {
				supplied, shell := value, "1"
				if direction == "shell_opt_out" {
					supplied, shell = "1", value
				}
				assertServiceTelemetryTransport(t, map[string]string{
					serviceTelemetryKey:            supplied,
					"NM_SERVICE_TELEMETRY_PREPARE": "1",
					"NM_SERVICE_TELEMETRY_SHELL":   shell,
				}, false)
			})
		}
	}
	for _, value := range []string{"", "1"} {
		t.Run("enabled_control/"+value, func(t *testing.T) {
			assertServiceTelemetryTransport(t, map[string]string{
				serviceTelemetryKey:            value,
				"NM_SERVICE_TELEMETRY_PREPARE": "1",
				"NM_SERVICE_TELEMETRY_SHELL":   "1",
			}, true)
		})
	}
}

func TestManagedServiceDefinitionNeverStoresTheInstallingShellsTelemetry(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			p, _ := stubProxyServiceRuntime(t, goos)
			t.Setenv(serviceTelemetryKey, "0")

			if _, err := installManagedServiceWithExecutable(p, "/usr/local/bin/no-mistakes"); err != nil {
				t.Fatal(err)
			}

			path := launchAgentPath(p)
			if goos == "linux" {
				path = systemdUserServicePath(p)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), serviceTelemetryKey) {
				t.Fatalf("service definition carries %s:\n%s", serviceTelemetryKey, data)
			}
		})
	}
}

func assertServiceTelemetryTransport(t *testing.T, env map[string]string, enabled bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagedServiceTelemetryTransport$")
	// A fresh process resets telemetry.Default's cache and models the daemon
	// receiving this environment, without launching a daemon.
	cmd.Env = []string{
		"NM_SERVICE_TELEMETRY_HELPER=1",
		"NM_SERVICE_TELEMETRY_ENABLED=" + strconv.FormatBool(enabled),
		"NO_MISTAKES_UMAMI_HOST=https://telemetry.invalid",
		"NO_MISTAKES_UMAMI_WEBSITE_ID=synthetic-test",
		"NM_HOME=" + t.TempDir(),
		"HOME=" + t.TempDir(),
	}
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Dir = t.TempDir()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fake daemon telemetry: %v\n%s", err, output)
	}
}

type serviceTelemetryTransport struct{ events atomic.Int32 }

func (r *serviceTelemetryTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.events.Add(1)
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
}

func TestManagedServiceTelemetryTransport(t *testing.T) {
	if os.Getenv("NM_SERVICE_TELEMETRY_HELPER") != "1" {
		t.Skip("invoked in a fresh process with a supplied daemon environment")
	}
	transport := &serviceTelemetryTransport{}
	http.DefaultTransport = transport
	if os.Getenv("NM_SERVICE_TELEMETRY_PREPARE") == "1" {
		oldApply := applyShellEnvToProcess
		defer func() { applyShellEnvToProcess = oldApply }()
		applied := false
		applyShellEnvToProcess = func(excluded ...string) error {
			applied = true
			for _, key := range excluded {
				if key == serviceTelemetryKey {
					return nil
				}
			}
			return os.Setenv(serviceTelemetryKey, os.Getenv("NM_SERVICE_TELEMETRY_SHELL"))
		}
		if err := prepareDaemonEnvironment(); err != nil {
			t.Fatal(err)
		}
		if !applied {
			t.Fatal("startup did not apply the shell environment")
		}
	}
	for _, event := range []string{"run started", "step_completed", "approval", "fix", "run finished"} {
		telemetry.Track(event, nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := telemetry.Close(ctx); err != nil {
		t.Fatal(err)
	}
	want := int32(0)
	if os.Getenv("NM_SERVICE_TELEMETRY_ENABLED") == "true" {
		want = 5
	}
	if got := transport.events.Load(); got != want {
		t.Fatalf("transport recorded %d events, want %d", got, want)
	}
}
