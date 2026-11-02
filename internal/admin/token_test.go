package admin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "admin.token")
	want := "setup-token-123"
	if err := SaveToken(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadToken("", path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("loaded token = %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestValidateSetupTokenLength(t *testing.T) {
	if err := ValidateSetupToken("short"); err == nil {
		t.Fatal("short token was accepted")
	}
	if err := ValidateSetupToken("123456"); err != nil {
		t.Fatalf("minimum-length token rejected: %v", err)
	}
	if err := ValidateSetupToken("12345678901234567890"); err != nil {
		t.Fatalf("maximum-length token rejected: %v", err)
	}
	if err := ValidateSetupToken("123456789012345678901"); err == nil {
		t.Fatal("overlong token was accepted")
	}
}
