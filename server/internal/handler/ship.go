package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service/ship"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	gh "github.com/multica-ai/multica/server/pkg/github"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// requireShipHubEnabled mirrors requireChannelsEnabled — every Ship Hub
// endpoint 404s when the workspace flag is off so the surface is invisible
// to anyone who hasn't opted in.
func (h *Handler) requireShipHubEnabled(w http.ResponseWriter, r *http.Request) (pgtype.UUID, db.Workspace, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	wsID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return pgtype.UUID{}, db.Workspace{}, false
	}
	ws, err := h.Queries.GetWorkspace(r.Context(), wsID)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return pgtype.UUID{}, db.Workspace{}, false
	}
	if !ws.ShipHubEnabled {
		writeError(w, http.StatusNotFound, "not found")
		return pgtype.UUID{}, db.Workspace{}, false
	}
	return wsID, ws, true
}

// loadShipHubGitHubToken returns the workspace's GitHub PAT, preferring the
// Phase 2 encrypted workspace_secret row and falling back to the legacy
// plaintext settings.ship_hub.github_token only for unmigrated rows. Mirrors
// the read order in cmd/server/ship_hub_reconciler.go so handler and
// reconciler always agree about whether a workspace has a token.
//
// Returns "" when no token is configured anywhere. Errors decrypting an
// encrypted row are logged and treated as no-token (fail closed) rather
// than leaking a partial value.
func (h *Handler) loadShipHubGitHubToken(ctx context.Context, ws db.Workspace) string {
	// Encrypted store wins. The Phase 2 startup migrator moves any legacy
	// settings.ship_hub.github_token into this row and clears the settings
	// path, so post-migration this is the only place the token lives.
	row, err := h.Queries.GetWorkspaceSecret(ctx, db.GetWorkspaceSecretParams{
		WorkspaceID: ws.ID,
		Name:        "github_token",
	})
	if err == nil {
		if tok := ReadShipHubGitHubTokenFromEncrypted(row.ValueEncrypted); tok != "" {
			return tok
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("ship hub: load encrypted token failed",
			"workspace_id", ws.ID, "error", err)
	}
	// Legacy fallback: pre-Phase-2 workspaces might still have a plaintext
	// token in settings if the startup migrator hasn't run yet (e.g. mid-
	// upgrade). The migrator is idempotent so this path heals on next boot.
	return readShipHubGitHubToken(ws.Settings)
}

// shipServiceFromWorkspace constructs a ship.Service whose GitHub client
// uses the workspace's stored token. Returns ok=false (with error already
// written) when the workspace has no token AND the request needed one.
//
// Read-only handlers (list PRs, list envs) call this with requireToken=false
// — they're fine working off cached rows. Sync/refresh handlers call it
// with requireToken=true.
//
// When a GitHub App is configured (GITHUB_APP_ID + GITHUB_APP_PRIVATE_KEY)
// AND the workspace has a github_installation row, the returned client
// uses installation tokens (own per-installation rate-limit quota,
// independent of any personal PAT). Otherwise it falls back to the
// workspace's stored personal token. The requireToken gate still applies
// to the personal-token path so workspaces using the legacy flow get the
// same UX as before.
func (h *Handler) shipServiceFromWorkspace(w http.ResponseWriter, r *http.Request, ws db.Workspace, requireToken bool) (*ship.Service, bool) {
	token := h.loadShipHubGitHubToken(r.Context(), ws)
	// App path can authenticate without a personal token, so a missing
	// PAT is only fatal when the App path is unavailable.
	hasAppPath := false
	if auth, _ := GitHubAppAuth(); auth != nil {
		if _, ok := lookupInstallationID(r.Context(), h.Queries, ws.ID); ok {
			hasAppPath = true
		}
	}
	if requireToken && token == "" && !hasAppPath {
		writeError(w, http.StatusBadRequest, "GitHub token not configured. Set it in workspace settings → Ship Hub.")
		return nil, false
	}
	return &ship.Service{
		Q:      h.Queries,
		Github: shipHubGitHubClient(r.Context(), h.Queries, ws.ID, token),
	}, true
}

// pullRequestToResponse is the JSON shape the frontend Kanban renders.
// Keep this stable — new fields go at the end so older Electron builds
// don't choke on a column they don't know about.
type pullRequestResponse struct {
	ID              string  `json:"id"`
	WorkspaceID     string  `json:"workspace_id"`
	ProjectID       *string `json:"project_id"`
	RepoURL         string  `json:"repo_url"`
	Number          int32   `json:"number"`
	Title           string  `json:"title"`
	State           string  `json:"state"`
	IsDraft         bool    `json:"is_draft"`
	AuthorLogin     string  `json:"author_login"`
	AuthorAvatarURL *string `json:"author_avatar_url"`
	BaseRef         string  `json:"base_ref"`
	HeadRef         string  `json:"head_ref"`
	HeadSHA         string  `json:"head_sha"`
	HTMLURL         string  `json:"html_url"`
	Body            *string `json:"body"`
	CIStatus        *string `json:"ci_status"`
	ReviewDecision  *string `json:"review_decision"`
	Mergeable       *string `json:"mergeable"`
	Additions       int32   `json:"additions"`
	Deletions       int32   `json:"deletions"`
	ChangedFiles    int32   `json:"changed_files"`
	Labels          any     `json:"labels"`
	PRCreatedAt     string  `json:"pr_created_at"`
	PRUpdatedAt     string  `json:"pr_updated_at"`
	PRMergedAt      *string `json:"pr_merged_at"`
	PRClosedAt      *string `json:"pr_closed_at"`
	FetchedAt       string  `json:"fetched_at"`
	// Phase 4 — linkage fields. Per CLAUDE.md "API Response Compatibility",
	// these are appended at the end so an older Electron build that
	// doesn't know the schema simply ignores them.
	OriginatingIssueID     *string `json:"originating_issue_id"`
	OriginatingAgentTaskID *string `json:"originating_agent_task_id"`
	AutoCloseIssueOnMerge  bool    `json:"auto_close_issue_on_merge"`
	ConversationChannelID  *string `json:"conversation_channel_id"`
	StackParentPRID        *string `json:"stack_parent_pr_id"`
	Source                 string  `json:"source"`
	// Phase 7a — when this PR is in an active release, decorate with
	// the release's id/title/stage so the card can render the
	// "🚂 in <release>" badge without an extra round-trip. Optional
	// per CLAUDE.md API Response Compatibility — older Electron
	// builds that don't know the field simply ignore it.
	ActiveRelease *activeReleaseRef `json:"active_release,omitempty"`
}

// activeReleaseRef is the minimal release descriptor attached to PR
// responses. Stays small (id + title + stage) so the bulk-decoration
// query stays cheap.
type activeReleaseRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Stage string `json:"stage"`
}

