package web_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"shopflow/internal/platform/web"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRespondJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	payload := map[string]string{"message": "success"}

	web.RespondJSON(rec, http.StatusOK, payload)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var res map[string]string
	err := json.Unmarshal(rec.Body.Bytes(), &res)
	require.NoError(t, err)
	assert.Equal(t, "success", res["message"])
}

func TestRespondProblem(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	rec := httptest.NewRecorder()

	web.RespondProblem(rec, req, http.StatusBadRequest, "INVALID_INPUT", "Invalid Input", "Field sku is required",
		web.InvalidParam{Name: "sku", Reason: "cannot be empty"},
	)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

	var problem web.ProblemDetails
	err := json.Unmarshal(rec.Body.Bytes(), &problem)
	require.NoError(t, err)

	assert.Equal(t, "https://shopflow.io/errors/INVALID_INPUT", problem.Type)
	assert.Equal(t, "Invalid Input", problem.Title)
	assert.Equal(t, http.StatusBadRequest, problem.Status)
	assert.Equal(t, "Field sku is required", problem.Detail)
	assert.Equal(t, "/api/v1/test", problem.Instance)
	assert.Equal(t, "INVALID_INPUT", problem.Code)
	require.Len(t, problem.InvalidParams, 1)
	assert.Equal(t, "sku", problem.InvalidParams[0].Name)
	assert.Equal(t, "cannot be empty", problem.InvalidParams[0].Reason)
}

func TestSetETag(t *testing.T) {
	rec := httptest.NewRecorder()
	web.SetETag(rec, 42)
	assert.Equal(t, `"42"`, rec.Header().Get("ETag"))
}

func TestExtractIfMatch(t *testing.T) {
	tests := []struct {
		name        string
		headerVal   string
		expectedVer int64
		expectErr   bool
	}{
		{
			name:        "quoted version",
			headerVal:   `"12"`,
			expectedVer: 12,
			expectErr:   false,
		},
		{
			name:        "unquoted version",
			headerVal:   `15`,
			expectedVer: 15,
			expectErr:   false,
		},
		{
			name:        "missing header",
			headerVal:   "",
			expectedVer: 0,
			expectErr:   true,
		},
		{
			name:        "non-integer header",
			headerVal:   `"abc"`,
			expectedVer: 0,
			expectErr:   true,
		},
		{
			name:        "zero version",
			headerVal:   `0`,
			expectedVer: 0,
			expectErr:   true,
		},
		{
			name:        "negative version",
			headerVal:   `-5`,
			expectedVer: 0,
			expectErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/", nil)
			if tt.headerVal != "" {
				req.Header.Set("If-Match", tt.headerVal)
			}

			ver, err := web.ExtractIfMatch(req)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedVer, ver)
			}
		})
	}
}

func TestDecodeJSON(t *testing.T) {
	type Sample struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	t.Run("valid payload", func(t *testing.T) {
		body := bytes.NewBufferString(`{"name":"Alice","age":30}`)
		req := httptest.NewRequest(http.MethodPost, "/", body)
		var s Sample
		err := web.DecodeJSON(req, &s)
		require.NoError(t, err)
		assert.Equal(t, "Alice", s.Name)
		assert.Equal(t, 30, s.Age)
	})

	t.Run("unknown field rejected", func(t *testing.T) {
		body := bytes.NewBufferString(`{"name":"Alice","age":30,"extra":"field"}`)
		req := httptest.NewRequest(http.MethodPost, "/", body)
		var s Sample
		err := web.DecodeJSON(req, &s)
		assert.Error(t, err)
	})

	t.Run("empty body rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(""))
		var s Sample
		err := web.DecodeJSON(req, &s)
		assert.Error(t, err)
	})
}
