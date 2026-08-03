package dynamic

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
)

// A RESTMapper without a Reset() method must make WatchCRDsAndInvalidate a no-op that
// returns nil before it ever touches the rest.Config / builds a client.
func TestWatchCRDsAndInvalidate_NoResetIsNoop(t *testing.T) {
	staticMapper := meta.NewDefaultRESTMapper(nil) // has no Reset() method
	if _, ok := interface{}(staticMapper).(interface{ Reset() }); ok {
		t.Fatal("precondition: DefaultRESTMapper unexpectedly implements Reset()")
	}

	// nil rest.Config is fine: the no-op branch returns before using it.
	if err := WatchCRDsAndInvalidate(context.Background(), nil, staticMapper, nil); err != nil {
		t.Fatalf("WatchCRDsAndInvalidate() with a non-resettable mapper = %v, want nil", err)
	}
}
