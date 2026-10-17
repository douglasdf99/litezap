package apierr

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestWriteAPIError(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, NotConnected())
	if rec.Code != 409 {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Error Error `json:"error"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Code != "instance_not_connected" || !body.Error.Retryable {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestWriteUnknownErrorIsInternal(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, errors.New("boom"))
	if rec.Code != 500 || !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}
