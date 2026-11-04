package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTrackingResponseWriterCapturesErrorWithoutChangingResponse(t *testing.T) {
	response := httptest.NewRecorder()
	tracked := &trackingResponseWriter{ResponseWriter: response}
	body := strings.Repeat("x", 1000)
	tracked.WriteHeader(http.StatusBadGateway)
	if _, err := tracked.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if got := len(tracked.errorBody); got != 512 {
		t.Fatalf("captured error length = %d, want 512", got)
	}
	if got := response.Body.Len(); got != len(body) {
		t.Fatalf("response body length = %d, want original %d", got, len(body))
	}
}
