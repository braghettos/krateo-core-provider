package helper

import (
	"net/http"
	"net/url"
	"testing"
)

func TestGetQueryParamWithDefault(t *testing.T) {
	tests := []struct {
		name         string
		queryParams  map[string]string
		key          string
		defaultValue string
		expected     string
	}{
		{
			name:         "param exists with value",
			queryParams:  map[string]string{"test": "value123"},
			key:          "test",
			defaultValue: "default",
			expected:     "value123",
		},
		{
			name:         "param exists but empty",
			queryParams:  map[string]string{"test": ""},
			key:          "test",
			defaultValue: "default",
			expected:     "default",
		},
		{
			name:         "param does not exist",
			queryParams:  map[string]string{},
			key:          "test",
			defaultValue: "default",
			expected:     "default",
		},
		{
			name:         "param exists with whitespace",
			queryParams:  map[string]string{"test": " value "},
			key:          "test",
			defaultValue: "default",
			expected:     " value ",
		},
		{
			name:         "different param exists",
			queryParams:  map[string]string{"other": "value"},
			key:          "test",
			defaultValue: "default",
			expected:     "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create URL with query parameters
			u, _ := url.Parse("http://example.com")
			q := u.Query()
			for k, v := range tt.queryParams {
				q.Set(k, v)
			}
			u.RawQuery = q.Encode()

			// Create request
			req := &http.Request{
				URL: u,
			}

			result := GetQueryParamWithDefault(req, tt.key, tt.defaultValue)
			if result != tt.expected {
				t.Errorf("GetQueryParamWithDefault() = %v, want %v", result, tt.expected)
			}
		})
	}
}
