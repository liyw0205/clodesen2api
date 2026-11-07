package config

import "strings"

func NormalizeStrategy(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "fixed", "random", "round_robin":
		return value
	}
	return ""
}

// StoredStrategy maps retired choices in existing files and backups to the default.
func StoredStrategy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "weighted", "least_inflight", "latency":
		return "fixed"
	}
	return NormalizeStrategy(value)
}
