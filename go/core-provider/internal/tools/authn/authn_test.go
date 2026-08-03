package authn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fakeJWT builds a 3-part token whose payload carries the given exp (the signature is irrelevant —
// the client only reads exp without verifying).
func fakeJWT(exp time.Time) string {
	payload, _ := json.Marshal(struct {
		Exp int64 `json:"exp"`
	}{Exp: exp.Unix()})
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"HS256"}`)) + "." + enc(payload) + ".sig"
}

func TestToken_ExchangesAndCachesUntilExpiry(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte("SA-TOKEN"), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	jwt := fakeJWT(now.Add(10 * time.Minute))

	var exchanges int32
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&exchanges, 1)
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]string{"accessToken": jwt})
	}))
	defer srv.Close()

	c := New(srv.URL, tokenPath)
	c.now = func() time.Time { return now }

	tok, err := c.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != jwt {
		t.Errorf("token = %q; want the issued JWT", tok)
	}
	if gotAuth != "Bearer SA-TOKEN" {
		t.Errorf("authn called with Authorization %q; want the projected SA token", gotAuth)
	}
	if gotPath != "/serviceaccount/login" {
		t.Errorf("authn path = %q; want /serviceaccount/login", gotPath)
	}

	// Second call well within expiry → served from cache, no new exchange.
	if _, err := c.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&exchanges); n != 1 {
		t.Errorf("exchanges = %d; want 1 (cached)", n)
	}

	// Advance past expiry minus skew → re-exchange.
	c.now = func() time.Time { return now.Add(10 * time.Minute) }
	if _, err := c.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&exchanges); n != 2 {
		t.Errorf("exchanges = %d; want 2 (re-exchanged near expiry)", n)
	}
}

func TestToken_PropagatesAuthnError(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	_ = os.WriteFile(tokenPath, []byte("SA"), 0o600)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, "serviceaccount not on allowlist")
	}))
	defer srv.Close()

	if _, err := New(srv.URL, tokenPath).Token(context.Background()); err == nil {
		t.Fatal("a non-200 from authn should error")
	}
}

func TestToken_MissingTokenFileErrors(t *testing.T) {
	if _, err := New("http://unused", "/no/such/token").Token(context.Background()); err == nil {
		t.Error("a missing projected token file should error")
	}
}
