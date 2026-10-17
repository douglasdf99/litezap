// Package apierr defines the API error envelope {"error":{code,message,retryable}}.
package apierr

import (
	"encoding/json"
	"errors"
	"net/http"
)

type Error struct {
	Status    int    `json:"-"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func New(status int, code, msg string, retryable bool) *Error {
	return &Error{Status: status, Code: code, Message: msg, Retryable: retryable}
}

func Validation(msg string) *Error {
	return New(http.StatusBadRequest, "validation_failed", msg, false)
}
func Unauthorized() *Error {
	return New(http.StatusUnauthorized, "unauthorized", "missing or invalid token", false)
}
func Forbidden() *Error {
	return New(http.StatusForbidden, "forbidden", "token not allowed for this resource", false)
}
func InstanceNotFound() *Error {
	return New(http.StatusNotFound, "instance_not_found", "instance not found", false)
}
func NotConnected() *Error {
	return New(http.StatusConflict, "instance_not_connected", "instance worker is not running", true)
}
func Internal() *Error {
	return New(http.StatusInternalServerError, "internal", "internal error", true)
}

// Write renders err; anything that is not an *Error becomes "internal".
func Write(w http.ResponseWriter, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = Internal()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(map[string]*Error{"error": e})
}
