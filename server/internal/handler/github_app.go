package handler

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	gh "github.com/multica-ai/multica/server/pkg/github"
)

// ── GitHub App credentials (env-driven) ─────────────────────────────────────

// githubAppID returns the numeric App ID configured via GITHUB_APP_ID.
// Zero when not configured (the legacy PAT path keeps working).
func githubAppID() int64 {
	raw := strings.TrimSpace(os.Getenv("GITHUB_APP_ID"))
	if raw == "" {
		return 0
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// githubAppPrivateKey returns the PEM-encoded RSA private key from the
// GITHUB_APP_PRIVATE_KEY env var. The env var carries the raw PEM
// (multi-line); operators paste it as-is into .env or set it via a
// Docker secret. Empty when not configured.
func githubAppPrivateKey() []byte {
	return []byte(strings.TrimSpace(os.Getenv("GITHUB_APP_PRIVATE_KEY")))
}

// ── App-authenticator singleton ─────────────────────────────────────────────

// The AppAuthenticator caches installation tokens per installation_id. A
// single instance must back every Ship Hub call path (handlers + the
// six pollers) so the cache actually hits — re-creating it per request
// would reduce the token mint rate to one-per-call. The init is lazy
// because the env vars may not be set in tests, and protected by once
// so concurrent first-request races don't double-mint a PEM parse.
var (
	appAuthOnce sync.Once
	appAuthInst *gh.AppAuthenticator
	appAuthErr  error
)

// GitHubAppAuth returns the process-wide App authenticator, or nil + nil
// when no App is configured (legitimate; callers fall back to PAT). A
// non-nil error means an App is configured but the private key won't
// parse — surfaced so the caller can log and fall back rather than
// hard-fail every request.
func GitHubAppAuth() (*gh.AppAuthenticator, error) {
	appAuthOnce.Do(func() {
		appID := githubAppID()
		key := githubAppPrivateKey()
		if appID == 0 || len(key) == 0 {
			return // not configured; ok
		}
		auth, err := gh.NewAppAuth(appID, key)
		if err != nil {
			appAuthErr = err
			slog.Error("github app auth: init failed; falling back to PAT for all workspaces",
				"error", err)
			return
		}
		appAuthInst = auth
		slog.Info("github app auth: initialized", "app_id", appID)
	})
	return appAuthInst, appAuthErr
}

// resetGitHubAppAuthForTest re-evaluates the env on demand. Test-only.
func resetGitHubAppAuthForTest() {
	appAuthOnce = sync.Once{}
	appAuthInst = nil
	appAuthErr = nil
}

// ── Per-workspace client selection ──────────────────────────────────────────

// shipHubGitHubClient returns the right GitHub client for a workspace:
//   - When the App is configured AND the workspace has a github_installation
//     row, returns a client whose token is a fresh installation access
//     token (own per-installation quota, independent of personal PATs).
//   - Otherwise falls back to gh.NewClient(personalToken). When personalToken
//     is also "" the returned client makes unauthenticated requests, which
//     is fine for public repos at the much lower anonymous rate limit.
//
// The (loader, queries) pair is needed to look up the github_installation
// row; both must be non-nil when the App is configured. When loader is
// nil we skip the App path entirely and fall back to the PAT.
func shipHubGitHubClient(ctx context.Context, queries *db.Queries, workspaceID pgtype.UUID, personalToken string) *gh.Client {
	auth, _ := GitHubAppAuth()
	if auth != nil && queries != nil {
		if installID, ok := lookupInstallationID(ctx, queries, workspaceID); ok {
			return auth.NewClientForInstallation(installID)
		}
	}
	return gh.NewClient(personalToken)
}

// ShipHubGitHubClientForBackground is the exported variant of
// shipHubGitHubClient used by cmd/server pollers that run outside the
// http request lifecycle. Returns hasAuth=true when EITHER a GitHub
// App installation exists OR personalToken is non-empty. Pollers use
// hasAuth=false as the "skip this workspace" signal.
func ShipHubGitHubClientForBackground(
	ctx context.Context,
	queries *db.Queries,
	workspaceID pgtype.UUID,
	personalToken string,
) (*gh.Client, bool) {
	auth, _ := GitHubAppAuth()
	if auth != nil && queries != nil {
		if installID, ok := lookupInstallationID(ctx, queries, workspaceID); ok {
			return auth.NewClientForInstallation(installID), true
		}
	}
	if personalToken == "" {
		return nil, false
	}
	return gh.NewClient(personalToken), true
}

// lookupInstallationID returns the installation_id for the first
// github_installation row associated with the workspace. Most workspaces
// have at most one App installation; if multiple exist we use the
// earliest by created_at (ListGitHubInstallationsByWorkspace orders that
// way already). Returns ok=false when no installation exists OR when the
// lookup errors — App path opts out cleanly without bubbling errors.
func lookupInstallationID(ctx context.Context, queries *db.Queries, workspaceID pgtype.UUID) (int64, bool) {
	rows, err := queries.ListGitHubInstallationsByWorkspace(ctx, workspaceID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("ship hub: lookup installation failed; falling back to PAT",
				"workspace_id", workspaceID, "error", err)
		}
		return 0, false
	}
	if len(rows) == 0 {
		return 0, false
	}
	return rows[0].InstallationID, true
}
