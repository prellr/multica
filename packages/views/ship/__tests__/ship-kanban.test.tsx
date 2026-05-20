import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { DeployEnvironment, PipelineConfig } from "@multica/core/types";
import { RESOURCES } from "../../locales";

const { mockEnvironments } = vi.hoisted(() => ({
  mockEnvironments: { current: [] as DeployEnvironment[] },
}));

vi.mock("@multica/ui/hooks/use-mobile", () => ({
  useIsMobile: () => false,
}));

vi.mock("@multica/core/ship", () => ({
  useDeployEnvironments: () => ({
    data: { environments: mockEnvironments.current },
    isLoading: false,
  }),
  useRecentDeploys: () => ({ data: { deploys: [], total: 0 } }),
}));

vi.mock("../components/ship-pr-card", () => ({
  ShipPRCard: () => null,
}));

import { ShipKanban } from "../components/ship-kanban";

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={RESOURCES}>
      {children}
    </I18nProvider>
  );
}

function env(kind: "staging" | "production"): DeployEnvironment {
  return {
    id: `env-${kind}`,
    workspace_id: "ws-1",
    project_id: "p-1",
    kind,
    name: kind === "staging" ? "Staging" : "Production",
    target_branch: kind === "staging" ? "dev" : "main",
    target_url: null,
    current_sha: null,
    current_deployed_at: null,
    auto_promote: false,
    created_at: "2026-05-12T00:00:00Z",
    updated_at: "2026-05-12T00:00:00Z",
  };
}

function renderKanban(
  pipelineKind?: "staged" | "direct_to_prod",
) {
  return render(
    <ShipKanban
      pullRequests={[]}
      isLoading={false}
      projectId="p-1"
      pipelineKind={pipelineKind}
    />,
    { wrapper: I18nWrapper },
  );
}

// ROA-264 rebuild: column visibility is now derived from the project's
// pipeline_kind, NOT from the presence of deploy environment rows. The
// deploy snapshot is still fetched (for bucketing merged PRs) but no
// longer gates which columns render. `env()` is retained as a fixture
// helper for the snapshot mock.
void env;

describe("ShipKanban column visibility (pipeline-driven)", () => {
  beforeEach(() => {
    mockEnvironments.current = [];
  });

  it("staged → full superset including In Staging then Verifying", () => {
    renderKanban("staged");

    expect(screen.queryByText("Drafted")).toBeInTheDocument();
    expect(screen.queryByText("In Review")).toBeInTheDocument();
    expect(screen.queryByText("Ready to Land")).toBeInTheDocument();
    expect(screen.queryByText("Merged · Pre-Staging")).toBeInTheDocument();
    expect(screen.queryByText("In Staging")).toBeInTheDocument();
    expect(screen.queryByText("Verifying")).toBeInTheDocument();
    expect(screen.queryByText("Promoting")).toBeInTheDocument();
    expect(screen.queryByText("In Production")).toBeInTheDocument();
    expect(screen.queryByText("Done")).toBeInTheDocument();
  });

  it("direct_to_prod → no In Staging, no Verifying, keeps Promoting", () => {
    renderKanban("direct_to_prod");

    expect(screen.queryByText("Drafted")).toBeInTheDocument();
    expect(screen.queryByText("Merged · Pre-Staging")).toBeInTheDocument();
    expect(screen.queryByText("In Staging")).not.toBeInTheDocument();
    expect(screen.queryByText("Verifying")).not.toBeInTheDocument();
    expect(screen.queryByText("Promoting")).toBeInTheDocument();
    expect(screen.queryByText("In Production")).toBeInTheDocument();
    expect(screen.queryByText("Done")).toBeInTheDocument();
  });

  it("undefined pipeline_kind → defaults to the staged superset", () => {
    renderKanban(undefined);

    expect(screen.queryByText("In Staging")).toBeInTheDocument();
    expect(screen.queryByText("Verifying")).toBeInTheDocument();
    expect(screen.queryByText("Promoting")).toBeInTheDocument();
  });
});

