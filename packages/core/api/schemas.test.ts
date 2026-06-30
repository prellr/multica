import { describe, expect, it } from "vitest";
import {
  DashboardUsageDailyListSchema,
  ListIssuesResponseSchema,
  PromoteDeployEnvironmentResponseSchema,
  DraftSchema,
  ListDraftsResponseSchema,
  DraftAnnotationSchema,
  ListDraftAnnotationsResponseSchema,
  DraftMessageSchema,
  ListDraftMessagesResponseSchema,
  DraftTurnResponseSchema,
} from "./schemas";

const baseIssue = {
  id: "11111111-1111-1111-1111-111111111111",
  workspace_id: "ws-1",
  number: 1,
  identifier: "MUL-1",
  title: "Test",
  description: null,
  status: "todo",
  priority: "medium",
  assignee_type: null,
  assignee_id: null,
  creator_type: "member",
  creator_id: "user-1",
  parent_issue_id: null,
  project_id: null,
  position: 0,
  start_date: null,
  due_date: null,
  metadata: {},
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

describe("IssueSchema (via ListIssuesResponseSchema)", () => {
  it("accepts a primitive metadata KV map", () => {
    const payload = {
      issues: [
        {
          ...baseIssue,
          metadata: { pipeline_status: "waiting", pr_number: 3, is_blocked: true },
        },
      ],
      total: 1,
    };
    const parsed = ListIssuesResponseSchema.parse(payload);
    expect(parsed.issues[0]?.metadata).toEqual({
      pipeline_status: "waiting",
      pr_number: 3,
      is_blocked: true,
    });
  });

  it("defaults metadata to {} when the server omits it (older backend)", () => {
    const { metadata: _omit, ...issueWithoutMetadata } = baseIssue;
    const payload = { issues: [issueWithoutMetadata], total: 1 };
    const parsed = ListIssuesResponseSchema.parse(payload);
    expect(parsed.issues[0]?.metadata).toEqual({});
  });

  it("rejects metadata with non-primitive values (nested object)", () => {
    const payload = {
      issues: [{ ...baseIssue, metadata: { nested: { x: 1 } } }],
      total: 1,
    };
    expect(ListIssuesResponseSchema.safeParse(payload).success).toBe(false);
  });
});

// The workspace dashboard and runtime-detail pages were re-pointed at the
// unified `task_usage_hourly` rollup. Every numeric field drives chart /
// KPI math, and string keys (date / agent_id / model) bucket the series.
// The contract these schemas must hold: a row missing a field degrades
// that field to a sane default rather than dropping the WHOLE array to
// the `[]` fallback — one drifted row must not blank the entire chart.
describe("dashboard + runtime usage schema drift", () => {
  it("coerces a missing numeric field to 0 instead of dropping the array", () => {
    const parsed = DashboardUsageDailyListSchema.parse([
      { date: "2026-05-19", model: "claude-opus-4-7", input_tokens: 100 },
    ]);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.output_tokens).toBe(0);
    expect(parsed[0]?.cache_read_tokens).toBe(0);
    expect(parsed[0]?.cache_write_tokens).toBe(0);
  });

  it("rejects a non-array body so parseWithFallback can return its fallback", () => {
    expect(DashboardUsageDailyListSchema.safeParse(null).success).toBe(false);
  });
});

describe("PromoteDeployEnvironmentResponseSchema (deploy-to-production)", () => {
  it("parses the happy { dispatched: true } body", () => {
    const parsed = PromoteDeployEnvironmentResponseSchema.parse({
      dispatched: true,
    });
    expect(parsed.dispatched).toBe(true);
  });

  it("downgrades a missing 'dispatched' field to false instead of throwing", () => {
    // Backend drift: an older/newer server omits the field. The UI must
    // read 'not dispatched' rather than white-screen.
    const parsed = PromoteDeployEnvironmentResponseSchema.parse({});
    expect(parsed.dispatched).toBe(false);
  });

  it("coerces a wrong-typed 'dispatched' so safeParse can fall back", () => {
    // A wrong type (string instead of boolean) fails closed; the client's
    // parseWithFallback then returns EMPTY_PROMOTE_DEPLOY_ENVIRONMENT_RESPONSE.
    expect(
      PromoteDeployEnvironmentResponseSchema.safeParse({
        dispatched: "yes",
      }).success,
    ).toBe(false);
  });
});

