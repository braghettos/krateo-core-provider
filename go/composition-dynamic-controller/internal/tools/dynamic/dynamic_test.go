package dynamic

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
)

func TestNewRESTMapper(t *testing.T) {
	tests := []struct {
		name    string
		config  *rest.Config
		wantErr bool
	}{
		{
			name:    "nil config should use in-cluster config",
			config:  nil,
			wantErr: true, // Will fail in test environment without kubeconfig
		},
		{
			name: "valid config should succeed",
			config: &rest.Config{
				Host: "https://localhost:8080",
			},
			wantErr: false, // Deferred discovery means creation succeeds, errors happen on use
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper, err := NewRESTMapper(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewRESTMapper() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && mapper == nil {
				t.Error("NewRESTMapper() returned nil mapper without error")
			}
		})
	}
}

func TestIsNamespaced(t *testing.T) {
	tests := []struct {
		name    string
		mapper  meta.RESTMapper
		gvk     schema.GroupVersionKind
		want    bool
		wantErr bool
	}{
		{
			name:   "nil mapper returns false without error",
			mapper: nil,
			gvk: schema.GroupVersionKind{
				Group:   "apps",
				Version: "v1",
				Kind:    "Deployment",
			},
			want:    false,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := IsNamespaced(tt.mapper, tt.gvk)
			if (err != nil) != tt.wantErr {
				t.Errorf("IsNamespaced() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("IsNamespaced() = %v, want %v", got, tt.want)
			}
		})
	}
}

// MockRESTMapper implements meta.RESTMapper for testing
type MockRESTMapper struct {
	scopeName meta.RESTScopeName
	err       error
}

// MockRESTScope implements meta.RESTScope for testing
type MockRESTScope struct {
	name meta.RESTScopeName
}

func (m *MockRESTScope) Name() meta.RESTScopeName {
	return m.name
}

func (m *MockRESTMapper) KindFor(resource schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	return schema.GroupVersionKind{}, m.err
}

func (m *MockRESTMapper) KindsFor(resource schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	return nil, m.err
}

func (m *MockRESTMapper) ResourceFor(input schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	return schema.GroupVersionResource{}, m.err
}

func (m *MockRESTMapper) ResourcesFor(input schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	return nil, m.err
}

func (m *MockRESTMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &meta.RESTMapping{
		Scope: &MockRESTScope{
			name: m.scopeName,
		},
	}, nil
}

func (m *MockRESTMapper) RESTMappings(gk schema.GroupKind, versions ...string) ([]*meta.RESTMapping, error) {
	return nil, m.err
}

func (m *MockRESTMapper) ResourceSingularizer(resource string) (singular string, err error) {
	return "", m.err
}

func TestIsNamespaced_WithMockMapper(t *testing.T) {
	tests := []struct {
		name      string
		scopeName meta.RESTScopeName
		mockErr   error
		gvk       schema.GroupVersionKind
		want      bool
		wantErr   bool
	}{
		{
			name:      "namespaced resource",
			scopeName: meta.RESTScopeNameNamespace,
			mockErr:   nil,
			gvk: schema.GroupVersionKind{
				Group:   "apps",
				Version: "v1",
				Kind:    "Deployment",
			},
			want:    true,
			wantErr: false,
		},
		{
			name:      "cluster-scoped resource",
			scopeName: meta.RESTScopeNameRoot,
			mockErr:   nil,
			gvk: schema.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "Namespace",
			},
			want:    false,
			wantErr: false,
		},
		{
			name:      "mapper returns error",
			scopeName: meta.RESTScopeNameNamespace,
			mockErr:   &meta.NoKindMatchError{},
			gvk: schema.GroupVersionKind{
				Group:   "unknown",
				Version: "v1",
				Kind:    "Unknown",
			},
			want:    false,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockMapper := &MockRESTMapper{
				scopeName: tt.scopeName,
				err:       tt.mockErr,
			}

			got, err := IsNamespaced(mockMapper, tt.gvk)
			if (err != nil) != tt.wantErr {
				t.Errorf("IsNamespaced() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("IsNamespaced() = %v, want %v", got, tt.want)
			}
		})
	}
}