func pullRequestToResponse(pr db.PullRequest) pullRequestResponse {
	var labels any
	if len(pr.Labels) > 0 {
		json.Unmarshal(pr.Labels, &labels)
	}
	if labels == nil {
		labels = []any{}
	}
	return pullRequestResponse{
		ID:              uuidToString(pr.ID),
		WorkspaceID:     uuidToString(pr.WorkspaceID),
		ProjectID:       uuidToPtr(pr.ProjectID),
		RepoURL:         pr.RepoUrl,
		Number:          pr.PrNumber,
		Title:           pr.Title,
		State:           string(pr.State),
		IsDraft:         pr.IsDraft,
		AuthorLogin:     pr.AuthorLogin,
		AuthorAvatarURL: textToPtr(pr.AuthorAvatarUrl),
		BaseRef:         pr.BaseRef,
		HeadRef:         pr.HeadRef,
		HeadSHA:         pr.HeadSha,
		HTMLURL:         pr.HtmlUrl,
		Body:            textToPtr(pr.Body),
		CIStatus:        textToPtr(pr.CiStatus),
		ReviewDecision:  textToPtr(pr.ReviewDecision),
		Mergeable:       textToPtr(pr.Mergeable),
		Additions:       pr.Additions,
		Deletions:       pr.Deletions,
		ChangedFiles:    pr.ChangedFiles,
		Labels:          labels,
		PRCreatedAt:     timestampToString(pr.PrCreatedAt),
		PRUpdatedAt:     timestampToString(pr.PrUpdatedAt),
		PRMergedAt:      timestampToPtr(pr.PrMergedAt),
		PRClosedAt:      timestampToPtr(pr.PrClosedAt),
		FetchedAt:       timestampToString(pr.FetchedAt),
		OriginatingIssueID:     uuidToPtr(pr.OriginatingIssueID),
		OriginatingAgentTaskID: uuidToPtr(pr.OriginatingAgentTaskID),
		AutoCloseIssueOnMerge:  pr.AutoCloseIssueOnMerge,
		ConversationChannelID:  uuidToPtr(pr.ConversationChannelID),
		StackParentPRID:        uuidToPtr(pr.StackParentPrID),
		Source:                 pr.Source,
	}
}

