package alerts

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
)

var ErrInvalid = errors.New("通知配置无效")

type Field struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Secret      bool     `json:"secret"`
	Placeholder string   `json:"placeholder,omitempty"`
	Options     []string `json:"options,omitempty"`
}

type ChannelDefinition struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Fields []Field `json:"fields"`
}

func field(key, label, kind string, required, secret bool, placeholder string, options ...string) Field {
	return Field{key, label, kind, required, secret, placeholder, options}
}

var definitions = []ChannelDefinition{
	{"webhook", "自定义 Webhook", []Field{
		field("url", "请求 URL", "url", true, true, "https://example.com/notify"),
		field("method", "请求方法", "select", false, false, "", "POST", "GET"),
		field("contentType", "Content-Type", "select", false, false, "", "application/json", "text/plain", "application/x-www-form-urlencoded"),
		field("headers", "请求头", "textarea", false, true, "Authorization: Bearer ..."),
		field("body", "请求体", "textarea", false, true, "支持 $title / $content；留空发送默认消息"),
	}},
	{"bark", "Bark", []Field{
		field("push", "Bark 地址或设备码", "text", true, true, "https://api.day.app/设备码"),
		field("group", "分组", "text", false, false, ""),
		field("sound", "声音", "text", false, false, ""),
		field("url", "点击跳转 URL", "url", false, false, ""),
	}},
	{"serverj", "Server 酱", []Field{field("sendKey", "SendKey", "text", true, true, "")}},
	{"pushplus", "PushPlus", []Field{
		field("token", "Token", "text", true, true, ""),
		field("topic", "群组编码", "text", false, false, ""),
		field("template", "模板", "select", false, false, "", "html", "markdown", "txt"),
	}},
	{"telegram", "Telegram Bot", []Field{
		field("token", "Bot Token", "text", true, true, ""),
		field("chatId", "User ID / Chat ID", "text", true, false, ""),
		field("apiHost", "API Host", "url", false, false, "https://api.telegram.org"),
	}},
	{"qywxbot", "企业微信机器人", []Field{
		field("key", "机器人 Key", "text", true, true, ""),
		field("origin", "API 地址", "url", false, false, "https://qyapi.weixin.qq.com"),
	}},
	{"dingding", "钉钉机器人", []Field{
		field("token", "Access Token", "text", true, true, ""),
		field("secret", "加签 Secret", "text", false, true, ""),
	}},
	{"feishu", "飞书机器人", []Field{
		field("key", "Webhook Key", "text", true, true, ""),
		field("secret", "签名 Secret", "text", false, true, ""),
	}},
	{"smtp", "SMTP 邮件", []Field{
		field("server", "SMTP 服务器", "text", true, false, "smtp.example.com:465"),
		field("security", "连接加密", "select", false, false, "", "tls", "starttls", "none"),
		field("email", "邮箱", "email", true, false, ""),
		field("password", "密码 / 授权码", "text", true, true, ""),
		field("name", "发件人名称", "text", false, false, "Clodesen"),
		field("to", "收件人", "text", false, false, "留空发送给自己；多个地址用逗号分隔"),
	}},
	{"ntfy", "ntfy", []Field{
		field("url", "ntfy 地址", "url", true, false, "https://ntfy.sh"),
		field("topic", "Topic", "text", true, false, ""),
		field("priority", "优先级", "select", false, false, "", "3", "1", "2", "4", "5"),
		field("token", "Token", "text", false, true, ""),
	}},
	{"wxpusher", "WxPusher", []Field{
		field("token", "App Token", "text", true, true, ""),
		field("topicIds", "Topic IDs", "text", false, false, "多个用英文分号分隔"),
		field("uids", "UIDs", "text", false, false, "多个用英文分号分隔"),
	}},
	{"gotify", "Gotify", []Field{
		field("url", "Gotify 地址", "url", true, false, "https://push.example.com"),
		field("token", "应用 Token", "text", true, true, ""),
		field("priority", "优先级", "number", false, false, "0"),
	}},
	{"pushdeer", "PushDeer", []Field{
		field("key", "PushDeer Key", "text", true, true, ""),
		field("url", "PushDeer URL", "url", false, false, "https://api2.pushdeer.com/message/push"),
	}},
}

