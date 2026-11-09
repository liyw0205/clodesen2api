// Package config 负责加载并合并命令行 / 配置文件 / 环境变量。
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata"

	"clodesen2api/internal/updater"
	"github.com/lfhy/flag"
)

// Config is immutable once passed to a running upstream session.
// CLI and explicit CLODESEN2API_SECTION_KEY environment values override files.
type Config struct {
	path           string
	sources        map[string]string
	ScheduleRearms map[string]time.Time `json:"-"`
	Cache          CacheSettings
	Server         struct {
		Addr string
	}
	Upstream struct {
		BaseURL        string
		OpenAIBaseURL  string
		LoginPath      string
		LoginTimeout   time.Duration
		RequestTimeout time.Duration
		UserAgent      string
	}
	Auth struct {
		Identifier              string
		Password                string
		Accounts                string
		Strategy                string
		RPM                     int
		AutoRelogin             bool
		CreditCooldownThreshold int64
	}
	Admin struct {
		Token        string
		TokenFile    string
		AccountsFile string
	}
	API struct {
		Key            string
		KeysFile       string
		AllowNoAuth    bool
		TrustedProxies string
	}
	Checkin struct {
		Enable   bool
		Path     string
		At       string // 每天签到时间，HH:MM
		Timezone string
	}
	Redemption struct {
		Enable bool
		At     string
		Code   string
	}
	Update struct {
		Source       string
		CustomPrefix string
	}
}

// PublicSettings excludes credentials and tokens from the management API.
type PublicSettings struct {
	Cache    CacheSettings         `json:"cache"`
	Revision int64                 `json:"revision,omitempty"`
	Fields   map[string]FieldState `json:"fields,omitempty"`
	Server   struct {
		Addr string `json:"addr"`
	} `json:"server"`
	Upstream struct {
		BaseURL        string `json:"baseURL"`
		OpenAIBaseURL  string `json:"openAIBaseURL"`
		LoginPath      string `json:"loginPath"`
		LoginTimeout   string `json:"loginTimeout"`
		RequestTimeout string `json:"requestTimeout"`
		UserAgent      string `json:"userAgent"`
	} `json:"upstream"`
	Auth struct {
		Strategy                string `json:"strategy"`
		RPM                     int    `json:"rpm"`
		AutoRelogin             bool   `json:"autoRelogin"`
		CreditCooldownThreshold int64  `json:"creditCooldownThreshold"`
	} `json:"auth"`
	API struct {
		KeysFile       string `json:"keysFile"`
		AllowNoAuth    bool   `json:"allowNoAuth"`
		TrustedProxies string `json:"trustedProxies"`
	} `json:"api"`
	Checkin struct {
		Enable   bool   `json:"enable"`
		Path     string `json:"path"`
		At       string `json:"at"`
		Timezone string `json:"timezone"`
	} `json:"checkin"`
	Redemption struct {
		Enable         bool   `json:"enable"`
		At             string `json:"at"`
		Code           string `json:"code"`
		CodeConfigured bool   `json:"codeConfigured"`
	} `json:"redemption"`
	Update struct {
		Source       string `json:"source"`
		CustomPrefix string `json:"customPrefix"`
	} `json:"update"`
}

