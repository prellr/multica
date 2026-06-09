package handler

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// githubAPIBase is the base URL for GitHub's REST API. Mutable so tests can
// point fetchInstallationAccount at an httptest server without touching the
// real GitHub.
var githubAPIBase = "https://api.github.com"

// ── Response shapes ─────────────────────────────────────────────────────────

// GitHubInstallationResponse is the JSON shape returned by the installation
// list endpoint and broadcast on installation-related WS events.
//
// InstallationID is admin-only: the numeric GitHub installation_id is the
// management handle used by the Connect/Disconnect flows, so non-admin
// members receive responses with the field omitted. The list handler gates
// it by role; realtime broadcasts always omit it because the WS fanout has
// no per-recipient view (admins re-query the list endpoint on invalidation
// to recover the management handle).
type GitHubInstallationResponse struct {
	ID               string  `json:"id"`
	WorkspaceID      string  `json:"workspace_id"`
	InstallationID   *int64  `json:"installation_id,omitempty"`
	AccountLogin     string  `json:"account_login"`
	AccountType      string  `json:"account_type"`
	AccountAvatarURL *string `json:"account_avatar_url"`
	CreatedAt        string  `json:"created_at"`
}

type GitHubPullRequestResponse struct {
	ID              string  `json:"id"`
	WorkspaceID     string  `json:"workspace_id"`
	RepoOwner       string  `json:"repo_owner"`
	RepoName        string  `json:"repo_name"`
	Number          int32   `json:"number"`
	Title           string  `json:"title"`
	State           string  `json:"state"`
	HtmlURL         string  `json:"html_url"`
	Branch          *string `json:"branch"`
	AuthorLogin     *string `json:"author_login"`
	AuthorAvatarURL *string `json:"author_avatar_url"`
	MergedAt        *string `json:"merged_at"`
	ClosedAt        *string `json:"closed_at"`
	PRCreatedAt     string  `json:"pr_created_at"`
	PRUpdatedAt     string  `json:"pr_updated_at"`
}

type GitHubConnectResponse struct {
	URL        string `json:"url"`
	Configured bool   `json:"configured"`
}

func githubInstallationToResponse(i db.GithubInstallation) GitHubInstallationResponse {
	instID := i.InstallationID
	return GitHubInstallationResponse{
		ID:               uuidToString(i.ID),
		WorkspaceID:      uuidToString(i.WorkspaceID),
		InstallationID:   &instID,
		AccountLogin:     i.AccountLogin,
		AccountType:      i.AccountType,
		AccountAvatarURL: textToPtr(i.AccountAvatarUrl),
		CreatedAt:        timestampToString(i.CreatedAt),
	}
}

// githubInstallationToBroadcast returns the same shape as the list endpoint's
// per-role response with the numeric `installation_id` stripped. Realtime
// events fan out to every WS client subscribed to the workspace, so the
// payload must match the weakest-role view — admin/owner clients re-query
// the list endpoint to recover the management handle. The frontend uses
// these events only to invalidate the installations query, so it does not
// read `installation_id` off the broadcast.
func githubInstallationToBroadcast(i db.GithubInstallation) GitHubInstallationResponse {
	resp := githubInstallationToResponse(i)
	resp.InstallationID = nil
	return resp
}

func githubPullRequestToResponse(p db.GithubPullRequest) GitHubPullRequestResponse {
	return GitHubPullRequestResponse{
		ID:              uuidToString(p.ID),
		WorkspaceID:     uuidToString(p.WorkspaceID),
		RepoOwner:       p.RepoOwner,
		RepoName:        p.RepoName,
		Number:          p.PrNumber,
		Title:           p.Title,
		State:           p.State,
		HtmlURL:         p.HtmlUrl,
		Branch:          textToPtr(p.Branch),
		AuthorLogin:     textToPtr(p.AuthorLogin),
		AuthorAvatarURL: textToPtr(p.AuthorAvatarUrl),
		MergedAt:        timestampToPtr(p.MergedAt),
		ClosedAt:        timestampToPtr(p.ClosedAt),
		PRCreatedAt:     timestampToString(p.PrCreatedAt),
		PRUpdatedAt:     timestampToString(p.PrUpdatedAt),
	}
}

// ── Connect / state token ───────────────────────────────────────────────────