type deployEnvironmentResponse struct {
	ID                     string  `json:"id"`
	WorkspaceID            string  `json:"workspace_id"`
	ProjectID              string  `json:"project_id"`
	Kind                   string  `json:"kind"`
	Name                   string  `json:"name"`
	TargetBranch           string  `json:"target_branch"`
	TargetURL              *string `json:"target_url"`
	CurrentSHA             *string `json:"current_sha"`
	CurrentDeployedAt      *string `json:"current_deployed_at"`
	AutoPromote            bool    `json:"auto_promote"`
	DeployWorkflowFilename *string `json:"deploy_workflow_filename"`
	// AutoDeploy is the per-env opt-in for Multica to fire the
	// deploy_workflow_filename via workflow_dispatch when this env's
	// release transitions into its deploy stage (in_staging for
	// staging, promoting for production). Hides Ship Hub's
	// tracking-only legacy and turns Promote into a real one-click
	// production deploy.
	AutoDeploy bool   `json:"auto_deploy"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

func deployEnvironmentToResponse(e db.DeployEnvironment) deployEnvironmentResponse {
	return deployEnvironmentResponse{
		ID:                     uuidToString(e.ID),
		WorkspaceID:            uuidToString(e.WorkspaceID),
		ProjectID:              uuidToString(e.ProjectID),
		Kind:                   string(e.Kind),
		Name:                   e.Name,
		TargetBranch:           e.TargetBranch,
		TargetURL:              textToPtr(e.TargetUrl),
		CurrentSHA:             textToPtr(e.CurrentSha),
		CurrentDeployedAt:      timestampToPtr(e.CurrentDeployedAt),
		AutoPromote:            e.AutoPromote,
		DeployWorkflowFilename: textToPtr(e.DeployWorkflowFilename),
		AutoDeploy:             e.AutoDeploy,
		CreatedAt:              timestampToString(e.CreatedAt),
		UpdatedAt:              timestampToString(e.UpdatedAt),
	}
}

type deployResponse struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspace_id"`
	EnvironmentID string  `json:"environment_id"`
	Ref           string  `json:"ref"`
	SHA           string  `json:"sha"`
	Status        string  `json:"status"`
	TriggeredBy   *string `json:"triggered_by"`
	TriggeredAt   string  `json:"triggered_at"`
	StartedAt     *string `json:"started_at"`
	CompletedAt   *string `json:"completed_at"`
	LogURL        *string `json:"log_url"`
	ErrorMessage  *string `json:"error_message"`
	CreatedAt     string  `json:"created_at"`
}

func deployToResponse(d db.Deploy) deployResponse {
	return deployResponse{
		ID:            uuidToString(d.ID),
		WorkspaceID:   uuidToString(d.WorkspaceID),
		EnvironmentID: uuidToString(d.EnvironmentID),
		Ref:           d.Ref,
		SHA:           d.Sha,
		Status:        string(d.Status),
		TriggeredBy:   uuidToPtr(d.TriggeredBy),
		TriggeredAt:   timestampToString(d.TriggeredAt),
		StartedAt:     timestampToPtr(d.StartedAt),
		CompletedAt:   timestampToPtr(d.CompletedAt),
		LogURL:        textToPtr(d.LogUrl),
		ErrorMessage:  textToPtr(d.ErrorMessage),
		CreatedAt:     timestampToString(d.CreatedAt),
	}
}

