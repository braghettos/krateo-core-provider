package tracer

import (
	"net/http"
	"strings"
	"sync"

	"github.com/krateo-platformops/chart-inspector/internal/handlers/resources"
)

// Tracer implements http.RoundTripper.  It prints each request and
// response/error to t.OutFile.  WARNING: this may output sensitive information
// including bearer tokens.
type Tracer struct {
	http.RoundTripper
	mu        sync.Mutex
	resources []resources.Resource
}

func (t *Tracer) GetResources() []resources.Resource {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Return a copy to prevent external modification
	resCopy := make([]resources.Resource, len(t.resources))
	copy(resCopy, t.resources)
	return resCopy
}

func (t *Tracer) WithRoundTripper(rt http.RoundTripper) *Tracer {
	t.RoundTripper = rt
	return t
}

// RoundTrip calls the nested RoundTripper while printing each request and
// response/error to t.OutFile on either side of the nested call.  WARNING: this
// may output sensitive information including bearer tokens.
func (t *Tracer) RoundTrip(req *http.Request) (*http.Response, error) {
	split := strings.Split(req.URL.Path, "/")

	// Capture resource metadata under mutex protection
	if len(split) > 2 {
		var resource *resources.Resource

		if len(split) == 8 && (split[1] == "apis" || split[1] == "api") && split[4] == "namespaces" {
			resource = &resources.Resource{
				Group:     split[2],
				Version:   split[3],
				Resource:  split[6],
				Namespace: split[5],
				Name:      split[7],
			}
		} else if len(split) == 7 && (split[1] == "apis" || split[1] == "api") && split[3] == "namespaces" {
			resource = &resources.Resource{
				Group:     "",
				Version:   split[2],
				Resource:  split[5],
				Namespace: split[4],
				Name:      split[6],
			}
		} else if len(split) == 6 && (split[1] == "apis" || split[1] == "api") {
			resource = &resources.Resource{
				Group:     split[2],
				Version:   split[3],
				Resource:  split[4],
				Namespace: "",
				Name:      split[5],
			}
		} else if len(split) == 5 && (split[1] == "apis" || split[1] == "api") {
			resource = &resources.Resource{
				Group:     "",
				Version:   split[2],
				Resource:  split[3],
				Namespace: "",
				Name:      split[4],
			}
		}

		if resource != nil {
			t.mu.Lock()
			t.resources = append(t.resources, *resource)
			t.mu.Unlock()
		}
	}

	// Call the nested RoundTripper.
	resp, err := t.RoundTripper.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	return resp, err
}