// githubAppSlug returns the GitHub App slug used to build the install URL.
// Empty when the integration is not configured for this deployment.
func githubAppSlug() string { return strings.TrimSpace(os.Getenv("GITHUB_APP_SLUG")) }

// githubWebhookSecret is shared by webhook verification and state-token signing.
// We reuse the webhook secret as the state HMAC key so operators only need to
// configure one value.
func githubWebhookSecret() string { return strings.TrimSpace(os.Getenv("GITHUB_WEBHOOK_SECRET")) }

// isGitHubConfigured returns true only when BOTH the install slug and the
// webhook secret are set. The Connect button uses this single flag, so the
// frontend never offers a flow that the backend would reject.
func isGitHubConfigured() bool { return githubAppSlug() != "" && githubWebhookSecret() != "" }

// signState produces an opaque token that binds a workspace ID to the
// install flow so the setup callback can recover the workspace without
// trusting query params alone. Format: "<workspaceID>.<nonce>.<sigHex>".
func signState(workspaceID string) (string, error) {
	secret := githubWebhookSecret()
	if secret == "" {
		return "", errors.New("github integration is not configured")
	}
	nonceBytes := make([]byte, 12)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", err
	}
	nonce := hex.EncodeToString(nonceBytes)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(workspaceID))
	mac.Write([]byte("."))
	mac.Write([]byte(nonce))
	sig := hex.EncodeToString(mac.Sum(nil))
	return workspaceID + "." + nonce + "." + sig, nil
}

func verifyState(token string) (string, bool) {
	secret := githubWebhookSecret()
	if secret == "" {
		return "", false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}
	workspaceID, nonce, sig := parts[0], parts[1], parts[2]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(workspaceID))
	mac.Write([]byte("."))
	mac.Write([]byte(nonce))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return "", false
	}
	return workspaceID, true
}

// GitHubConnect (GET /api/workspaces/{id}/github/connect) returns the URL the
// browser should open to install the Multica GitHub App against the caller's
// repos. The state token binds the resulting setup callback to this workspace.
func (h *Handler) GitHubConnect(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if _, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id"); !ok {
		return
	}
	if !isGitHubConfigured() {
		writeJSON(w, http.StatusOK, GitHubConnectResponse{Configured: false})
		return
	}
	slug := githubAppSlug()
	state, err := signState(workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sign state")
		return
	}
	installURL := fmt.Sprintf(
		"https://github.com/apps/%s/installations/new?state=%s",
		url.PathEscape(slug),
		url.QueryEscape(state),
	)
	writeJSON(w, http.StatusOK, GitHubConnectResponse{URL: installURL, Configured: true})
}

// GitHubSetupCallback (GET /api/github/setup) handles the redirect GitHub
// sends after a user installs (or re-authorizes) the App. We expect
// ?installation_id=<id>&state=<signed token>. We persist the installation
// row (workspace ↔ installation_id mapping), then bounce the user back to
// the new Settings → GitHub tab in the web app (RFC MUL-2414 §4.1). The
// previous destination was the catch-all Settings page, which after the
// GitHub-tab split would land users on the default profile tab instead of
// the place that shows the connection they just completed.
func (h *Handler) GitHubSetupCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	installationIDStr := q.Get("installation_id")
	state := q.Get("state")
	frontend := strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN"))
	if frontend == "" {
		frontend = "http://localhost:3000"
	}
	settingsURL := strings.TrimRight(frontend, "/") + "/settings?tab=github"

	if installationIDStr == "" || state == "" {
		http.Redirect(w, r, settingsURL+"&github_error=missing_params", http.StatusFound)
		return
	}
	workspaceID, ok := verifyState(state)
	if !ok {
		http.Redirect(w, r, settingsURL+"&github_error=invalid_state", http.StatusFound)
		return
	}
	installationID, err := strconv.ParseInt(installationIDStr, 10, 64)
	if err != nil {
		http.Redirect(w, r, settingsURL+"&github_error=bad_installation_id", http.StatusFound)
		return
	}
	wsUUID, err := parseStrictUUID(workspaceID)
	if err != nil {
		http.Redirect(w, r, settingsURL+"&github_error=bad_workspace", http.StatusFound)
		return
	}
	// Resolve the installation against GitHub's API to capture display info.
	// If the App auth is not configured we still create the row with the
	// minimum we know; webhook events will refresh it as soon as one fires.
	login, accountType, avatar := fetchInstallationAccount(r.Context(), installationID)

	// Best-effort capture of the connecting user (may be nil if the public
	// callback was hit without a session — e.g. user wasn't logged in to
	// Multica when they finished the GitHub install). Either way we save
	// the row so the workspace owner sees the connection on next reload.
	connectedBy := pgtype.UUID{}
	if userID := requestUserID(r); userID != "" {
		if u, err := parseStrictUUID(userID); err == nil {
			connectedBy = u
		}
	}

	inst, err := h.Queries.CreateGitHubInstallation(r.Context(), db.CreateGitHubInstallationParams{
		WorkspaceID:      wsUUID,
		InstallationID:   installationID,
		AccountLogin:     login,
		AccountType:      accountType,
		AccountAvatarUrl: ptrToText(avatar),
		ConnectedByID:    connectedBy,
	})
	if err != nil {
		slog.Error("github: failed to persist installation", "err", err, "installation_id", installationID)
		http.Redirect(w, r, settingsURL+"&github_error=persist_failed", http.StatusFound)
		return
	}
	h.publish(protocol.EventGitHubInstallationCreated, workspaceID, "system", "", map[string]any{
		"installation": githubInstallationToBroadcast(inst),
	})
	http.Redirect(w, r, settingsURL+"&github_connected=1", http.StatusFound)
}

