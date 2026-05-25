package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// helperPEM generates a fresh RSA key pair and returns its PKCS#1 PEM
// encoding. RSA keygen with 1024 bits is fine for tests — we're not
// guarding production secrets, just exercising the parser + signer.
func helperPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

// fakeAppServer returns an httptest server that emulates GitHub's
// /app/installations/{id}/access_tokens endpoint. callsOut is incremented
// on each successful mint; force401 causes a one-shot 401 (stale token
// simulation).
func fakeAppServer(t *testing.T, calls *int32, force401 *atomic.Bool, expiresIn time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/app/installations/") || !strings.HasSuffix(r.URL.Path, "/access_tokens") {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("missing/malformed Authorization header: %q", auth)
		}
		// Validate the JWT looks well-formed (3 segments). We don't
		// re-validate the signature here — that's golang-jwt's job.
		if parts := strings.Split(strings.TrimPrefix(auth, "Bearer "), "."); len(parts) != 3 {
			t.Errorf("Authorization JWT did not have 3 segments")
		}
		if force401 != nil && force401.Swap(false) {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		if calls != nil {
			atomic.AddInt32(calls, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		// GitHub returns ISO-8601 with Z timezone; mimic that.
		exp := time.Now().Add(expiresIn).UTC().Format(time.RFC3339)
		fmt.Fprintf(w, `{"token":"ghs_minted_%d","expires_at":%q}`, atomic.LoadInt32(calls), exp)
	}))
}

func TestNewAppAuth_RejectsBadInput(t *testing.T) {
	if _, err := NewAppAuth(0, []byte("anything")); err == nil {
		t.Errorf("appID=0 should error")
	}
	if _, err := NewAppAuth(123, nil); err == nil {
		t.Errorf("empty key should error")
	}
	if _, err := NewAppAuth(123, []byte("not a real pem")); err == nil {
		t.Errorf("unparseable key should error")
	}
}

func TestAppAuth_ColdMint(t *testing.T) {
	var calls int32
	srv := fakeAppServer(t, &calls, nil, time.Hour)
	defer srv.Close()

	auth, err := NewAppAuth(42, helperPEM(t))
	if err != nil {
		t.Fatalf("NewAppAuth: %v", err)
	}
	auth.BaseURL = srv.URL

	tok, err := auth.InstallationToken(context.Background(), 1000)
	if err != nil {
		t.Fatalf("InstallationToken: %v", err)
	}
	if !strings.HasPrefix(tok, "ghs_minted_") {
		t.Errorf("token shape: got %q", tok)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("mint calls: got %d, want 1", got)
	}
}

func TestAppAuth_CacheHit(t *testing.T) {
	var calls int32
	srv := fakeAppServer(t, &calls, nil, time.Hour)
	defer srv.Close()

	auth, err := NewAppAuth(42, helperPEM(t))
	if err != nil {
		t.Fatalf("NewAppAuth: %v", err)
	}
	auth.BaseURL = srv.URL

	tok1, _ := auth.InstallationToken(context.Background(), 1000)
	tok2, _ := auth.InstallationToken(context.Background(), 1000)
	tok3, _ := auth.InstallationToken(context.Background(), 1000)
	if tok1 != tok2 || tok2 != tok3 {
		t.Errorf("cache hit should reuse token; got %q / %q / %q", tok1, tok2, tok3)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected 1 mint call across 3 reads; got %d", got)
	}

	// Different installation_id → separate cache entry → separate mint.
	if _, err := auth.InstallationToken(context.Background(), 2000); err != nil {
		t.Fatalf("InstallationToken(2000): %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 mint calls after second installation; got %d", got)
	}
}

func TestAppAuth_NearExpiryRefresh(t *testing.T) {
	var calls int32
	// Token "expires" 4 minutes from issue time; refresh skew is 5 min,
	// so the very next call after the first mint hits the skew window
	// and re-mints.
	srv := fakeAppServer(t, &calls, nil, 4*time.Minute)
	defer srv.Close()

	auth, err := NewAppAuth(42, helperPEM(t))
	if err != nil {
		t.Fatalf("NewAppAuth: %v", err)
	}
	auth.BaseURL = srv.URL
	auth.RefreshSkew = 5 * time.Minute // tokens with <5 min left re-mint

	if _, err := auth.InstallationToken(context.Background(), 1000); err != nil {
		t.Fatalf("first InstallationToken: %v", err)
	}
	if _, err := auth.InstallationToken(context.Background(), 1000); err != nil {
		t.Fatalf("second InstallationToken: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 mint calls (refresh-before-expiry); got %d", got)
	}
}

