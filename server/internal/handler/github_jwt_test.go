package handler

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// signGitHubAppJWT and fetchInstallationAccount were added by porting upstream
// MUL-3078 (PR #3811). These tests pin the behaviors that matter for the bug
// they were ported to fix:
//
//   * The setup-callback path stops writing "Connected to unknown" when the
//     operator HAS configured GITHUB_APP_ID + GITHUB_APP_PRIVATE_KEY.
//   * The same path stays usable when the operator has NOT configured them —
//     signGitHubAppJWT returns ("", nil) (a soft "App auth not available"),
//     fetchInstallationAccount falls through to the unauthenticated path and
//     the "unknown" placeholder, and the next webhook fixes the row.
//   * Misconfiguration (garbage in the PEM env var) surfaces as an error so
//     the operator notices instead of silently writing "unknown" forever.

// genTestRSAKey generates a fresh 2048-bit RSA key and returns its PEM
// encoding. 2048 bits is the minimum GitHub accepts for App keys and is also
// the cheapest length that produces a credible test signature; bumping to
// 4096 just slows the test down without changing what it verifies.
func genTestRSAKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return key, string(pemBytes)
}

func TestSignGitHubAppJWT_NoEnvReturnsEmpty(t *testing.T) {
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
	tok, err := signGitHubAppJWT(time.Now())
	if err != nil {
		t.Fatalf("unexpected error with no env: %v", err)
	}
	if tok != "" {
		t.Fatalf("expected empty token with no env, got %q", tok)
	}
}

func TestSignGitHubAppJWT_OnlyAppIDReturnsEmpty(t *testing.T) {
	// Half-configured = same as not configured. Operators occasionally set
	// only the App ID while wiring secrets, and a partial config must NOT
	// fall through to "use my unconfigured key" — that would either crash
	// or silently emit a bad token. Treat it like "App auth not available."
	t.Setenv("GITHUB_APP_ID", "12345")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
	tok, err := signGitHubAppJWT(time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "" {
		t.Fatalf("expected empty token with half-config, got %q", tok)
	}
}

func TestSignGitHubAppJWT_MalformedKeyReturnsError(t *testing.T) {
	t.Setenv("GITHUB_APP_ID", "12345")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "not-a-real-pem")
	tok, err := signGitHubAppJWT(time.Now())
	if err == nil {
		t.Fatalf("expected error with malformed key, got token %q", tok)
	}
	if !strings.Contains(err.Error(), "parse GITHUB_APP_PRIVATE_KEY") {
		t.Errorf("error should identify which env var was bad, got %q", err)
	}
}

func TestSignGitHubAppJWT_ClaimsAndSignature(t *testing.T) {
	// Sign with a known clock, verify with the same clock. Both sides must
	// share the time function — using real time.Now() on the parser would
	// flip the test to a failure once the token's exp (now + 9m) crossed
	// real wall clock. That's the clock-bomb upstream's review caught.
	key, pemKey := genTestRSAKey(t)
	const appID = "424242"
	t.Setenv("GITHUB_APP_ID", appID)
	t.Setenv("GITHUB_APP_PRIVATE_KEY", pemKey)

	fixedNow := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	tok, err := signGitHubAppJWT(fixedNow)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if tok == "" {
		t.Fatal("expected non-empty token with config + valid key")
	}

	parsed, err := jwt.Parse(tok, func(*jwt.Token) (any, error) {
		return &key.PublicKey, nil
	}, jwt.WithTimeFunc(func() time.Time { return fixedNow }))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !parsed.Valid {
		t.Fatal("parsed token is not valid")
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("unexpected claims type %T", parsed.Claims)
	}
	if got := claims["iss"]; got != appID {
		t.Errorf("iss: got %v, want %q", got, appID)
	}
	// iat is back-dated 60 s to absorb clock skew.
	if got, want := int64(claims["iat"].(float64)), fixedNow.Add(-60*time.Second).Unix(); got != want {
		t.Errorf("iat: got %d, want %d (now - 60s)", got, want)
	}
	// exp is capped at 9 minutes from now (one minute under GitHub's 10-min
	// hard ceiling so the same 60 s of skew still keeps us inside the cap).
	if got, want := int64(claims["exp"].(float64)), fixedNow.Add(9*time.Minute).Unix(); got != want {
		t.Errorf("exp: got %d, want %d (now + 9m)", got, want)
	}
}

func TestFetchInstallationAccount_AuthenticatedSuccess(t *testing.T) {
	// With JWT auth configured, the JSON body becomes parseable and we get
	// the real account login back. This is the post-fix-success scenario:
	// the "Connected to unknown" rendering disappears because the setup
	// callback persists the real login from the very first write.
	_, pemKey := genTestRSAKey(t)
	t.Setenv("GITHUB_APP_ID", "424242")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", pemKey)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("expected Bearer Authorization, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"account":{"login":"prellr","type":"User","avatar_url":"https://example.test/a.png"}}`))
	}))
	defer srv.Close()

	oldBase := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = oldBase }()

	login, accType, avatar := fetchInstallationAccount(t.Context(), 9999)
	if login != "prellr" {
		t.Errorf("login: got %q, want %q", login, "prellr")
	}
	if accType != "User" {
		t.Errorf("accountType: got %q, want %q", accType, "User")
	}
	if avatar == nil || *avatar != "https://example.test/a.png" {
		t.Errorf("avatar: got %v, want %q", avatar, "https://example.test/a.png")
	}
}

func TestFetchInstallationAccount_UnauthenticatedFallsBackToUnknown(t *testing.T) {
	// Without env vars: signGitHubAppJWT returns ("", nil), no Authorization
	// header is set, GitHub's /app/installations/{id} returns 401, and the
	// function falls back to the "unknown" placeholder. This is the path
	// that produced the original bug. After the fix, this still happens —
	// but the WEBHOOK now publishes the broadcast that lets the UI converge
	// once the real account info arrives via installation.created.
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("expected no Authorization header when env unset, got %q", got)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	oldBase := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = oldBase }()

	login, accType, avatar := fetchInstallationAccount(t.Context(), 9999)
	if login != "unknown" {
		t.Errorf("login: got %q, want %q", login, "unknown")
	}
	if accType != "User" {
		t.Errorf("accountType: got %q, want %q", accType, "User")
	}
	if avatar != nil {
		t.Errorf("avatar: got %v, want nil", avatar)
	}
}
