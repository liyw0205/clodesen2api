package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"

	"clodesen2api/internal/openai"
)

func (u *Upstream) ListModels(ctx context.Context) (json.RawMessage, error) {
	recorder := httptest.NewRecorder()
	err, wrote := u.Forward(ctx, recorder, openai.Request{Method: http.MethodGet, Path: openai.ModelsPath})
	if err != nil {
		return nil, err
	}
	if !wrote || recorder.Code < http.StatusOK || recorder.Code >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("upstream model list returned HTTP %d", recorder.Code)
	}
	var payload struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil || payload.Data == nil {
		return nil, errors.New("upstream model list is not an OpenAI-compatible model response")
	}
	return append(json.RawMessage(nil), recorder.Body.Bytes()...), nil
}
