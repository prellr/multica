import { describe, expect, it } from "vitest";
import {
  DashboardUsageDailyListSchema,
  DeployEnvironmentSchema,
  EMPTY_LIST_DEPLOY_ENVIRONMENTS_RESPONSE,
  ListDeployEnvironmentsResponseSchema,
  ListIssuesResponseSchema,
  PromoteDeployEnvironmentResponseSchema,
} from "./schemas";
import { parseWithFallback } from "./schema";

const baseDeployEnv = {
  id: "env-1",
  workspace_id: "ws-1",
  project_id: "proj-1",
  kind: "production",
  name: "Production",
  target_branch: "main",
  target_url: null,
  current_sha: null,
  current_deployed_at: null,
  auto_promote: false,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

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

describe("DeployEnvironmentSchema (deploy_workflow_inputs)", () => {
  it("parses a flat string→string inputs map", () => {
    const parsed = DeployEnvironmentSchema.parse({
      ...baseDeployEnv,
      deploy_workflow_inputs: { confirm: "deploy-prod", tier: "all" },
    });
    expect(parsed.deploy_workflow_inputs).toEqual({
      confirm: "deploy-prod",
      tier: "all",
    });
  });

  it("accepts null / omitted inputs from older backends without throwing", () => {
    const withNull = DeployEnvironmentSchema.parse({
      ...baseDeployEnv,
      deploy_workflow_inputs: null,
    });
    expect(withNull.deploy_workflow_inputs).toBeNull();

    const { deploy_workflow_inputs: _omit, ...withoutField } = {
      ...baseDeployEnv,
      deploy_workflow_inputs: null,
    };
    // Older backend omits the field entirely — must still parse.
    expect(() => DeployEnvironmentSchema.parse(withoutField)).not.toThrow();
  });

  it("fails validation on a malformed inputs value (non-string member)", () => {
    // A nested object / number where a string is required is exactly the
    // backend-drift shape CLAUDE.md's API compat rules guard against. The
    // field's parse fails, so the whole object fails — which is the trigger
    // for parseWithFallback to return its fallback (asserted below).
    expect(
      DeployEnvironmentSchema.safeParse({
        ...baseDeployEnv,
        deploy_workflow_inputs: { confirm: { nested: true } },
      }).success,
    ).toBe(false);

    expect(
      DeployEnvironmentSchema.safeParse({
        ...baseDeployEnv,
        deploy_workflow_inputs: ["not", "an", "object"],
      }).success,
    ).toBe(false);
  });

  it("falls back gracefully (no throw) when a row's inputs are malformed", () => {
    // End-to-end with the real client wrapper: a malformed
    // deploy_workflow_inputs in the list response must NOT white-screen.
    // parseWithFallback returns the empty fallback instead of throwing.
    const malformed = {
      environments: [
        { ...baseDeployEnv, deploy_workflow_inputs: { confirm: 42 } },
      ],
    };
    const result = parseWithFallback(
      malformed,
      ListDeployEnvironmentsResponseSchema,
      EMPTY_LIST_DEPLOY_ENVIRONMENTS_RESPONSE,
      { endpoint: "GET /api/projects/:id/deploy_environments" },
    );
    expect(result).toEqual(EMPTY_LIST_DEPLOY_ENVIRONMENTS_RESPONSE);
  });
});
