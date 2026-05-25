package github

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// AppAuthenticator mints GitHub-App installation access tokens for a
// configured App, caching them per installation_id so a hot Ship Hub
// reconciler doesn't burn an API call per request to /access_tokens.
//
// Token lifecycle:
//   - The App JWT is signed with the App's RSA private key, has a 10-min
//     TTL, and is generated on demand each time we exchange it for an
//     installation token.
//   - Installation access tokens have a ~1-hour TTL. We refresh when
//     within refreshSkew (default 5 min) of expiry so a request that
//     starts close to the deadline isn't burnt mid-flight.
//   - A 401 from a stale token (clock skew, manual revoke) flushes the
//     entry so the next call re-mints. Use OnUnauthorized for that path.
//
// Zero-value AppAuthenticator is not usable; build one with NewAppAuth.
type AppAuthenticator struct {
	AppID       int64
	PrivateKey  *rsa.PrivateKey
	BaseURL     string       // optional; defaults to api.github.com (tests override)
	HTTPClient  *http.Client // optional; defaults to 10-second timeout
	RefreshSkew time.Duration

	mu     sync.Mutex
	cache  map[int64]*cachedInstallationToken
	nowFn  func() time.Time // injectable for tests; defaults to time.Now
	jwtTTL time.Duration    // injectable for tests; defaults to 10 min
}

type cachedInstallationToken struct {
	Token     string
	ExpiresAt time.Time
}

// NewAppAuth constructs an AppAuthenticator from the App ID and a PEM-
// encoded RSA private key. Returns an error when the PEM is unparseable —
// catching this at boot is much friendlier than at first request time.
func NewAppAuth(appID int64, privateKeyPEM []byte) (*AppAuthenticator, error) {
	if appID <= 0 {
		return nil, errors.New("github app auth: app_id must be > 0")
	}
	if len(privateKeyPEM) == 0 {
		return nil, errors.New("github app auth: private key is empty")
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("github app auth: parse private key: %w", err)
	}
	return &AppAuthenticator{
		AppID:       appID,
		PrivateKey:  key,
		RefreshSkew: 5 * time.Minute,
		cache:       map[int64]*cachedInstallationToken{},
		nowFn:       time.Now,
		jwtTTL:      10 * time.Minute,
	}, nil
}

// InstallationToken returns a valid installation access token for the
// given installation_id, minting one (and caching it) when no fresh
// cached entry exists.
func (a *AppAuthenticator) InstallationToken(ctx context.Context, installationID int64) (string, error) {
	if installationID <= 0 {
		return "", errors.New("github app auth: installation_id must be > 0")
	}
	a.mu.Lock()
	cached, ok := a.cache[installationID]
	a.mu.Unlock()
	if ok && a.now().Add(a.RefreshSkew).Before(cached.ExpiresAt) {
		return cached.Token, nil
	}
	tok, expiresAt, err := a.mintInstallationToken(ctx, installationID)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.cache[installationID] = &cachedInstallationToken{Token: tok, ExpiresAt: expiresAt}
	a.mu.Unlock()
	return tok, nil
}

// OnUnauthorized drops the cached entry for installationID so the next
// InstallationToken call re-mints. The Client doesn't call this directly
// (it doesn't know about installations), but a wrapper TokenSource can.
func (a *AppAuthenticator) OnUnauthorized(installationID int64) {
	a.mu.Lock()
	delete(a.cache, installationID)
	a.mu.Unlock()
}

// NewClientForInstallation builds a *Client whose TokenSource auto-mints
// (and caches) installation tokens for the given installation_id. Wrap a
// long-lived AppAuthenticator with as many per-installation Clients as
// you need — they all share the cache.
func (a *AppAuthenticator) NewClientForInstallation(installationID int64) *Client {
	return NewClientWithTokenSource(&installationTokenSource{auth: a, installationID: installationID})
}

// installationTokenSource is the TokenSource glue that bridges the
// AppAuthenticator cache into the per-request Authorization header path
// in github.go's doWithBody.
type installationTokenSource struct {
	auth           *AppAuthenticator
	installationID int64
}

func (s *installationTokenSource) Token(ctx context.Context) (string, error) {
	return s.auth.InstallationToken(ctx, s.installationID)
}

// signAppJWT mints a short-lived JWT signed with the App's RSA private
// key. iat is back-dated 30 s to tolerate small clock skew between us
// and GitHub.
func (a *AppAuthenticator) signAppJWT() (string, error) {
	now := a.now()
	claims := jwt.MapClaims{
		"iat": now.Add(-30 * time.Second).Unix(),
		"exp": now.Add(a.jwtTTL).Unix(),
		"iss": a.AppID,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return tok.SignedString(a.PrivateKey)
}

// mintInstallationToken exchanges an App JWT for an installation access
// token via POST /app/installations/{id}/access_tokens.
func (a *AppAuthenticator) mintInstallationToken(ctx context.Context, installationID int64) (string, time.Time, error) {
	jwtTok, err := a.signAppJWT()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("github app auth: sign JWT: %w", err)
	}
	base := a.BaseURL
	if base == "" {
		base = apiBase
	}
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", strings.TrimRight(base, "/"), installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+jwtTok)

	client := a.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return "", time.Time{}, fmt.Errorf("github app auth: unauthorized (status 401) — App ID / private key mismatch?")
	}
	if resp.StatusCode == http.StatusNotFound {
		return "", time.Time{}, fmt.Errorf("github app auth: installation %d not found (status 404)", installationID)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", time.Time{}, fmt.Errorf("github app auth: access_tokens: status %d", resp.StatusCode)
	}
	var payload struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", time.Time{}, fmt.Errorf("github app auth: decode access_tokens response: %w", err)
	}
	if payload.Token == "" || payload.ExpiresAt.IsZero() {
		return "", time.Time{}, errors.New("github app auth: access_tokens response missing token or expires_at")
	}
	return payload.Token, payload.ExpiresAt, nil
}

func (a *AppAuthenticator) now() time.Time {
	if a.nowFn != nil {
		return a.nowFn()
	}
	return time.Now()
}