// Load 用 lfhy/flag 注册所有参数，支持 -c config.toml 统一载入。
func Load() *Config {
	c := &Config{}
	c.path = configPathFromArgs()
	if err := ensureConfigFile(c.path); err != nil {
		fmt.Fprintf(os.Stderr, "[config] 无法创建默认配置文件 %s: %v\n", c.path, err)
	}

	flag.StringConfigVar(&c.Server.Addr, "addr", "server", "addr", "0.0.0.0:6687", "本地监听地址")

	flag.StringConfigVar(&c.Upstream.BaseURL, "base-url", "upstream", "base_url", "https://pai.zaiduyu.top", "上游站点基础地址（登录、签到等）")
	flag.StringConfigVar(&c.Upstream.OpenAIBaseURL, "openai-base-url", "upstream", "openai_base_url", "https://pai.zaiduyu.top/api/openai", "上游 OpenAI API 基础地址")
	flag.StringConfigVar(&c.Upstream.LoginPath, "login-path", "upstream", "login_path", "/api/user-auth", "登录端点路径")
	flag.DurationConfigVar(&c.Upstream.LoginTimeout, "login-timeout", "upstream", "login_timeout", 30*time.Second, "登录请求超时")
	flag.DurationConfigVar(&c.Upstream.RequestTimeout, "request-timeout", "upstream", "request_timeout", 0, "上游请求超时(0=不限)")
	flag.StringConfigVar(&c.Upstream.UserAgent, "user-agent", "upstream", "user_agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		"浏览器 UA")

	flag.StringConfigVar(&c.Auth.Identifier, "identifier", "auth", "identifier", "", "登录账号(邮箱/用户名)")
	flag.StringConfigVar(&c.Auth.Password, "password", "auth", "password", "", "登录密码")
	flag.StringConfigVar(&c.Auth.Accounts, "accounts", "auth", "accounts", "", "逗号分隔的账号列表，元素格式为 identifier:password")
	flag.StringConfigVar(&c.Auth.Strategy, "strategy", "auth", "strategy", "fixed", "账号选择策略：fixed、round_robin 或 random")
	flag.IntConfigVar(&c.Auth.RPM, "account-rpm", "auth", "rpm", 4, "每个账号允许同时处理的请求数")
	flag.BoolConfigVar(&c.Auth.AutoRelogin, "auto-relogin", "auth", "auto_relogin", true, "401 时是否自动重新登录")
	flag.Int64ConfigVar(&c.Auth.CreditCooldownThreshold, "credit-cooldown-threshold", "auth", "credit_cooldown_threshold", 100, "积分低于等于此值时自动冷却账号")
	flag.StringConfigVar(&c.Admin.Token, "admin-token", "admin", "token", "", "管理后台访问令牌（首次设置为 6-20 个字符）")
	flag.StringConfigVar(&c.Admin.TokenFile, "admin-token-file", "admin", "token_file", "data/admin.token", "管理后台令牌持久化文件")
	flag.StringConfigVar(&c.Admin.AccountsFile, "admin-accounts-file", "admin", "accounts_file", "data/accounts.json", "管理后台账号与策略存储文件")
	flag.StringConfigVar(&c.API.Key, "api-key", "api", "key", "", "OpenAI 兼容接口外部 API key（只用于兼容/紧急管理）")
	flag.StringConfigVar(&c.API.KeysFile, "api-keys-file", "api", "keys_file", "data/api-keys.json", "客户端 API key 存储文件")
	flag.BoolConfigVar(&c.API.AllowNoAuth, "api-allow-no-auth", "api", "allow_no_auth", false, "无任何 key 时是否允许匿名调用（不建议）")
	flag.StringConfigVar(&c.API.TrustedProxies, "api-trusted-proxies", "api", "trusted_proxies", "", "可信反向代理 IP/CIDR，逗号分隔")

	flag.BoolConfigVar(&c.Checkin.Enable, "checkin", "checkin", "enable", true, "是否启用每日自动签到(GET)")
	flag.StringConfigVar(&c.Checkin.Path, "checkin-path", "checkin", "path", "/api/checkin", "签到端点路径")
	flag.StringConfigVar(&c.Checkin.At, "checkin-at", "checkin", "at", "04:00", "每天签到时间(HH:MM)")
	flag.StringConfigVar(&c.Checkin.Timezone, "checkin-tz", "checkin", "timezone", "Asia/Shanghai", "签到所用时区")
	flag.BoolConfigVar(&c.Redemption.Enable, "auto-redeem", "redemption", "enable", false, "是否启用每日自动兑换")
	flag.StringConfigVar(&c.Redemption.At, "auto-redeem-at", "redemption", "at", "04:30", "自动兑换时间(HH:MM，UTC+8)")
	flag.StringConfigVar(&c.Redemption.Code, "auto-redeem-code", "redemption", "code", "", "自动兑换码")
	flag.StringConfigVar(&c.Update.Source, "update-source", "update", "source", "builtin:gh-proxy-com", "版本更新下载源")
	flag.StringConfigVar(&c.Update.CustomPrefix, "update-custom-prefix", "update", "custom_prefix", "", "自定义 GitHub 加速前缀")
	flag.IntConfigVar(&c.Cache.AccountsSeconds, "cache-accounts-seconds", "cache", "accounts_seconds", 3600, "账号缓存有效时间（秒）")
	flag.IntConfigVar(&c.Cache.ModelsSeconds, "cache-models-seconds", "cache", "models_seconds", 3600, "模型缓存有效时间（秒）")
	flag.IntConfigVar(&c.Cache.CreditsSeconds, "cache-credits-seconds", "cache", "credits_seconds", 3600, "积分记录缓存有效时间（秒）")

	flag.StringVar(&c.path, "c", c.path, "配置文件路径")
	if err := flag.Parse(); err != nil {
		fmt.Fprintln(os.Stderr, "[config] 参数解析失败")
		os.Exit(2)
	}
	if err := c.captureSources(flag.GetDefaultFlagSet(), os.Args[1:], os.LookupEnv); err != nil {
		fmt.Fprintln(os.Stderr, "[config] 环境变量格式无效:", err)
		os.Exit(2)
	}
	return c
}

func (c *Config) ConfigFilePath() string {
	if c == nil || strings.TrimSpace(c.path) == "" {
		return "config.toml"
	}
	return c.path
}

func (c *Config) PublicSettings() PublicSettings {
	var out PublicSettings
	out.Cache = c.Cache.defaults()
	out.Server.Addr = c.Server.Addr
	out.Upstream.BaseURL, out.Upstream.OpenAIBaseURL, out.Upstream.LoginPath = c.Upstream.BaseURL, c.Upstream.OpenAIBaseURL, c.Upstream.LoginPath
	out.Upstream.LoginTimeout, out.Upstream.RequestTimeout, out.Upstream.UserAgent = c.Upstream.LoginTimeout.String(), c.Upstream.RequestTimeout.String(), c.Upstream.UserAgent
	out.Auth.Strategy, out.Auth.RPM, out.Auth.AutoRelogin = c.Auth.Strategy, c.Auth.RPM, c.Auth.AutoRelogin
	out.Auth.CreditCooldownThreshold = c.Auth.CreditCooldownThreshold
	if out.Auth.CreditCooldownThreshold < 1 {
		out.Auth.CreditCooldownThreshold = 100
	}
	if out.Auth.RPM <= 0 {
		out.Auth.RPM = 4
	}
	out.API.KeysFile = c.API.KeysFile
	if out.API.KeysFile == "" {
		out.API.KeysFile = "data/api-keys.json"
	}
	out.API.AllowNoAuth = c.API.AllowNoAuth
	out.API.TrustedProxies = c.API.TrustedProxies
	out.Checkin.Enable, out.Checkin.Path, out.Checkin.At, out.Checkin.Timezone = c.Checkin.Enable, c.Checkin.Path, c.Checkin.At, c.Checkin.Timezone
	out.Redemption.Enable, out.Redemption.At = c.Redemption.Enable, c.Redemption.At
	out.Redemption.Code = c.Redemption.Code
	out.Redemption.CodeConfigured = strings.TrimSpace(c.Redemption.Code) != ""
	out.Update.Source, out.Update.CustomPrefix = c.Update.Source, c.Update.CustomPrefix
	if out.Update.Source == "" {
		out.Update.Source = "builtin:gh-proxy-com"
	}
	return out
}

func (c *Config) ApplyPublicSettings(settings PublicSettings) error {
	cache, err := settings.Cache.validate(c.Cache)
	if err != nil {
		return err
	}
	if strings.TrimSpace(settings.Server.Addr) == "" {
		return errors.New("监听地址不能为空")
	}
	strategy := NormalizeStrategy(settings.Auth.Strategy)
	if strategy == "" {
		return errors.New("账号策略无效")
	}
	rpm := settings.Auth.RPM
	if rpm == 0 {
		rpm = c.Auth.RPM
		if rpm == 0 {
			rpm = 4
		}
	}
	if rpm < 1 || rpm > 1000 {
		return errors.New("每账号并发上限必须在 1 到 1000 之间")
	}
	if settings.Auth.CreditCooldownThreshold < 1 || settings.Auth.CreditCooldownThreshold > 1000000000000 {
		return errors.New("积分冷却阈值必须在 1 到 1000000000000 之间")
	}
	loginTimeout, err := time.ParseDuration(strings.TrimSpace(settings.Upstream.LoginTimeout))
	if err != nil || loginTimeout <= 0 {
		return errors.New("登录超时必须是正数时长，例如 30s")
	}
	requestTimeout, err := time.ParseDuration(strings.TrimSpace(settings.Upstream.RequestTimeout))
	if err != nil || requestTimeout < 0 {
		return errors.New("请求超时必须是非负时长，例如 0s")
	}
	if _, _, err := net.SplitHostPort(strings.TrimSpace(settings.Server.Addr)); err != nil {
		return errors.New("监听地址必须包含有效端口")
	}
	for _, address := range []string{settings.Upstream.BaseURL, settings.Upstream.OpenAIBaseURL} {
		u, err := url.Parse(strings.TrimSpace(address))
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("上游地址必须是有效的 HTTP/HTTPS 地址")
		}
	}
	if strings.TrimSpace(settings.Upstream.LoginPath) == "" || strings.TrimSpace(settings.Checkin.Path) == "" {
		return errors.New("接口路径不能为空")
	}
	for _, path := range []string{settings.Upstream.LoginPath, settings.Checkin.Path} {
		if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
			return errors.New("上游路径必须以单个 / 开头")
		}
	}
	apiSettings := settings.API
	// Older panel clients do not send the API block. Preserve startup-only
	// values in that case instead of turning a normal settings save into an
	// invalid empty-path update.
	if strings.TrimSpace(apiSettings.KeysFile) == "" {
		apiSettings.KeysFile = c.API.KeysFile
		apiSettings.AllowNoAuth = c.API.AllowNoAuth
		apiSettings.TrustedProxies = c.API.TrustedProxies
		if strings.TrimSpace(apiSettings.KeysFile) == "" {
			apiSettings.KeysFile = "data/api-keys.json"
		}
	}
	for _, network := range strings.Split(apiSettings.TrustedProxies, ",") {
		network = strings.TrimSpace(network)
		if network == "" {
			continue
		}
		if net.ParseIP(network) == nil {
			if _, _, err := net.ParseCIDR(network); err != nil {
				return errors.New("可信代理必须是有效的 IP/CIDR")
			}
		}
	}
	if strings.TrimSpace(settings.Checkin.At) == "" || strings.TrimSpace(settings.Checkin.Timezone) == "" {
		return errors.New("签到时间和时区不能为空")
	}
	if _, err := time.Parse("15:04", settings.Checkin.At); err != nil {
		return errors.New("签到时间必须是 HH:MM")
	}
	if _, err := time.LoadLocation(settings.Checkin.Timezone); err != nil {
		return errors.New("签到时区无效")
	}
	redemptionAt := strings.TrimSpace(settings.Redemption.At)
	if redemptionAt == "" {
		redemptionAt = strings.TrimSpace(c.Redemption.At)
		if redemptionAt == "" {
			redemptionAt = "04:30"
		}
	}
	if _, err := time.Parse("15:04", redemptionAt); err != nil {
		return errors.New("自动兑换时间必须是 HH:MM")
	}
	updateSource := strings.TrimSpace(settings.Update.Source)
	if updateSource == "" {
		updateSource = strings.TrimSpace(c.Update.Source)
		if updateSource == "" {
			updateSource = "builtin:gh-proxy-com"
		}
	}
	customPrefix := strings.TrimSpace(settings.Update.CustomPrefix)
	if customPrefix == "" && updateSource == "custom" {
		customPrefix = strings.TrimSpace(c.Update.CustomPrefix)
	}
	if _, err := updater.ResolveSource(updater.SourceSelection{ID: updateSource, CustomPrefix: customPrefix}); err != nil {
		return err
	}
	c.Server.Addr = strings.TrimSpace(settings.Server.Addr)
	c.Upstream.BaseURL = strings.TrimRight(strings.TrimSpace(settings.Upstream.BaseURL), "/")
	c.Upstream.OpenAIBaseURL = strings.TrimRight(strings.TrimSpace(settings.Upstream.OpenAIBaseURL), "/")
	c.Upstream.LoginPath, c.Upstream.LoginTimeout, c.Upstream.RequestTimeout, c.Upstream.UserAgent = strings.TrimSpace(settings.Upstream.LoginPath), loginTimeout, requestTimeout, strings.TrimSpace(settings.Upstream.UserAgent)
	c.Auth.Strategy, c.Auth.RPM, c.Auth.AutoRelogin = strategy, rpm, settings.Auth.AutoRelogin
	c.Auth.CreditCooldownThreshold = settings.Auth.CreditCooldownThreshold
	c.API.KeysFile, c.API.AllowNoAuth, c.API.TrustedProxies = strings.TrimSpace(apiSettings.KeysFile), apiSettings.AllowNoAuth, strings.TrimSpace(apiSettings.TrustedProxies)
	c.Checkin.Enable, c.Checkin.Path, c.Checkin.At, c.Checkin.Timezone = settings.Checkin.Enable, strings.TrimSpace(settings.Checkin.Path), strings.TrimSpace(settings.Checkin.At), strings.TrimSpace(settings.Checkin.Timezone)
	c.Redemption.Enable, c.Redemption.At = settings.Redemption.Enable, redemptionAt
	if strings.TrimSpace(settings.Redemption.Code) != "" {
		c.Redemption.Code = strings.TrimSpace(settings.Redemption.Code)
	}
	c.Update.Source = updateSource
	c.Update.CustomPrefix = customPrefix
	c.Cache = cache
	return nil
}

