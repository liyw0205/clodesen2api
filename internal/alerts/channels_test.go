package alerts

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"clodesen2api/internal/controlstore"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPChannelsBuildRequestsAndRejectBusinessFailures(t *testing.T) {
	for _, item := range []struct {
		id             string
		config         map[string]string
		path, response string
	}{
		{"bark", map[string]string{"push": "device", "group": "ops"}, "/device", `{"code":200}`},
		{"serverj", map[string]string{"sendKey": "sctp123tkey"}, "/send/sctp123tkey.send", `{"code":0}`},
		{"pushplus", map[string]string{"token": "push-secret", "topic": "team"}, "/send", `{"code":200}`},
		{"telegram", map[string]string{"token": "123:secret", "chatId": "-100", "apiHost": "http://localhost:8000/proxy"}, "/proxy/bot123:secret/sendMessage", `{"ok":true}`},
		{"qywxbot", map[string]string{"key": "wx-key", "origin": "http://localhost:8001"}, "/cgi-bin/webhook/send", `{"errcode":0}`},
		{"dingding", map[string]string{"token": "ding-token", "secret": "ding-secret"}, "/robot/send", `{"errcode":0}`},
		{"feishu", map[string]string{"key": "hook-key", "secret": "hook-secret"}, "/open-apis/bot/v2/hook/hook-key", `{"code":0}`},
		{"ntfy", map[string]string{"url": "http://localhost:8002", "topic": "topic", "token": "ntfy-secret", "priority": "5"}, "/topic", `{"id":"ok"}`},
		{"wxpusher", map[string]string{"token": "wx-secret", "topicIds": "12;34", "uids": "UID_1;UID_2"}, "/api/send/message", `{"code":1000}`},
		{"gotify", map[string]string{"url": "http://localhost:8003", "token": "gotify-secret", "priority": "4"}, "/message", `{"id":42}`},
		{"pushdeer", map[string]string{"key": "deer-secret"}, "/message/push", `{"code":0,"content":{"result":["ok"]}}`},
		{"webhook", map[string]string{"url": "http://localhost:8004/notify", "headers": "X-Token: header-secret"}, "/notify", `{}`},
	} {
		t.Run(item.id, func(t *testing.T) {
			if err := validateChannel(item.id, ChannelSettings{Config: item.config}, true); err != nil {
				t.Fatal(err)
			}
			event := Event{ID: "stable-id", Channel: item.id, Condition: "no_accounts", Active: true, At: time.Now()}
			var body string
			status, responseBody := 200, item.response
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != "POST" || r.URL.Path != item.path || r.Header.Get("Idempotency-Key") != event.ID {
					t.Errorf("invalid request %s %s", r.Method, r.URL.Path)
				}
				data, _ := io.ReadAll(r.Body)
				body = string(data)
				if r.Header.Get("Content-Type") == "application/json" && !json.Valid(data) {
					t.Error("invalid JSON")
				}
				if item.id == "telegram" && (!strings.Contains(body, `"chat_id":"-100"`) || !strings.Contains(body, "测试")) {
					t.Error(body)
				}
				if item.id == "qywxbot" && r.URL.Query().Get("key") != "wx-key" {
					t.Error("missing key")
				}
				if item.id == "gotify" && r.URL.Query().Get("token") != "gotify-secret" {
					t.Error("missing token")
				}
				if item.id == "ntfy" && (r.Header.Get("Authorization") != "Bearer ntfy-secret" || r.Header.Get("Priority") != "5" || strings.Contains(r.Header.Get("Title"), "测试")) {
					t.Error("invalid ntfy headers")
				}
				if item.id == "webhook" && r.Header.Get("X-Token") != "header-secret" {
					t.Error("missing custom header")
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(responseBody)), Header: http.Header{}}, nil
			})}
			if err := deliverHTTP(context.Background(), client, item.id, item.config, event, "测试标题", "正文\n第二行"); err != nil {
				t.Fatal(err)
			}
			responseBody = `{}`
			if item.id != "webhook" && item.id != "ntfy" {
				if !errors.Is(deliverHTTP(context.Background(), client, item.id, item.config, event, "测试标题", "正文"), ErrDelivery) {
					t.Fatal("HTTP 200 with missing provider success must fail")
				}
				responseBody = `{"code":500,"errno":500,"errcode":500,"StatusCode":500,"ok":false,"id":0}`
				if !errors.Is(deliverHTTP(context.Background(), client, item.id, item.config, event, "测试标题", "正文"), ErrDelivery) {
					t.Fatal("business failure accepted")
				}
			}
			status = 503
			if !errors.Is(deliverHTTP(context.Background(), client, item.id, item.config, event, "测试标题", "正文"), ErrDelivery) {
				t.Fatal("HTTP failure accepted")
			}
		})
	}
}

