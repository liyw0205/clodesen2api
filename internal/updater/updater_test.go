package updater

import (
	"archive/zip"
	"bytes"
	"io"
	"testing"
)

func TestPlatformAssetName(t *testing.T) {
	tests := []struct {
		goos, goarch, goarm, want string
	}{
		{"linux", "amd64", "", "clodesen2api-linux-amd64.zip"},
		{"linux", "arm64", "", "clodesen2api-linux-arm64.zip"},
		{"linux", "arm", "7", "clodesen2api-linux-armv7.zip"},
		{"linux", "arm", "6", ""},
		{"android", "arm64", "", "clodesen2api-android-arm64-termux.zip"},
		{"windows", "amd64", "", "clodesen2api-windows-amd64.zip"},
		{"darwin", "arm64", "", ""},
	}
	for _, test := range tests {
		if got := platformAssetName(test.goos, test.goarch, test.goarm); got != test.want {
			t.Errorf("platformAssetName(%q, %q, %q) = %q, want %q", test.goos, test.goarch, test.goarm, got, test.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		left, right string
		want        int
	}{
		{"v0.2.10", "v0.2.9", 1},
		{"v0.2.9", "v0.2.10", -1},
		{"v0.2.9", "0.2.9", 0},
		{"v0.2.0", "dev", 1},
		{"dev", "v0.2.0", -1},
	}
	for _, test := range tests {
		if got := compareVersions(test.left, test.right); got != test.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestCanInstallUpdateIncludesUnversionedLegacyBuilds(t *testing.T) {
	tests := []struct {
		current, latest, goos string
		want                  bool
	}{
		{"dev", "v0.2.8", "linux", true},
		{"v0.1.7", "v0.2.8", "android", true},
		{"v0.2.8", "v0.2.8", "linux", false},
		{"v0.2.9", "v0.2.8", "linux", false},
		{"dev", "v0.2.8", "windows", false},
	}
	for _, test := range tests {
		if got := canInstallUpdate(test.current, test.latest, test.goos); got != test.want {
			t.Errorf("canInstallUpdate(%q, %q, %q) = %v, want %v", test.current, test.latest, test.goos, got, test.want)
		}
	}
}

func TestPackageBinaryUsesExpectedPath(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, err := writer.Create("bin/clodesen2api")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, "updated executable"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	binary, err := packageBinary(archive.Bytes(), false)
	if err != nil {
		t.Fatalf("packageBinary() error = %v", err)
	}
	if got, want := string(binary), "updated executable"; got != want {
		t.Fatalf("binary = %q, want %q", got, want)
	}
	if _, err := packageBinary(archive.Bytes(), true); err == nil {
		t.Fatal("packageBinary() accepted a package without the Windows executable")
	}
}

func TestValidateGitHubReleaseURL(t *testing.T) {
	for _, url := range []string{
		"https://github.com/liyw0205/clodesen2api/releases/download/v1/package.zip",
		"https://github.com/liyw0205/clodesen2api/releases/download/v1/package.sha256",
	} {
		if err := validateGitHubReleaseURL(url); err != nil {
			t.Errorf("validateGitHubReleaseURL(%q) error = %v", url, err)
		}
	}
	for _, url := range []string{
		"http://github.com/liyw0205/clodesen2api/releases/download/v1/package.zip",
		"https://example.com/liyw0205/clodesen2api/releases/download/v1/package.zip",
		"https://github.com/another/repo/releases/download/v1/package.zip",
	} {
		if err := validateGitHubReleaseURL(url); err == nil {
			t.Errorf("validateGitHubReleaseURL(%q) unexpectedly succeeded", url)
		}
	}
}

func TestAvailableSourcesAndResolveCustomSource(t *testing.T) {
	options := AvailableSources()
	if len(options) != 7 {
		t.Fatalf("AvailableSources() returned %d options, want direct + 5 built-ins + custom", len(options))
	}
	source, err := ResolveSource(SourceSelection{ID: "custom", CustomPrefix: "https://mirror.example/{url}"})
	if err != nil {
		t.Fatalf("ResolveSource(custom) error = %v", err)
	}
	if source.Name != "自定义加速源" {
		t.Fatalf("custom source name = %q", source.Name)
	}
	if _, err := ResolveSource(SourceSelection{ID: "custom", CustomPrefix: "http://mirror.example/"}); err == nil {
		t.Fatal("ResolveSource accepted an insecure custom source")
	}
	if _, err := ResolveSource(SourceSelection{ID: "missing"}); err == nil {
		t.Fatal("ResolveSource accepted an unknown built-in source")
	}
}

func TestSourceRewriteSupportsPrefixAndTemplate(t *testing.T) {
	for _, test := range []struct {
		selection SourceSelection
		want      string
	}{
		{SourceSelection{ID: "builtin:ghfast-top"}, "https://ghfast.top/https://github.com/example/release.zip"},
		{SourceSelection{ID: "custom", CustomPrefix: "https://mirror.example/{url}"}, "https://mirror.example/https://github.com/example/release.zip"},
		{SourceSelection{ID: "direct"}, "https://github.com/example/release.zip"},
	} {
		source, err := resolveSource(test.selection)
		if err != nil {
			t.Fatalf("resolveSource(%+v) error = %v", test.selection, err)
		}
		if got := source.rewrite("https://github.com/example/release.zip"); got != test.want {
			t.Errorf("rewrite(%+v) = %q, want %q", test.selection, got, test.want)
		}
	}
}
