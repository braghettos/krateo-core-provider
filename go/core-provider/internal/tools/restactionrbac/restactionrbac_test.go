package restactionrbac

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInspect_OKSendsBearerAndParamsDecodesReadSet(t *testing.T) {
	var gotAuth, gotName, gotNs, gotExtras string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotName = r.URL.Query().Get("apiRefName")
		gotNs = r.URL.Query().Get("apiRefNamespace")
		gotExtras = r.URL.Query().Get("extras")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"restaction": map[string]string{"name": "ra", "namespace": "krateo-system"},
			"readSet": []map[string]string{
				{"group": "", "version": "v1", "resource": "services", "namespace": "apps", "verb": "list"},
			},
		})
	}))
	defer srv.Close()

	cli := New(srv.URL).WithToken(func(context.Context) (string, error) { return "JWT123", nil })
	got, err := cli.Inspect(context.Background(), Params{
		APIRefName:      "ra",
		APIRefNamespace: "krateo-system",
		Extras:          map[string]any{"region": "eu-west"},
	})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if gotAuth != "Bearer JWT123" {
		t.Errorf("Authorization = %q; want %q", gotAuth, "Bearer JWT123")
	}
	if gotName != "ra" || gotNs != "krateo-system" {
		t.Errorf("query apiRef = %q/%q; want ra/krateo-system", gotName, gotNs)
	}
	if gotExtras == "" {
		t.Errorf("extras query param not sent")
	}
	if len(got) != 1 || got[0].Resource != "services" || got[0].Verb != "list" {
		t.Errorf("readSet = %+v; want one services/list row", got)
	}
}

func TestInspect_NoTokenSendsNoAuthHeader(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"readSet":[]}`))
	}))
	defer srv.Close()

	if _, err := New(srv.URL).Inspect(context.Background(), Params{APIRefName: "ra", APIRefNamespace: "ns"}); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if hadAuth {
		t.Errorf("Authorization header sent without a TokenFunc")
	}
}

func TestInspect_422IsErrIncomplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`stage "scaled" cannot be enumerated without dispatching`))
	}))
	defer srv.Close()

	_, err := New(srv.URL).Inspect(context.Background(), Params{APIRefName: "ra", APIRefNamespace: "ns"})
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("422 should map to ErrIncomplete, got %v", err)
	}
	// the stage detail must be preserved for the caller's condition message
	if got := err.Error(); got == "" || !contains(got, "scaled") {
		t.Errorf("error should name the unresolvable stage, got %q", got)
	}
}

func TestInspect_401IsPlainError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`missing authorization header`))
	}))
	defer srv.Close()

	_, err := New(srv.URL).Inspect(context.Background(), Params{APIRefName: "ra", APIRefNamespace: "ns"})
	if err == nil {
		t.Fatal("401 should be an error")
	}
	if errors.Is(err, ErrIncomplete) {
		t.Errorf("401 must NOT be ErrIncomplete (only 422 is)")
	}
}

func TestInspect_RequiresApiRefName(t *testing.T) {
	if _, err := New("http://unused").Inspect(context.Background(), Params{APIRefNamespace: "ns"}); err == nil {
		t.Error("missing apiRefName should error before any HTTP call")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
