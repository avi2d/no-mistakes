package cli

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/paths"
)

const proxyDialTimeout = 2 * time.Second

// doctorDaemonProxies checks the proxies of the running daemon itself: the
// installing shell, its service definition and the login-shell probe can each
// disagree with it, and its environment is what every agent inherits.
func doctorDaemonProxies(p *paths.Paths, ok, warn, fail func(string, string)) bool {
	const label = "daemon proxy  "
	client, err := ipc.Dial(p.Socket())
	if err != nil {
		warn(label, fmt.Sprintf("not checked: %v", err))
		return true
	}
	defer client.Close()
	var env ipc.DaemonEnvironmentResult
	if err := client.CallWithTimeout(ipc.MethodDaemonEnvironment, &ipc.DaemonEnvironmentParams{}, &env, ipc.DefaultDialTimeout); err != nil {
		warn(label, fmt.Sprintf("not checked: the daemon did not report its environment (%v); a daemon older than this build cannot", err))
		return true
	}
	if len(env.Proxies) == 0 {
		ok(label, "none set")
		return true
	}
	allOK := true
	for _, proxy := range env.Proxies {
		addr, valid := proxyDialAddress(proxy.Value)
		if !valid {
			fail(label, fmt.Sprintf("%s does not name a proxy host", proxy.Name))
			allOK = false
			continue
		}
		conn, err := net.DialTimeout("tcp", addr, proxyDialTimeout)
		if err != nil {
			fail(label, fmt.Sprintf("%s names %s, where nothing listens (%v); every agent the daemon starts inherits it", proxy.Name, addr, err))
			allOK = false
			continue
		}
		_ = conn.Close()
		ok(label, fmt.Sprintf("%s %s is listening", proxy.Name, addr))
	}
	return allOK
}

// proxyDialAddress never returns the value itself, which can carry
// credentials.
func proxyDialAddress(value string) (string, bool) {
	raw := strings.TrimSpace(value)
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", false
	}
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = "443"
		case "socks", "socks4", "socks4a", "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	return net.JoinHostPort(u.Hostname(), port), true
}
