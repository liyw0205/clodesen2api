package openai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelMappingPreservesContentInJSONAndMultipart(t *testing.T) {
	r := Request{Model: "public", ContentType: "application/json", Body: []byte(`{"model":"public","messages":[{"content":"public source"}]}`)}
	mapped, err := r.WithModel("source", "public")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(mapped.Body, &body); err != nil {
		t.Fatal(err)
	}
	if string(body["model"]) != `"source"` || !bytes.Contains(body["messages"], []byte("public source")) {
		t.Fatalf("mapped %s", mapped.Body)
	}
	r, err = BuildRequest(httptest.NewRecorder(), newEditInput(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	mapped, err = r.WithModel("mapped-image", "public-image")
	if err != nil {
		t.Fatal(err)
	}
	parse := func(request Request) *http.Request {
		req := httptest.NewRequest(http.MethodPost, EditsPath, bytes.NewReader(request.Body))
		req.Header.Set("Content-Type", request.ContentType)
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { req.MultipartForm.RemoveAll() })
		return req
	}
	original, next := parse(r), parse(mapped)
	if next.FormValue("model") != "mapped-image" || next.FormValue("prompt") != original.FormValue("prompt") {
		t.Fatal("multipart fields changed incorrectly")
	}
	a, _, err := original.FormFile("image")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, _, err := next.FormFile("image")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	x, _ := io.ReadAll(a)
	y, _ := io.ReadAll(b)
	if !bytes.Equal(x, y) {
		t.Fatal("image bytes changed")
	}
}

func TestStreamMappingPreservesContentAndDone(t *testing.T) {
	input := ": ping\r\n\r\ndata: {\"model\":\"source\",\"choices\":[{\"delta\":{\"content\":\"source\"}}]}\r\n\r\ndata: [DONE]\n\n"
	var output bytes.Buffer
	if err := CopyModelStream(&output, strings.NewReader(input), "public"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"model":"public"`) || !strings.Contains(output.String(), `"content":"source"`) || !strings.HasSuffix(output.String(), "data: [DONE]\n\n") {
		t.Fatalf("mapped SSE %s", output.String())
	}
	if err := CopyModelStream(io.Discard, strings.NewReader("data: "+strings.Repeat("a", (2<<20)+1)), "public"); err == nil {
		t.Fatal("unbounded SSE line accepted")
	}
}