// PR5b of the Ship Hub rebuild — when pipelineConfig is provided it
// supersedes the legacy pipelineKind enum, rendering one column per
// stage in `config.stages` order. Unknown stage IDs (library /
// manual_compose shapes' custom stages like image_published) render
// with the operator's stage.name verbatim and an empty bucket until
// future PRs add per-id derivation.
function renderKanbanWithConfig(config: PipelineConfig) {
  return render(
    <ShipKanban
      pullRequests={[]}
      isLoading={false}
      projectId="p-1"
      pipelineConfig={config}
    />,
    { wrapper: I18nWrapper },
  );
}

describe("ShipKanban dynamic columns (pipeline_config-driven)", () => {
  beforeEach(() => {
    mockEnvironments.current = [];
  });

  it("library shape renders the operator's bespoke stage names", () => {
    // Hermes-Multi-style repo: library default config has stages
    // in_review → merged → image_published → done. The custom
    // "image_published" stage has no legacy ShipKanbanColumn equivalent
    // and is expected to render as an empty column with that label.
    renderKanbanWithConfig({
      shape: "library",
      stages: [
        { id: "in_review", name: "In Review", position: 0 },
        { id: "merged", name: "Merged to main", position: 1 },
        { id: "image_published", name: "Image published", position: 2 },
        { id: "done", name: "Done", position: 3, is_terminal: true },
      ],
    });

    expect(screen.queryByText("In Review")).toBeInTheDocument();
    expect(screen.queryByText("Merged to main")).toBeInTheDocument();
    expect(screen.queryByText("Image published")).toBeInTheDocument();
    expect(screen.queryByText("Done")).toBeInTheDocument();
    // Legacy-only columns shouldn't appear when the config explicitly
    // doesn't include them.
    expect(screen.queryByText("In Staging")).not.toBeInTheDocument();
    expect(screen.queryByText("Verifying")).not.toBeInTheDocument();
    expect(screen.queryByText("Promoting")).not.toBeInTheDocument();
  });

  it("manual_compose shape hides all auto-deploy columns", () => {
    // WineryManager / pulse: no GH automation, single "Manually
    // deployed" ack stage. The kanban should NOT walk the operator
    // through staging/promoting steps that don't exist.
    renderKanbanWithConfig({
      shape: "manual_compose",
      stages: [
        { id: "in_review", name: "In Review", position: 0 },
        { id: "merged", name: "Merged to main", position: 1 },
        {
          id: "manually_deployed",
          name: "Manually deployed",
          position: 2,
          requires_human_ack: true,
        },
        { id: "done", name: "Done", position: 3, is_terminal: true },
      ],
    });

    expect(screen.queryByText("Manually deployed")).toBeInTheDocument();
    expect(screen.queryByText("In Staging")).not.toBeInTheDocument();
    expect(screen.queryByText("Promoting")).not.toBeInTheDocument();
    expect(screen.queryByText("In Production")).not.toBeInTheDocument();
  });

  it("pipelineConfig takes precedence over pipelineKind when both are passed", () => {
    // A direct_to_prod project that's been introspected with a
    // staged_strict config (e.g. operator manually overrode via
    // .shiphub.yml) renders the explicit config, not the legacy enum.
    render(
      <ShipKanban
        pullRequests={[]}
        isLoading={false}
        projectId="p-1"
        pipelineKind="direct_to_prod"
        pipelineConfig={{
          shape: "staged_strict",
          stages: [
            { id: "in_review", name: "In Review", position: 0 },
            { id: "in_staging", name: "In Staging", position: 1 },
            { id: "done", name: "Done", position: 2, is_terminal: true },
          ],
        }}
      />,
      { wrapper: I18nWrapper },
    );

    // From the config: yes
    expect(screen.queryByText("In Staging")).toBeInTheDocument();
    // From the legacy enum (would have appeared for direct_to_prod): no
    expect(screen.queryByText("Promoting")).not.toBeInTheDocument();
    expect(screen.queryByText("In Production")).not.toBeInTheDocument();
  });
});
