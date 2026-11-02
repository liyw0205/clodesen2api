// Package httpx 提供跨包共享的轻量 HTTP 工具函数。
package httpx

import (
	"encoding/json"
	"net/url"
	"strings"
)

// imageURLKeys 是生图响应里可能承载图片地址的字段名。
var imageURLKeys = map[string]bool{
	"url":       true,
	"uri":       true,
	"image_url": true,
	"image":     true,
}

// IsRelativeImageURL 判断 value 是否是站内相对路径（如 /api/generated-images/xxx）。
func IsRelativeImageURL(value string) bool {
	text := strings.TrimSpace(value)
	if text == "" {
		return false
	}
	if strings.ContainsAny(text, " \t\r\n") {
		return false
	}
	lowered := strings.ToLower(text)
	if strings.HasPrefix(lowered, "http://") || strings.HasPrefix(lowered, "https://") ||
		strings.HasPrefix(lowered, "data:") || strings.HasPrefix(lowered, "//") {
		return false
	}
	return strings.HasPrefix(text, "/")
}

// AbsolutizeImageURLs 递归把 JSON 里的相对图片地址改写为 baseURL 下的绝对地址。
// 返回是否发生了改写。
func AbsolutizeImageURLs(node any, baseURL string) bool {
	root := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if root == "" {
		return false
	}
	changed := false
	var walk func(value any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				if text, ok := child.(string); ok && imageURLKeys[strings.ToLower(key)] && IsRelativeImageURL(text) {
					typed[key] = root + "/" + strings.TrimLeft(strings.TrimSpace(text), "/")
					changed = true
					continue
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(node)
	return changed
}

// RewriteJSONImageURLs 尝试把 body 当作 JSON 改写其中的相对图片地址。
// 第二个返回值表示是否产出了新的 JSON（调用方据此决定是否替换响应体）。
func RewriteJSONImageURLs(body []byte, baseURL string) ([]byte, bool) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return body, false
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return body, false
	}
	if !AbsolutizeImageURLs(parsed, baseURL) {
		return body, false
	}
	encoded, err := json.Marshal(parsed)
	if err != nil {
		return body, false
	}
	return encoded, true
}

// JoinURL 把相对路径拼到 base 上，用于代理站内资源。
func JoinURL(base, path string) string {
	root := strings.TrimRight(strings.TrimSpace(base), "/")
	if root == "" {
		return path
	}
	parsed, err := url.Parse(root)
	if err != nil {
		return root + "/" + strings.TrimLeft(path, "/")
	}
	rel, err := url.Parse(path)
	if err != nil {
		return root + "/" + strings.TrimLeft(path, "/")
	}
	return parsed.ResolveReference(rel).String()
}
