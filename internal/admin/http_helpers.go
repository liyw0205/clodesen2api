package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

func queryPositiveInt(r *http.Request, key string, fallback, min, max int) int {
	value := fallback
	if raw := strings.TrimSpace(r.URL.Query().Get(key)); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			value = parsed
		}
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func forceRefresh(r *http.Request) bool {
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("refresh")))
	return value == "1" || value == "true" || value == "yes"
}

func writeRawJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func writeCachedJSON(w http.ResponseWriter, body []byte, cacheState string) {
	w.Header().Set("X-Admin-Cache", cacheState)
	writeRawJSON(w, body)
}

func decodeObject(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, error) {
	var input map[string]json.RawMessage
	if err := decodeJSON(w, r, &input); err != nil {
		return nil, err
	}
	if input == nil {
		return nil, errors.New("JSON object required")
	}
	return input, nil
}

func readString(input map[string]json.RawMessage, key string) (string, error) {
	var value string
	if err := json.Unmarshal(input[key], &value); err != nil || strings.TrimSpace(value) == "" {
		return "", errors.New("required string field is missing")
	}
	return strings.TrimSpace(value), nil
}