// ListShipProjects returns the projects in the workspace that have at
// least one github_repo resource attached, decorated with open-PR + env
// counts. The Kanban renders one column per project.
func (h *Handler) ListShipProjects(w http.ResponseWriter, r *http.Request) {
	wsID, _, ok := h.requireShipHubEnabled(w, r)
	if !ok {
		return
	}
	projects, err := h.Queries.ListProjects(r.Context(), db.ListProjectsParams{
		WorkspaceID:     wsID,
		IncludeArchived: false,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list projects")
		return
	}
	if len(projects) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"projects": []any{}})
		return
	}

	projectIDs := make([]pgtype.UUID, len(projects))
	for i, p := range projects {
		projectIDs[i] = p.ID
	}

	// We need each project's github_repo resources to filter the list.
	// One query, then bucket in-memory.
	resources, _ := h.Queries.ListProjectResourcesForProjects(r.Context(), projectIDs)
	hasGithub := map[string]bool{}
	for _, res := range resources {
		if res.ResourceType == "github_repo" {
			hasGithub[uuidToString(res.ProjectID)] = true
		}
	}

	prCounts := map[string]int64{}
	if rows, err := h.Queries.CountOpenPullRequestsForProjects(r.Context(), projectIDs); err == nil {
		for _, row := range rows {
			prCounts[uuidToString(row.ProjectID)] = row.OpenCount
		}
	}
	envCounts := map[string]int64{}
	if rows, err := h.Queries.CountDeployEnvironmentsForProjects(r.Context(), projectIDs); err == nil {
		for _, row := range rows {
			envCounts[uuidToString(row.ProjectID)] = row.EnvCount
		}
	}

	out := make([]map[string]any, 0, len(projects))
	for _, p := range projects {
		id := uuidToString(p.ID)
		if !hasGithub[id] {
			continue
		}
		out = append(out, map[string]any{
			"id":             id,
			"title":          p.Title,
			"icon":           textToPtr(p.Icon),
			"open_pr_count":  prCounts[id],
			"env_count":      envCounts[id],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

// loadShipProject resolves the project ID URL param and enforces workspace
// scoping. Returns the project row + the workspace UUID so callers can pass
// both downstream.
func (h *Handler) loadShipProject(w http.ResponseWriter, r *http.Request) (db.Project, pgtype.UUID, db.Workspace, bool) {
	wsID, ws, ok := h.requireShipHubEnabled(w, r)
	if !ok {
		return db.Project{}, pgtype.UUID{}, db.Workspace{}, false
	}
	projectUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "project id")
	if !ok {
		return db.Project{}, pgtype.UUID{}, db.Workspace{}, false
	}
	project, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
		ID: projectUUID, WorkspaceID: wsID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return db.Project{}, pgtype.UUID{}, db.Workspace{}, false
	}
	return project, wsID, ws, true
}

// ListProjectPullRequests returns the cached PR rows for a project. Use
// SyncProjectPullRequests first to refresh from GitHub.
func (h *Handler) ListProjectPullRequests(w http.ResponseWriter, r *http.Request) {
	project, _, _, ok := h.loadShipProject(w, r)
	if !ok {
		return
	}
	stateParam := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("state")))
	stateFilter := db.NullPullRequestState{}
	switch stateParam {
	case "":
		// default: open only
		stateFilter = db.NullPullRequestState{PullRequestState: db.PullRequestStateOpen, Valid: true}
	case "open":
		stateFilter = db.NullPullRequestState{PullRequestState: db.PullRequestStateOpen, Valid: true}
	case "closed":
		stateFilter = db.NullPullRequestState{PullRequestState: db.PullRequestStateClosed, Valid: true}
	case "merged":
		stateFilter = db.NullPullRequestState{PullRequestState: db.PullRequestStateMerged, Valid: true}
	case "all":
		// Valid=false means "no filter" in the query.
	default:
		writeError(w, http.StatusBadRequest, "invalid state")
		return
	}
	rows, err := h.Queries.ListPullRequestsByProject(r.Context(), db.ListPullRequestsByProjectParams{
		ProjectID: project.ID,
		State:     stateFilter,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list pull requests")
		return
	}
	out := make([]pullRequestResponse, len(rows))
	for i, pr := range rows {
		out[i] = pullRequestToResponse(pr)
	}
	// Phase 7a — bulk-decorate with active_release. One round-trip
	// across every PR in the listing, so the card can render the
	// "in release" badge without an N+1 lookup. Best-effort: a
	// failure here drops the badge but renders the rest of the
	// list normally.
	h.attachActiveReleaseToPRList(r.Context(), out, rows)
	writeJSON(w, http.StatusOK, map[string]any{"pull_requests": out, "total": len(out)})
}

// SyncProjectPullRequests triggers a fresh pull from GitHub and returns
// the per-call SyncResult so the UI can confirm what changed.
func (h *Handler) SyncProjectPullRequests(w http.ResponseWriter, r *http.Request) {
	project, wsID, ws, ok := h.loadShipProject(w, r)
	if !ok {
		return
	}
	svc, ok := h.shipServiceFromWorkspace(w, r, ws, true)
	if !ok {
		return
	}
	result, err := svc.SyncProject(r.Context(), wsID, project.ID)
	if err != nil {
		// Translate the typed errors so the UI can render something
		// actionable. Auth errors get 401 so the workspace owner can
		// re-enter the token; rate limits get 429 so the UI can back off.
		if errors.Is(err, gh.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "github: invalid or revoked token")
			return
		}
		if errors.Is(err, gh.ErrRateLimited) {
			writeError(w, http.StatusTooManyRequests, "github: rate limit hit, retry shortly")
			return
		}
		writeError(w, http.StatusInternalServerError, "ship hub sync failed: "+err.Error())
		return
	}
	userID := requestUserID(r)
	h.publish(protocol.EventPullRequestSynced, uuidToString(wsID), "member", userID, map[string]any{
		"project_id": uuidToString(project.ID),
		"result":     result,
	})
	writeJSON(w, http.StatusOK, result)
}

