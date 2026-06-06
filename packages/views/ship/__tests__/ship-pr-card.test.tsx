import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "../../locales";
import type { PullRequest } from "@multica/core/types";
import { useShipPrDetailStore } from "@multica/core/ship";
import { ShipPRCard } from "../components/ship-pr-card";

// Phase 3 added a chip row inside ShipPRCard whose mutation hooks call
// useWorkspaceId() and useQueryClient(). Mock the workspace id and supply
// a real QueryClient so the card mounts cleanly.
vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

function I18nWrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return (
    <QueryClientProvider client={qc}>
      <I18nProvider locale="en" resources={RESOURCES}>
        {children}
      </I18nProvider>
    </QueryClientProvider>
  );
}

function makePR(overrides: Partial<PullRequest> = {}): PullRequest {
  return {
    id: "pr-1",
    workspace_id: "ws-1",
    project_id: "p-1",
    repo_url: "https://github.com/acme/app",
    number: 1234,
    title: "Memory KB UI",
    state: "open",
    is_draft: false,
    author_login: "alice",
    author_avatar_url: null,
    base_ref: "main",
    head_ref: "feat/x",
    head_sha: "deadbee",
    html_url: "https://github.com/acme/app/pull/1234",
    body: null,
    ci_status: "success",
    review_decision: "",
    mergeable: "MERGEABLE",
    additions: 611,
    deletions: 20,
    changed_files: 25,
    labels: [],
    pr_created_at: "2026-05-01T00:00:00Z",
    pr_updated_at: "2026-05-08T10:00:00Z",
    pr_merged_at: null,
    pr_closed_at: null,
    fetched_at: "2026-05-09T00:00:00Z",
    ...overrides,
  };
}

