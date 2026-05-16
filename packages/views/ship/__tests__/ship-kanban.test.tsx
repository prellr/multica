import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { DeployEnvironment } from "@multica/core/types";
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
