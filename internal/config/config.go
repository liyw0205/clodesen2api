// Package config 负责加载并合并命令行 / 配置文件 / 环境变量。
package config

import (
	"time"

	"github.com/lfhy/flag"
)

// Config 由命令行 / 配置文件 / 环境变量统一注入。
// lfhy/flag 会按 命令行 > 环境变量 > 配置文件 > 默认值 的优先级合并。
type Config struct {
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
}

// Load 用 lfhy/flag 注册所有参数，支持 -c config.toml 统一载入。
func Load() *Config {
	c := &Config{}

	flag.StringConfigVar(&c.Server.Addr, "addr", "server", "addr", ":8080", "本地监听地址")

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

	flag.Parse()
	return c
}