// Drafts is a standalone entity behind parseWithFallback on every read. These
// tests pin the contract the schema must hold (API Response Compatibility):
// a malformed row fails closed so the client returns its fallback, an unknown
// status value parses (open enum / enum-drift), and a missing field is caught.
const baseDraft = {
  id: "33333333-3333-3333-3333-333333333333",
  workspace_id: "ws-1",
  owner_user_id: "user-1",
  title: "Draft title",
  body: "# body",
  status: "draft",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  has_yjs_state: false,
};

describe("DraftSchema / ListDraftsResponseSchema drift", () => {
  it("accepts a well-formed draft and an unknown status value (open enum)", () => {
    // A future server value like "accepted" — or anything the frontend has
    // never seen — must parse, not crash, so the UI can render a generic
    // fallback for it.
    const parsed = DraftSchema.parse({ ...baseDraft, status: "some_future_status" });
    expect(parsed.status).toBe("some_future_status");
  });

  it("rejects a draft missing a required field so parseWithFallback falls back", () => {
    const { title: _omit, ...draftMissingTitle } = baseDraft;
    expect(DraftSchema.safeParse(draftMissingTitle).success).toBe(false);
  });

  it("rejects a wrong-typed body (number) so the client returns EMPTY_DRAFT", () => {
    expect(DraftSchema.safeParse({ ...baseDraft, body: 123 }).success).toBe(false);
  });

  it("defaults a missing has_yjs_state to false (older backend omits it)", () => {
    // An older server that predates the seed-duplication fix omits the field.
    // The editor reads it as "no persisted Yjs state" — the same as a brand-new
    // draft — rather than failing the parse.
    const { has_yjs_state: _omit, ...draftMissingFlag } = baseDraft;
    const parsed = DraftSchema.parse(draftMissingFlag);
    expect(parsed.has_yjs_state).toBe(false);
  });

  it("preserves has_yjs_state=true so the editor suppresses the seed", () => {
    const parsed = DraftSchema.parse({ ...baseDraft, has_yjs_state: true });
    expect(parsed.has_yjs_state).toBe(true);
  });

  it("defaults a null/missing drafts array to [] instead of throwing", () => {
    // An older/newer backend that omits the wrapper array must degrade to an
    // empty list, never a non-iterable that crashes the list page.
    const parsed = ListDraftsResponseSchema.parse({ total: 0 });
    expect(parsed.drafts).toEqual([]);
  });

  it("rejects a non-array 'drafts' so parseWithFallback can return its fallback", () => {
    expect(
      ListDraftsResponseSchema.safeParse({ drafts: "nope", total: 0 }).success,
    ).toBe(false);
  });
});

// Draft annotations (slice 1). The author_type / type / state are open enums;
// slice 2 adds an "agent" author with no schema change. The thread defaults to
// [] (a bare highlight has none, and a drifted backend that omits it must not
// throw). All reads go through parseWithFallback.
const baseAnnotation = {
  id: "44444444-4444-4444-4444-444444444444",
  draft_id: "33333333-3333-3333-3333-333333333333",
  workspace_id: "ws-1",
  author_type: "user",
  author_user_id: "user-1",
  type: "comment",
  quote: "quick brown fox",
  context_before: "The ",
  context_after: " jumps",
  pos_hint: 4,
  state: "open",
  suggestion_before: null,
  suggestion_after: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  messages: [],
};

describe("DraftAnnotationSchema / ListDraftAnnotationsResponseSchema drift", () => {
  it("accepts unknown type / state / author_type values (open enums downgrade, not crash)", () => {
    const parsed = DraftAnnotationSchema.parse({
      ...baseAnnotation,
      type: "some_future_type",
      state: "some_future_state",
      author_type: "agent", // slice-2 value, must parse on a slice-1 client
    });
    expect(parsed.type).toBe("some_future_type");
    expect(parsed.state).toBe("some_future_state");
    expect(parsed.author_type).toBe("agent");
  });

  it("defaults a null/missing messages thread to [] instead of throwing", () => {
    const { messages: _omit, ...annotationNoThread } = baseAnnotation;
    const parsed = DraftAnnotationSchema.parse(annotationNoThread);
    expect(parsed.messages).toEqual([]);
    const parsedNull = DraftAnnotationSchema.parse({ ...baseAnnotation, messages: null });
    expect(parsedNull.messages).toEqual([]);
  });

  it("rejects a wrong-typed pos_hint (string) so the client returns its fallback", () => {
    expect(DraftAnnotationSchema.safeParse({ ...baseAnnotation, pos_hint: "nope" }).success).toBe(false);
  });

  it("rejects an annotation missing a required field so parseWithFallback falls back", () => {
    const { id: _omit, ...annotationMissingId } = baseAnnotation;
    expect(DraftAnnotationSchema.safeParse(annotationMissingId).success).toBe(false);
  });

  it("defaults a null/missing annotations array to [] instead of throwing", () => {
    const parsed = ListDraftAnnotationsResponseSchema.parse({ total: 0 });
    expect(parsed.annotations).toEqual([]);
  });

  it("rejects a non-array 'annotations' so parseWithFallback can return its fallback", () => {
    expect(
      ListDraftAnnotationsResponseSchema.safeParse({ annotations: "nope", total: 0 }).success,
    ).toBe(false);
  });
});

