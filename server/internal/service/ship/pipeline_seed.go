// Package ship — pipeline introspection seed driver.
//
// PR5a phase 2's introspector emits a PipelineConfig from a repo's
// workflows; PR5b's kanban consumes it. This file glues those two
// together: it walks a workspace's projects, runs the introspector
// against each project's primary github_repo resource, and persists
// the result so the kanban renders the operator's repo-specific
// shape instead of the read shim's legacy-enum fallback.
//
// Designed as an idempotent operator-triggered action rather than an
// auto-running migration:
//   - Re-running is safe (the result is recomputed from current repo
//     state each time; old config is overwritten).
//   - Skips projects with no github_repo resource (no introspection
//     possible — those stay on the legacy-enum fallback).
//   - One project's failure doesn't block the rest of the workspace.
//
// The handler at POST /api/workspaces/{id}/ship/introspect-pipelines
// invokes this; PR8 will reuse the same method on a schedule + on
// workflow-file-change webhooks.
package ship

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	gh "github.com/multica-ai/multica/server/pkg/github"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ProjectIntrospectionResult captures one project's outcome from a
// batch introspection. The handler returns a list of these so the
// operator can see exactly which projects got introspected, which
// were skipped (no repo), and which errored.
type ProjectIntrospectionResult struct {
	ProjectID string `json:"project_id"`
	RepoURL   string `json:"repo_url,omitempty"`
	Shape     string `json:"shape,omitempty"`
	// Status is "introspected" | "skipped_no_repo" | "skipped_no_github_client" | "error".
	Status string `json:"status"`
	// Error is populated only when Status == "error". The handler
	// translates auth/rate-limit errors to HTTP status codes; this
	// field carries the human-readable message for everything else.
	Error string `json:"error,omitempty"`
}

// IntrospectAllWorkspaceProjects iterates every non-archived project
// in the workspace, runs IntrospectPipeline against each project's
// primary github_repo resource, and persists the result via
// UpdateProjectPipelineConfig. Returns one result per project so the
// caller can render a summary.
//
// Failures are per-project — one bad repo doesn't stop the rest. The
// only error this function returns is for a workspace-level failure
// (e.g. ListProjects returns DB error).
func (s *Service) IntrospectAllWorkspaceProjects(ctx context.Context, workspaceID pgtype.UUID) ([]ProjectIntrospectionResult, error) {
	if s.Github == nil {
		// Caller is supposed to gate on a GH client being present;
		// defensive guard so we don't crash if the seed is invoked
		// without one.
		return nil, fmt.Errorf("ship: github client not configured")
	}
	projects, err := s.Q.ListProjects(ctx, db.ListProjectsParams{
		WorkspaceID:     workspaceID,
		IncludeArchived: false,
	})
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	results := make([]ProjectIntrospectionResult, 0, len(projects))
	for _, p := range projects {
		results = append(results, s.introspectOneProject(ctx, p))
	}
	return results, nil
}

// IntrospectProjectPipeline runs the introspector against a single
// project. Used by the per-project "Refresh pipeline from repo"
// button that PR8 will add — exported here so PR8 can call it
// without going through the workspace-wide path.
func (s *Service) IntrospectProjectPipeline(ctx context.Context, project db.Project) ProjectIntrospectionResult {
	if s.Github == nil {
		return ProjectIntrospectionResult{
			ProjectID: uuidString(project.ID),
			Status:    "skipped_no_github_client",
			Error:     "no github client configured for workspace",
		}
	}
	return s.introspectOneProject(ctx, project)
}

// introspectOneProject is the per-project workhorse. Finds the primary
// github_repo resource, parses it, calls the introspector, and writes
// the result back via UpdateProjectPipelineConfig.
//
// Multiple github_repo resources on a project are handled by taking
// the FIRST one (matching what the rest of Ship Hub does in
// SyncProject — primary repo = first in resource order). A project
// with no github_repo resource at all is skipped with a clear status.
func (s *Service) introspectOneProject(ctx context.Context, project db.Project) ProjectIntrospectionResult {
	result := ProjectIntrospectionResult{
		ProjectID: uuidString(project.ID),
	}
	resources, err := s.Q.ListProjectResources(ctx, project.ID)
	if err != nil {
		result.Status = "error"
		result.Error = fmt.Sprintf("list project resources: %v", err)
		return result
	}
	repoURL := ""
	for _, res := range resources {
		if res.ResourceType != "github_repo" {
			continue
		}
		u, err := repoURLFromResource(res.ResourceRef)
		if err != nil {
			continue
		}
		repoURL = u
		break
	}
	if repoURL == "" {
		result.Status = "skipped_no_repo"
		return result
	}
	result.RepoURL = repoURL
	owner, repo, err := gh.ParseRepoURL(repoURL)
	if err != nil {
		result.Status = "error"
		result.Error = fmt.Sprintf("parse repo url: %v", err)
		return result
	}
	cfg, err := IntrospectPipeline(ctx, s.Github, owner, repo)
	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
		slog.Warn("ship: introspect project failed",
			"project_id", uuidString(project.ID),
			"repo", owner+"/"+repo,
			"error", err)
		return result
	}
	raw, err := cfg.ToJSON()
	if err != nil {
		result.Status = "error"
		result.Error = fmt.Sprintf("serialize pipeline config: %v", err)
		return result
	}
	if _, err := s.Q.UpdateProjectPipelineConfig(ctx, db.UpdateProjectPipelineConfigParams{
		ID:             project.ID,
		PipelineConfig: raw,
	}); err != nil {
		result.Status = "error"
		result.Error = fmt.Sprintf("persist pipeline config: %v", err)
		return result
	}
	result.Status = "introspected"
	result.Shape = cfg.Shape
	slog.Info("ship: introspected project pipeline",
		"project_id", uuidString(project.ID),
		"repo", owner+"/"+repo,
		"shape", cfg.Shape)
	return result
}