// IntrospectWorkspacePipelines is the operator-triggered seed for
// PR5b's dynamic kanban: walks the workspace's projects, runs the PR5a
// phase 2 introspector against each project's primary github_repo
// resource, and persists the result so the kanban renders the
// repo-specific shape instead of the read shim's legacy-enum fallback.
//
// POST /api/workspaces/{id}/ship/introspect-pipelines
//
// Idempotent: re-running re-introspects every project from current
// repo state and overwrites the prior stored config. Per-project
// failures don't block the rest of the workspace — the response
// returns one ProjectIntrospectionResult per project so the operator
// can see exactly what changed and what failed.
//
// Designed as an operator action rather than an auto-running migration
// because:
//   1. The workspace's GitHub token is the auth boundary. Background
//      auto-introspection on every deploy would hit rate limits on
//      large workspaces; on-demand keeps it predictable.
//   2. The kanban-shape change is visible — the operator might
//      legitimately want to wait or batch this with other config
//      edits.
//   3. PR8 will add scheduled + webhook-driven refreshes on top of
//      this same service method; PR8's diff/approve flow is the
//      auto-pilot. This endpoint is the manual seed.
func (h *Handler) IntrospectWorkspacePipelines(w http.ResponseWriter, r *http.Request) {
	wsID, ws, ok := h.requireShipHubEnabled(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, uuidToString(wsID), "workspace not found"); !ok {
		return
	}
	svc, ok := h.shipServiceFromWorkspace(w, r, ws, true)
	if !ok {
		return
	}
	results, err := svc.IntrospectAllWorkspaceProjects(r.Context(), wsID)
	if err != nil {
		// Translate the typed GH errors so the UI can render something
		// actionable. Auth + rate-limit failures hit the workspace
		// itself (token bad, rate limit exhausted), not per-project,
		// so they're whole-call errors.
		if errors.Is(err, gh.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "github: invalid or revoked token")
			return
		}
		if errors.Is(err, gh.ErrRateLimited) {
			writeError(w, http.StatusTooManyRequests, "github: rate limit hit, retry shortly")
			return
		}
		writeError(w, http.StatusInternalServerError, "introspect workspace pipelines failed: "+err.Error())
		return
	}
	// Summary counters so the operator gets a quick "20 introspected,
	// 3 skipped, 1 error" headline without having to read every entry.
	summary := map[string]int{
		"introspected":              0,
		"skipped_no_repo":           0,
		"skipped_no_github_client":  0,
		"error":                     0,
	}
	for _, res := range results {
		summary[res.Status]++
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results,
		"summary": summary,
		"total":   len(results),
	})
}

// ListProjectDeployEnvironments returns the staging/production envs the
// user has configured for a project.
func (h *Handler) ListProjectDeployEnvironments(w http.ResponseWriter, r *http.Request) {
	project, _, _, ok := h.loadShipProject(w, r)
	if !ok {
		return
	}
	envs, err := h.Queries.ListDeployEnvironmentsByProject(r.Context(), project.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list deploy environments")
		return
	}
	out := make([]deployEnvironmentResponse, len(envs))
	for i, e := range envs {
		out[i] = deployEnvironmentToResponse(e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"environments": out})
}

// CreateDeployEnvironmentRequest is the body for POST /api/projects/{id}/deploy_environments.
//
// `DeployWorkflowFilename` is optional; when set, the auto-detect
// deploy poller watches this specific workflow file in the project's
// GitHub repo. When null/empty, the poller falls back to the
// workspace-level `ship_hub_deploy_workflow_<kind>` setting (legacy
// default for single-project workspaces). Per-env override is
// required for multi-project workspaces where each repo's workflow
// filename differs.
type CreateDeployEnvironmentRequest struct {
	Kind                   string  `json:"kind"`
	Name                   string  `json:"name"`
	TargetBranch           *string `json:"target_branch"`
	TargetURL              *string `json:"target_url"`
	AutoPromote            *bool   `json:"auto_promote"`
	DeployWorkflowFilename *string `json:"deploy_workflow_filename"`
	// AutoDeploy — when true and DeployWorkflowFilename is set, the
	// release transition into this env's deploy stage fires the
	// workflow_dispatch on the project's repo. See migration 093 and
	// the PromoteRelease service hook for the dispatch trigger.
	AutoDeploy *bool `json:"auto_deploy"`
}

