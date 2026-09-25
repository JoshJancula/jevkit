package agents

import "strings"

func compactEnvEnabled(getenv func(string) string, key string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
