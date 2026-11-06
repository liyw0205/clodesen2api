// Package openai defines the OpenAI-compatible requests this gateway accepts.
package openai

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"strings"
)

const (
	ModelsPath          = "/v1/models"
	ChatCompletionsPath = "/v1/chat/completions"
	GenerationsPath     = "/v1/images/generations"
	EditsPath           = "/v1/images/edits"

	imageModel               = "gpt-image-2"
	responseFormat           = "b64_json"
	maxJSONBodyBytes   int64 = 1 << 20
	maxChatBodyBytes   int64 = 20 << 20
	maxPromptBytes           = 64 << 10
	maxImageCount            = 10
	maxImageBytes      int64 = 20 << 20
	maxEditBodyBytes         = maxImageCount*maxImageBytes + 2<<20
	maxMultipartMemory       = 8 << 20
)

// Request is a complete, locally constructed request for the upstream API.
// It contains no client-controlled HTTP metadata.
type Request struct {
	Method string
	Path   string
	// Model is used by upstream routing for image requests. It is not
	// user-controlled because image requests are normalized below.
	Model       string
	PublicModel string
	ContentType string
	Body        []byte
	// Stream and ThinkingLevel are normalized request metadata used by the
	// management request ledger. Image prompts are retained separately so the
	// panel can inspect them without persisting messages or image bytes.
	Stream        bool
	ThinkingLevel string
	Prompt        string
	Parameters    map[string]any
}

// BodyBudget is a conservative reservation covering input and rebuilt buffers.
func BodyBudget(path string, contentLength int64) int64 {
	var maximum int64 = maxChatBodyBytes
	switch path {
	case ModelsPath:
		return 0
	case GenerationsPath:
		maximum = maxJSONBodyBytes
	case EditsPath:
		maximum = maxEditBodyBytes
	}
	if contentLength >= 0 && contentLength < maximum {
		maximum = contentLength
	}
	return 8 * maximum
}

// RequestError is a client-facing validation failure.
type RequestError struct {
	Status  int
	Message string
	Param   string
}

func (e *RequestError) Error() string {
	return e.Message
}

// AsRequestError returns a client-facing request error when err represents one.
func AsRequestError(err error) (*RequestError, bool) {
	var requestErr *RequestError
	if errors.As(err, &requestErr) {
		return requestErr, true
	}
	return nil, false
}

// BuildRequest parses the incoming request and recreates a minimal request for
// the compatible upstream API. Client query parameters and request headers are
// deliberately discarded. Image requests are normalized, while chat payloads
// are preserved so OpenAI-compatible options can reach the upstream API.
func BuildRequest(w http.ResponseWriter, r *http.Request) (Request, error) {
	switch r.URL.Path {
	case ModelsPath:
		if err := requireMethod(r, http.MethodGet); err != nil {
			return Request{}, err
		}
		return Request{Method: http.MethodGet, Path: ModelsPath}, nil
	case ChatCompletionsPath:
		if err := requireMethod(r, http.MethodPost); err != nil {
			return Request{}, err
		}
		return buildChatCompletionRequest(r)
	case GenerationsPath:
		if err := requireMethod(r, http.MethodPost); err != nil {
			return Request{}, err
		}
		return buildGenerationRequest(r)
	case EditsPath:
		if err := requireMethod(r, http.MethodPost); err != nil {
			return Request{}, err
		}
		return buildEditRequest(w, r)
	default:
		return Request{}, &RequestError{Status: http.StatusNotFound, Message: "unsupported route"}
	}
}