func configPathFromArgs() string {
	args := os.Args[1:]
	for i, arg := range args {
		if (arg == "-c" || arg == "--c") && i+1 < len(args) && strings.TrimSpace(args[i+1]) != "" {
			return args[i+1]
		}
		for _, prefix := range []string{"-c=", "--c="} {
			if strings.HasPrefix(arg, prefix) && strings.TrimSpace(strings.TrimPrefix(arg, prefix)) != "" {
				return strings.TrimPrefix(arg, prefix)
			}
		}
	}
	return "config.toml"
}

func ensureConfigFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return err
	}
	defer file.Close()
	_, err = file.WriteString(defaultTOML)
	return err
}

// Save writes the complete local configuration atomically. Sensitive values
// stay on disk for the service, but are never part of PublicSettings.
func (c *Config) Save() error {
	path := c.ConfigFilePath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	content := fmt.Sprintf(`# clodesen2api configuration

[server]
addr = %q

[upstream]
base_url = %q
openai_base_url = %q
login_path = %q
login_timeout = %q
request_timeout = %q
user_agent = %q

[auth]
identifier = %q
password = %q
accounts = %q
strategy = %q
rpm = %d
auto_relogin = %t
credit_cooldown_threshold = %d

[admin]
token = %q
token_file = %q
accounts_file = %q

[api]
key = %q
keys_file = %q
allow_no_auth = %t
trusted_proxies = %q

[checkin]
enable = %t
path = %q
at = %q
timezone = %q

[redemption]
enable = %t
at = %q
code = %q

[update]
source = %q
custom_prefix = %q

[cache]
accounts_seconds = %d
models_seconds = %d
credits_seconds = %d
`, c.Server.Addr, c.Upstream.BaseURL, c.Upstream.OpenAIBaseURL, c.Upstream.LoginPath,
		c.Upstream.LoginTimeout.String(), c.Upstream.RequestTimeout.String(), c.Upstream.UserAgent,
		c.Auth.Identifier, c.Auth.Password, c.Auth.Accounts, c.Auth.Strategy, c.Auth.RPM, c.Auth.AutoRelogin, c.PublicSettings().Auth.CreditCooldownThreshold,
		c.Admin.Token, c.Admin.TokenFile, c.Admin.AccountsFile, c.API.Key, c.API.KeysFile, c.API.AllowNoAuth, c.API.TrustedProxies,
		c.Checkin.Enable, c.Checkin.Path, c.Checkin.At, c.Checkin.Timezone,
		c.Redemption.Enable, c.Redemption.At, c.Redemption.Code,
		c.Update.Source, c.Update.CustomPrefix, c.Cache.defaults().AccountsSeconds, c.Cache.defaults().ModelsSeconds, c.Cache.defaults().CreditsSeconds)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

const defaultTOML = `# Generated by clodesen2api. Add accounts from the management panel.

[server]
addr = "0.0.0.0:6687"
[upstream]
base_url = "https://pai.zaiduyu.top"
openai_base_url = "https://pai.zaiduyu.top/api/openai"
login_path = "/api/user-auth"
login_timeout = "30s"
request_timeout = "0s"
user_agent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
[auth]
identifier = ""
password = ""
accounts = ""
strategy = "fixed"
rpm = 4
auto_relogin = true
credit_cooldown_threshold = 100
[admin]
token = ""
token_file = "data/admin.token"
accounts_file = "data/accounts.json"
[api]
key = ""
keys_file = "data/api-keys.json"
allow_no_auth = false
trusted_proxies = ""
[checkin]
enable = true
path = "/api/checkin"
at = "04:00"
timezone = "Asia/Shanghai"
[redemption]
enable = false
at = "04:30"
code = ""
[update]
source = "builtin:gh-proxy-com"
custom_prefix = ""
[cache]
accounts_seconds = 3600
models_seconds = 3600
credits_seconds = 3600
`
