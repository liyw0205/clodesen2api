package alerts

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrDelivery = errors.New("通知发送失败，请检查渠道配置和网络")

func eventCopy(event Event) (string, string) {
	names := map[string]string{"low_balance": "余额不足", "no_accounts": "无可用账号", "job_failure": "任务失败", "audit_degraded": "审计异常"}
	state := "告警"
	if !event.Active {
		state = "已恢复"
	}
	return "Clodesen · " + names[event.Condition] + " · " + state,
		"事件：" + names[event.Condition] + "\n状态：" + state + "\n时间：" + event.At.Format(time.RFC3339) + "\n事件 ID：" + event.ID
}

func deliver(ctx context.Context, id string, settings ChannelSettings, event Event, title, content string) error {
	if id == "smtp" {
		return sendSMTP(ctx, settings.Config, title, content)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return deliverHTTP(ctx, client, id, settings.Config, event, title, content)
}

func deliverHTTP(ctx context.Context, client *http.Client, id string, config map[string]string, event Event, title, content string) error {
	req, err := notificationRequest(ctx, id, config, event, title, content, time.Now())
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return ErrDelivery
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(body) > 65536 || response.StatusCode < 200 || response.StatusCode >= 300 {
		return ErrDelivery
	}
	if id == "webhook" || id == "ntfy" {
		return nil
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(body, &result) != nil {
		return ErrDelivery
	}
	code := func(key string, expected int) bool {
		var value int
		raw, exists := result[key]
		return exists && json.Unmarshal(raw, &value) == nil && value == expected
	}
	ok := false
	switch id {
	case "bark", "pushplus":
		ok = code("code", 200)
	case "serverj":
		ok = code("code", 0) || code("errno", 0)
	case "telegram":
		var value bool
		ok = json.Unmarshal(result["ok"], &value) == nil && value
	case "qywxbot", "dingding":
		ok = code("errcode", 0)
	case "feishu":
		ok = code("code", 0) || code("StatusCode", 0)
	case "wxpusher":
		ok = code("code", 1000)
	case "gotify":
		var value int64
		ok = json.Unmarshal(result["id"], &value) == nil && value > 0
	case "pushdeer":
		var value struct {
			Result []json.RawMessage `json:"result"`
		}
		ok = code("code", 0) && json.Unmarshal(result["content"], &value) == nil && len(value.Result) > 0
	}
	if !ok {
		return ErrDelivery
	}
	return nil
}

func notificationRequest(ctx context.Context, id string, c map[string]string, event Event, title, content string, now time.Time) (*http.Request, error) {
	target, method, contentType := "", http.MethodPost, "application/json"
	var payload any
	var body []byte
	headers := http.Header{}
	text := title + "\n\n" + content
	switch id {
	case "bark":
		target = c["push"]
		if !strings.Contains(target, "://") {
			target = "https://api.day.app/" + url.PathEscape(target)
		}
		payload = map[string]string{"title": title, "body": content, "group": c["group"], "sound": c["sound"], "url": c["url"]}
	case "serverj":
		key := c["sendKey"]
		target = "https://sctapi.ftqq.com/" + url.PathEscape(key) + ".send"
		if match := regexp.MustCompile(`^sctp(\d+)t`).FindStringSubmatch(key); len(match) == 2 {
			target = "https://" + match[1] + ".push.ft07.com/send/" + url.PathEscape(key) + ".send"
		}
		body = []byte(url.Values{"text": {title}, "desp": {content}}.Encode())
		contentType = "application/x-www-form-urlencoded"
	case "pushplus":
		target = "https://www.pushplus.plus/send"
		payload = map[string]string{"token": c["token"], "title": title, "content": content, "topic": c["topic"], "template": defaultValue(c["template"], "html")}
	case "telegram":
		target = strings.TrimRight(defaultValue(c["apiHost"], "https://api.telegram.org"), "/") + "/bot" + url.PathEscape(c["token"]) + "/sendMessage"
		payload = map[string]any{"chat_id": c["chatId"], "text": text, "disable_web_page_preview": true}
	case "qywxbot":
		target = strings.TrimRight(defaultValue(c["origin"], "https://qyapi.weixin.qq.com"), "/") + "/cgi-bin/webhook/send?" + url.Values{"key": {c["key"]}}.Encode()
		payload = map[string]any{"msgtype": "text", "text": map[string]string{"content": text}}
	case "dingding":
		query := url.Values{"access_token": {c["token"]}}
		if c["secret"] != "" {
			stamp := strconv.FormatInt(now.UnixMilli(), 10)
			mac := hmac.New(sha256.New, []byte(c["secret"]))
			mac.Write([]byte(stamp + "\n" + c["secret"]))
			query.Set("timestamp", stamp)
			query.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		}
		target = "https://oapi.dingtalk.com/robot/send?" + query.Encode()
		payload = map[string]any{"msgtype": "text", "text": map[string]string{"content": text}}
	case "feishu":
		target = "https://open.feishu.cn/open-apis/bot/v2/hook/" + url.PathEscape(c["key"])
		data := map[string]any{"msg_type": "text", "content": map[string]string{"text": text}}
		if c["secret"] != "" {
			stamp := strconv.FormatInt(now.Unix(), 10)
			mac := hmac.New(sha256.New, []byte(stamp+"\n"+c["secret"]))
			data["timestamp"] = stamp
			data["sign"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
		}
		payload = data
	case "ntfy":
		target = strings.TrimRight(c["url"], "/") + "/" + url.PathEscape(c["topic"])
		body = []byte(content)
		contentType = "text/plain; charset=utf-8"
		headers.Set("Title", "=?UTF-8?B?"+base64.StdEncoding.EncodeToString([]byte(title))+"?=")
		headers.Set("Priority", defaultValue(c["priority"], "3"))
		if c["token"] != "" {
			headers.Set("Authorization", "Bearer "+c["token"])
		}
	case "wxpusher":
		target = "https://wxpusher.zjiecode.com/api/send/message"
		topics := []int64{}
		for _, item := range splitList(c["topicIds"]) {
			value, _ := strconv.ParseInt(item, 10, 64)
			topics = append(topics, value)
		}
		payload = map[string]any{"appToken": c["token"], "content": text, "summary": truncate(title, 96), "contentType": 1, "topicIds": topics, "uids": splitList(c["uids"]), "verifyPayType": 0}
	case "gotify":
		target = strings.TrimRight(c["url"], "/") + "/message?" + url.Values{"token": {c["token"]}}.Encode()
		priority, _ := strconv.Atoi(c["priority"])
		payload = map[string]any{"title": title, "message": content, "priority": priority}
	case "pushdeer":
		target = defaultValue(c["url"], "https://api2.pushdeer.com/message/push")
		body = []byte(url.Values{"pushkey": {c["key"]}, "text": {title}, "desp": {content}, "type": {"markdown"}}.Encode())
		contentType = "application/x-www-form-urlencoded"
	case "webhook":
		target = replaceText(c["url"], url.QueryEscape(title), url.QueryEscape(content))
		method = defaultValue(c["method"], http.MethodPost)
		contentType = defaultValue(c["contentType"], "application/json")
		var err error
		headers, err = parseHeaders(c["headers"])
		if err != nil {
			return nil, err
		}
		if method == http.MethodPost {
			if c["body"] != "" {
				body, err = renderBody(c["body"], contentType, title, content)
				if err != nil {
					return nil, err
				}
			} else if contentType == "application/json" {
				data, _ := json.Marshal(event)
				var message map[string]any
				_ = json.Unmarshal(data, &message)
				message["title"], message["content"] = title, content
				payload = message
			} else if contentType == "application/x-www-form-urlencoded" {
				body = []byte(url.Values{"title": {title}, "content": {content}}.Encode())
			} else {
				body = []byte(text)
			}
		}
	default:
		return nil, invalid("未知通知渠道")
	}
	if payload != nil {
		body, _ = json.Marshal(payload)
	}
	if !validURL(target) {
		return nil, invalid("通知请求 URL 无效")
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, ErrDelivery
	}
	req.Header = headers
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Idempotency-Key", event.ID)
	return req, nil
}

func defaultValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
func truncate(value string, max int) string {
	runes := []rune(value)
	if len(runes) > max {
		runes = runes[:max]
	}
	return string(runes)
}
func replaceText(value, title, content string) string {
	return strings.NewReplacer("$title", title, "$content", content).Replace(value)
}

func renderBody(template, contentType, title, content string) ([]byte, error) {
	if contentType != "application/json" {
		if contentType == "application/x-www-form-urlencoded" {
			title, content = url.QueryEscape(title), url.QueryEscape(content)
		}
		return []byte(replaceText(template, title, content)), nil
	}
	var value any
	if json.Unmarshal([]byte(template), &value) != nil {
		return nil, invalid("Webhook JSON 请求体无效")
	}
	var replace func(any) any
	replace = func(value any) any {
		switch item := value.(type) {
		case string:
			return replaceText(item, title, content)
		case []any:
			for i := range item {
				item[i] = replace(item[i])
			}
			return item
		case map[string]any:
			for key := range item {
				item[key] = replace(item[key])
			}
			return item
		default:
			return value
		}
	}
	return json.Marshal(replace(value))
}

func parseHeaders(raw string) (http.Header, error) {
	result := http.Header{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || key == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, invalid("请求头应为每行 Key: Value")
		}
		for _, r := range key {
			if r <= 32 || r >= 127 || strings.ContainsRune("()<>@,;:\\\"/[]?={}", r) {
				return nil, invalid("请求头名称无效")
			}
		}
		switch strings.ToLower(key) {
		case "host", "content-length", "connection", "transfer-encoding", "idempotency-key":
			return nil, invalid(fmt.Sprintf("不能覆盖 %s 请求头", key))
		}
		result.Set(key, value)
	}
	return result, nil
}
