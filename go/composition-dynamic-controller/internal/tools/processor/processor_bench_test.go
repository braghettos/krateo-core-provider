package processor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/krateo-platformops/plumbing/helm"
)

// --- Benchmarks ---

// Helper to generate consistent large manifests for benchmarking
func generateLargeManifest(kbSize int) string {
	sb := strings.Builder{}
	chunk := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: chunk-%d
data:
  payload: "%s"
---
`
	// 1KB of payload data per object
	payload := strings.Repeat("x", 1024)

	// Build the manifest until we reach the desired size roughly
	for i := 0; i < kbSize; i++ {
		sb.WriteString(fmt.Sprintf(chunk, i, payload))
	}
	return sb.String()
}

func BenchmarkReleaseProcessing(b *testing.B) {
	// We test 3 different scales to see how the performance gap widens
	sizes := []struct {
		name string
		kb   int
	}{
		{"Small_10KB", 10},    // Typical chart
		{"Medium_500KB", 500}, // Large chart
		{"Large_5MB", 5000},   // Huge chart / CRDs
	}

	for _, tc := range sizes {
		manifest := generateLargeManifest(tc.kb)
		rel := &helm.Release{Manifest: manifest}

		// Benchmark 1: The expensive "Decode" way (simulating discard)
		b.Run("DecodeRelease_"+tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(manifest)))

			for i := 0; i < b.N; i++ {
				_, _, err := DecodeRelease[MinimalMetadata](rel)
				if err != nil {
					b.Fatal(err)
				}
			}
		})

		// Benchmark 2: The optimized "Hash Only" way
		b.Run("ComputeDigest_"+tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(manifest)))

			for i := 0; i < b.N; i++ {
				digest, err := ComputeReleaseDigest(rel)
				if err != nil {
					b.Fatal(err)
				}
				if digest == "" {
					b.Fatal("digest shouldn't be empty")
				}
			}
		})
	}
}
