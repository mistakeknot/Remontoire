package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var sensitiveKey = regexp.MustCompile(`(?i)(api[-_]?key|access[-_]?key|authorization|credential|password|passwd|private[-_]?key|secret|token)`)

var sensitiveValue = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(gh[pousr]_[A-Za-z0-9_]{20,})`),
	regexp.MustCompile(`(?i)(sk-[A-Za-z0-9_-]{20,})`),
	regexp.MustCompile(`(?i)(xox[bpras]-[A-Za-z0-9-]{20,})`),
}

func SanitizeObservation(raw []byte, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	if len(raw) > maxBytes {
		return nil, fmt.Errorf("observation is %d bytes, limit is %d", len(raw), maxBytes)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("observation must be valid JSON: %w", err)
	}
	value = redactValue(value)
	sanitized, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal sanitized observation: %w", err)
	}
	return sanitized, nil
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			if sensitiveKey.MatchString(key) {
				result[key] = "[REDACTED]"
				continue
			}
			result[key] = redactValue(child)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, child := range typed {
			result[i] = redactValue(child)
		}
		return result
	case string:
		for _, pattern := range sensitiveValue {
			if pattern.MatchString(typed) {
				return "[REDACTED]"
			}
		}
		return typed
	default:
		return value
	}
}

func safeEnvironment() ([]string, func(), error) {
	allowed := map[string]bool{
		"CODEX_HOME": true, "HOME": true, "LANG": true, "LC_ALL": true,
		"LOGNAME": true, "PATH": true, "SHELL": true, "TERM": true,
		"TMPDIR": true, "USER": true, "XDG_CACHE_HOME": true,
		"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true,
	}
	result := make([]string, 0, len(allowed))
	for _, entry := range environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowed[key] && key != "XDG_CACHE_HOME" {
			result = append(result, entry)
		}
	}
	cacheHome, err := os.MkdirTemp(os.TempDir(), "remontoire-cache-")
	if err != nil {
		return nil, nil, fmt.Errorf("create sandbox cache: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(cacheHome) }
	result = append(result, "XDG_CACHE_HOME="+cacheHome)
	return result, cleanup, nil
}

func SafeEnvironment() ([]string, func(), error) {
	return safeEnvironment()
}
