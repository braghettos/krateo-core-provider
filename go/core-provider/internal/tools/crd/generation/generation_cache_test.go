package generation

import (
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func cacheTestSchema() []byte {
	return []byte(`{"type":"object","properties":{"spec":{"type":"object","properties":{"foo":{"type":"string"}}}}}`)
}

// GenerateCRD shells out to the Go toolchain (crdgen.Generate). The cold call runs it; a second
// call with the same (schema, gvk) must be served from crdGenCache instead of re-running the
// toolchain — otherwise Observe pays a compilation on every reconcile.
func TestGenerateCRD_CacheAvoidsToolchainReinvocation(t *testing.T) {
	spec := cacheTestSchema()
	gvk := schema.GroupVersionKind{Group: "cache.example.org", Version: "v1", Kind: "Cachewidget"}

	t0 := time.Now()
	crd1, err := GenerateCRD(spec, gvk)
	if err != nil {
		t.Fatalf("first GenerateCRD: %v", err)
	}
	coldDur := time.Since(t0)

	t1 := time.Now()
	crd2, err := GenerateCRD(spec, gvk)
	if err != nil {
		t.Fatalf("second GenerateCRD: %v", err)
	}
	warmDur := time.Since(t1)

	if crd1.Name != crd2.Name {
		t.Fatalf("cached CRD differs from freshly generated: %q vs %q", crd1.Name, crd2.Name)
	}
	// The cold call runs the toolchain (hundreds of ms at minimum); a cache hit is orders of
	// magnitude faster. Guard the fast-machine case with an absolute floor.
	if warmDur > coldDur/4 && warmDur > 50*time.Millisecond {
		t.Fatalf("second GenerateCRD was not served from cache: cold=%s warm=%s", coldDur, warmDur)
	}
}

// Reproduces the cold-start storm that wedged installer-release: many workers generate CRDs
// concurrently. crdgen.Generate execs `go mod tidy` + `go run controller-gen`; run in parallel
// those thrash the shared build/module cache into a livelock. The serialize-the-toolchain fix must
// let every worker finish (no deadlock) and stay race-free (go test -race).
func TestGenerateCRD_ConcurrentNoDeadlock(t *testing.T) {
	spec := cacheTestSchema()
	gvks := []schema.GroupVersionKind{
		{Group: "conc.example.org", Version: "v1", Kind: "Aaa"},
		{Group: "conc.example.org", Version: "v1", Kind: "Bbb"},
		{Group: "conc.example.org", Version: "v1", Kind: "Ccc"},
	}

	const workers = 12
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if _, err := GenerateCRD(spec, gvks[idx%len(gvks)]); err != nil {
				errCh <- err
			}
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(180 * time.Second):
		t.Fatal("concurrent GenerateCRD deadlocked (did not finish in 180s)")
	}

	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent GenerateCRD error: %v", err)
		}
	}
}
