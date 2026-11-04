package updater

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	githubLatestReleaseURL = "https://api.github.com/repos/liyw0205/clodesen2api/releases/latest"
	maxReleaseMetadata     = 2 << 20
	maxPackageBytes        = 128 << 20
	maxBinaryBytes         = 100 << 20
)

type Status struct {
	CurrentVersion   string         `json:"currentVersion"`
	LatestVersion    string         `json:"latestVersion"`
	UpdateAvailable  bool           `json:"updateAvailable"`
	CanInstall       bool           `json:"canInstall"`
	Platform         string         `json:"platform"`
	ReleaseURL       string         `json:"releaseURL"`
	ReleaseNotes     string         `json:"releaseNotes,omitempty"`
	PublishedAt      time.Time      `json:"publishedAt,omitempty"`
	AssetName        string         `json:"assetName,omitempty"`
	DownloadURL      string         `json:"downloadURL,omitempty"`
	ChecksumURL      string         `json:"-"`
	SourceID         string         `json:"sourceID"`
	SourceName       string         `json:"sourceName"`
	AvailableSources []SourceOption `json:"availableSources"`
	officialURL      string
	sourcePrefix     string
}

type SourceSelection struct {
	ID           string `json:"source"`
	CustomPrefix string `json:"customPrefix"`
}

type SourceOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type resolvedSource struct {
	id     string
	name   string
	prefix string
}

var builtInSources = []resolvedSource{
	{id: "builtin:gh-proxy-com", name: "gh-proxy.com", prefix: "https://gh-proxy.com/"},
	{id: "builtin:ghfast-top", name: "ghfast.top", prefix: "https://ghfast.top/"},
	{id: "builtin:ghproxy-net", name: "ghproxy.net", prefix: "https://ghproxy.net/"},
	{id: "builtin:ghproxy-icu", name: "ghproxy.icu", prefix: "https://ghproxy.icu/"},
	{id: "builtin:ghproxy-imciel", name: "ghproxy.imciel.com", prefix: "https://ghproxy.imciel.com/"},
}

type release struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func AvailableSources() []SourceOption {
	options := []SourceOption{{ID: "direct", Name: "不使用加速源"}}
	for _, source := range builtInSources {
		options = append(options, SourceOption{ID: source.id, Name: source.name})
	}
	options = append(options, SourceOption{ID: "custom", Name: "自定义加速源"})
	return options
}

func ResolveSource(selection SourceSelection) (SourceOption, error) {
	source, err := resolveSource(selection)
	if err != nil {
		return SourceOption{}, err
	}
	return SourceOption{ID: source.id, Name: source.name}, nil
}

func resolveSource(selection SourceSelection) (resolvedSource, error) {
	id := strings.TrimSpace(selection.ID)
	if id == "" || id == "direct" {
		return resolvedSource{id: "direct", name: "不使用加速源"}, nil
	}
	for _, source := range builtInSources {
		if id == source.id {
			return source, nil
		}
	}
	if id != "custom" {
		return resolvedSource{}, errors.New("更新加速源无效")
	}
	prefix := strings.TrimSpace(selection.CustomPrefix)
	probe := strings.ReplaceAll(prefix, "{url}", "https://github.com/example/project")
	parsed, err := url.Parse(probe)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return resolvedSource{}, errors.New("自定义加速源必须是有效的 HTTPS 地址")
	}
	if strings.Count(prefix, "{url}") > 1 {
		return resolvedSource{}, errors.New("自定义加速源只能包含一个 {url}")
	}
	if prefix == "" {
		return resolvedSource{}, errors.New("请填写自定义加速源地址")
	}
	return resolvedSource{id: "custom", name: "自定义加速源", prefix: prefix}, nil
}

func (source resolvedSource) rewrite(target string) string {
	if source.id == "direct" {
		return target
	}
	if strings.Contains(source.prefix, "{url}") {
		return strings.Replace(source.prefix, "{url}", target, 1)
	}
	return source.prefix + target
}

