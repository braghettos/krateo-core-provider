package tracer

import (
	"context"
	"io"
	"net/http"
	"strings"

	xcontext "github.com/krateo-platformops/unstructured-runtime/pkg/context"
)

type Resource struct {
	Group     string `json:"group"`
	Version   string `json:"version"`
	Resource  string `json:"resource"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// Tracer implements http.RoundTripper.  It prints each request and
// response/error to t.OutFile.  WARNING: this may output sensitive information
// including bearer tokens.
type Tracer struct {
	http.RoundTripper
	resources []Resource
	verbose   bool
	context   context.Context
}

func NewTracer(ctx context.Context, verbose bool) *Tracer {
	return &Tracer{
		verbose: verbose,
		context: ctx,
	}
}

func (t *Tracer) WithRoundTripper(rt http.RoundTripper) *Tracer {
	t.RoundTripper = rt
	return t
}

func (t *Tracer) GetResources() []Resource {
	return t.resources
}

// RoundTrip implements the http.RoundTripper interface.
// It dumps each request different from GET and its response using the context logger provided.
func (t *Tracer) RoundTrip(req *http.Request) (resp *http.Response, err error) {

	reqBody := ""
	if t.verbose && req.Body != nil { // We need to read the body to log it because t.RoundTripper.RoundTrip may consume it.
		b, err := io.ReadAll(req.Body)
		if err == nil {
			reqBody = string(b)
			// Reconstruct the Body since it was read.
			req.Body = io.NopCloser(strings.NewReader(reqBody))
		}
	}

	// Call the nested RoundTripper.
	resp, err = t.RoundTripper.RoundTrip(req)
	// If an error was returned, dump it to t.OutFile.
	if err != nil {
		return resp, err
	}
	if req.Method != http.MethodGet && (req.Method == http.MethodDelete || req.URL.Query().Get("fieldManager") != "") {
		split := strings.Split(req.URL.Path, "/")

		if len(split) > 2 {
			if len(split) == 8 && (split[1] == "apis" || split[1] == "api") && split[4] == "namespaces" {
				t.resources = append(t.resources, Resource{
					Group:     split[2],
					Version:   split[3],
					Resource:  split[6],
					Namespace: split[5],
					Name:      split[7],
				})
			} else if len(split) == 7 && (split[1] == "apis" || split[1] == "api") && split[3] == "namespaces" {
				t.resources = append(t.resources, Resource{
					Group:     "",
					Version:   split[2],
					Resource:  split[5],
					Namespace: split[4],
					Name:      split[6],
				})
			} else if len(split) == 6 && (split[1] == "apis" || split[1] == "api") {
				t.resources = append(t.resources, Resource{
					Group:     split[2],
					Version:   split[3],
					Resource:  split[4],
					Namespace: "",
					Name:      split[5],
				})
			} else if len(split) == 5 && (split[1] == "apis" || split[1] == "api") {
				t.resources = append(t.resources, Resource{
					Group:     "",
					Version:   split[2],
					Resource:  split[3],
					Namespace: "",
					Name:      split[4],
				})
			}
		}

		if t.verbose && t.context != nil {
			log := xcontext.Logger(t.context).WithValues("op", "http-tracer")
			respBody := ""
			if resp.Body != nil {
				b, err := io.ReadAll(resp.Body)
				if err == nil {
					respBody = string(b)
					// Reconstruct the Body since it was read.
					resp.Body = io.NopCloser(strings.NewReader(respBody))
				}
			}

			log.WithValues("method", req.Method).
				WithValues("url", req.URL.String()).
				WithValues("content-type", req.Header.Get("Content-Type")).
				WithValues("content-length", req.Header.Get("Content-Length")).
				WithValues("accept-encoding", req.Header.Get("Accept")).
				WithValues("authorization", "redacted").
				WithValues("requestBody", reqBody).
				WithValues("responseStatus", resp.Status).
				WithValues("responseBody", respBody).
				Debug("HTTP Request traced")
		}
	}

	return resp, err
}