type MockResettableMapper struct {
	*MockRESTMapper
	resetCalled bool
	failFirst   bool
}

func (m *MockResettableMapper) Reset() {
	m.resetCalled = true
	m.err = nil
}

func (m *MockResettableMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	if m.failFirst && !m.resetCalled {
		return nil, fmt.Errorf("no matches for kind")
	}
	return m.MockRESTMapper.RESTMapping(gk, versions...)
}

func TestIsNamespaced_WithCacheReset(t *testing.T) {
	gvk := schema.GroupVersionKind{
		Group:   "example.com",
		Version: "v1",
		Kind:    "Example",
	}

	mock := &MockResettableMapper{
		MockRESTMapper: &MockRESTMapper{
			scopeName: meta.RESTScopeNameNamespace,
		},
		failFirst: true,
	}

	got, err := IsNamespaced(mock, gvk)
	if err != nil {
		t.Errorf("IsNamespaced() unexpected error after reset = %v", err)
	}
	if got != true {
		t.Errorf("IsNamespaced() = %v, want %v", got, true)
	}
	if !mock.resetCalled {
		t.Error("IsNamespaced() did not call Reset() on mapper")
	}
}

func TestIsNamespaced_EdgeCases(t *testing.T) {
	t.Run("empty GVK", func(t *testing.T) {
		mockMapper := &MockRESTMapper{
			scopeName: meta.RESTScopeNameNamespace,
			err:       nil,
		}

		gvk := schema.GroupVersionKind{}
		got, err := IsNamespaced(mockMapper, gvk)
		if err != nil {
			t.Errorf("IsNamespaced() unexpected error = %v", err)
		}
		if got != true {
			t.Errorf("IsNamespaced() = %v, want %v", got, true)
		}
	})

	t.Run("core group resources", func(t *testing.T) {
		testCases := []struct {
			kind      string
			scopeName meta.RESTScopeName
			want      bool
		}{
			{"Pod", meta.RESTScopeNameNamespace, true},
			{"Service", meta.RESTScopeNameNamespace, true},
			{"Namespace", meta.RESTScopeNameRoot, false},
			{"Node", meta.RESTScopeNameRoot, false},
			{"PersistentVolume", meta.RESTScopeNameRoot, false},
		}

		for _, tc := range testCases {
			t.Run(tc.kind, func(t *testing.T) {
				mockMapper := &MockRESTMapper{
					scopeName: tc.scopeName,
					err:       nil,
				}

				gvk := schema.GroupVersionKind{
					Group:   "",
					Version: "v1",
					Kind:    tc.kind,
				}

				got, err := IsNamespaced(mockMapper, gvk)
				if err != nil {
					t.Errorf("IsNamespaced() unexpected error = %v", err)
				}
				if got != tc.want {
					t.Errorf("IsNamespaced() for %s = %v, want %v", tc.kind, got, tc.want)
				}
			})
		}
	})

	t.Run("custom resources", func(t *testing.T) {
		mockMapper := &MockRESTMapper{
			scopeName: meta.RESTScopeNameNamespace,
			err:       nil,
		}

		gvk := schema.GroupVersionKind{
			Group:   "krateo.io",
			Version: "v1alpha1",
			Kind:    "CompositionDefinition",
		}

		got, err := IsNamespaced(mockMapper, gvk)
		if err != nil {
			t.Errorf("IsNamespaced() unexpected error = %v", err)
		}
		if got != true {
			t.Errorf("IsNamespaced() for custom resource = %v, want %v", got, true)
		}
	})
}

// Benchmark tests
func BenchmarkIsNamespaced(b *testing.B) {
	mockMapper := &MockRESTMapper{
		scopeName: meta.RESTScopeNameNamespace,
		err:       nil,
	}

	gvk := schema.GroupVersionKind{
		Group:   "apps",
		Version: "v1",
		Kind:    "Deployment",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = IsNamespaced(mockMapper, gvk)
	}
}

func BenchmarkIsNamespaced_NilMapper(b *testing.B) {
	gvk := schema.GroupVersionKind{
		Group:   "apps",
		Version: "v1",
		Kind:    "Deployment",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = IsNamespaced(nil, gvk)
	}
}
