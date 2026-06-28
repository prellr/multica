import { describe, expect, it } from "vitest";
import {
  DashboardUsageDailyListSchema,
  ListIssuesResponseSchema,
  PromoteDeployEnvironmentResponseSchema,
  DraftSchema,
  ListDraftsResponseSchema,
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