func Check(ctx context.Context, currentVersion, goos, goarch, goarm string, selections ...SourceSelection) (Status, error) {
	status := Status{CurrentVersion: currentVersion, Platform: platformName(goos, goarch, goarm)}
	selection := SourceSelection{ID: "builtin:gh-proxy-com"}
	if len(selections) > 0 {
		selection = selections[0]
	}
	source, err := resolveSource(selection)
	if err != nil {
		return status, err
	}
	status.SourceID, status.SourceName, status.sourcePrefix = source.id, source.name, source.prefix
	status.AvailableSources = AvailableSources()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, githubLatestReleaseURL, nil)
	if err != nil {
		return status, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "clodesen2api-updater")
	response, err := updateHTTPClient.Do(request)
	if err != nil {
		return status, fmt.Errorf("check latest release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return status, fmt.Errorf("check latest release returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxReleaseMetadata+1))
	if err != nil {
		return status, fmt.Errorf("read latest release: %w", err)
	}
	if len(body) > maxReleaseMetadata {
		return status, errors.New("latest release metadata is too large")
	}
	var latest release
	if err := json.Unmarshal(body, &latest); err != nil {
		return status, fmt.Errorf("decode latest release: %w", err)
	}
	status.LatestVersion = latest.TagName
	status.ReleaseURL = latest.HTMLURL
	status.ReleaseNotes = latest.Body
	status.PublishedAt = latest.PublishedAt
	status.UpdateAvailable = compareVersions(latest.TagName, currentVersion) > 0
	status.CanInstall = canInstallUpdate(currentVersion, latest.TagName, goos)
	assetName := platformAssetName(goos, goarch, goarm)
	status.AssetName = assetName
	if assetName == "" {
		status.CanInstall = false
		return status, nil
	}
	checksumName := strings.TrimSuffix(assetName, ".zip") + ".sha256"
	for _, asset := range latest.Assets {
		if asset.Name == assetName {
			status.officialURL = asset.BrowserDownloadURL
			status.DownloadURL = source.rewrite(asset.BrowserDownloadURL)
		}
		if asset.Name == checksumName {
			status.ChecksumURL = asset.BrowserDownloadURL
		}
	}
	if status.DownloadURL == "" || status.ChecksumURL == "" {
		status.CanInstall = false
	}
	return status, nil
}

func Install(ctx context.Context, status Status, executable string) error {
	if !status.UpdateAvailable || !status.CanInstall {
		return errors.New("no installable update is available")
	}
	if err := validateGitHubReleaseURL(status.officialURL); err != nil {
		return err
	}
	source, err := resolveSource(SourceSelection{ID: status.SourceID, CustomPrefix: status.sourcePrefix})
	if err != nil || source.rewrite(status.officialURL) != status.DownloadURL {
		return errors.New("update asset URL does not match the selected source")
	}
	if err := validateGitHubReleaseURL(status.ChecksumURL); err != nil {
		return err
	}
	archive, err := downloadWithClient(ctx, status.DownloadURL, maxPackageBytes, clientForSource(sourceHost(source), updateHTTPClient.Timeout))
	if err != nil {
		return fmt.Errorf("download update package: %w", err)
	}
	checksum, err := download(ctx, status.ChecksumURL, 4096)
	if err != nil {
		return fmt.Errorf("download update checksum: %w", err)
	}
	fields := strings.Fields(string(checksum))
	if len(fields) < 1 || len(fields[0]) != sha256.Size*2 {
		return errors.New("update checksum file has an invalid format")
	}
	if len(fields) > 1 && strings.TrimPrefix(fields[1], "*") != filepath.Base(status.AssetName) {
		return errors.New("update checksum does not name the selected package")
	}
	expected, err := hex.DecodeString(fields[0])
	if err != nil {
		return errors.New("update checksum is not valid hexadecimal")
	}
	actual := sha256.Sum256(archive)
	if !bytes.Equal(actual[:], expected) {
		return errors.New("update package checksum does not match")
	}
	binary, err := packageBinary(archive, filepath.Ext(executable) == ".exe")
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return errors.New("automatic installation is not supported on Windows")
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err == nil {
		executable = resolved
	}
	directory := filepath.Dir(executable)
	temporary, err := os.CreateTemp(directory, ".clodesen2api-update-*")
	if err != nil {
		return fmt.Errorf("stage updated binary: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o755); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set updated binary permissions: %w", err)
	}
	if _, err := temporary.Write(binary); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write updated binary: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync updated binary: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close updated binary: %w", err)
	}
	if err := os.Rename(temporaryPath, executable); err != nil {
		return fmt.Errorf("replace application binary: %w", err)
	}
	return nil
}

var updateHTTPClient = &http.Client{
	Timeout: 4 * time.Minute,
	CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects while downloading update")
		}
		if request.URL.Scheme != "https" {
			return errors.New("update redirect must use HTTPS")
		}
		host := strings.ToLower(request.URL.Hostname())
		if host != "api.github.com" && host != "github.com" && host != "release-assets.githubusercontent.com" && host != "objects.githubusercontent.com" {
			return errors.New("update redirected outside trusted GitHub hosts")
		}
		return nil
	},
}

func download(ctx context.Context, target string, maxBytes int64) ([]byte, error) {
	return downloadWithClient(ctx, target, maxBytes, updateHTTPClient)
}

func downloadWithClient(ctx context.Context, target string, maxBytes int64, client *http.Client) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "clodesen2api-updater")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, errors.New("download exceeds the allowed size")
	}
	return body, nil
}

func sourceHost(source resolvedSource) string {
	if source.id == "direct" {
		return ""
	}
	probe := strings.ReplaceAll(source.prefix, "{url}", "https://github.com/example/project")
	parsed, _ := url.Parse(probe)
	if parsed == nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func clientForSource(host string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects while downloading update")
			}
			if request.URL.Scheme != "https" {
				return errors.New("update redirect must use HTTPS")
			}
			redirectHost := strings.ToLower(request.URL.Hostname())
			if redirectHost == host || trustedGitHubHost(redirectHost) {
				return nil
			}
			return errors.New("update redirected outside the selected source")
		},
	}
}