// fetchInstallationAccount tries to enrich the installation row with the
// account name + avatar from GitHub.
//
// GitHub's `GET /app/installations/{id}` endpoint requires GitHub App
// authentication (a JWT signed with the App's RSA private key). When the
// operator has configured GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY we
// sign a short-lived JWT and use it; on any failure (env not set, key
// malformed, GitHub returns non-200) we fall back to the "unknown"
// placeholder. The next `installation` webhook delivery from GitHub will
// upsert the row with the real account info — see handleInstallationEvent.
//
// The HTTP call is synchronous (no independent timeout — that's a pre-
// existing wart of the install path), but we deliberately do NOT let a
// failure abort the setup callback: a network blip here just leaves the
// "unknown" placeholder in place, and the frontend re-queries on the
// realtime broadcast emitted by the webhook handler, so the UI converges
// without a manual refresh.
//
// Ported from upstream MUL-3078 (PR #3811). The fork's pre-port version
// called the endpoint unauthenticated, which always 401'd, which always
// fell through to "unknown" — visible in the workspace integration UI as
// "Connected to unknown" and load-bearing because the App-token-vs-PAT
// auth selector keyed off the row's account info.
func fetchInstallationAccount(ctx context.Context, installationID int64) (login, accountType string, avatar *string) {
	login = "unknown"
	accountType = "User"
	avatar = nil
	endpoint := fmt.Sprintf("%s/app/installations/%d", strings.TrimRight(githubAPIBase, "/"), installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token, err := signGitHubAppJWT(time.Now()); err != nil {
		// Misconfigured private key is operator-actionable — log so the
		// install path doesn't silently fall back to "unknown" forever
		// without leaving a breadcrumb.
		slog.Warn("github: sign App JWT failed", "err", err)
	} else if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var body struct {
		Account struct {
			Login     string `json:"login"`
			Type      string `json:"type"`
			AvatarURL string `json:"avatar_url"`
		} `json:"account"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return
	}
	if body.Account.Login != "" {
		login = body.Account.Login
	}
	if body.Account.Type != "" {
		accountType = body.Account.Type
	}
	if body.Account.AvatarURL != "" {
		v := body.Account.AvatarURL
		avatar = &v
	}
	return
}

// signGitHubAppJWT mints the short-lived RS256 JWT GitHub requires for
// App-authenticated REST calls (see fetchInstallationAccount). Returns
// ("", nil) when the operator hasn't configured the App identity — that's
// a soft "App auth not available" signal, not an error, so callers can
// fall through to their unauthenticated path. A malformed
// GITHUB_APP_PRIVATE_KEY surfaces as an error so the operator notices.
//
// `now` is injected for deterministic tests; production callers pass
// time.Now().
//
// Ported from upstream MUL-3078 (PR #3811).
func signGitHubAppJWT(now time.Time) (string, error) {
	appID := strings.TrimSpace(os.Getenv("GITHUB_APP_ID"))
	pemKey := strings.TrimSpace(os.Getenv("GITHUB_APP_PRIVATE_KEY"))
	if appID == "" || pemKey == "" {
		return "", nil
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(pemKey))
	if err != nil {
		return "", fmt.Errorf("parse GITHUB_APP_PRIVATE_KEY: %w", err)
	}
	// GitHub allows JWTs valid for up to 10 minutes. We back-date `iat`
	// by 60 seconds to absorb modest clock skew between us and GitHub
	// (otherwise an "iat in the future" verdict from GitHub fails the
	// request) and cap `exp` at 9 minutes ahead to stay inside the cap
	// even with the same skew applied.
	claims := jwt.MapClaims{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign App JWT: %w", err)
	}
	return signed, nil
}

// ── Listing / disconnect ────────────────────────────────────────────────────

// ListGitHubInstallations returns the workspace's connected GitHub
// installations to any workspace member. Connect/disconnect remain
// admin-only at the router level, so the response carries a `can_manage`
// hint and strips the numeric `installation_id` for non-admin callers —
// they get visibility into "is GitHub wired up, and by whom?" without the
// management handle.
func (h *Handler) ListGitHubInstallations(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	member, _ := middleware.MemberFromContext(r.Context())
	canManage := roleAllowed(member.Role, "owner", "admin")

	rows, err := h.Queries.ListGitHubInstallationsByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list installations")
		return
	}
	out := make([]GitHubInstallationResponse, 0, len(rows))
	for _, row := range rows {
		resp := githubInstallationToResponse(row)
		if !canManage {
			resp.InstallationID = nil
		}
		out = append(out, resp)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installations": out,
		"configured":    isGitHubConfigured(),
		"can_manage":    canManage,
	})
}

func (h *Handler) DeleteGitHubInstallation(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	id := chi.URLParam(r, "installationId")
	idUUID, ok := parseUUIDOrBadRequest(w, id, "installation id")
	if !ok {
		return
	}
	if err := h.Queries.DeleteGitHubInstallation(r.Context(), db.DeleteGitHubInstallationParams{
		ID:          idUUID,
		WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove installation")
		return
	}
	h.publish(protocol.EventGitHubInstallationDeleted, workspaceID, "system", "", map[string]any{
		"id": id,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ── List PRs for an issue ───────────────────────────────────────────────────

func (h *Handler) ListPullRequestsForIssue(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListPullRequestsByIssue(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list pull requests")
		return
	}
	out := make([]GitHubPullRequestResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, githubPullRequestToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"pull_requests": out})
}

// ── Webhook ─────────────────────────────────────────────────────────────────

// identifierRe extracts identifiers like "MUL-1510" from text. Case-insensitive
// because branch names are conventionally lowercase but issue prefixes are
// uppercase. Word boundary on the left prevents matching inside email-style
// strings (e.g. "abc@MUL-1") and the digit anchor on the right rules out
// version numbers like "v1.2-3".
var identifierRe = regexp.MustCompile(`(?i)\b([a-z][a-z0-9]{1,9})-(\d+)\b`)

// HandleGitHubWebhook (POST /api/webhooks/github) is GitHub's destination for
// every event from a connected installation. We verify HMAC signature, route
// on X-GitHub-Event, and either upsert PR rows + auto-link to issues or
// remove the installation on uninstall.
func (h *Handler) HandleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10 MiB cap
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body failed")
		return
	}
	secret := githubWebhookSecret()
	if secret == "" {
		// Refusing to process webhooks at all is safer than treating an
		// unconfigured deployment as "all signatures valid".
		writeError(w, http.StatusServiceUnavailable, "github webhooks not configured")
		return
	}
	sigHeader := r.Header.Get("X-Hub-Signature-256")
	if !verifyWebhookSignature(secret, sigHeader, body) {
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	ctx := r.Context()
	switch event {
	case "ping":
		writeJSON(w, http.StatusOK, map[string]string{"ok": "pong"})
		return
	case "installation":
		h.handleInstallationEvent(ctx, body)
	case "pull_request":
		h.handlePullRequestEvent(ctx, body)
	default:
		// Acknowledge every event so GitHub doesn't mark the endpoint failing,
		// but ignore types we don't model.
	}
	w.WriteHeader(http.StatusAccepted)
}

func verifyWebhookSignature(secret, header string, body []byte) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	want, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

type ghInstallationPayload struct {
	Action       string `json:"action"`
	Installation struct {
		ID      int64 `json:"id"`
		Account struct {
			Login     string `json:"login"`
			Type      string `json:"type"`
			AvatarURL string `json:"avatar_url"`
		} `json:"account"`
	} `json:"installation"`
}

func (h *Handler) handleInstallationEvent(ctx context.Context, body []byte) {
	var p ghInstallationPayload
	if err := json.Unmarshal(body, &p); err != nil {
		slog.Warn("github: bad installation payload", "err", err)
		return
	}
	switch p.Action {
	case "deleted", "suspend":
		// User removed the App on GitHub — drop our row so the workspace
		// stops trusting this installation_id. We DELETE … RETURNING so
		// the broadcast can be scoped to the right workspace; events
		// without WorkspaceID are dropped by the realtime listener and
		// would leave already-open Settings tabs stale.
		deleted, err := h.Queries.DeleteGitHubInstallationByInstallationID(ctx, p.Installation.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return // already gone — nothing to broadcast
			}
			slog.Warn("github: delete installation failed", "err", err, "installation_id", p.Installation.ID)
			return
		}
		// Broadcast the internal row id only — the numeric installation_id is
		// a management handle that non-admin members are not allowed to see.
		// The frontend invalidates the installations query on this event and
		// does not read the broadcast payload directly.
		h.publish(protocol.EventGitHubInstallationDeleted, uuidToString(deleted.WorkspaceID), "system", "", map[string]any{
			"id": uuidToString(deleted.ID),
		})
	case "created", "new_permissions_accepted", "unsuspend":
		// We don't know which workspace this maps to from the webhook
		// alone — the setup callback handler is what binds installation
		// to workspace, so we just refresh metadata if we already have
		// a row.
		existing, err := h.Queries.GetGitHubInstallationByInstallationID(ctx, p.Installation.ID)
		if err != nil {
			return
		}
		avatar := p.Installation.Account.AvatarURL
		inst, err := h.Queries.CreateGitHubInstallation(ctx, db.CreateGitHubInstallationParams{
			WorkspaceID:      existing.WorkspaceID,
			InstallationID:   p.Installation.ID,
			AccountLogin:     p.Installation.Account.Login,
			AccountType:      coalesce(p.Installation.Account.Type, "User"),
			AccountAvatarUrl: ptrToText(strPtrOrNil(avatar)),
			ConnectedByID:    existing.ConnectedByID,
		})
		if err != nil {
			slog.Warn("github: refresh installation failed", "err", err)
			return
		}
		// Broadcast so any open Settings → GitHub tab re-queries the
		// installations list. Without this, a row created by the setup
		// callback with the "unknown" placeholder (e.g. because GitHub
		// App JWT auth wasn't configured, or this webhook arrived after
		// the user already loaded the page) would stay visibly stale
		// until the user manually refreshes. Ported from upstream
		// MUL-3078 (PR #3811).
		h.publish(protocol.EventGitHubInstallationCreated, uuidToString(inst.WorkspaceID), "system", "", map[string]any{
			"installation": githubInstallationToBroadcast(inst),
		})
	}
}

type ghPullRequestPayload struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number    int32  `json:"number"`
		HTMLURL   string `json:"html_url"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		State     string `json:"state"`
		Draft     bool   `json:"draft"`
		Merged    bool   `json:"merged"`
		MergedAt  string `json:"merged_at"`
		ClosedAt  string `json:"closed_at"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
		Head      struct {
			Ref string `json:"ref"`
		} `json:"head"`
		User struct {
			Login     string `json:"login"`
			AvatarURL string `json:"avatar_url"`
		} `json:"user"`
	} `json:"pull_request"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

func (h *Handler) handlePullRequestEvent(ctx context.Context, body []byte) {
	var p ghPullRequestPayload
	if err := json.Unmarshal(body, &p); err != nil {
		slog.Warn("github: bad pull_request payload", "err", err)
		return
	}
	if p.Installation.ID == 0 {
		return
	}
	inst, err := h.Queries.GetGitHubInstallationByInstallationID(ctx, p.Installation.ID)
	if err != nil {
		// Webhook from an installation we never wired up — nothing we
		// can attribute to a workspace, so drop it silently.
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("github: lookup installation failed", "err", err)
		}
		return
	}

	state := derivePRState(p.PullRequest.State, p.PullRequest.Draft, p.PullRequest.Merged)
	pr, err := h.Queries.UpsertGitHubPullRequest(ctx, db.UpsertGitHubPullRequestParams{
		WorkspaceID:     inst.WorkspaceID,
		InstallationID:  pgtype.Int8{Int64: inst.InstallationID, Valid: true},
		RepoOwner:       p.Repository.Owner.Login,
		RepoName:        p.Repository.Name,
		PrNumber:        p.PullRequest.Number,
		Title:           p.PullRequest.Title,
		State:           state,
		HtmlUrl:         p.PullRequest.HTMLURL,
		Branch:          ptrToText(strPtrOrNil(p.PullRequest.Head.Ref)),
		AuthorLogin:     ptrToText(strPtrOrNil(p.PullRequest.User.Login)),
		AuthorAvatarUrl: ptrToText(strPtrOrNil(p.PullRequest.User.AvatarURL)),
		MergedAt:        parseGHTime(p.PullRequest.MergedAt),
		ClosedAt:        parseGHTime(p.PullRequest.ClosedAt),
		PrCreatedAt:     parseGHTimeRequired(p.PullRequest.CreatedAt),
		PrUpdatedAt:     parseGHTimeRequired(p.PullRequest.UpdatedAt),
	})
	if err != nil {
		slog.Warn("github: upsert pr failed", "err", err)
		return
	}

	workspaceID := uuidToString(inst.WorkspaceID)
	resp := githubPullRequestToResponse(pr)

	// Auto-link: scan title/body/branch for issue identifiers, look them
	// up in this workspace, attach the link rows. Idempotent (ON CONFLICT
	// DO NOTHING) so re-firing the webhook doesn't duplicate.
	//
	// RFC MUL-2414 §4.8: the PR mirror upsert above always runs (so re-enabling
	// GitHub features restores history without backfill), but the link rows
	// are a "new side-effect" and must be gated by the workspace's auto-link
	// flag (which itself short-circuits when the master `github_enabled`
	// switch is off).
	linkedIssueIDs := make([]string, 0)
	if h.workspaceAutoLinkPRsEnabled(ctx, inst.WorkspaceID) {
		idents := extractIdentifiers(p.PullRequest.Title, p.PullRequest.Body, p.PullRequest.Head.Ref)
		prefix := h.getIssuePrefix(ctx, inst.WorkspaceID)
		for _, id := range idents {
			issue, ok := h.lookupIssueByIdentifier(ctx, inst.WorkspaceID, prefix, id)
			if !ok {
				continue
			}
			if err := h.Queries.LinkIssueToPullRequest(ctx, db.LinkIssueToPullRequestParams{
				IssueID:       issue.ID,
				PullRequestID: pr.ID,
				LinkedByType:  strToText("system"),
				LinkedByID:    pgtype.UUID{},
			}); err != nil {
				slog.Warn("github: link failed", "err", err)
				continue
			}
			linkedIssueIDs = append(linkedIssueIDs, uuidToString(issue.ID))

			// A terminal PR event (`merged` or `closed`) may be the moment the
			// last in-flight sibling resolves, so we re-evaluate the issue on
			// both. We advance the issue to done when:
			//   1. the issue isn't already terminal (`done` / `cancelled`);
			//   2. no sibling PR is still `open` / `draft`;
			//   3. at least one linked PR (this one or a sibling) is `merged`.
			// Rule (3) prevents an "all closed-without-merge" sequence from
			// silently auto-closing the issue — if nothing was ever delivered,
			// the user should decide what to do manually.
			if (state == "merged" || state == "closed") && issue.Status != "done" && issue.Status != "cancelled" {
				counts, err := h.Queries.GetSiblingPullRequestStateCountsForIssue(ctx, db.GetSiblingPullRequestStateCountsForIssueParams{
					IssueID: issue.ID,
					ID:      pr.ID,
				})
				if err != nil {
					slog.Warn("github: count sibling pr states failed", "err", err, "issue_id", uuidToString(issue.ID))
					continue
				}
				anyMerged := state == "merged" || counts.MergedCount > 0
				if counts.OpenCount == 0 && anyMerged {
					h.advanceIssueToDone(ctx, issue, workspaceID)
				}
			}
		}
	}

	// Broadcast PR change to the workspace so any open issue detail page
	// re-queries its PR list.
	h.publish(protocol.EventPullRequestUpdated, workspaceID, "system", "", map[string]any{
		"pull_request":     resp,
		"linked_issue_ids": linkedIssueIDs,
	})
}

func derivePRState(state string, draft, merged bool) string {
	if merged {
		return "merged"
	}
	if state == "closed" {
		return "closed"
	}
	if draft {
		return "draft"
	}
	return "open"
}

func parseGHTime(s string) pgtype.Timestamptz {
	if s == "" {
		return pgtype.Timestamptz{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func parseGHTimeRequired(s string) pgtype.Timestamptz {
	t := parseGHTime(s)
	if !t.Valid {
		return pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}
	return t
}

// extractIdentifiers pulls every "PREFIX-NUMBER" match across the supplied
// fields, deduplicating in input order.
func extractIdentifiers(parts ...string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, src := range parts {
		for _, m := range identifierRe.FindAllStringSubmatch(src, -1) {
			ident := strings.ToUpper(m[1]) + "-" + m[2]
			if _, dup := seen[ident]; dup {
				continue
			}
			seen[ident] = struct{}{}
			out = append(out, ident)
		}
	}
	return out
}

// lookupIssueByIdentifier looks up an issue in the given workspace by its
// "PREFIX-NUMBER" identifier. Returns the row + true if the prefix matches
// workspaceAutoLinkPRsEnabled reports whether the workspace allows the
// GitHub webhook to create issue ↔ PR link rows. Defaults to true so that
// workspaces predating RFC MUL-2414 keep the historical "auto-link on"
// behavior, and short-circuits to false whenever the master GitHub switch
// is explicitly off — mirroring the precedence used on the client side.
func (h *Handler) workspaceAutoLinkPRsEnabled(ctx context.Context, workspaceID pgtype.UUID) bool {
	ws, err := h.Queries.GetWorkspace(ctx, workspaceID)
	if err != nil || len(ws.Settings) == 0 {
		return true
	}
	var s struct {
		GitHubEnabled            *bool `json:"github_enabled"`
		GitHubAutoLinkPRsEnabled *bool `json:"github_auto_link_prs_enabled"`
	}
	if err := json.Unmarshal(ws.Settings, &s); err != nil {
		return true
	}
	if s.GitHubEnabled != nil && !*s.GitHubEnabled {
		return false
	}
	if s.GitHubAutoLinkPRsEnabled == nil {
		return true
	}
	return *s.GitHubAutoLinkPRsEnabled
}

// the workspace's configured prefix and the number resolves to a real issue.
func (h *Handler) lookupIssueByIdentifier(ctx context.Context, workspaceID pgtype.UUID, prefix, identifier string) (db.Issue, bool) {
	idx := strings.LastIndex(identifier, "-")
	if idx < 0 {
		return db.Issue{}, false
	}
	gotPrefix, numStr := identifier[:idx], identifier[idx+1:]
	if !strings.EqualFold(gotPrefix, prefix) {
		return db.Issue{}, false
	}
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return db.Issue{}, false
	}
	issue, err := h.Queries.GetIssueByNumber(ctx, db.GetIssueByNumberParams{
		WorkspaceID: workspaceID,
		Number:      pgtype.Int4{Int32: int32(n), Valid: true},
	})
	if err != nil {
		return db.Issue{}, false
	}
	return issue, true
}

func (h *Handler) advanceIssueToDone(ctx context.Context, issue db.Issue, workspaceID string) {
	updated, err := h.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ID:          issue.ID,
		Status:      "done",
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("github: advance issue to done failed", "err", err)
		return
	}

	// Fire the platform parent-notification path on the same transition the
	// HTTP UpdateIssue / BatchUpdateIssues paths use. A merged PR is one of
	// the most common ways a sub-issue actually reaches `done`, and skipping
	// it here would leave the parent silent for the dominant completion path.
	// notifyParentOfChildDone re-checks every guard (prev != done, parent
	// exists, parent not terminal), so calling it unconditionally is safe.
	h.notifyParentOfChildDone(ctx, issue, updated)

	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	resp := issueToResponse(updated, prefix)
	h.publish(protocol.EventIssueUpdated, workspaceID, "system", "", map[string]any{
		"issue":          resp,
		"status_changed": true,
		"prev_status":    issue.Status,
		"creator_type":   issue.CreatorType,
		"creator_id":     uuidToString(issue.CreatorID),
		"source":         "github_pr_merged",
	})
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func parseStrictUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	return u, nil
}

func coalesce(a, fallback string) string {
	if strings.TrimSpace(a) == "" {
		return fallback
	}
	return a
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}
