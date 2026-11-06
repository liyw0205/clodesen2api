package processguard

import (
	"path/filepath"
	"testing"
)

func TestRestoreExcludesOtherInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Exclusive(); err == nil {
		b.Close()
		t.Fatal("restore allowed while another instance is live")
	}
	if err := b.Exclusive(); err == nil {
		b.Close()
		t.Fatal("failed upgrade lost the first instance shared lock")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Exclusive(); err != nil {
		t.Fatal(err)
	}
	if c, err := Open(path); err == nil {
		c.Close()
		t.Fatal("instance opened during restore")
	}
	if err := a.Shared(); err != nil {
		t.Fatal(err)
	}
}