func buildChatCompletionRequest(r *http.Request) (Request, error) {
	if !hasMediaType(r, "application/json") {
		return Request{}, &RequestError{
			Status:  http.StatusUnsupportedMediaType,
			Message: "content type must be application/json",
		}
	}

	body, err := readBody(r.Body, maxChatBodyBytes)
	if err != nil {
		return Request{}, err
	}

	var payload map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		return Request{}, &RequestError{Status: http.StatusBadRequest, Message: "invalid JSON request body"}
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Request{}, &RequestError{Status: http.StatusBadRequest, Message: "request body must contain one JSON object"}
	}

	var model string
	_ = json.Unmarshal(payload["model"], &model)
	stream := false
	_ = json.Unmarshal(payload["stream"], &stream)
	return Request{
		Method:        http.MethodPost,
		Path:          ChatCompletionsPath,
		Model:         strings.TrimSpace(model),
		ContentType:   "application/json",
		Body:          body,
		Stream:        stream,
		ThinkingLevel: thinkingLevel(payload),
		Parameters:    requestParameters(payload, "messages", "prompt"),
	}, nil
}

func thinkingLevel(payload map[string]json.RawMessage) string {
	for _, key := range []string{"thinking_level", "reasoning_effort", "thinking"} {
		var value string
		if json.Unmarshal(payload[key], &value) == nil && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
		var nested struct {
			Level string `json:"level"`
		}
		if json.Unmarshal(payload[key], &nested) == nil && strings.TrimSpace(nested.Level) != "" {
			return strings.TrimSpace(nested.Level)
		}
	}
	return ""
}

func requestParameters(payload map[string]json.RawMessage, excluded ...string) map[string]any {
	exclude := make(map[string]struct{}, len(excluded))
	for _, key := range excluded {
		exclude[key] = struct{}{}
	}
	result := make(map[string]any)
	allowed := map[string]bool{"temperature": true, "top_p": true, "max_tokens": true, "max_completion_tokens": true, "presence_penalty": true, "frequency_penalty": true, "n": true, "seed": true, "logprobs": true, "top_logprobs": true, "parallel_tool_calls": true, "reasoning_effort": true, "thinking_level": true, "size": true, "quality": true, "response_format": true, "aspect_ratio": true, "resolution": true}
	for key, raw := range payload {
		if key == "model" || key == "stream" || key == "messages" || key == "prompt" || key == "input" {
			continue
		}
		if _, skip := exclude[key]; skip {
			continue
		}
		if key == "tools" {
			var tools []json.RawMessage
			if json.Unmarshal(raw, &tools) == nil {
				result["tool_count"] = len(tools)
			}
			continue
		}
		if key == "response_format" {
			var format struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(raw, &format) == nil && (format.Type == "json_schema" || format.Type == "json_object" || format.Type == "text") {
				result["response_format"] = format.Type
				continue
			}
		}
		if !allowed[key] {
			continue
		}
		var value any
		if json.Unmarshal(raw, &value) != nil {
			continue
		}
		switch typed := value.(type) {
		case float64, bool:
		case string:
			if len(typed) > 64 {
				continue
			}
			switch key {
			case "reasoning_effort", "thinking_level":
				if typed != "none" && typed != "minimal" && typed != "low" && typed != "medium" && typed != "high" && typed != "xhigh" {
					continue
				}
			case "quality", "response_format", "size", "aspect_ratio", "resolution":
				if strings.ContainsAny(typed, " \t\n\r") {
					continue
				}
			default:
				continue
			}
		default:
			continue
		}
		// Keep controls useful in the ledger while preventing arbitrary client
		// metadata from becoming a large persisted record.
		encoded, _ := json.Marshal(value)
		if len(encoded) > 2048 {
			continue
		}
		result[key] = value
	}
	return result
}

func requireMethod(r *http.Request, method string) error {
	if r.Method == method {
		return nil
	}
	return &RequestError{
		Status:  http.StatusMethodNotAllowed,
		Message: fmt.Sprintf("method %s is not allowed for %s", r.Method, r.URL.Path),
	}
}

type generationInput struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Size   string `json:"size"`
}

type generationPayload struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n"`
	Quality        string `json:"quality"`
	Size           string `json:"size,omitempty"`
	ResponseFormat string `json:"response_format"`
}

