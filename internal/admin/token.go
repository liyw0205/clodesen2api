package admin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	setupTokenMinLength = 6
	setupTokenMaxLength = 20
)

// LoadToken prefers an explicitly configured token and otherwise reads the
// persisted token created by the first-run setup page.
func LoadToken(configured, path string) (string, error) {
	if token := strings.TrimSpace(configured); token != "" {
		return token, nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read admin token file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("restrict admin token file permissions: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func ValidateSetupToken(token string) error {
	length := len([]byte(strings.TrimSpace(token)))
	if length < setupTokenMinLength || length > setupTokenMaxLength {
		return fmt.Errorf("管理令牌长度需要在 %d 到 %d 个字符之间", setupTokenMinLength, setupTokenMaxLength)
	}
	return nil
}

// SaveToken atomically writes a token with owner-only permissions.
func SaveToken(path, token string) error {
	path = strings.TrimSpace(path)
	token = strings.TrimSpace(token)
	if path == "" {
		return errors.New("admin token file path is empty")
	}
	if err := ValidateSetupToken(token); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create admin token directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".admin-token-*.tmp")
	if err != nil {
		return fmt.Errorf("create admin token file: %w", err)
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("restrict admin token permissions: %w", err)
	}
	if _, err := file.WriteString(token + "\n"); err != nil {
		_ = file.Close()
		return fmt.Errorf("write admin token: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync admin token: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close admin token: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace admin token: %w", err)
	}
	return nil
}
