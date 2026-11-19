// Package wire is the shared JSON-over-HTTP plumbing of the v1 contracts
// (ADR-0105 D5, SPEC-0100 R2). Only contracts/... may import it.
package wire

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Error is a typed contract error; errors.Is matches on Code.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// Typed errors shared by every v1 contract (SPEC-0100 R2.5).
var (
	ErrNotFound    = &Error{Code: "not_found"}
	ErrConflict    = &Error{Code: "conflict"}
	ErrForbidden   = &Error{Code: "forbidden"}
	ErrInvalid     = &Error{Code: "invalid"}
	ErrUnavailable = &Error{Code: "unavailable"}
)

// Errorf builds a typed error with a message.
func Errorf(kind *Error, format string, args ...any) error {
	return &Error{Code: kind.Code, Message: fmt.Sprintf(format, args...)}
}

const maxBody = 1 << 20

func statusOf(err error) int {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrConflict):
		return http.StatusConflict
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusServiceUnavailable
	}
}

// errorOf maps an HTTP status back to a typed error; anything unexpected is
// unavailable, never success.
func errorOf(status int, message string) error {
	kind := ErrUnavailable
	switch status {
	case http.StatusNotFound:
		kind = ErrNotFound
	case http.StatusConflict:
		kind = ErrConflict
	case http.StatusUnauthorized, http.StatusForbidden:
		kind = ErrForbidden
	case http.StatusBadRequest:
		kind = ErrInvalid
	}
	return &Error{Code: kind.Code, Message: message}
}

// Client is the HTTP side of a contract.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// Do sends in as JSON (no body when in is nil) and decodes a 200 body into out.
func (c Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return Errorf(ErrInvalid, "encode request: %v", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return Errorf(ErrInvalid, "request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Errorf(ErrUnavailable, "%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&failure)
		return errorOf(resp.StatusCode, failure.Error)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(out); err != nil {
		return Errorf(ErrUnavailable, "decode response: %v", err)
	}
	return nil
}

// Handle serves one JSON endpoint behind a constant-time bearer check.
func Handle(token string, fn func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !bearer || token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			write(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		out, err := fn(r)
		if err != nil {
			var typed *Error
			message := ""
			if errors.As(err, &typed) {
				message = typed.Message
			}
			write(w, statusOf(err), map[string]string{"error": message})
			return
		}
		write(w, http.StatusOK, out)
	}
}

// Decode reads a JSON request body into v.
func Decode(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return Errorf(ErrInvalid, "decode request: %v", err)
	}
	return nil
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Copy round-trips v through JSON so the in-process transport cannot share
// memory with the server and only serializable DTOs pass.
func Copy[T any](v T) (T, error) {
	var out T
	raw, err := json.Marshal(v)
	if err != nil {
		return out, Errorf(ErrInvalid, "encode: %v", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, Errorf(ErrInvalid, "decode: %v", err)
	}
	return out, nil
}

// Normalize gives the in-process transport the HTTP error semantics: a typed
// error keeps its code, anything else becomes unavailable.
func Normalize(err error) error {
	var typed *Error
	if err == nil {
		return err
	}
	if errors.As(err, &typed) {
		return typed
	}
	return ErrUnavailable
}
