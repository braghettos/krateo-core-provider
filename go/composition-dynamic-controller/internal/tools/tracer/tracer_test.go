package tracer

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTracer_RoundTrip(t *testing.T) {
	tests := []struct {
		name          string
		method        string
		url           string
		fieldManager  string
		wantResources []Resource
	}{
		{
			name:         "Test with 8 segments and namespaces",
			method:       http.MethodPost,
			url:          "http://example.com/apis/group/version/namespaces/namespace/resource/name",
			fieldManager: "manager",
			wantResources: []Resource{
				{
					Group:     "group",
					Version:   "version",
					Resource:  "resource",
					Namespace: "namespace",
					Name:      "name",
				},
			},
		},
		{
			name:         "Test with 7 segments and namespaces",
			method:       http.MethodPost,
			url:          "http://example.com/apis/version/namespaces/namespace/resource/name",
			fieldManager: "manager",
			wantResources: []Resource{
				{
					Group:     "",
					Version:   "version",
					Resource:  "resource",
					Namespace: "namespace",
					Name:      "name",
				},
			},
		},
		{
			name:         "Test with 6 segments",
			method:       http.MethodPost,
			url:          "http://example.com/apis/group/version/resource/name",
			fieldManager: "manager",
			wantResources: []Resource{
				{
					Group:     "group",
					Version:   "version",
					Resource:  "resource",
					Namespace: "",
					Name:      "name",
				},
			},
		},
		{
			name:         "Test with 5 segments",
			method:       http.MethodPost,
			url:          "http://example.com/apis/version/resource/name",
			fieldManager: "manager",
			wantResources: []Resource{
				{
					Group:     "",
					Version:   "version",
					Resource:  "resource",
					Namespace: "",
					Name:      "name",
				},
			},
		},
		{
			name:          "Test with GET method",
			method:        http.MethodGet,
			url:           "http://example.com/apis/version/resource/name",
			fieldManager:  "",
			wantResources: []Resource{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracer := &Tracer{
				RoundTripper: http.DefaultTransport,
			}

			req := httptest.NewRequest(tt.method, tt.url, nil)
			if tt.fieldManager != "" {
				q := req.URL.Query()
				q.Add("fieldManager", tt.fieldManager)
				req.URL.RawQuery = q.Encode()
			}

			_, err := tracer.RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip() error = %v", err)
			}

			if len(tracer.GetResources()) != len(tt.wantResources) {
				t.Fatalf("expected %d resources, got %d", len(tt.wantResources), len(tracer.GetResources()))
			}

			for i, resource := range tracer.GetResources() {
				if resource != tt.wantResources[i] {
					t.Errorf("expected resource %v, got %v", tt.wantResources[i], resource)
				}
			}
		})
	}
}
