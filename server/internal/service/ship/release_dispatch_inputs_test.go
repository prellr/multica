package ship

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// pr6InsertDeployEnv inserts a deploy_environment row for the pr6 fixture
// project carrying the given workflow filename + raw JSONB inputs, then
// returns the loaded db.DeployEnvironment. inputsJSON is the literal JSON
// stored in the column ("" → SQL NULL); this lets tests feed both valid
// and deliberately-malformed values.
func pr6InsertDeployEnv(t *testing.T, kind, workflowFile, inputsJSON string) db.DeployEnvironment {
	t.Helper()
	ctx := context.Background()
	var envID pgtype.UUID
	if inputsJSON == "" {
		if err := pr6Pool.QueryRow(ctx, `
			INSERT INTO deploy_environment
				(workspace_id, project_id, kind, name, target_branch, deploy_workflow_filename)
			VALUES ($1, $2, $3, $4, 'main', $5)
			RETURNING id
		`, pr6WorkspaceID, pr6ProjectID, kind, "Env "+kind, workflowFile).Scan(&envID); err != nil {
			t.Fatalf("insert deploy_environment: %v", err)
		}
	} else {
		if err := pr6Pool.QueryRow(ctx, `
			INSERT INTO deploy_environment
				(workspace_id, project_id, kind, name, target_branch, deploy_workflow_filename, deploy_workflow_inputs)
			VALUES ($1, $2, $3, $4, 'main', $5, $6::jsonb)
			RETURNING id
		`, pr6WorkspaceID, pr6ProjectID, kind, "Env "+kind, workflowFile, inputsJSON).Scan(&envID); err != nil {
			t.Fatalf("insert deploy_environment with inputs: %v", err)
		}
	}
	env, err := pr6Queries.GetDeployEnvironment(ctx, envID)
	if err != nil {
		t.Fatalf("get deploy_environment: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pr6Pool.Exec(context.Background(), `DELETE FROM deploy_environment WHERE id = $1`, envID)
	})
	return env
}

// TestPromoteEnvironment_PassesConfiguredInputs verifies the stored
// deploy_workflow_inputs map is unmarshaled and handed to the GitHub
// client's DispatchWorkflow call verbatim — the fix for the 422 a
// required-input workflow returns to an inputless dispatch.
func TestPromoteEnvironment_PassesConfiguredInputs(t *testing.T) {
	if pr6Pool == nil {
		t.Skip("no database; skipping")
	}
	env := pr6InsertDeployEnv(t, "production", "deploy-prod.yml", `{"confirm":"deploy-prod","tier":"all"}`)

	var captured map[string]string
	var calls int
	fake := &fakeGithub{
		dispatchFn: func(_ context.Context, _, _, _, _ string, inputs map[string]string) error {
			calls++
			captured = inputs
			return nil
		},
	}
	svc := &Service{Q: pr6Queries, Github: fake}

	if err := svc.PromoteEnvironment(context.Background(), env, pgtype.UUID{}, nil); err != nil {
		t.Fatalf("PromoteEnvironment: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 dispatch call, got %d", calls)
	}
	want := map[string]string{"confirm": "deploy-prod", "tier": "all"}
	if !reflect.DeepEqual(captured, want) {
		t.Fatalf("dispatched inputs mismatch:\n got %#v\nwant %#v", captured, want)
	}
}

// TestPromoteEnvironment_NilInputsWhenUnset verifies that an env with no
// deploy_workflow_inputs dispatches with nil inputs — unchanged legacy
// behavior, not an empty map.
func TestPromoteEnvironment_NilInputsWhenUnset(t *testing.T) {
	if pr6Pool == nil {
		t.Skip("no database; skipping")
	}
	env := pr6InsertDeployEnv(t, "production", "deploy-prod.yml", "")

	var captured map[string]string
	var captureSet bool
	fake := &fakeGithub{
		dispatchFn: func(_ context.Context, _, _, _, _ string, inputs map[string]string) error {
			captured = inputs
			captureSet = true
			return nil
		},
	}
	svc := &Service{Q: pr6Queries, Github: fake}

	if err := svc.PromoteEnvironment(context.Background(), env, pgtype.UUID{}, nil); err != nil {
		t.Fatalf("PromoteEnvironment: %v", err)
	}
	if !captureSet {
		t.Fatalf("dispatch was never called")
	}
	if captured != nil {
		t.Fatalf("expected nil inputs when unset, got %#v", captured)
	}
}

// TestParseDeployWorkflowInputs_MalformedDegrades verifies a corrupt
// stored value does NOT panic and degrades to nil (no inputs). We feed a
// JSON array (valid jsonb, passes the column's object CHECK only if we
// bypass it — so we exercise parseDeployWorkflowInputs directly on a raw
// non-object byte slice).
func TestParseDeployWorkflowInputs_MalformedDegrades(t *testing.T) {
	cases := [][]byte{
		[]byte(`[1,2,3]`),         // array, not an object
		[]byte(`{"a":{"b":1}}`),   // nested object → not string values
		[]byte(`not json at all`), // garbage
		[]byte(`{}`),              // empty object → no inputs
	}
	for _, raw := range cases {
		got := parseDeployWorkflowInputs(db.DeployEnvironment{DeployWorkflowInputs: raw})
		if got != nil {
			t.Fatalf("parseDeployWorkflowInputs(%s): want nil, got %#v", raw, got)
		}
	}
	// A flat object round-trips.
	got := parseDeployWorkflowInputs(db.DeployEnvironment{DeployWorkflowInputs: []byte(`{"confirm":"x"}`)})
	if got["confirm"] != "x" {
		t.Fatalf("flat object: want confirm=x, got %#v", got)
	}
}