func ChannelDefinitions() []ChannelDefinition { return definitions }

func channelDefinition(id string) *ChannelDefinition {
	for i := range definitions {
		if definitions[i].ID == id {
			return &definitions[i]
		}
	}
	return nil
}

func invalid(message string) error { return fmt.Errorf("%w：%s", ErrInvalid, message) }

func normalizeSettings(settings Settings) Settings {
	channels := make(map[string]ChannelSettings, len(settings.Channels))
	for id, channel := range settings.Channels {
		config := make(map[string]string, len(channel.Config))
		for key, value := range channel.Config {
			if key != "body" && key != "headers" && key != "password" {
				value = strings.TrimSpace(value)
			}
			config[key] = value
		}
		channel.Config = config
		channels[id] = channel
	}
	if settings.Channels == nil && (settings.Enabled || strings.TrimSpace(settings.URL) != "") {
		channels["webhook"] = ChannelSettings{Enabled: settings.Enabled, Config: map[string]string{"url": strings.TrimSpace(settings.URL)}}
	}
	settings.Channels = channels
	settings.URL = ""
	settings.Enabled = false
	for _, channel := range channels {
		settings.Enabled = settings.Enabled || channel.Enabled
	}
	return settings
}

func mergeSecrets(old, input Settings, clear []string) (Settings, error) {
	result := normalizeSettings(old)
	result.LowBalance = input.LowBalance
	for id, channel := range input.Channels {
		definition := channelDefinition(id)
		if definition == nil {
			return Settings{}, invalid("未知通知渠道")
		}
		for _, field := range definition.Fields {
			previous := result.Channels[id].Config[field.Key]
			if field.Secret && channel.Config[field.Key] == "" && previous != "" {
				channel.Config[field.Key] = previous
			}
		}
		result.Channels[id] = channel
	}
	for _, key := range clear {
		id, name, ok := strings.Cut(key, ".")
		definition := channelDefinition(id)
		if !ok || definition == nil {
			return Settings{}, invalid("凭据清除字段无效")
		}
		valid := false
		for _, field := range definition.Fields {
			if field.Key == name && field.Secret {
				valid = true
			}
		}
		channel, exists := result.Channels[id]
		if !valid || !exists {
			return Settings{}, invalid("凭据清除字段无效")
		}
		delete(channel.Config, name)
		result.Channels[id] = channel
	}
	return normalizeSettings(result), nil
}

func (settings Settings) Validate() error {
	settings = normalizeSettings(settings)
	if settings.LowBalance < 0 || settings.LowBalance > 1000000000000 {
		return invalid("余额阈值无效")
	}
	if len(settings.Channels) > len(definitions) {
		return invalid("通知渠道数量无效")
	}
	for id, channel := range settings.Channels {
		if err := validateChannel(id, channel, channel.Enabled); err != nil {
			return err
		}
	}
	return nil
}

func validURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && u.Fragment == "" && len(value) <= 2048 && !strings.ContainsAny(value, "\r\n")
}

