package authn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// makeJWT builds an unsigned-looking three-part token whose payload carries the given exp.
func makeJWT(exp int64) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{"sub": "cdc", "exp": exp})
	body := base64.RawURLEncoding.EncodeToString(payload)
	return hdr + "." + body + ".sig"
}

func writeToken(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestToken_ExchangeAndCache(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	exp := now.Add(time.Hour).Unix()

	var calls int32
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/serviceaccount/login" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": makeJWT(exp)})
	}))
	defer srv.Close()

	c := New(srv.URL, writeToken(t, "sa-token-xyz"))
	c.now = func() time.Time { return now }

	tok, err := c.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
	if gotAuth != "Bearer sa-token-xyz" {
		t.Errorf("auth = %q", gotAuth)
	}

	// second call within validity must be served from cache (no extra HTTP call)
	if _, err := c.Token(context.Background()); err != nil {
		t.Fatalf("Token (cached): %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("expected 1 exchange, got %d", n)
	}

	// advance past expiry - skew: must re-exchange
	c.now = func() time.Time { return now.Add(time.Hour) }
	if _, err := c.Token(context.Background()); err != nil {
		t.Fatalf("Token (refresh): %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("expected refresh, got %d exchanges", n)
	}
}

func TestToken_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no mapping for this service account", http.StatusForbidden)
	}))
	defer srv.Close()

	c := New(srv.URL, writeToken(t, "sa"))
	if _, err := c.Token(context.Background()); err == nil {
		t.Fatal("expected error on 403")
	}
}

func TestToken_MissingTokenFile(t *testing.T) {
	c := New("http://authn", filepath.Join(t.TempDir(), "does-not-exist"))
	if _, err := c.Token(context.Background()); err == nil {
		t.Fatal("expected error for missing token file")
	}
}

func TestToken_NoAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"username": "cdc"}})
	}))
	defer srv.Close()
	c := New(srv.URL, writeToken(t, "sa"))
	if _, err := c.Token(context.Background()); err == nil {
		t.Fatal("expected error when response carries no accessToken")
	}
}

func TestJWTExpiry(t *testing.T) {
	want := int64(1_700_000_000)
	if got := jwtExpiry(makeJWT(want)); got.Unix() != want {
		t.Errorf("jwtExpiry = %v, want unix %d", got, want)
	}
	if got := jwtExpiry("not-a-jwt"); !got.IsZero() {
		t.Errorf("malformed token should yield zero time, got %v", got)
	}
	if got := jwtExpiry("a.b.c"); !got.IsZero() {
		t.Errorf("undecodable payload should yield zero time, got %v", got)
	}
}

func TestToken_FallbackExpiryWhenUnparseable(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// opaque token without a parseable exp
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "opaque-token"})
	}))
	defer srv.Close()

	c := New(srv.URL, writeToken(t, "sa"))
	c.now = func() time.Time { return now }
	if _, err := c.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	// fallback window is 5m; cache should be valid just under it and expired past it
	if !c.expiry.After(now) {
		t.Errorf("fallback expiry %v not after now %v", c.expiry, now)
	}
}
