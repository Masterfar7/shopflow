// internal/platform/web/response.go
package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// ProblemDetails implements RFC 7807 Problem Details for HTTP APIs.
type ProblemDetails struct {
	Type          string         `json:"type"`
	Title         string         `json:"title"`
	Status        int            `json:"status"`
	Detail        string         `json:"detail"`
	Instance      string         `json:"instance,omitempty"`
	Code          string         `json:"code"`
	InvalidParams []InvalidParam `json:"invalid_params,omitempty"`
}

// InvalidParam describes a specific validation failure for a field.
type InvalidParam struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// RespondJSON marshals payload to JSON and sets Content-Type application/json.
func RespondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}

// RespondProblem renders an RFC 7807 problem details response.
func RespondProblem(w http.ResponseWriter, r *http.Request, status int, code, title, detail string, params ...InvalidParam) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	instance := ""
	if r != nil && r.URL != nil {
		instance = r.URL.Path
	}
	problem := ProblemDetails{
		Type:          fmt.Sprintf("https://shopflow.io/errors/%s", code),
		Title:         title,
		Status:        status,
		Detail:        detail,
		Instance:      instance,
		Code:          code,
		InvalidParams: params,
	}
	_ = json.NewEncoder(w).Encode(problem)
}

// SetETag writes the ETag header using the entity version.
func SetETag(w http.ResponseWriter, version int64) {
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, version))
}

// ExtractIfMatch parses the entity version from the If-Match header.
// Supports both quoted `"3"` and unquoted `3`.
func ExtractIfMatch(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		return 0, fmt.Errorf("missing If-Match header")
	}
	trimmed := strings.Trim(raw, "\"")
	version, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("invalid If-Match version: %q", raw)
	}
	return version, nil
}

// DecodeJSON decodes the request body into target, disallowing unknown fields.
func DecodeJSON(r *http.Request, target any) error {
	if r.Body == nil {
		return errors.New("missing request body")
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	return nil
}