func buildGenerationRequest(r *http.Request) (Request, error) {
	if !hasMediaType(r, "application/json") {
		return Request{}, &RequestError{
			Status:  http.StatusUnsupportedMediaType,
			Message: "content type must be application/json",
		}
	}

	body, err := readBody(r.Body, maxJSONBodyBytes)
	if err != nil {
		return Request{}, err
	}

	var input generationInput
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&input); err != nil {
		return Request{}, &RequestError{Status: http.StatusBadRequest, Message: "invalid JSON request body"}
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Request{}, &RequestError{Status: http.StatusBadRequest, Message: "request body must contain one JSON object"}
	}

	prompt, err := validatePrompt(input.Prompt)
	if err != nil {
		return Request{}, err
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = imageModel
	}
	payload, err := json.Marshal(generationPayload{
		Model:          model,
		Prompt:         prompt,
		N:              1,
		Quality:        "auto",
		Size:           input.Size,
		ResponseFormat: responseFormat,
	})
	if err != nil {
		return Request{}, fmt.Errorf("encode generation request: %w", err)
	}

	return Request{
		Method:      http.MethodPost,
		Path:        GenerationsPath,
		Model:       model,
		ContentType: "application/json",
		Body:        payload,
		Prompt:      prompt,
		Parameters: map[string]any{
			"n": 1, "quality": "auto", "size": input.Size,
			"response_format": responseFormat,
		},
	}, nil
}

func buildEditRequest(w http.ResponseWriter, r *http.Request) (Request, error) {
	if hasMediaType(r, "application/json") {
		return buildJSONEditRequest(r)
	}
	if !hasMediaType(r, "multipart/form-data") {
		return Request{}, &RequestError{
			Status:  http.StatusUnsupportedMediaType,
			Message: "content type must be multipart/form-data",
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxEditBodyBytes)
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
		if isReadTimeout(err) {
			return Request{}, &RequestError{Status: http.StatusRequestTimeout, Message: "request body read timed out"}
		}
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return Request{}, &RequestError{Status: http.StatusRequestEntityTooLarge, Message: "multipart request body is too large"}
		}
		return Request{}, &RequestError{Status: http.StatusBadRequest, Message: "invalid multipart request body"}
	}

	prompt, err := requiredFormValue(r.MultipartForm.Value, "prompt")
	if err != nil {
		return Request{}, err
	}
	prompt, err = validatePrompt(prompt)
	if err != nil {
		return Request{}, err
	}
	size, err := optionalFormValue(r.MultipartForm.Value, "size")
	if err != nil {
		return Request{}, err
	}
	files := append([]*multipart.FileHeader{}, r.MultipartForm.File["image"]...)
	files = append(files, r.MultipartForm.File["image[]"]...)
	if len(files) == 0 {
		return Request{}, &RequestError{Status: http.StatusBadRequest, Message: "at least one image is required", Param: "image"}
	}
	if len(files) > maxImageCount {
		return Request{}, &RequestError{Status: http.StatusBadRequest, Message: "at most 10 images are allowed", Param: "image"}
	}
	model, err := optionalFormValue(r.MultipartForm.Value, "model")
	if err != nil {
		return Request{}, err
	}
	if strings.TrimSpace(model) == "" {
		model = imageModel
	}

	images := make([]imageFile, 0, len(files))
	for _, fileHeader := range files {
		image, err := readImage(fileHeader)
		if err != nil {
			return Request{}, err
		}
		images = append(images, image)
	}

	return buildMultipartEditRequest(model, prompt, size, images)
}

type jsonImageEditPayload struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Size   string `json:"size"`
	Image  struct {
		URL string `json:"url"`
	} `json:"image"`
	AspectRatio    string `json:"aspect_ratio"`
	Resolution     string `json:"resolution"`
	ResponseFormat string `json:"response_format"`
	N              int    `json:"n"`
}

