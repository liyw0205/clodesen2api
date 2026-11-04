package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildGenerationRequestRebuildsPayload(t *testing.T) {
	incoming := httptest.NewRequest(http.MethodPost, GenerationsPath+"?ignored=true", strings.NewReader(`{
		"model":"untrusted-model",
		"prompt":"  draw a cat  ",
		"n":10,
		"quality":"high",
		"size":"1792x1024",
		"response_format":"url",
		"unexpected":{"nested":true}
	}`))
	incoming.Header.Set("Content-Type", "application/json; charset=utf-8")
	incoming.Header.Set("Authorization", "Bearer client-token")

	request, err := BuildRequest(httptest.NewRecorder(), incoming)
	if err != nil {
		t.Fatalf("BuildRequest() error = %v", err)
	}
	if got, want := request.Method, http.MethodPost; got != want {
		t.Errorf("method = %q, want %q", got, want)
	}
	if got, want := request.Path, GenerationsPath; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if got, want := request.ContentType, "application/json"; got != want {
		t.Errorf("content type = %q, want %q", got, want)
	}

	var payload generationPayload
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		t.Fatalf("decode rebuilt body: %v", err)
	}
	wantPayload := generationPayload{
		Model:          imageModel,
		Prompt:         "draw a cat",
		N:              1,
		Quality:        "auto",
		Size:           "1792x1024",
		ResponseFormat: responseFormat,
	}
	if payload != wantPayload {
		t.Errorf("payload = %#v, want %#v", payload, wantPayload)
	}
}

func TestBuildChatCompletionRequestPreservesPayload(t *testing.T) {
	body := `{
		"model":"gpt-4.1",
		"messages":[{"role":"user","content":"hello"}],
		"stream":true,
		"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object"}}}]
	}`
	incoming := httptest.NewRequest(http.MethodPost, ChatCompletionsPath+"?ignored=true", strings.NewReader(body))
	incoming.Header.Set("Content-Type", "application/json; charset=utf-8")
	incoming.Header.Set("Authorization", "Bearer client-token")

	request, err := BuildRequest(httptest.NewRecorder(), incoming)
	if err != nil {
		t.Fatalf("BuildRequest() error = %v", err)
	}
	if got, want := request.Method, http.MethodPost; got != want {
		t.Errorf("method = %q, want %q", got, want)
	}
	if got, want := request.Path, ChatCompletionsPath; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if got, want := request.Model, "gpt-4.1"; got != want {
		t.Errorf("model = %q, want %q", got, want)
	}
	if got, want := request.ContentType, "application/json"; got != want {
		t.Errorf("content type = %q, want %q", got, want)
	}
	if got, want := string(request.Body), body; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if !request.Stream || request.ThinkingLevel != "" {
		t.Fatalf("request metadata = stream=%v thinking=%q", request.Stream, request.ThinkingLevel)
	}
	if _, ok := request.Parameters["messages"]; ok {
		t.Fatal("request parameters retained messages")
	}
	if _, ok := request.Parameters["tools"]; !ok {
		t.Fatal("request parameters omitted tools")
	}
}

func TestBuildChatCompletionRequestRejectsInvalidJSON(t *testing.T) {
	incoming := httptest.NewRequest(http.MethodPost, ChatCompletionsPath, strings.NewReader(`{"model":"gpt-4.1"} {}`))
	incoming.Header.Set("Content-Type", "application/json")

	_, err := BuildRequest(httptest.NewRecorder(), incoming)
	requestErr, ok := AsRequestError(err)
	if !ok {
		t.Fatalf("error = %v, want RequestError", err)
	}
	if got, want := requestErr.Status, http.StatusBadRequest; got != want {
		t.Errorf("status = %d, want %d", got, want)
	}
}

