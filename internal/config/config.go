// Package config 负责加载并合并命令行 / 配置文件 / 环境变量。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lfhy/flag"
)

// Config 由命令行 / 配置文件 / 环境变量统一注入。
// lfhy/flag 会按 命令行 > 环境变量 > 配置文件 > 默认值 的优先级合并。
type Config struct {
	path   string
	Server struct {
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
		Identifier  string
		Password    string
		Accounts    string
		Strategy    string
		AutoRelogin bool
	}
	Admin struct {
		Token        string
		TokenFile    string
		AccountsFile string
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
}

// PublicSettings excludes credentials and tokens from the management API.
type PublicSettings struct {
	Server struct {
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
		Strategy    string `json:"strategy"`
		AutoRelogin bool   `json:"autoRelogin"`
	} `json:"auth"`
	Checkin struct {
		Enable   bool   `json:"enable"`
		Path     string `json:"path"`
		At       string `json:"at"`
		Timezone string `json:"timezone"`
	} `json:"checkin"`
	Redemption struct {
		Enable         bool   `json:"enable"`
		At             string `json:"at"`
		Code           string `json:"code,omitempty"`
		CodeConfigured bool   `json:"codeConfigured"`
	} `json:"redemption"`
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
	flag.StringConfigVar(&c.Auth.Strategy, "strategy", "auth", "strategy", "round_robin", "账号选择策略：round_robin 或 random")
	flag.BoolConfigVar(&c.Auth.AutoRelogin, "auto-relogin", "auth", "auto_relogin", true, "401 时是否自动重新登录")
	flag.StringConfigVar(&c.Admin.Token, "admin-token", "admin", "token", "", "管理后台访问令牌（首次设置为 6-20 个字符）")
	flag.StringConfigVar(&c.Admin.TokenFile, "admin-token-file", "admin", "token_file", "data/admin.token", "管理后台令牌持久化文件")
	flag.StringConfigVar(&c.Admin.AccountsFile, "admin-accounts-file", "admin", "accounts_file", "data/accounts.json", "管理后台账号与策略存储文件")

	flag.BoolConfigVar(&c.Checkin.Enable, "checkin", "checkin", "enable", true, "是否启用每日自动签到(GET)")
	flag.StringConfigVar(&c.Checkin.Path, "checkin-path", "checkin", "path", "/api/checkin", "签到端点路径")
	flag.StringConfigVar(&c.Checkin.At, "checkin-at", "checkin", "at", "04:00", "每天签到时间(HH:MM)")
	flag.StringConfigVar(&c.Checkin.Timezone, "checkin-tz", "checkin", "timezone", "Asia/Shanghai", "签到所用时区")
	flag.BoolConfigVar(&c.Redemption.Enable, "auto-redeem", "redemption", "enable", false, "是否启用每日自动兑换")
	flag.StringConfigVar(&c.Redemption.At, "auto-redeem-at", "redemption", "at", "04:30", "自动兑换时间(HH:MM，UTC+8)")
	flag.StringConfigVar(&c.Redemption.Code, "auto-redeem-code", "redemption", "code", "", "自动兑换码")

	flag.Parse()
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
	out.Server.Addr = c.Server.Addr
	out.Upstream.BaseURL, out.Upstream.OpenAIBaseURL, out.Upstream.LoginPath = c.Upstream.BaseURL, c.Upstream.OpenAIBaseURL, c.Upstream.LoginPath
	out.Upstream.LoginTimeout, out.Upstream.RequestTimeout, out.Upstream.UserAgent = c.Upstream.LoginTimeout.String(), c.Upstream.RequestTimeout.String(), c.Upstream.UserAgent
	out.Auth.Strategy, out.Auth.AutoRelogin = c.Auth.Strategy, c.Auth.AutoRelogin
	out.Checkin.Enable, out.Checkin.Path, out.Checkin.At, out.Checkin.Timezone = c.Checkin.Enable, c.Checkin.Path, c.Checkin.At, c.Checkin.Timezone
	out.Redemption.Enable, out.Redemption.At = c.Redemption.Enable, c.Redemption.At
	out.Redemption.CodeConfigured = strings.TrimSpace(c.Redemption.Code) != ""
	return out
}

func (c *Config) ApplyPublicSettings(settings PublicSettings) error {
	if strings.TrimSpace(settings.Server.Addr) == "" {
		return errors.New("监听地址不能为空")
	}
	strategy := strings.ToLower(strings.TrimSpace(settings.Auth.Strategy))
	if strategy != "round_robin" && strategy != "random" {
		return errors.New("账号策略必须是 round_robin 或 random")
	}
	loginTimeout, err := time.ParseDuration(strings.TrimSpace(settings.Upstream.LoginTimeout))
	if err != nil || loginTimeout <= 0 {
		return errors.New("登录超时必须是正数时长，例如 30s")
	}
	requestTimeout, err := time.ParseDuration(strings.TrimSpace(settings.Upstream.RequestTimeout))
	if err != nil || requestTimeout < 0 {
		return errors.New("请求超时必须是非负时长，例如 0s")
	}
	if strings.TrimSpace(settings.Upstream.BaseURL) == "" || strings.TrimSpace(settings.Upstream.OpenAIBaseURL) == "" {
		return errors.New("上游地址不能为空")
	}
	if strings.TrimSpace(settings.Upstream.LoginPath) == "" || strings.TrimSpace(settings.Checkin.Path) == "" {
		return errors.New("接口路径不能为空")
	}
	if strings.TrimSpace(settings.Checkin.At) == "" || strings.TrimSpace(settings.Checkin.Timezone) == "" {
		return errors.New("签到时间和时区不能为空")
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
	c.Server.Addr = strings.TrimSpace(settings.Server.Addr)
	c.Upstream.BaseURL = strings.TrimRight(strings.TrimSpace(settings.Upstream.BaseURL), "/")
	c.Upstream.OpenAIBaseURL = strings.TrimRight(strings.TrimSpace(settings.Upstream.OpenAIBaseURL), "/")
	c.Upstream.LoginPath, c.Upstream.LoginTimeout, c.Upstream.RequestTimeout, c.Upstream.UserAgent = strings.TrimSpace(settings.Upstream.LoginPath), loginTimeout, requestTimeout, strings.TrimSpace(settings.Upstream.UserAgent)
	c.Auth.Strategy, c.Auth.AutoRelogin = strategy, settings.Auth.AutoRelogin
	c.Checkin.Enable, c.Checkin.Path, c.Checkin.At, c.Checkin.Timezone = settings.Checkin.Enable, strings.TrimSpace(settings.Checkin.Path), strings.TrimSpace(settings.Checkin.At), strings.TrimSpace(settings.Checkin.Timezone)
	c.Redemption.Enable, c.Redemption.At = settings.Redemption.Enable, redemptionAt
	if strings.TrimSpace(settings.Redemption.Code) != "" {
		c.Redemption.Code = strings.TrimSpace(settings.Redemption.Code)
	}
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
auto_relogin = %t

[admin]
token = %q
token_file = %q
accounts_file = %q

[checkin]
enable = %t
path = %q
at = %q
timezone = %q

[redemption]
enable = %t
at = %q
code = %q
`, c.Server.Addr, c.Upstream.BaseURL, c.Upstream.OpenAIBaseURL, c.Upstream.LoginPath,
		c.Upstream.LoginTimeout.String(), c.Upstream.RequestTimeout.String(), c.Upstream.UserAgent,
		c.Auth.Identifier, c.Auth.Password, c.Auth.Accounts, c.Auth.Strategy, c.Auth.AutoRelogin,
		c.Admin.Token, c.Admin.TokenFile, c.Admin.AccountsFile, c.Checkin.Enable, c.Checkin.Path, c.Checkin.At, c.Checkin.Timezone,
		c.Redemption.Enable, c.Redemption.At, c.Redemption.Code)
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
strategy = "round_robin"
auto_relogin = true
[admin]
token = ""
token_file = "data/admin.token"
accounts_file = "data/accounts.json"
[checkin]
enable = true
path = "/api/checkin"
at = "04:00"
timezone = "Asia/Shanghai"
[redemption]
enable = false
at = "04:30"
code = ""
`