func buildJSONEditRequest(r *http.Request) (Request, error) {
	body, err := readBody(r.Body, maxEditBodyBytes)
	if err != nil {
		return Request{}, err
	}
	var input jsonImageEditPayload
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&input); err != nil {
		return Request{}, &RequestError{Status: http.StatusBadRequest, Message: "invalid JSON image edit request"}
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Request{}, &RequestError{Status: http.StatusBadRequest, Message: "request body must contain one JSON object"}
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = imageModel
	}
	prompt, err := validatePrompt(input.Prompt)
	if err != nil {
		return Request{}, err
	}
	image, err := decodeImageDataURL(input.Image.URL)
	if err != nil {
		return Request{}, err
	}
	requestedFormat := strings.TrimSpace(input.ResponseFormat)
	if requestedFormat == "" {
		requestedFormat = responseFormat
	}
	fields := map[string]string{}
	if input.AspectRatio != "" {
		fields["aspect_ratio"] = input.AspectRatio
	}
	if input.Resolution != "" {
		fields["resolution"] = input.Resolution
	}
	if input.N > 0 {
		fields["n"] = fmt.Sprint(input.N)
	}
	return buildMultipartEditRequestWithOptions(model, prompt, input.Size, requestedFormat, fields, []imageFile{image})
}

func decodeImageDataURL(value string) (imageFile, error) {
	const invalid = "image must be a base64 PNG, JPEG, or WebP data URL"
	if !strings.HasPrefix(strings.ToLower(value), "data:") {
		return imageFile{}, &RequestError{Status: http.StatusBadRequest, Message: invalid, Param: "image"}
	}
	parts := strings.SplitN(value[5:], ",", 2)
	if len(parts) != 2 {
		return imageFile{}, &RequestError{Status: http.StatusBadRequest, Message: invalid, Param: "image"}
	}
	metadata := strings.Split(parts[0], ";")
	if len(metadata) != 2 || !strings.EqualFold(metadata[1], "base64") {
		return imageFile{}, &RequestError{Status: http.StatusBadRequest, Message: invalid, Param: "image"}
	}
	declaredType, _, err := mime.ParseMediaType(metadata[0])
	if err != nil {
		return imageFile{}, &RequestError{Status: http.StatusBadRequest, Message: invalid, Param: "image"}
	}
	data, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(parts[1])
	}
	if err != nil {
		return imageFile{}, &RequestError{Status: http.StatusBadRequest, Message: invalid, Param: "image"}
	}
	image, err := validateImageData(data)
	if err != nil {
		return imageFile{}, err
	}
	if !strings.EqualFold(declaredType, image.contentType) && !(strings.EqualFold(declaredType, "image/jpg") && image.contentType == "image/jpeg") {
		return imageFile{}, &RequestError{Status: http.StatusBadRequest, Message: "image data URL type does not match its contents", Param: "image"}
	}
	return image, nil
}

func hasMediaType(r *http.Request, expected string) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && strings.EqualFold(mediaType, expected)
}

func readBody(body io.ReadCloser, maxBytes int64) ([]byte, error) {
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		if isReadTimeout(err) {
			return nil, &RequestError{Status: http.StatusRequestTimeout, Message: "request body read timed out"}
		}
		return nil, &RequestError{Status: http.StatusBadRequest, Message: "unable to read request body"}
	}
	if int64(len(data)) > maxBytes {
		return nil, &RequestError{Status: http.StatusRequestEntityTooLarge, Message: "request body is too large"}
	}
	return data, nil
}

func isReadTimeout(err error) bool {
	var timeout net.Error
	return errors.As(err, &timeout) && timeout.Timeout()
}

func validatePrompt(value string) (string, error) {
	prompt := strings.TrimSpace(value)
	if prompt == "" {
		return "", &RequestError{Status: http.StatusBadRequest, Message: "prompt is required", Param: "prompt"}
	}
	if len(prompt) > maxPromptBytes {
		return "", &RequestError{Status: http.StatusRequestEntityTooLarge, Message: "prompt is too large", Param: "prompt"}
	}
	return prompt, nil
}

