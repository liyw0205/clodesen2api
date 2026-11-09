package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"strings"
)

// WithModel reconstructs structured model fields and preserves image bytes.
func (r Request) WithModel(upstream, public string) (Request, error) {
	r.PublicModel = public
	if r.Model == upstream {
		return r, nil
	}
	media, params, err := mime.ParseMediaType(r.ContentType)
	if err != nil {
		return Request{}, err
	}
	if media == "application/json" {
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(r.Body, &payload); err != nil {
			return Request{}, err
		}
		payload["model"], _ = json.Marshal(upstream)
		r.Body, err = json.Marshal(payload)
		if err != nil {
			return Request{}, err
		}
	} else if media == "multipart/form-data" {
		reader := multipart.NewReader(bytes.NewReader(r.Body), params["boundary"])
		var output bytes.Buffer
		writer := multipart.NewWriter(&output)
		for {
			part, err := reader.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return Request{}, err
			}
			next, err := writer.CreatePart(part.Header)
			if err != nil {
				part.Close()
				return Request{}, err
			}
			if part.FormName() == "model" && part.FileName() == "" {
				_, err = io.WriteString(next, upstream)
			} else {
				_, err = io.Copy(next, part)
			}
			part.Close()
			if err != nil {
				return Request{}, err
			}
		}
		if err := writer.Close(); err != nil {
			return Request{}, err
		}
		r.Body = output.Bytes()
		r.ContentType = writer.FormDataContentType()
	} else {
		return Request{}, errors.New("unsupported model mapping content type")
	}
	r.Model = upstream
	return r, nil
}

func ResponseModel(body []byte, public string) []byte {
	if public == "" {
		return body
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		return body
	}
	changed := false
	if _, ok := payload["model"]; ok {
		payload["model"], _ = json.Marshal(public)
		changed = true
	}
	// Responses SSE events carry the model inside their response object.
	var eventType string
	_ = json.Unmarshal(payload["type"], &eventType)
	if raw, ok := payload["response"]; ok && strings.HasPrefix(eventType, "response.") {
		var response map[string]json.RawMessage
		if json.Unmarshal(raw, &response) == nil && response != nil {
			if _, ok := response["model"]; ok {
				response["model"], _ = json.Marshal(public)
				payload["response"], _ = json.Marshal(response)
				changed = true
			}
		}
	}
	if !changed {
		return body
	}
	result, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return result
}

// CopyModelStream rewrites complete SSE data lines with a fixed memory bound.
func CopyModelStream(w io.Writer, r io.Reader, public string) error {
	reader := bufio.NewReader(r)
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		line = append(line, fragment...)
		if len(line) > 2<<20 {
			return errors.New("upstream SSE line exceeds mapping limit")
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if len(line) > 0 {
			output := line
			if bytes.HasPrefix(line, []byte("data:")) {
				data := bytes.TrimSpace(line[5:])
				mapped := ResponseModel(data, public)
				if !bytes.Equal(mapped, data) {
					ending := ""
					if bytes.HasSuffix(line, []byte("\r\n")) {
						ending = "\r\n"
					} else if bytes.HasSuffix(line, []byte("\n")) {
						ending = "\n"
					}
					output = []byte("data: " + string(mapped) + ending)
				}
			}
			if _, writeErr := w.Write(output); writeErr != nil {
				return writeErr
			}
		}
		line = line[:0]
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func IsMapped(r Request) bool {
	return strings.TrimSpace(r.PublicModel) != "" && r.PublicModel != r.Model
}
