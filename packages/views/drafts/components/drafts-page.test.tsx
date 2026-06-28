import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { Draft } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enDrafts from "../../locales/en/drafts.json";

const TEST_RESOURCES = { en: { common: enCommon, drafts: enDrafts } };

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

// Captured nav state — `replace` is what the master-detail selection uses to
// sync ?draft=<id> into the URL. The test reads it to assert selection wiring.
const replaceMock = vi.hoisted(() => vi.fn());
const searchParams = vi.hoisted(() => new URLSearchParams());
vi.mock("../../navigation", () => ({
  AppLink: ({ children, href, ...rest }: any) => (
    <a href={href} {...rest}>{children}</a>
  ),
  useNavigation: () => ({
    push: vi.fn(),
    replace: replaceMock,
    pathname: "/ws/drafts",
    searchParams,
  }),
  NavigationProvider: ({ children }: any) => children,
}));

// Mock the editor pane — it pulls in tiptap (ContentEditor/TitleEditor), which
// is heavy and out of scope for the page-level master-detail test. The page's
// own list + "New draft" behavior is what's under test here.
vi.mock("./draft-editor", () => ({
  DraftEditor: ({ draftId }: { draftId: string }) => (
    <div data-testid="draft-editor">editing {draftId}</div>
  ),
}));

const createMutate = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/drafts", () => ({
  draftListOptions: () => ({
    queryKey: ["drafts", "ws-1", "list"],
    queryFn: () => Promise.resolve(mockDrafts),
    select: (data: any) => data,
  }),
  useCreateDraft: () => ({ mutate: createMutate, isPending: false }),
}));

vi.mock("../../layout/page-header", () => ({
  PageHeader: ({ children, ...rest }: any) => <header {...rest}>{children}</header>,
}));

// react-resizable-panels' useDefaultLayout reads from storage/context we don't
// set up; stub it to a static layout so the page renders deterministically.
vi.mock("react-resizable-panels", async () => {
  const actual = await vi.importActual<any>("react-resizable-panels");
  return {
    ...actual,
    useDefaultLayout: () => ({ defaultLayout: undefined, onLayoutChanged: vi.fn() }),
  };
});

let mockDrafts: Draft[] = [];

function makeDraft(over: Partial<Draft> = {}): Draft {
  return {
    id: "d-1",
    workspace_id: "ws-1",
    owner_user_id: "user-1",
    title: "My first draft",
    body: "# hello",
    status: "draft",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...over,
  };
}

// ---------------------------------------------------------------------------
// Imports under test
// ---------------------------------------------------------------------------

import { DraftsPage } from "./drafts-page";

function renderPage() {
  const qc = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <DraftsPage />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  searchParams.delete("draft");
  mockDrafts = [makeDraft()];
});

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("DraftsPage", () => {
  it("renders the user's drafts in the list", async () => {
    renderPage();
    expect(await screen.findByText("My first draft")).toBeInTheDocument();
  });

  it("shows the select prompt when no draft is selected", async () => {
    renderPage();
    await screen.findByText("My first draft");
    expect(screen.getByText(enDrafts.detail.select_prompt)).toBeInTheDocument();
    expect(screen.queryByTestId("draft-editor")).not.toBeInTheDocument();
  });

  it("selects a draft (syncs ?draft=<id>) when its row is clicked", async () => {
    renderPage();
    fireEvent.click(await screen.findByText("My first draft"));
    expect(replaceMock).toHaveBeenCalledWith("/ws/drafts?draft=d-1");
  });

  it("opens the editor pane for the draft named in ?draft", async () => {
    searchParams.set("draft", "d-1");
    renderPage();
    const editor = await screen.findByTestId("draft-editor");
    expect(editor).toHaveTextContent("editing d-1");
  });

  it("creates a draft via the New draft button", async () => {
    renderPage();
    await screen.findByText("My first draft");
    fireEvent.click(screen.getByRole("button", { name: enDrafts.page.new_draft }));
    expect(createMutate).toHaveBeenCalledTimes(1);
    // Called with an empty create payload (blank draft) and an onSuccess that
    // selects the freshly-created draft.
    const [payload, opts] = createMutate.mock.calls[0]!;
    expect(payload).toEqual({});
    opts.onSuccess(makeDraft({ id: "d-new" }));
    expect(replaceMock).toHaveBeenCalledWith("/ws/drafts?draft=d-new");
  });

  it("renders the empty state with a create affordance when there are no drafts", async () => {
    mockDrafts = [];
    renderPage();
    expect(await screen.findByText(enDrafts.page.empty_title)).toBeInTheDocument();
  });
});