func requiredFormValue(values map[string][]string, name string) (string, error) {
	value, err := optionalFormValue(values, name)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", &RequestError{Status: http.StatusBadRequest, Message: name + " is required", Param: name}
	}
	return value, nil
}

func optionalFormValue(values map[string][]string, name string) (string, error) {
	valuesForName := values[name]
	if len(valuesForName) == 0 {
		return "", nil
	}
	if len(valuesForName) != 1 {
		return "", &RequestError{Status: http.StatusBadRequest, Message: name + " must be provided once", Param: name}
	}
	return valuesForName[0], nil
}

type imageFile struct {
	data        []byte
	contentType string
	extension   string
}

func readImage(header *multipart.FileHeader) (imageFile, error) {
	file, err := header.Open()
	if err != nil {
		return imageFile{}, &RequestError{Status: http.StatusBadRequest, Message: "unable to read image", Param: "image"}
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxImageBytes+1))
	if err != nil {
		return imageFile{}, &RequestError{Status: http.StatusBadRequest, Message: "unable to read image", Param: "image"}
	}
	if int64(len(data)) > maxImageBytes {
		return imageFile{}, &RequestError{Status: http.StatusRequestEntityTooLarge, Message: "image is too large", Param: "image"}
	}

	return validateImageData(data)
}

func validateImageData(data []byte) (imageFile, error) {
	if int64(len(data)) > maxImageBytes {
		return imageFile{}, &RequestError{Status: http.StatusRequestEntityTooLarge, Message: "image is too large", Param: "image"}
	}
	contentType := http.DetectContentType(data)
	extension := ""
	switch contentType {
	case "image/png":
		extension = ".png"
	case "image/jpeg":
		extension = ".jpg"
	case "image/webp":
		extension = ".webp"
	default:
		return imageFile{}, &RequestError{Status: http.StatusBadRequest, Message: "image must be PNG, JPEG, or WebP", Param: "image"}
	}

	return imageFile{data: data, contentType: contentType, extension: extension}, nil
}

func buildMultipartEditRequest(model, prompt, size string, images []imageFile) (Request, error) {
	return buildMultipartEditRequestWithOptions(model, prompt, size, responseFormat, nil, images)
}

func buildMultipartEditRequestWithOptions(model, prompt, size, responseFormat string, fields map[string]string, images []imageFile) (Request, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if strings.TrimSpace(responseFormat) == "" {
		responseFormat = "b64_json"
	}

	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "model", value: model},
		{name: "prompt", value: prompt},
		{name: "response_format", value: responseFormat},
	} {
		if err := writer.WriteField(field.name, field.value); err != nil {
			return Request{}, fmt.Errorf("write multipart %s: %w", field.name, err)
		}
	}
	if size != "" {
		if err := writer.WriteField("size", size); err != nil {
			return Request{}, fmt.Errorf("write multipart size: %w", err)
		}
	}
	parameters := map[string]any{"size": size, "image_count": len(images), "response_format": responseFormat}
	for name, value := range fields {
		if name == "model" || name == "prompt" || name == "response_format" || name == "size" {
			continue
		}
		if err := writer.WriteField(name, value); err != nil {
			return Request{}, fmt.Errorf("write multipart %s: %w", name, err)
		}
		parameters[name] = value
	}

	for index, image := range images {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image"; filename="image-%d%s"`, index+1, image.extension))
		header.Set("Content-Type", image.contentType)
		part, err := writer.CreatePart(header)
		if err != nil {
			return Request{}, fmt.Errorf("create image part: %w", err)
		}
		if _, err := part.Write(image.data); err != nil {
			return Request{}, fmt.Errorf("write image part: %w", err)
		}
	}

	if err := writer.Close(); err != nil {
		return Request{}, fmt.Errorf("close multipart request: %w", err)
	}

	return Request{
		Method:      http.MethodPost,
		Path:        EditsPath,
		Model:       model,
		ContentType: writer.FormDataContentType(),
		Body:        body.Bytes(),
		Prompt:      prompt,
		Parameters:  parameters,
	}, nil
}
