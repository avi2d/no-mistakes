package daemon

import (
	"os"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
)

// Both spellings are distinct variables outside Windows, and tools disagree
// on which one they read.
var proxyEnvNames = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"}

func proxyEnvironment() []ipc.EnvVar {
	proxies := []ipc.EnvVar{}
	seen := map[string]bool{}
	for _, name := range proxyEnvNames {
		value, ok := os.LookupEnv(name)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		key := name
		if runtimeGOOS == "windows" {
			key = strings.ToUpper(name)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		proxies = append(proxies, ipc.EnvVar{Name: name, Value: value})
	}
	return proxies
}
