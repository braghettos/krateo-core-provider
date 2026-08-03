package processor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/krateo-platformops/plumbing/helm"
	"github.com/stretchr/testify/assert"
)

func TestDecodeRelease(t *testing.T) {
	tests := []struct {
		name          string
		manifest      string
		expectedCount int
		expectError   bool
	}{
		{
			name: "Valid Single Document",
			manifest: `
apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
data:
  key: value
`,
			expectedCount: 1,
			expectError:   false,
		},
		{
			name: "Multiple Documents with Separators",
			manifest: `
apiVersion: v1
kind: Service
metadata:
  name: svc-1
---
apiVersion: v1
kind: Service
metadata:
  name: svc-2
`,
			expectedCount: 2,
			expectError:   false,
		},
		{
			name:          "Empty Manifest",
			manifest:      "",
			expectedCount: 0,
			expectError:   false,
		},
		{
			name: "Whitespace and Comments Only",
			manifest: `
# This is a comment
   
---
# Another empty doc
`,
			expectedCount: 0,
			expectError:   false,
		},
		{
			name: "Malformed YAML Document",
			manifest: `
apiVersion: v1
kind: Pod
metadata:
  name: [unclosed-bracket
`,
			expectedCount: 0,
			expectError:   true,
		},
		{
			name: "Document with Leading/Trailing Separators",
			manifest: `
---
apiVersion: v1
kind: Namespace
metadata:
  name: test
---
`,
			expectedCount: 1,
			expectError:   false,
		},
		{
			name:          "Large Manifest (Stream Stress)",
			manifest:      strings.Repeat("---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm\n", 100),
			expectedCount: 100,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rel := &helm.Release{
				Manifest: tt.manifest,
			}

			// We test with MinimalMetadata to ensure partial unmarshaling works
			objs, hash, err := DecodeRelease[MinimalMetadata](rel)

			if (err != nil) != tt.expectError {
				t.Fatalf("expected error: %v, got: %v", tt.expectError, err)
			}

			if err == nil {
				if len(objs) != tt.expectedCount {
					t.Errorf("expected %d objects, got %d", tt.expectedCount, len(objs))
				}

				if tt.manifest != "" && hash == "" {
					t.Error("expected a hash for non-empty manifest, got empty string")
				}
			}
		})
	}
}

func TestHashConsistency(t *testing.T) {
	manifest := "apiVersion: v1\nkind: Pod\nmetadata:\n  name: foo"
	rel := &helm.Release{Manifest: manifest}

	_, hash1, _ := DecodeRelease[MinimalMetadata](rel)
	_, hash2, _ := DecodeRelease[MinimalMetadata](rel)

	if hash1 != hash2 {
		t.Errorf("hashes are not consistent: %s vs %s", hash1, hash2)
	}
}

func TestDigestConsistency(t *testing.T) {
	// 1. Create a realistic Helm Release manifest with multiple documents
	manifest := `
apiVersion: v1
kind: Service
metadata:
  name: test-service
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-deployment
spec:
  replicas: 3
`
	rel := &helm.Release{
		Manifest: manifest,
	}

	// 2. Run the old heavy function
	// We use mockMetadata to satisfy the generic constraints
	_, digestFull, err := DecodeRelease[MinimalMetadata](rel)
	assert.NoError(t, err)

	// 3. Run the new light function
	digestFast, err := ComputeReleaseDigest(rel)
	assert.NoError(t, err)

	// 4. Verify they are identical
	assert.NotEmpty(t, digestFull, "Digest should not be empty")
	assert.Equal(t, digestFull, digestFast, "Optimized digest MUST match the decoded digest exactly")
}

func TestDigestConsistency_Empty(t *testing.T) {
	// Test handling of whitespace-only strings
	rel := &helm.Release{Manifest: "   \n  "}

	_, digestFull, err := DecodeRelease[MinimalMetadata](rel)
	assert.NoError(t, err)

	digestFast, err := ComputeReleaseDigest(rel)
	assert.NoError(t, err)

	fmt.Println("Digest for empty manifest (full):", digestFull)
	fmt.Println("Digest for empty manifest (fast):", digestFast)

	assert.Equal(t, "", digestFull)
	assert.Equal(t, "", digestFast)
}