func TestBuildEditRequestRebuildsMultipartAndSupportsTenImages(t *testing.T) {
	incoming := newEditInput(t, maxImageCount)

	request, err := BuildRequest(httptest.NewRecorder(), incoming)
	if err != nil {
		t.Fatalf("BuildRequest() error = %v", err)
	}
	if got, want := request.Method, http.MethodPost; got != want {
		t.Errorf("method = %q, want %q", got, want)
	}
	if got, want := request.Path, EditsPath; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}

	mediaType, _, err := mime.ParseMediaType(request.ContentType)
	if err != nil {
		t.Fatalf("parse rebuilt content type: %v", err)
	}
	if got, want := mediaType, "multipart/form-data"; got != want {
		t.Errorf("content type = %q, want %q", got, want)
	}

	rebuilt := httptest.NewRequest(http.MethodPost, EditsPath, bytes.NewReader(request.Body))
	rebuilt.Header.Set("Content-Type", request.ContentType)
	if err := rebuilt.ParseMultipartForm(1 << 20); err != nil {
		t.Fatalf("parse rebuilt multipart request: %v", err)
	}
	defer rebuilt.MultipartForm.RemoveAll()

	if got, want := rebuilt.MultipartForm.Value["model"], []string{imageModel}; !sameStrings(got, want) {
		t.Errorf("model = %#v, want %#v", got, want)
	}
	if got, want := rebuilt.MultipartForm.Value["prompt"], []string{"draw a cat from the source images"}; !sameStrings(got, want) {
		t.Errorf("prompt = %#v, want %#v", got, want)
	}
	if got, want := rebuilt.MultipartForm.Value["response_format"], []string{responseFormat}; !sameStrings(got, want) {
		t.Errorf("response_format = %#v, want %#v", got, want)
	}
	if got, want := rebuilt.MultipartForm.Value["size"], []string{"1792x1024"}; !sameStrings(got, want) {
		t.Errorf("size = %#v, want %#v", got, want)
	}
	if got, want := request.Parameters["image_count"], maxImageCount; got != want {
		t.Errorf("image_count parameter = %#v, want %#v", got, want)
	}
	if _, ok := rebuilt.MultipartForm.Value["ignored"]; ok {
		t.Error("unexpected client form field reached rebuilt request")
	}
	if got := len(rebuilt.MultipartForm.File["image"]); got != maxImageCount {
		t.Fatalf("image part count = %d, want %d", got, maxImageCount)
	}
	if got := len(rebuilt.MultipartForm.File["image[]"]); got != 0 {
		t.Errorf("image[] part count = %d, want 0", got)
	}
	for index, header := range rebuilt.MultipartForm.File["image"] {
		if got, want := header.Filename, fmt.Sprintf("image-%d.png", index+1); got != want {
			t.Errorf("image %d filename = %q, want %q", index, got, want)
		}
		file, err := header.Open()
		if err != nil {
			t.Fatalf("open rebuilt image %d: %v", index, err)
		}
		data, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil {
			t.Fatalf("read rebuilt image %d: %v", index, err)
		}
		if !bytes.Equal(data, testPNG) {
			t.Errorf("image %d content changed", index)
		}
	}
}

func TestBuildEditRequestRejectsMoreThanTenImages(t *testing.T) {
	_, err := BuildRequest(httptest.NewRecorder(), newEditInput(t, maxImageCount+1))
	requestErr, ok := AsRequestError(err)
	if !ok {
		t.Fatalf("error = %v, want RequestError", err)
	}
	if got, want := requestErr.Status, http.StatusBadRequest; got != want {
		t.Errorf("status = %d, want %d", got, want)
	}
}

var testPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

func newEditInput(t *testing.T, imageCount int) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "model", value: "untrusted-model"},
		{name: "prompt", value: "draw a cat from the source images"},
		{name: "response_format", value: "url"},
		{name: "size", value: "1792x1024"},
		{name: "ignored", value: "discarded"},
	} {
		if err := writer.WriteField(field.name, field.value); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < imageCount; index++ {
		field := "image"
		if index%2 == 1 {
			field = "image[]"
		}
		part, err := writer.CreateFormFile(field, "client-controlled-name.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(testPNG); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, EditsPath, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