// Draft conversation rail (Rail-1 — the draft-level, un-anchored chat surface).
// GET /api/drafts/:id/messages is consumed by the rail list. The schema must
// survive backend drift: an unknown author_type downgrades, a malformed row /
// non-array messages fails closed so parseWithFallback returns its EMPTY
// fallback rather than white-screening.
const baseMessage = {
  id: "m-1",
  draft_id: "d-1",
  workspace_id: "ws-1",
  author_type: "user",
  author_user_id: "u-1",
  body: "first thought",
  created_at: "2026-01-01T00:00:00Z",
};

describe("DraftMessageSchema / ListDraftMessagesResponseSchema drift", () => {
  it("accepts an unknown author_type value (open enum downgrades, not crash)", () => {
    const parsed = DraftMessageSchema.parse({
      ...baseMessage,
      author_type: "agent", // Rail-2 value, must parse on a Rail-1 client
    });
    expect(parsed.author_type).toBe("agent");
  });

  it("defaults a missing author_user_id to '' instead of throwing", () => {
    const { author_user_id: _omit, ...messageNoAuthor } = baseMessage;
    const parsed = DraftMessageSchema.parse(messageNoAuthor);
    expect(parsed.author_user_id).toBe("");
  });

  it("rejects a wrong-typed body (number) so the client returns its fallback", () => {
    expect(DraftMessageSchema.safeParse({ ...baseMessage, body: 42 }).success).toBe(false);
  });

  it("rejects a message missing a required field so parseWithFallback falls back", () => {
    const { id: _omit, ...messageMissingId } = baseMessage;
    expect(DraftMessageSchema.safeParse(messageMissingId).success).toBe(false);
  });

  it("defaults a null/missing messages array to [] instead of throwing", () => {
    const parsed = ListDraftMessagesResponseSchema.parse({ total: 0 });
    expect(parsed.messages).toEqual([]);
  });

  it("rejects a non-array 'messages' so parseWithFallback can return its fallback", () => {
    expect(
      ListDraftMessagesResponseSchema.safeParse({ messages: "nope", total: 0 }).success,
    ).toBe(false);
  });
});

// Draft turn (slice 2 — the Send-turn). POST /api/drafts/:id/turn is consumed
// by the Send control. The schema is fully optional (every field defaults) so a
// drifted/garbled response degrades to an empty turn rather than throwing into
// the UI — the control reads an empty task_id as "the turn didn't start".
describe("DraftTurnResponseSchema drift", () => {
  it("parses a well-formed turn response", () => {
    const parsed = DraftTurnResponseSchema.parse({
      task_id: "t-1",
      draft_id: "d-1",
      agent_id: "a-1",
      status: "queued",
    });
    expect(parsed.task_id).toBe("t-1");
    expect(parsed.status).toBe("queued");
  });

  it("defaults every field when the server omits them (no white-screen)", () => {
    // A badly-drifted backend returning {} must not throw; the empty fallback
    // is what the client treats as "turn didn't start".
    const parsed = DraftTurnResponseSchema.parse({});
    expect(parsed.task_id).toBe("");
    expect(parsed.status).toBe("");
  });

  it("accepts an unknown status value (open enum)", () => {
    const parsed = DraftTurnResponseSchema.parse({ status: "some_future_status" });
    expect(parsed.status).toBe("some_future_status");
  });

  it("rejects a wrong-typed task_id (number) so parseWithFallback falls back", () => {
    expect(DraftTurnResponseSchema.safeParse({ task_id: 123 }).success).toBe(false);
  });
});
