package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/paths"
)

func installSystemdUserService(p *paths.Paths, exe string) error {
	path := systemdUserServicePath(p)
	home, err := serviceUserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve user home: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create systemd user directory: %w", err)
	}
	if err := writeServiceFile(path, renderSystemdUnit(exe, p, home)); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}
	if _, err := serviceCommandRunner("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if _, err := serviceCommandRunner("systemctl", "--user", "enable", systemdServiceName(p)); err != nil {
		return fmt.Errorf("systemctl enable: %w", err)
	}
	cleanupLegacySystemdUnit(p)
	return nil
}

func cleanupLegacySystemdUnit(p *paths.Paths) {
	path := legacySystemdUserServicePath()
	data, err := os.ReadFile(path)
	if err != nil || !serviceDefinitionMatchesRoot(data, p) {
		return
	}
	_, _ = serviceCommandRunner("systemctl", "--user", "stop", legacySystemdServiceName)
	_, _ = serviceCommandRunner("systemctl", "--user", "disable", legacySystemdServiceName)
	_ = os.Remove(path)
}

func startSystemdUserService(p *paths.Paths) error {
	_, err := serviceCommandRunner("systemctl", "--user", "start", systemdServiceName(p))
	if err != nil {
		return fmt.Errorf("systemctl start: %w", err)
	}
	return nil
}

func restartSystemdUserService(p *paths.Paths) error {
	_, err := serviceCommandRunner("systemctl", "--user", "restart", systemdServiceName(p))
	if err != nil {
		return fmt.Errorf("systemctl restart: %w", err)
	}
	return nil
}

func stopSystemdUserService(p *paths.Paths) error {
	_, err := serviceCommandRunner("systemctl", "--user", "stop", systemdServiceName(p))
	if err != nil {
		return fmt.Errorf("systemctl stop: %w", err)
	}
	return nil
}

func systemdUserServicePath(p *paths.Paths) string {
	home, err := serviceUserHomeDir()
	if err != nil {
		home = ""
	}
	return filepath.Join(home, ".config", "systemd", "user", systemdServiceName(p))
}

func legacySystemdUserServicePath() string {
	home, _ := serviceUserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", legacySystemdServiceName)
}

func renderSystemdUnit(exe string, p *paths.Paths, home string) string {
	command := strings.Join([]string{
		systemdEscapeArg(exe),
		systemdEscapeArg("daemon"),
		systemdEscapeArg("run"),
		systemdEscapeArg("--root"),
		systemdEscapeArg(p.Root()),
	}, " ")
	envLines := []string{
		systemdEnvironmentLine("HOME", home),
		systemdEnvironmentLine("PATH", managedServicePath(home)),
	}
	return fmt.Sprintf(`[Unit]
Description=no-mistakes background daemon

[Service]
Type=simple
# continue: a step the kernel OOM-kills must not SIGTERM the rest of the unit.
# stop (the default) terminates every process in the service, which is how one
# step's allocation used to fail every other in-flight run.
OOMPolicy=continue
ExecStart=%s
WorkingDirectory=%s
%s
StandardOutput=%s
StandardError=%s
Restart=always
RestartSec=2

[Install]
WantedBy=default.target
`, command, systemdEscapeArg(p.Root()), strings.Join(envLines, "\n"), systemdEscapeArg("append:"+p.DaemonBootstrapLog()), systemdEscapeArg("append:"+p.DaemonBootstrapLog()))
}

// systemdEnvironmentLine renders one `Environment=` directive. systemd runs
// specifier expansion on directive values, so a literal `%` (e.g. in a HOME
// such as /home/a%b) must be doubled to `%%` to survive it -
// otherwise a known specifier letter corrupts the value and an unknown one
// rejects the assignment on systemd >= v249. strconv.Quote never emits `%`, so
// doubling first leaves the quoting unaffected.
func systemdEnvironmentLine(key, value string) string {
	return "Environment=" + strconv.Quote(strings.ReplaceAll(key+"="+value, "%", "%%"))
}

func systemdEscapeArg(arg string) string {
	if arg == "" {
		return `""`
	}
	if strings.ContainsAny(arg, " \t\n\r\"'\\") {
		return strconv.Quote(arg)
	}
	return arg
}