describe("ShipPRCard", () => {
  it("renders title, PR number, author, and diff stats", () => {
    render(<ShipPRCard pr={makePR()} />, { wrapper: I18nWrapper });
    expect(screen.getByText("Memory KB UI")).toBeInTheDocument();
    expect(screen.getByText("#1234")).toBeInTheDocument();
    expect(screen.getByText("alice")).toBeInTheDocument();
    // The interpolated stats string carries unicode minus from the locale
    // file (`+611 −20`) — match by both segments.
    expect(screen.getByText(/\+611/)).toBeInTheDocument();
    expect(screen.getByText(/25 files/)).toBeInTheDocument();
    // CI passing pill is rendered for ci_status === "success".
    expect(screen.getByText(/CI passing/i)).toBeInTheDocument();
  });

  it("shows a server-classified high-risk badge with reasons", () => {
    // Phase 5 — risk derivation reads `risk_level` / `risk_reasons` off
    // the PR row instead of scanning the title.
    render(
      <ShipPRCard
        pr={makePR({
          risk_level: "high",
          risk_reasons: ["migration file: 083_x.up.sql"],
        })}
      />,
      { wrapper: I18nWrapper },
    );
    expect(screen.getByTestId("risk-badge")).toBeInTheDocument();
    expect(screen.getByText(/High risk/i)).toBeInTheDocument();
  });

  it("renders the conflict warning when mergeable is CONFLICTING", () => {
    render(<ShipPRCard pr={makePR({ mergeable: "CONFLICTING" })} />, {
      wrapper: I18nWrapper,
    });
    expect(screen.getByText(/Merge conflicts/i)).toBeInTheDocument();
  });

  it("renders the Draft pill when is_draft is true", () => {
    render(<ShipPRCard pr={makePR({ is_draft: true })} />, {
      wrapper: I18nWrapper,
    });
    expect(screen.getByText(/Draft/i)).toBeInTheDocument();
  });

  it("opens the PR detail drawer when the card body is clicked", () => {
    // Reset the global store first so a previous test in this file
    // doesn't leak openPrId state across runs.
    useShipPrDetailStore.getState().close();

    render(<ShipPRCard pr={makePR()} />, { wrapper: I18nWrapper });
    const card = screen.getByTestId("ship-pr-card");
    fireEvent.click(card);
    expect(useShipPrDetailStore.getState().openPrId).toBe("pr-1");

    // Cleanup so the next test sees a closed drawer.
    useShipPrDetailStore.getState().close();
  });

  it("does NOT open the drawer when the View diff link is clicked", () => {
    // The chip stops propagation so a click on it doesn't bubble to
    // the card root. This is the load-bearing UX behavior — the user
    // wants the diff fast, not the in-app drawer.
    useShipPrDetailStore.getState().close();
    render(<ShipPRCard pr={makePR()} />, { wrapper: I18nWrapper });
    fireEvent.click(screen.getByTestId("ship-card-view-diff"));
    expect(useShipPrDetailStore.getState().openPrId).toBeNull();
  });

  // Multi-select checkbox visibility — the checkbox only makes sense for
  // PRs that aren't yet part of a release pipeline. Once a PR is locked
  // into a release (any stage, including in_production / done), the
  // checkbox is hidden so it doesn't pile up as visual noise on cards
  // that can't lead anywhere via selection.
  it("renders the selection checkbox for a PR not in any release", () => {
    render(<ShipPRCard pr={makePR()} />, { wrapper: I18nWrapper });
    // The checkbox is always in the DOM for eligible PRs (hidden until
    // hover via opacity classes); only the rendered presence matters here.
    expect(screen.getByTestId("ship-pr-card-checkbox")).toBeInTheDocument();
  });

  it("hides the selection checkbox when the PR is locked into a release", () => {
    // Stage doesn't matter — any active_release pins the PR to that
    // release for the rest of its pipeline. Use `in_production` because
    // that's the user-reported case that prompted this rule.
    render(
      <ShipPRCard
        pr={makePR({
          active_release: {
            id: "rel-1",
            title: "feat release",
            stage: "in_production",
          },
        })}
      />,
      { wrapper: I18nWrapper },
    );
    expect(screen.queryByTestId("ship-pr-card-checkbox")).not.toBeInTheDocument();
    // The release badge would normally render alongside (so the card
    // still tells you which release this PR belongs to) but it requires
    // a workspace slug from the provider; that wiring is tested
    // separately. The load-bearing assertion here is the missing
    // checkbox.
  });

  // Title wrapping — clamp to two lines so a long PR title carrying a
  // ROA-NNN prefix + scope tag still has room for the human-readable
  // summary before clipping. Card heights become mildly variable; columns
  // accept that for legibility.
  it("clamps the title to two lines instead of single-line truncating", () => {
    render(
      <ShipPRCard
        pr={makePR({
          title:
            "feat(memory): hybrid vector + FTS retrieval with RRF blend and HNSW index for the semantic-search rollout [ROA-NNN]",
        })}
      />,
      { wrapper: I18nWrapper },
    );
    const title = screen.getByText(/hybrid vector/);
    // We assert on the utility class rather than the rendered height —
    // jsdom doesn't compute multi-line layout reliably, but the class
    // presence is the load-bearing wiring this test guards.
    expect(title.className).toMatch(/line-clamp-2/);
    expect(title.className).not.toMatch(/\btruncate\b/);
  });

  it("renders a 'View diff' link pointing at /files in a new tab", () => {
    // Phase 6.5 — the deep-link goes to the GitHub Files tab so a
    // reviewer lands on the unified diff rather than the conversation.
    render(<ShipPRCard pr={makePR()} />, { wrapper: I18nWrapper });
    const link = screen.getByTestId("ship-card-view-diff");
    expect(link).toHaveAttribute(
      "href",
      "https://github.com/acme/app/pull/1234/files",
    );
    expect(link).toHaveAttribute("target", "_blank");
    // rel="noopener" prevents the new-tab GitHub page from grabbing
    // window.opener; defensive even when target=_blank already
    // protects most browsers.
    expect(link.getAttribute("rel")).toMatch(/noopener/);
  });

  // PR3 of the Ship Hub rebuild — freshness indicator.
  //
  // The hint renders only when (state === "open") AND
  // (now - fetched_at > 7 minutes). Closed/merged rows don't change,
  // so freshness is meaningless for them; recent fetches stay quiet.
  describe("FreshnessHint", () => {
    const aboveThreshold = () =>
      new Date(Date.now() - 9 * 60 * 1000).toISOString(); // 9 min ago
    const belowThreshold = () =>
      new Date(Date.now() - 2 * 60 * 1000).toISOString(); // 2 min ago

    it("renders the stale hint on an open PR whose fetched_at exceeds the threshold", () => {
      render(
        <ShipPRCard
          pr={makePR({ state: "open", fetched_at: aboveThreshold() })}
        />,
        { wrapper: I18nWrapper },
      );
      // Locate by the marker attribute the component sets — robust to
      // copy changes in the locale file.
      const hint = document.querySelector('[data-stale="true"]');
      expect(hint).toBeTruthy();
      expect(hint?.getAttribute("title") ?? "").toMatch(/refreshed/i);
    });

    it("does not render the hint for a fresh open PR", () => {
      render(
        <ShipPRCard
          pr={makePR({ state: "open", fetched_at: belowThreshold() })}
        />,
        { wrapper: I18nWrapper },
      );
      expect(document.querySelector('[data-stale="true"]')).toBeNull();
    });

    it("does not render the hint for a merged PR even if fetched_at is old", () => {
      // Merged rows don't change; their head SHA isn't interesting and
      // PR2 doesn't refresh them. Showing "stale" on a merged PR would
      // mislead the operator into thinking Sync Now would do something.
      render(
        <ShipPRCard
          pr={makePR({ state: "merged", fetched_at: aboveThreshold() })}
        />,
        { wrapper: I18nWrapper },
      );
      expect(document.querySelector('[data-stale="true"]')).toBeNull();
    });

    it("does not render the hint when fetched_at is empty", () => {
      // Newly-inserted PR rows that haven't been touched by a fetch
      // yet land here. Better to render nothing than guess.
      render(<ShipPRCard pr={makePR({ state: "open", fetched_at: "" })} />, {
        wrapper: I18nWrapper,
      });
      expect(document.querySelector('[data-stale="true"]')).toBeNull();
    });
  });

  // PR9 — production-CD warning.
  describe("auto-deploy warning", () => {
    const WARNING = '[data-testid="ship-pr-card-auto-deploy-warning"]';

    it("warns on an open PR to main when the project auto-deploys", () => {
      render(
        <ShipPRCard pr={makePR({ state: "open", base_ref: "main" })} autoDeploysOnMerge />,
        { wrapper: I18nWrapper },
      );
      expect(document.querySelector(WARNING)).not.toBeNull();
    });

    it("does not warn when the project does not auto-deploy", () => {
      // autoDeploysOnMerge omitted (undefined) — staged / manual repos.
      render(<ShipPRCard pr={makePR({ state: "open", base_ref: "main" })} />, {
        wrapper: I18nWrapper,
      });
      expect(document.querySelector(WARNING)).toBeNull();
    });

    it("does not warn on a merged PR — it has already deployed", () => {
      render(
        <ShipPRCard pr={makePR({ state: "merged", base_ref: "main" })} autoDeploysOnMerge />,
        { wrapper: I18nWrapper },
      );
      expect(document.querySelector(WARNING)).toBeNull();
    });

    it("does not warn on a PR targeting a non-default branch", () => {
      // A PR into a feature branch doesn't trigger production CD.
      render(
        <ShipPRCard pr={makePR({ state: "open", base_ref: "develop" })} autoDeploysOnMerge />,
        { wrapper: I18nWrapper },
      );
      expect(document.querySelector(WARNING)).toBeNull();
    });
  });
});