func validateChannel(id string, channel ChannelSettings, require bool) error {
	definition := channelDefinition(id)
	if definition == nil {
		return invalid("未知通知渠道")
	}
	known := map[string]bool{}
	for _, field := range definition.Fields {
		known[field.Key] = true
		value := channel.Config[field.Key]
		if len(value) > 8192 {
			return invalid(field.Label + "过长")
		}
		if value == "" {
			if require && field.Required {
				return invalid("请填写" + field.Label)
			}
			continue
		}
		if field.Type != "textarea" && strings.ContainsAny(value, "\r\n") {
			return invalid(field.Label + "包含换行")
		}
		if field.Type == "url" && !validURL(value) {
			return invalid(field.Label + "必须为 HTTP 或 HTTPS URL")
		}
		if field.Type == "select" {
			valid := false
			for _, option := range field.Options {
				if option == value {
					valid = true
				}
			}
			if !valid {
				return invalid(field.Label + "选项无效")
			}
		}
	}
	for key := range channel.Config {
		if !known[key] {
			return invalid("渠道包含未知配置字段")
		}
	}
	c := channel.Config
	if c["priority"] != "" && id == "gotify" {
		value, err := strconv.Atoi(c["priority"])
		if err != nil || value < 0 || value > 10 {
			return invalid("Gotify 优先级应为 0-10")
		}
	}
	if id == "webhook" && c["headers"] != "" {
		if _, err := parseHeaders(c["headers"]); err != nil {
			return err
		}
	}
	if id == "webhook" && c["body"] != "" && defaultValue(c["contentType"], "application/json") == "application/json" {
		if _, err := renderBody(c["body"], "application/json", "title", "content"); err != nil {
			return err
		}
	}
	if id == "bark" && strings.Contains(c["push"], "://") && !validURL(c["push"]) {
		return invalid("Bark 地址无效")
	}
	if id == "ntfy" && strings.ContainsAny(c["topic"], "/?# ") {
		return invalid("ntfy Topic 不能包含路径或空格")
	}
	if id == "wxpusher" {
		if require && len(splitList(c["topicIds"])) == 0 && len(splitList(c["uids"])) == 0 {
			return invalid("Topic IDs 和 UIDs 至少填写一项")
		}
		for _, value := range splitList(c["topicIds"]) {
			if n, err := strconv.ParseInt(value, 10, 64); err != nil || n <= 0 {
				return invalid("Topic IDs 必须为正整数")
			}
		}
	}
	if id == "smtp" {
		if c["server"] != "" {
			if _, _, err := smtpAddress(c["server"], c["security"]); err != nil {
				return err
			}
		}
		if c["email"] != "" {
			if _, err := mail.ParseAddress(c["email"]); err != nil {
				return invalid("邮箱地址无效")
			}
		}
		if c["to"] != "" {
			if _, err := mail.ParseAddressList(c["to"]); err != nil {
				return invalid("收件人地址无效")
			}
		}
	}
	return nil
}

func splitList(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ';' || r == ',' || r == ' ' || r == '\n' })
}

func smtpAddress(server, security string) (string, string, error) {
	if strings.ContainsAny(server, "/\r\n\t ") {
		return "", "", invalid("SMTP 服务器无效")
	}
	port := "465"
	if security == "starttls" {
		port = "587"
	} else if security == "none" {
		port = "25"
	}
	host := server
	if strings.Contains(server, ":") {
		var err error
		host, port, err = net.SplitHostPort(server)
		if err != nil {
			return "", "", invalid("SMTP 服务器请使用主机:端口格式")
		}
	}
	n, err := strconv.Atoi(port)
	if host == "" || err != nil || n < 1 || n > 65535 {
		return "", "", invalid("SMTP 服务器端口无效")
	}
	return host, net.JoinHostPort(host, port), nil
}

type PublicView struct {
	View
	Definitions []ChannelDefinition        `json:"definitions"`
	Configured  map[string]map[string]bool `json:"configured"`
}

func Public(view View) PublicView {
	view.Settings = normalizeSettings(view.Settings)
	channels := make(map[string]ChannelSettings, len(view.Settings.Channels))
	for id, channel := range view.Settings.Channels {
		config := make(map[string]string, len(channel.Config))
		for key, value := range channel.Config {
			config[key] = value
		}
		channel.Config = config
		channels[id] = channel
	}
	view.Settings.Channels = channels
	configured := map[string]map[string]bool{}
	for id, channel := range view.Settings.Channels {
		configured[id] = map[string]bool{}
		for _, field := range channelDefinition(id).Fields {
			if field.Secret {
				configured[id][field.Key] = channel.Config[field.Key] != ""
				delete(channel.Config, field.Key)
			}
		}
	}
	view.Claim = ""
	return PublicView{view, definitions, configured}
}