func (h *Handler) CreateProjectDeployEnvironment(w http.ResponseWriter, r *http.Request) {
	project, wsID, _, ok := h.loadShipProject(w, r)
	if !ok {
		return
	}
	var req CreateDeployEnvironmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	kind, ok := normalizeDeployKind(req.Kind)
	if !ok {
		writeError(w, http.StatusBadRequest, "kind must be 'staging' or 'production'")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	branch := "main"
	if req.TargetBranch != nil && strings.TrimSpace(*req.TargetBranch) != "" {
		branch = strings.TrimSpace(*req.TargetBranch)
	}
	autoPromote := false
	if req.AutoPromote != nil {
		autoPromote = *req.AutoPromote
	}
	autoDeploy := false
	if req.AutoDeploy != nil {
		autoDeploy = *req.AutoDeploy
	}
	env, err := h.Queries.UpsertDeployEnvironment(r.Context(), db.UpsertDeployEnvironmentParams{
		WorkspaceID:            wsID,
		ProjectID:              project.ID,
		Kind:                   kind,
		Name:                   name,
		TargetBranch:           branch,
		TargetUrl:              ptrToText(req.TargetURL),
		AutoPromote:            autoPromote,
		DeployWorkflowFilename: ptrToText(req.DeployWorkflowFilename),
		AutoDeploy:             autoDeploy,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create deploy environment")
		return
	}
	writeJSON(w, http.StatusCreated, deployEnvironmentToResponse(env))
}

// UpdateDeployEnvironmentRequest is the PATCH /api/deploy_environments/{id} body.
// Pointer fields distinguish "not present" from "present and empty"; nil
// leaves the column unchanged.
type UpdateDeployEnvironmentRequest struct {
	Name                   *string `json:"name"`
	TargetBranch           *string `json:"target_branch"`
	TargetURL              *string `json:"target_url"`
	AutoPromote            *bool   `json:"auto_promote"`
	DeployWorkflowFilename *string `json:"deploy_workflow_filename"`
	AutoDeploy             *bool   `json:"auto_deploy"`
}

// loadDeployEnvironment resolves the environment ID URL param and verifies
// it lives in the caller's workspace.
func (h *Handler) loadDeployEnvironment(w http.ResponseWriter, r *http.Request) (db.DeployEnvironment, pgtype.UUID, bool) {
	wsID, _, ok := h.requireShipHubEnabled(w, r)
	if !ok {
		return db.DeployEnvironment{}, pgtype.UUID{}, false
	}
	envUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "environment id")
	if !ok {
		return db.DeployEnvironment{}, pgtype.UUID{}, false
	}
	env, err := h.Queries.GetDeployEnvironmentInWorkspace(r.Context(), db.GetDeployEnvironmentInWorkspaceParams{
		ID: envUUID, WorkspaceID: wsID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "deploy environment not found")
		return db.DeployEnvironment{}, pgtype.UUID{}, false
	}
	return env, wsID, true
}

func (h *Handler) UpdateDeployEnvironment(w http.ResponseWriter, r *http.Request) {
	env, _, ok := h.loadDeployEnvironment(w, r)
	if !ok {
		return
	}
	var req UpdateDeployEnvironmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	updated, err := h.Queries.UpdateDeployEnvironment(r.Context(), db.UpdateDeployEnvironmentParams{
		ID:                     env.ID,
		Name:                   ptrToText(req.Name),
		TargetBranch:           ptrToText(req.TargetBranch),
		TargetUrl:              ptrToText(req.TargetURL),
		AutoPromote:            pgBoolPtr(req.AutoPromote),
		DeployWorkflowFilename: ptrToText(req.DeployWorkflowFilename),
		AutoDeploy:             pgBoolPtr(req.AutoDeploy),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update deploy environment")
		return
	}
	writeJSON(w, http.StatusOK, deployEnvironmentToResponse(updated))
}

// LogDeployRequest is the body for POST /api/deploy_environments/{id}/deploys.
// Manual logging endpoint — used by the user to record a deploy that
// happened outside Multica (e.g. Vercel CLI, GitHub Actions). Webhook
// ingestion lands later.
type LogDeployRequest struct {
	Ref          string  `json:"ref"`
	SHA          string  `json:"sha"`
	Status       string  `json:"status"`
	LogURL       *string `json:"log_url"`
	ErrorMessage *string `json:"error_message"`
}

func (h *Handler) LogDeploy(w http.ResponseWriter, r *http.Request) {
	env, wsID, ok := h.loadDeployEnvironment(w, r)
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req LogDeployRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	status, ok := normalizeDeployStatus(req.Status)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	if strings.TrimSpace(req.SHA) == "" {
		writeError(w, http.StatusBadRequest, "sha is required")
		return
	}
	ref := strings.TrimSpace(req.Ref)
	if ref == "" {
		ref = env.TargetBranch
	}

	creator, _ := h.parseUserUUIDOrZero(userID)
	// Manual logging endpoint, so we don't have separate "started" /
	// "completed" calls to thread through. Synthesize the timestamps from
	// the status: anything past 'pending' has started; terminal states are
	// also completed.
	nowTs := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	startedAt := pgtype.Timestamptz{}
	completedAt := pgtype.Timestamptz{}
	switch status {
	case db.DeployStatusPending:
		// Neither populated.
	case db.DeployStatusInProgress:
		startedAt = nowTs
	case db.DeployStatusSucceeded, db.DeployStatusFailed, db.DeployStatusRolledBack:
		startedAt = nowTs
		completedAt = nowTs
	}

	deploy, err := h.Queries.InsertDeploy(r.Context(), db.InsertDeployParams{
		WorkspaceID:   wsID,
		EnvironmentID: env.ID,
		Ref:           ref,
		Sha:           strings.TrimSpace(req.SHA),
		Status:        status,
		TriggeredBy:   creator,
		StartedAt:     startedAt,
		CompletedAt:   completedAt,
		LogUrl:        ptrToText(req.LogURL),
		ErrorMessage:  ptrToText(req.ErrorMessage),
		// log_deploy endpoint is the public-API write path that users
		// or external CI call to record "this deploy happened." The
		// log_url is provenance if present; otherwise this is a manual
		// assertion through the API.
		Provenance: func() db.DeployProvenance {
			if req.LogURL != nil && *req.LogURL != "" {
				return db.DeployProvenanceWorkflowRun
			}
			return db.DeployProvenanceManualAssertion
		}(),
		ProvenanceRef: ptrToText(req.LogURL),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to log deploy")
		return
	}

	// On success, recompute env.current_sha from the deploy table.
	// Single-writer path; never rolls backwards in time.
	if status == db.DeployStatusSucceeded {
		_, _ = h.Queries.RecomputeEnvCurrentFromDeploys(r.Context(), env.ID)
		// Phase 5 — production deploy storytelling. The manual logging
		// path is the most common way users record "we just shipped"
		// today, so we run the same hook the webhook path runs.
		if env.Kind == db.DeployEnvironmentKindProduction {
			svc := &ship.Service{Q: h.Queries}
			if err := svc.EmitDeployRunbook(r.Context(), wsID, env, deploy); err != nil {
				// Non-fatal — the deploy already landed; the runbook
				// is a nice-to-have.
				_ = err
			}
		}
		// Phase 7c polish — manual deploy logging now triggers the
		// release-staging linkage so a release whose merged_main_sha
		// matches this deploy's sha advances out of in_staging without
		// requiring a GitHub deployment_status webhook (CI providers
		// like Vercel / Netlify / Cloudflare don't fire those by
		// default). Mirrors the webhook code path.
		if status == db.DeployStatusSucceeded && env.Kind == db.DeployEnvironmentKindStaging {
			h.linkStagingDeployForRelease(r.Context(), wsID, deploy.ID, deploy.Sha, "")
		}
	}

	eventType := protocol.EventDeployStarted
	if status == db.DeployStatusSucceeded || status == db.DeployStatusFailed || status == db.DeployStatusRolledBack {
		eventType = protocol.EventDeployCompleted
	}
	h.publish(eventType, uuidToString(wsID), "member", userID, map[string]any{
		"deploy":         deployToResponse(deploy),
		"environment_id": uuidToString(env.ID),
	})
	writeJSON(w, http.StatusCreated, deployToResponse(deploy))
}

// PromoteDeployEnvironment — POST /api/deploy_environments/{id}/promote.
//
// The board-level "Deploy to production" action. Dispatches the env's
// deploy_workflow_filename via workflow_dispatch on its target_branch,
// shipping everything currently merged to main. The Phase B ancestry
// resolver (on the resulting deployment_status webhook) advances every
// release the deploy provably contains — so this single action replaces
// N per-release promotes.
//
// Works for any env kind; the UI exposes it on production. Requires a
// GitHub token (the dispatch call) and workspace membership. A missing
// deploy_workflow_filename is a 400 with a helpful message.
func (h *Handler) PromoteDeployEnvironment(w http.ResponseWriter, r *http.Request) {
	env, wsID, ok := h.loadDeployEnvironment(w, r)
	if !ok {
		return
	}
	ws, err := h.Queries.GetWorkspace(r.Context(), wsID)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, uuidToString(wsID), "workspace not found"); !ok {
		return
	}
	// requireToken=true: dispatching the workflow needs a GitHub token
	// (or the App installation path).
	svc, ok := h.shipServiceFromWorkspace(w, r, ws, true)
	if !ok {
		return
	}
	userID := requestUserID(r)
	actor, _ := h.parseUserUUIDOrZero(userID)
	deps := h.stagingDepsFor(wsID)
	if err := svc.PromoteEnvironment(r.Context(), env, actor, deps); err != nil {
		if errors.Is(err, ship.ErrEnvNoDeployWorkflow) {
			writeError(w, http.StatusBadRequest,
				"this environment has no deploy workflow configured. Set a deploy_workflow_filename in the environment settings to enable one-click deploys.")
			return
		}
		if errors.Is(err, gh.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "github: invalid or revoked token")
			return
		}
		if errors.Is(err, gh.ErrRateLimited) {
			writeError(w, http.StatusTooManyRequests, "github: rate limit hit, retry shortly")
			return
		}
		writeError(w, http.StatusBadGateway, "failed to dispatch deploy workflow: "+err.Error())
		return
	}
	h.publish(protocol.EventDeployStarted, uuidToString(wsID), "member", userID, map[string]any{
		"environment_id": uuidToString(env.ID),
		"promote":        true,
	})
	writeJSON(w, http.StatusOK, map[string]any{"dispatched": true})
}

// ReconcileDeployEnvironmentReleases — POST
// /api/deploy_environments/{id}/reconcile.
//
// Phase C — heals releases stranded by the old exact-SHA-only linkage.
// Production already has the env's current_sha running, but releases
// whose code shipped under an earlier deploy may still sit in
// in_staging / verifying / promoting. This runs the SAME Phase B
// resolver, driven off the env's last-known prod state (current_sha +
// current_deployed_at), advancing every non-terminal release for the
// project merged at-or-before current_deployed_at to in_production / done.
//
// Workspace membership; no GitHub token needed (pure DB reconciliation).
// Returns { "reconciled": <n> } — the number of releases advanced.
func (h *Handler) ReconcileDeployEnvironmentReleases(w http.ResponseWriter, r *http.Request) {
	env, wsID, ok := h.loadDeployEnvironment(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, uuidToString(wsID), "workspace not found"); !ok {
		return
	}
	// Reconciliation needs a known prod SHA + deploy time to anchor the
	// resolver. Without them there's nothing to reconcile against.
	if !env.CurrentSha.Valid || env.CurrentSha.String == "" {
		writeError(w, http.StatusUnprocessableEntity,
			"environment has no current_sha — nothing to reconcile against yet")
		return
	}
	cutoff := time.Now()
	if env.CurrentDeployedAt.Valid {
		cutoff = env.CurrentDeployedAt.Time
	}
	// Find the deploy that produced current_sha so the advanced releases
	// link a real production_deploy_id. Fall back to the zero UUID when
	// no matching deploy row exists (the resolver tolerates it).
	var deployID pgtype.UUID
	if d, err := h.Queries.GetDeployByEnvAndSHA(r.Context(), db.GetDeployByEnvAndSHAParams{
		EnvironmentID: env.ID,
		Sha:           env.CurrentSha.String,
	}); err == nil {
		deployID = d.ID
	} else if !isNotFound(err) {
		slog.Warn("ship: reconcile — get deploy by env+sha failed",
			"env_id", uuidToString(env.ID), "error", err)
	}

	svc := &ship.Service{Q: h.Queries}
	deps := h.stagingDepsFor(wsID)
	reconciled := svc.ResolveReleasesForProdSHA(r.Context(), env.ProjectID, deployID, env.CurrentSha.String, cutoff, deps)
	writeJSON(w, http.StatusOK, map[string]any{"reconciled": reconciled})
}

// ListDeploys returns the most recent deploy attempts for an environment,
// newest first. Default limit 20.
func (h *Handler) ListDeploys(w http.ResponseWriter, r *http.Request) {
	env, _, ok := h.loadDeployEnvironment(w, r)
	if !ok {
		return
	}
	limit := int32(20)
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = int32(n)
		}
	}
	rows, err := h.Queries.ListRecentDeploysByEnvironment(r.Context(), db.ListRecentDeploysByEnvironmentParams{
		EnvironmentID: env.ID,
		Limit:         limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list deploys")
		return
	}
	out := make([]deployResponse, len(rows))
	for i, d := range rows {
		out[i] = deployToResponse(d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"deploys": out, "total": len(out)})
}

