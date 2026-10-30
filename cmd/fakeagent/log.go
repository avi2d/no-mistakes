package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

// logMu guards appends to $FAKEAGENT_LOG. The fake agent stays
// single-process per invocation, but opencode runs as a long-lived server
// that handles concurrent message POSTs from a single test, so the lock
// keeps log lines from interleaving.
var logMu sync.Mutex

type invocation struct {
	Time   string   `json:"time"`
	Agent  string   `json:"agent"`
	Args   []string `json:"args"`
	Prompt string   `json:"prompt"`
	CWD    string   `json:"cwd,omitempty"`
	// A digest, not the value: the log must never hold the secret a test hunts for.
	EnvDigests map[string]string `json:"env_digests,omitempty"`
}

func recordedEnvDigests() map[string]string {
	var digests map[string]string
	for _, name := range strings.Split(os.Getenv("FAKEAGENT_RECORD_ENV"), ",") {
		value := os.Getenv(strings.TrimSpace(name))
		if value == "" {
			continue
		}
		if digests == nil {
			digests = make(map[string]string)
		}
		sum := sha256.Sum256([]byte(value))
		digests[strings.TrimSpace(name)] = hex.EncodeToString(sum[:])
	}
	return digests
}

func logInvocation(agent, prompt string, args []string) {
	path := os.Getenv("FAKEAGENT_LOG")
	if path == "" {
		return
	}
	cwd, _ := os.Getwd()
	rec := invocation{
		Time:       time.Now().UTC().Format(time.RFC3339Nano),
		Agent:      agent,
		Args:       args,
		Prompt:     prompt,
		CWD:        cwd,
		EnvDigests: recordedEnvDigests(),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	logMu.Lock()
	defer logMu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(data, '\n'))
}
