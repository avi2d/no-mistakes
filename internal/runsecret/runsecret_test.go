package runsecret

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const fakeToken = "sk-ant-oat01-FAKE-run-secret-value"

func TestParseKeepsValuesVerbatimAndSkipsComments(t *testing.T) {
	vars, err := Parse([]byte("# handed by orc\r\n\nCLAUDE_CODE_OAUTH_TOKEN=" + fakeToken + "\r\nOTHER= spaced=value \n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(vars) != 2 || vars["CLAUDE_CODE_OAUTH_TOKEN"] != fakeToken || vars["OTHER"] != " spaced=value " {
		t.Fatalf("vars = %q", vars)
	}
}

func TestParseErrorsNeverQuoteTheCredential(t *testing.T) {
	for name, input := range map[string]string{
		"bare token line":  fakeToken + "\n",
		"token as name":    fakeToken + "=x\n",
		"empty value":      "CLAUDE_CODE_OAUTH_TOKEN=\n",
		"repeated name":    "A=" + fakeToken + "\nA=" + fakeToken + "\n",
		"leading digit":    "1A=" + fakeToken + "\n",
		"NUL in the value": "A=" + fakeToken + "\x00\n",
		"no variables":     "# only a comment\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(input))
			if err == nil {
				t.Fatal("parse accepted invalid input")
			}
			if strings.Contains(err.Error(), "FAKE") {
				t.Fatalf("error quotes the credential: %v", err)
			}
		})
	}
}

func TestReadFileRefusesAFileOthersCanRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits")
	}
	path := filepath.Join(t.TempDir(), "run.env")
	if err := os.WriteFile(path, []byte("CLAUDE_CODE_OAUTH_TOKEN="+fakeToken+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("mode 644 error = %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	vars, err := ReadFile(path)
	if err != nil || vars["CLAUDE_CODE_OAUTH_TOKEN"] != fakeToken {
		t.Fatalf("mode 600 vars=%q err=%v", vars, err)
	}
}

func TestValidNameRejectsShellUnsafeNames(t *testing.T) {
	for _, name := range []string{"", "1A", "A-B", "A B", "A=B", "Ä"} {
		if ValidName(name) {
			t.Fatalf("ValidName(%q) = true", name)
		}
	}
	for _, name := range []string{"A", "_A", "CLAUDE_CODE_OAUTH_TOKEN", "a1"} {
		if !ValidName(name) {
			t.Fatalf("ValidName(%q) = false", name)
		}
	}
}