// normalizeDeployKind validates the user-supplied kind string against the
// pgtype enum. Returning the typed value lets handlers round-trip it back
// into sqlc params without an extra cast.
func normalizeDeployKind(s string) (db.DeployEnvironmentKind, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "staging":
		return db.DeployEnvironmentKindStaging, true
	case "production":
		return db.DeployEnvironmentKindProduction, true
	default:
		return "", false
	}
}

func normalizeDeployStatus(s string) (db.DeployStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "pending":
		return db.DeployStatusPending, true
	case "in_progress":
		return db.DeployStatusInProgress, true
	case "succeeded":
		return db.DeployStatusSucceeded, true
	case "failed":
		return db.DeployStatusFailed, true
	case "rolled_back":
		return db.DeployStatusRolledBack, true
	default:
		return "", false
	}
}

// attachActiveReleaseToPRList bulk-decorates a slice of
// pullRequestResponse with the ActiveRelease field. Mutates `out` in
// place. Pass the source `db.PullRequest` slice so we can take the
// IDs without re-parsing the response strings.
func (h *Handler) attachActiveReleaseToPRList(ctx context.Context, out []pullRequestResponse, src []db.PullRequest) {
	if len(src) == 0 {
		return
	}
	ids := make([]pgtype.UUID, len(src))
	for i, pr := range src {
		ids[i] = pr.ID
	}
	rows, err := h.Queries.ListActiveReleasesForPullRequests(ctx, ids)
	if err != nil {
		// Non-fatal — the badge just doesn't render.
		return
	}
	byPR := map[string]activeReleaseRef{}
	for _, row := range rows {
		byPR[uuidToString(row.PullRequestID)] = activeReleaseRef{
			ID:    uuidToString(row.ReleaseID),
			Title: row.ReleaseTitle,
			Stage: string(row.ReleaseStage),
		}
	}
	for i := range out {
		if ref, ok := byPR[out[i].ID]; ok {
			c := ref
			out[i].ActiveRelease = &c
		}
	}
}

// pgBoolPtr converts a *bool to pgtype.Bool — nil → Valid=false (leaves
// column unchanged in COALESCE-style updates).
func pgBoolPtr(b *bool) pgtype.Bool {
	if b == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *b, Valid: true}
}
