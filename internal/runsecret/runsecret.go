package runsecret

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

const (
	MaxFileBytes = 64 << 10
	MaxVariables = 64
)

func ValidName(name string) bool {
	if name == "" {
		return false
	}
	for i, char := range name {
		switch {
		case char == '_', char >= 'A' && char <= 'Z', char >= 'a' && char <= 'z':
		case char >= '0' && char <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// A file group or others can read has already leaked its credentials.
func ReadFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open run env file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat run env file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("run env file %s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("run env file %s has mode %04o; it must be readable by its owner only (chmod 600)", path, info.Mode().Perm())
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read run env file: %w", err)
	}
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("run env file %s exceeds %d bytes", path, MaxFileBytes)
	}
	return Parse(data)
}

// An error never quotes a value or an unparsed line: either may be the credential.
func Parse(data []byte) (map[string]string, error) {
	vars := make(map[string]string)
	for i, line := range bytes.Split(data, []byte("\n")) {
		text := strings.TrimSuffix(string(line), "\r")
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		name, value, ok := strings.Cut(text, "=")
		if !ok || !ValidName(name) {
			return nil, fmt.Errorf("run env file line %d is not NAME=VALUE with a valid variable name", i+1)
		}
		if err := Validate(name, value); err != nil {
			return nil, fmt.Errorf("run env file line %d: %w", i+1, err)
		}
		if _, dup := vars[name]; dup {
			return nil, fmt.Errorf("run env file line %d repeats %s", i+1, name)
		}
		vars[name] = value
	}
	if len(vars) == 0 {
		return nil, fmt.Errorf("run env file names no variables")
	}
	if len(vars) > MaxVariables {
		return nil, fmt.Errorf("run env file names more than %d variables", MaxVariables)
	}
	return vars, nil
}

func Validate(name, value string) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid variable name")
	}
	if value == "" {
		return fmt.Errorf("%s has an empty value", name)
	}
	if strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s contains a NUL byte", name)
	}
	return nil
}
