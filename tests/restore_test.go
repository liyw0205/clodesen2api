package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	l.Close()
	return address
}

func TestProductionBackupRestoreAndInstanceExclusion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this test supervises exec within one PID; Windows starts a replacement process")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "clodesen2api")
	build := exec.Command("go", "build", "-o", binary, "./cmd/clodesen2api")
	build.Dir = ".."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	address := freeAddress(t)
	configuration := fmt.Sprintf("[server]\naddr=%q\n[admin]\ntoken=\"admin-test-123\"\ntoken_file=\"data/admin.token\"\n[checkin]\nenable=false\n", address)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	start := func(args ...string) *exec.Cmd {
		cmd := exec.Command(binary, args...)
		cmd.Dir = dir
		log, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("process-%d.log", time.Now().UnixNano())), os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdout, cmd.Stderr = log, log
		if err := cmd.Start(); err != nil {
			log.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); log.Close() })
		return cmd
	}
	start()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 3 * time.Second}
	base := "http://" + address
	wait := func(condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("service state did not converge")
	}
	request := func(method, path string, body []byte) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}
	wait(func() bool {
		resp, err := client.Get(base + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	})
	if status, raw := request("POST", "/admin/api/session", []byte(`{"token":"admin-test-123"}`)); status != 200 {
		t.Fatalf("login: %d %s", status, raw)
	}
	if status, raw := request("POST", "/admin/api/keys", []byte(`{"name":"before-backup"}`)); status != 201 {
		t.Fatalf("key: %d %s", status, raw)
	}
	status, raw := request("POST", "/admin/api/backups", nil)
	if status != 201 {
		t.Fatalf("backup: %d %s", status, raw)
	}
	var record struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	_, archive := request("GET", "/admin/api/backups/"+record.ID, nil)
	if status, raw := request("POST", "/admin/api/keys", []byte(`{"name":"after-backup"}`)); status != 201 {
		t.Fatalf("key: %d %s", status, raw)
	}
	status, raw = request("POST", "/admin/api/backups/validate", archive)
	if status != 200 {
		t.Fatalf("validate: %d %s", status, raw)
	}
	var plan struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	secondAddress := freeAddress(t)
	second := start("-addr", secondAddress)
	wait(func() bool {
		resp, err := client.Get("http://" + secondAddress + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	})
	payload, _ := json.Marshal(map[string]string{"planId": plan.ID})
	if status, raw := request("POST", "/admin/api/backups/restore", payload); status != 409 {
		t.Fatalf("live-instance restore allowed: %d %s", status, raw)
	}
	_ = second.Process.Kill()
	_ = second.Wait()
	if status, raw := request("POST", "/admin/api/backups/restore", payload); status != 202 {
		t.Fatalf("restore: %d %s", status, raw)
	}
	wait(func() bool {
		resp, err := client.Get(base + "/admin/api/backups/restore-status")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var state struct {
			State string `json:"state"`
		}
		return json.NewDecoder(resp.Body).Decode(&state) == nil && state.State == "restored"
	})
	status, raw = request("GET", "/admin/api/keys", nil)
	if status != 200 || bytes.Contains(raw, []byte("after-backup")) || !bytes.Contains(raw, []byte("before-backup")) {
		t.Fatalf("restored keys: %d %s", status, raw)
	}
}
