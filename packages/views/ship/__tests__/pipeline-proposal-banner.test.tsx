import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "../../locales";
import type { PipelineConfig } from "@multica/core/types";

// Hoisted mock fns — each test wires the mutation behavior it needs.
const { refreshFn, acceptFn, rejectFn } = vi.hoisted(() => ({
  refreshFn: vi.fn(),
  acceptFn: vi.fn(),
  rejectFn: vi.fn(),
}));

vi.mock("@multica/core/ship", () => ({
  useRefreshPipeline: () => ({ mutateAsync: refreshFn, isPending: false }),
  useAcceptPipelineProposal: () => ({ mutateAsync: acceptFn, isPending: false }),
  useRejectPipelineProposal: () => ({ mutateAsync: rejectFn, isPending: false }),
}));

// ApiError is a real class — the banner does `e instanceof ApiError`.
// Defined via vi.hoisted so the class is available both to the hoisted
// vi.mock factory and to the test bodies that throw it.
const { FakeApiError } = vi.hoisted(() => {
  class FakeApiError extends Error {
    status: number;
    body?: unknown;
    constructor(status: number, body?: unknown) {
      super("api error");
      this.name = "ApiError";
      this.status = status;
      this.body = body;
    }
  }
  return { FakeApiError };
});
vi.mock("@multica/core/api", () => ({ ApiError: FakeApiError }));

import { PipelineProposalBanner } from "../components/pipeline-proposal-banner";

function Wrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={RESOURCES}>
      {children}
    </I18nProvider>
  );
}

const currentConfig: PipelineConfig = {
  shape: "staged_strict",
  stages: [
    { id: "in_review", name: "In Review", position: 0 },
    { id: "in_staging", name: "In Staging", position: 1 },
    { id: "done", name: "Done", position: 2, is_terminal: true },
  ],
};

// Proposed config drops the in_staging stage — a destructive change.
const proposedConfig: PipelineConfig = {
  shape: "direct_to_prod",
  stages: [
    { id: "in_review", name: "In Review", position: 0 },
    { id: "done", name: "Done", position: 1, is_terminal: true },
  ],
};

beforeEach(() => {
  refreshFn.mockReset();
  acceptFn.mockReset();
  rejectFn.mockReset();
});

describe("PipelineProposalBanner", () => {
  it("shows only the refresh affordance when there is no proposal", () => {
    render(
      <Wrapper>
        <PipelineProposalBanner
          projectId="p-1"
          pipelineConfig={currentConfig}
        />
      </Wrapper>,
    );
    expect(
      screen.getByText("Refresh pipeline from repo"),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Pipeline change detected"),
    ).not.toBeInTheDocument();
  });

  it("renders the destructive proposal with what changed", () => {
    render(
      <Wrapper>
        <PipelineProposalBanner
          projectId="p-1"
          pipelineConfig={currentConfig}
          pipelineConfigProposed={proposedConfig}
        />
      </Wrapper>,
    );
    expect(screen.getByText("Pipeline change detected")).toBeInTheDocument();
    // Shape change + removed stage are both surfaced.
    expect(
      screen.getByText("Pipeline shape: staged_strict → direct_to_prod"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Removes stage “In Staging”"),
    ).toBeInTheDocument();
  });

  it("calls accept when Accept is clicked", async () => {
    acceptFn.mockResolvedValue({ project_id: "p-1", applied: true });
    render(
      <Wrapper>
        <PipelineProposalBanner
          projectId="p-1"
          pipelineConfig={currentConfig}
          pipelineConfigProposed={proposedConfig}
        />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("Accept"));
    await waitFor(() => expect(acceptFn).toHaveBeenCalledWith("p-1"));
  });

  it("surfaces a 409 block with the affected-release count", async () => {
    acceptFn.mockRejectedValue(
      new FakeApiError(409, {
        affected_release_ids: ["rel-1", "rel-2"],
      }),
    );
    render(
      <Wrapper>
        <PipelineProposalBanner
          projectId="p-1"
          pipelineConfig={currentConfig}
          pipelineConfigProposed={proposedConfig}
        />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("Accept"));
    // Title + description render in a single <p>; match on a substring.
    await waitFor(() =>
      expect(screen.getByText(/Can't apply yet/)).toBeInTheDocument(),
    );
    // The 2 affected releases are interpolated into the description.
    expect(
      screen.getByText(/2 in-flight release/),
    ).toBeInTheDocument();
  });

  it("calls reject when Reject is clicked", async () => {
    rejectFn.mockResolvedValue({ project_id: "p-1", rejected: true });
    render(
      <Wrapper>
        <PipelineProposalBanner
          projectId="p-1"
          pipelineConfig={currentConfig}
          pipelineConfigProposed={proposedConfig}
        />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("Reject"));
    await waitFor(() => expect(rejectFn).toHaveBeenCalledWith("p-1"));
  });

  it("shows the unchanged notice after a no-op refresh", async () => {
    refreshFn.mockResolvedValue({ project_id: "p-1", kind: "unchanged" });
    render(
      <Wrapper>
        <PipelineProposalBanner
          projectId="p-1"
          pipelineConfig={currentConfig}
        />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("Refresh pipeline from repo"));
    await waitFor(() =>
      expect(
        screen.getByText("Pipeline already matches the repo."),
      ).toBeInTheDocument(),
    );
  });
});