func trustedGitHubHost(host string) bool {
	switch host {
	case "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	default:
		return false
	}
}

func TestSource(ctx context.Context, selection SourceSelection) (time.Duration, error) {
	source, err := resolveSource(selection)
	if err != nil {
		return 0, err
	}
	const checksumURL = "https://github.com/liyw0205/clodesen2api/releases/download/v0.2.18/clodesen2api-linux-amd64.sha256"
	start := time.Now()
	body, err := downloadWithClient(ctx, source.rewrite(checksumURL), 4096, clientForSource(sourceHost(source), 15*time.Second))
	if err != nil {
		return time.Since(start), err
	}
	fields := strings.Fields(string(body))
	if len(fields) == 0 || len(fields[0]) != sha256.Size*2 {
		return time.Since(start), errors.New("加速源未返回有效的 GitHub 校验文件")
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return time.Since(start), errors.New("加速源未返回有效的 GitHub 校验文件")
	}
	if len(fields) > 1 && strings.TrimPrefix(fields[1], "*") != "clodesen2api-linux-amd64.zip" {
		return time.Since(start), errors.New("加速源返回了其他文件的校验信息")
	}
	return time.Since(start), nil
}

func packageBinary(data []byte, windows bool) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("read update ZIP: %w", err)
	}
	wanted := "bin/clodesen2api"
	if windows {
		wanted += ".exe"
	}
	for _, file := range reader.File {
		if file.Name != wanted {
			continue
		}
		if !file.Mode().IsRegular() {
			return nil, errors.New("updated binary is not a regular file")
		}
		if file.UncompressedSize64 > maxBinaryBytes {
			return nil, errors.New("updated binary exceeds the allowed size")
		}
		input, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("open updated binary: %w", err)
		}
		binary, readErr := io.ReadAll(io.LimitReader(input, maxBinaryBytes+1))
		closeErr := input.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read updated binary: %w", readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close updated binary: %w", closeErr)
		}
		if int64(len(binary)) > maxBinaryBytes {
			return nil, errors.New("updated binary exceeds the allowed size")
		}
		return binary, nil
	}
	return nil, fmt.Errorf("update package does not contain %s", wanted)
}

func validateGitHubReleaseURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") || !strings.HasPrefix(parsed.Path, "/liyw0205/clodesen2api/releases/download/") {
		return errors.New("update asset URL is not a trusted project release")
	}
	return nil
}

func platformAssetName(goos, goarch, goarm string) string {
	switch {
	case goos == "android" && goarch == "arm64":
		return "clodesen2api-android-arm64-termux.zip"
	case goos == "linux" && goarch == "amd64":
		return "clodesen2api-linux-amd64.zip"
	case goos == "linux" && goarch == "arm64":
		return "clodesen2api-linux-arm64.zip"
	case goos == "linux" && goarch == "arm" && (goarm == "" || goarm == "7"):
		return "clodesen2api-linux-armv7.zip"
	case goos == "windows" && goarch == "amd64":
		return "clodesen2api-windows-amd64.zip"
	default:
		return ""
	}
}

func platformName(goos, goarch, goarm string) string {
	name := goos + "-" + goarch
	if goos == "android" && goarch == "arm64" {
		return "android-arm64-termux"
	}
	if goos == "linux" && goarch == "arm" {
		return name + "v" + goarm
	}
	return name
}

func autoInstallSupported(goos string) bool {
	return goos == "linux" || goos == "android"
}

func canInstallUpdate(currentVersion, latestVersion, goos string) bool {
	return compareVersions(latestVersion, currentVersion) > 0 && autoInstallSupported(goos)
}

func compareVersions(left, right string) int {
	leftParts, leftOK := versionParts(left)
	rightParts, rightOK := versionParts(right)
	if !leftOK {
		if rightOK {
			return -1
		}
		return 0
	}
	if !rightOK {
		return 1
	}
	length := len(leftParts)
	if len(rightParts) > length {
		length = len(rightParts)
	}
	for index := 0; index < length; index++ {
		var a, b int
		if index < len(leftParts) {
			a = leftParts[index]
		}
		if index < len(rightParts) {
			b = rightParts[index]
		}
		if a > b {
			return 1
		}
		if a < b {
			return -1
		}
	}
	return 0
}

func versionParts(value string) ([]int, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if value == "" || strings.EqualFold(value, "dev") {
		return nil, false
	}
	parts := strings.Split(value, ".")
	result := make([]int, len(parts))
	for index, part := range parts {
		cut := strings.IndexFunc(part, func(r rune) bool { return r < '0' || r > '9' })
		if cut >= 0 {
			part = part[:cut]
		}
		if part == "" {
			return nil, false
		}
		number, err := strconv.Atoi(part)
		if err != nil {
			return nil, false
		}
		result[index] = number
	}
	return result, true
}