func TestAppAuth_OnUnauthorizedReMints(t *testing.T) {
	var calls int32
	srv := fakeAppServer(t, &calls, nil, time.Hour)
	defer srv.Close()

	auth, err := NewAppAuth(42, helperPEM(t))
	if err != nil {
		t.Fatalf("NewAppAuth: %v", err)
	}
	auth.BaseURL = srv.URL

	tok1, _ := auth.InstallationToken(context.Background(), 1000)
	auth.OnUnauthorized(1000)
	tok2, _ := auth.InstallationToken(context.Background(), 1000)
	if tok1 == tok2 {
		t.Errorf("OnUnauthorized should force re-mint; got same token twice")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 mint calls; got %d", got)
	}
}

func TestAppAuth_404SurfacesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	auth, err := NewAppAuth(42, helperPEM(t))
	if err != nil {
		t.Fatalf("NewAppAuth: %v", err)
	}
	auth.BaseURL = srv.URL
	if _, err := auth.InstallationToken(context.Background(), 1000); err == nil {
		t.Errorf("expected error on 404")
	} else if !strings.Contains(err.Error(), "installation 1000") {
		t.Errorf("error should mention installation id; got %v", err)
	}
}

func TestAppAuth_JWTClaimsLookSane(t *testing.T) {
	// White-box: sign a JWT and parse it back to make sure iat is back-
	// dated and iss matches AppID.
	key, _ := rsa.GenerateKey(rand.Reader, 1024)
	auth := &AppAuthenticator{
		AppID:      99,
		PrivateKey: key,
		nowFn:      func() time.Time { return time.Unix(1_700_000_000, 0) },
		jwtTTL:     10 * time.Minute,
	}
	signed, err := auth.signAppJWT()
	if err != nil {
		t.Fatalf("signAppJWT: %v", err)
	}
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	parsed, err := parser.Parse(signed, func(t *jwt.Token) (any, error) { return &key.PublicKey, nil })
	if err != nil {
		t.Fatalf("parse signed JWT: %v", err)
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("claims: not MapClaims")
	}
	if iss, _ := claims["iss"].(float64); int64(iss) != 99 {
		t.Errorf("iss: got %v, want 99", claims["iss"])
	}
	if iat, _ := claims["iat"].(float64); int64(iat) != 1_700_000_000-30 {
		t.Errorf("iat: got %v, want %d", claims["iat"], 1_700_000_000-30)
	}
	if exp, _ := claims["exp"].(float64); int64(exp) != 1_700_000_000+600 {
		t.Errorf("exp: got %v, want %d", claims["exp"], 1_700_000_000+600)
	}
}

// TestAppAuth_ClientUsesMintedToken verifies the end-to-end glue: a
// Client built from NewClientForInstallation hits a backend with the
// installation token in the Authorization header.
func TestAppAuth_ClientUsesMintedToken(t *testing.T) {
	var calls int32
	mintSrv := fakeAppServer(t, &calls, nil, time.Hour)
	defer mintSrv.Close()

	// API server: assert the request carried the minted token, NOT the JWT.
	var seenAuth string
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("[]"))
	}))
	defer apiSrv.Close()

	auth, err := NewAppAuth(42, helperPEM(t))
	if err != nil {
		t.Fatalf("NewAppAuth: %v", err)
	}
	auth.BaseURL = mintSrv.URL

	client := auth.NewClientForInstallation(7777)
	client.BaseURL = apiSrv.URL

	if _, err := client.ListPullRequests(context.Background(), "owner", "repo", ListOptions{}); err != nil {
		t.Fatalf("ListPullRequests: %v", err)
	}
	if !strings.HasPrefix(seenAuth, "Bearer ghs_minted_") {
		t.Errorf("API call did not use minted installation token: %q", seenAuth)
	}
}
