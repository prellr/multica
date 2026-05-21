package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// shipCmd groups Ship Hub operator actions that don't fit the
// resource-CRUD shape of `project` / `repo`. Today it carries a single
// subcommand — introspect-pipelines — added so the per-repo pipeline
// config (Ship Hub rebuild PR5a/PR5b) can be seeded from the CLI with
// the operator's stored credentials, rather than requiring a hand-built
// curl with a copied session cookie.
var shipCmd = &cobra.Command{
	Use:   "ship",
	Short: "Ship Hub operator actions",
}

var shipIntrospectPipelinesCmd = &cobra.Command{
	Use:   "introspect-pipelines",
	Short: "Detect each project's deploy pipeline shape from its GitHub workflows",
	Long: `Walks every non-archived project in the workspace, inspects each
project's primary github_repo resource (its .github/workflows + optional
.shiphub.yml), and stores the detected pipeline shape so the Ship Hub
kanban renders the columns that match the repo's actual deploy story.

This is the operator-triggered seed for the per-repo dynamic kanban. Safe
to re-run: each project is re-introspected from current repo state and the
stored config is overwritten. Projects with no github_repo resource are
skipped. One project's failure does not block the rest of the workspace.`,
	RunE: runShipIntrospectPipelines,
}

func init() {
	shipCmd.AddCommand(shipIntrospectPipelinesCmd)
	shipIntrospectPipelinesCmd.Flags().String("output", "table", "Output format: table or json")
	shipIntrospectPipelinesCmd.Flags().Bool("full-id", false, "Show full UUIDs in table output")
}

// shipIntrospectResult mirrors ship.ProjectIntrospectionResult on the
// wire. Declared locally (not imported from internal/service/ship) so
// the CLI binary doesn't pull the server package graph.
type shipIntrospectResult struct {
	ProjectID string `json:"project_id"`
	RepoURL   string `json:"repo_url"`
	Shape     string `json:"shape"`
	Status    string `json:"status"`
	Error     string `json:"error"`
}

type shipIntrospectResponse struct {
	Results []shipIntrospectResult `json:"results"`
	Summary map[string]int         `json:"summary"`
	Total   int                    `json:"total"`
}

func runShipIntrospectPipelines(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if client.WorkspaceID == "" {
		return fmt.Errorf("no workspace selected — pass --workspace-id or set one with `multica workspace use`")
	}

	// Introspection fans out one GitHub API round-trip per workflow file
	// per project; a workspace with many repos can legitimately take a
	// while. 3 minutes is generous headroom over the observed worst case
	// while still bounding a hung request.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var resp shipIntrospectResponse
	path := "/api/workspaces/" + client.WorkspaceID + "/ship/introspect-pipelines"
	if err := client.PostJSON(ctx, path, nil, &resp); err != nil {
		return fmt.Errorf("introspect pipelines: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, resp)
	}

	// Headline summary line first — operators want "did it work" before
	// the per-project detail.
	fmt.Fprintf(os.Stdout, "Introspected %d project(s): %d updated, %d skipped (no repo), %d error(s)\n\n",
		resp.Total,
		resp.Summary["introspected"],
		resp.Summary["skipped_no_repo"]+resp.Summary["skipped_no_github_client"],
		resp.Summary["error"],
	)

	fullID, _ := cmd.Flags().GetBool("full-id")
	headers := []string{"PROJECT", "REPO", "SHAPE", "STATUS", "DETAIL"}
	rows := make([][]string, 0, len(resp.Results))
	for _, r := range resp.Results {
		detail := r.Error
		if detail == "" && r.Status == "skipped_no_repo" {
			detail = "no github_repo resource attached"
		}
		rows = append(rows, []string{
			displayID(r.ProjectID, fullID),
			shortRepo(r.RepoURL),
			r.Shape,
			r.Status,
			detail,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)

	// Non-zero exit when any project errored, so a CI / scripted invocation
	// notices. Skips are NOT errors — a project legitimately without a
	// repo is an expected state.
	if resp.Summary["error"] > 0 {
		return fmt.Errorf("%d project(s) failed introspection — see DETAIL column", resp.Summary["error"])
	}
	return nil
}

// shortRepo trims a github repo URL to owner/name for table display.
// "https://github.com/prellr/multica" → "prellr/multica". Leaves any
// non-github or unparseable value untouched.
func shortRepo(repoURL string) string {
	const prefix = "https://github.com/"
	if len(repoURL) > len(prefix) && repoURL[:len(prefix)] == prefix {
		return repoURL[len(prefix):]
	}
	return repoURL
}