func TestSignaturesAndWebhookTemplates(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for _, id := range []string{"dingding", "feishu"} {
		config := map[string]string{"token": "token", "key": "key", "secret": "secret"}
		req, err := notificationRequest(context.Background(), id, config, Event{ID: "id"}, "标题", "正文", now)
		if err != nil {
			t.Fatal(err)
		}
		var actual string
		var mac []byte
		if id == "dingding" {
			if req.URL.Query().Get("timestamp") != "1700000000000" {
				t.Fatal(req.URL)
			}
			h := hmac.New(sha256.New, []byte("secret"))
			h.Write([]byte("1700000000000\nsecret"))
			mac = h.Sum(nil)
			actual = req.URL.Query().Get("sign")
		} else {
			var body map[string]any
			_ = json.NewDecoder(req.Body).Decode(&body)
			if body["timestamp"] != "1700000000" {
				t.Fatal(body)
			}
			h := hmac.New(sha256.New, []byte("1700000000\nsecret"))
			mac = h.Sum(nil)
			actual, _ = body["sign"].(string)
		}
		if actual != base64.StdEncoding.EncodeToString(mac) {
			t.Fatal("invalid signature")
		}
	}
	title, content := "引号\"&", "换行\n反斜杠\\和$切换"
	config := map[string]string{"url": "http://localhost/notify?q=$title", "body": `{"title":"$title","nested":["$content"]}`}
	req, err := notificationRequest(context.Background(), "webhook", config, Event{ID: "event"}, title, content, now)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Title  string   `json:"title"`
		Nested []string `json:"nested"`
	}
	if json.NewDecoder(req.Body).Decode(&value) != nil || value.Title != title || value.Nested[0] != content || req.URL.Query().Get("q") != title {
		t.Fatal(value)
	}
	config["contentType"] = "application/x-www-form-urlencoded"
	config["body"] = "title=$title&content=$content"
	req, _ = notificationRequest(context.Background(), "webhook", config, Event{ID: "event"}, title, content, now)
	body, _ := io.ReadAll(req.Body)
	form, _ := url.ParseQuery(string(body))
	if form.Get("content") != content || form.Get("title") != title {
		t.Fatal(string(body))
	}
	config["method"] = "GET"
	req, _ = notificationRequest(context.Background(), "webhook", config, Event{ID: "event"}, title, content, now)
	if req.Method != "GET" || req.ContentLength != 0 {
		t.Fatal("GET sent a body")
	}
	for _, raw := range []string{"Host: evil", "Bad header: value", "Authorization: a\rb", "Content-Length: 0"} {
		if _, err := parseHeaders(raw); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestChannelSecretsMigrationAndIndependentDelivery(t *testing.T) {
	ctx := context.Background()
	db, err := controlstore.Open(filepath.Join(t.TempDir(), "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	legacy := `{"schema":1,"revision":4,"settings":{"enabled":true,"url":"http://localhost/legacy?token=old-secret","lowBalance":100},"conditions":{},"pending":[]}`
	if _, err := db.Save(ctx, Document, 0, json.RawMessage(legacy)); err != nil {
		t.Fatal(err)
	}
	view, err := Load(ctx, db)
	if err != nil || !view.Settings.Channels["webhook"].Enabled || view.Schema != 2 {
		t.Fatalf("migration failed: %+v %v", view, err)
	}
	public, _ := json.Marshal(Public(view))
	if strings.Contains(string(public), "old-secret") || !Public(view).Configured["webhook"]["url"] {
		t.Fatal("secret exposed")
	}
	if view.Settings.Channels["webhook"].Config["url"] == "" {
		t.Fatal("Public mutated stored view")
	}
	var badCalls, goodCalls atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { badCalls.Add(1); w.WriteHeader(503) }))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer good.Close()
	view, err = Save(ctx, db, view.Revision, Settings{LowBalance: 100, Channels: map[string]ChannelSettings{
		"webhook":  {Enabled: true, Config: map[string]string{"url": bad.URL}},
		"telegram": {Enabled: true, Config: map[string]string{"token": "bot-secret", "chatId": "123", "apiHost": good.URL}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 0; i < 2; i++ {
		if err := Observe(ctx, db, map[string]bool{"no_accounts": true}, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	view, _ = Load(ctx, db)
	if goodCalls.Load() != 1 || badCalls.Load() != 1 || view.Delivery["telegram"].LastSent == nil || len(view.Pending) != 1 {
		t.Fatalf("channels blocked each other: %+v", view)
	}
	view, err = Save(ctx, db, view.Revision, Settings{LowBalance: 100, Channels: map[string]ChannelSettings{"telegram": {Enabled: true, Config: map[string]string{"chatId": "456", "apiHost": good.URL}}}})
	if err != nil || view.Settings.Channels["telegram"].Config["token"] != "bot-secret" || len(view.Pending) != 2 {
		t.Fatalf("secret retention or independent pending lost: %+v %v", view, err)
	}
	view, err = SaveWithClear(ctx, db, view.Revision, Settings{LowBalance: 100, Channels: map[string]ChannelSettings{"telegram": {Enabled: false, Config: map[string]string{"chatId": "456", "apiHost": good.URL}}}}, []string{"telegram.token"})
	if err != nil || view.Settings.Channels["telegram"].Config["token"] != "" || len(view.Pending) != 1 {
		t.Fatalf("clear failed: %+v %v", view, err)
	}
	_, err = Save(ctx, db, view.Revision, Settings{LowBalance: 100, Channels: map[string]ChannelSettings{"telegram": {Enabled: true, Config: map[string]string{"chatId": "456"}}}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("enabled incomplete channel accepted")
	}
	before := view.Revision
	if err := Test(ctx, db, before, "telegram", ChannelSettings{Config: map[string]string{"token": "test-secret", "chatId": "1", "apiHost": good.URL}}, nil); err != nil {
		t.Fatal(err)
	}
	after, _ := Load(ctx, db)
	if after.Revision != before || after.Settings.Channels["telegram"].Enabled || after.Settings.Channels["telegram"].Config["token"] != "" {
		t.Fatal("test unexpectedly persisted/enabled draft")
	}
}
