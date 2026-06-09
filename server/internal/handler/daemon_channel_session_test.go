package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// Regression coverage for the 2026-06-09 server2 migration incident: the
// channel-mention claim path resumed agent CLI sessions produced by a
// DIFFERENT runtime. CLI session transcripts are machine-local state, so a
// cross-runtime resume is guaranteed to fail ("No conversation found with
// session ID: ...") and, because failed channel tasks stay eligible as
// resume sources, every subsequent mention re-failed forever. The issue and
// chat claim paths already had a runtime guard; these tests pin the same
// guard onto the channel path.

// createChannelMentionFixtureTask inserts a channel-mention task row. Channel
// tasks carry channel identity in the JSONB context (all four FK columns are
// NULL); prior completed rows carry the resume pointer in session_id/work_dir.
func createChannelMentionFixtureTask(
	t *testing.T,
	ctx context.Context,
	agentID, runtimeID, channelID, status, sessionID, workDir string,
) string {
	t.Helper()

	contextJSON := fmt.Sprintf(`{
		"type": "channel_mention",
		"workspace_id": %q,
		"channel_id": %q,
		"channel_name": "general",
		"channel_kind": "channel",
		"message_id": %q,
		"message_content": "[@Agent](mention://agent/%s) ping",
		"author_type": "member",
		"author_id": %q
	}`, testWorkspaceID, channelID, uuid.NewString(), agentID, testUserID)

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, status, priority, context,
			session_id, work_dir, completed_at
		)
		VALUES (
			$1, $2, $3, 0, $4::jsonb,
			NULLIF($5, ''), NULLIF($6, ''),
			CASE WHEN $3 = 'completed' THEN now() ELSE NULL END
		)
		RETURNING id
	`, agentID, runtimeID, status, contextJSON, sessionID, workDir).Scan(&taskID); err != nil {
		t.Fatalf("setup: create channel-mention task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	return taskID
}

// claimChannelTaskForTest claims from the given runtime and decodes the
// session-resume fields the daemon uses to drive `claude --resume`.
func claimChannelTaskForTest(t *testing.T, runtimeID string) (*struct {
	ID             string `json:"id"`
	PriorSessionID string `json:"prior_session_id"`
	PriorWorkDir   string `json:"prior_work_dir"`
}, string) {
	t.Helper()

	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil,
		testWorkspaceID, "channel-session-guard-test")
	req = withURLParam(req, "runtimeId", runtimeID)

	testHandler.ClaimTaskByRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Task *struct {
			ID             string `json:"id"`
			PriorSessionID string `json:"prior_session_id"`
			PriorWorkDir   string `json:"prior_work_dir"`
		} `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode claim response: %v", err)
	}
	return resp.Task, w.Body.String()
}

func TestClaimTaskByRuntime_ChannelMentionResumesSameRuntimeSession(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	runtimeID := createClaimReclaimRuntime(t, ctx, "Channel session same-runtime")
	agentID, _ := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "Channel session same-runtime agent")
	channelID := uuid.NewString()

	createChannelMentionFixtureTask(t, ctx, agentID, runtimeID, channelID,
		"completed", "sess-same-runtime", "/tmp/prior-workdir")
	createChannelMentionFixtureTask(t, ctx, agentID, runtimeID, channelID,
		"queued", "", "")

	task, body := claimChannelTaskForTest(t, runtimeID)
	if task == nil {
		t.Fatalf("expected queued channel task to be claimed, got nil: %s", body)
	}
	if task.PriorSessionID != "sess-same-runtime" {
		t.Fatalf("prior_session_id = %q, want %q (same-runtime session must resume)",
			task.PriorSessionID, "sess-same-runtime")
	}
	if task.PriorWorkDir != "/tmp/prior-workdir" {
		t.Fatalf("prior_work_dir = %q, want %q", task.PriorWorkDir, "/tmp/prior-workdir")
	}
}

func TestClaimTaskByRuntime_ChannelMentionSkipsCrossRuntimeSession(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	// The migration scenario: the prior session was produced on the old
	// machine's runtime; the agent has since been re-homed to a new runtime.
	oldRuntimeID := createClaimReclaimRuntime(t, ctx, "Channel session old runtime")
	newRuntimeID := createClaimReclaimRuntime(t, ctx, "Channel session new runtime")
	agentID, _ := createClaimReclaimAgentAndIssue(t, ctx, newRuntimeID, "Channel session cross-runtime agent")
	channelID := uuid.NewString()

	createChannelMentionFixtureTask(t, ctx, agentID, oldRuntimeID, channelID,
		"completed", "sess-old-machine", "/tmp/old-machine-workdir")
	createChannelMentionFixtureTask(t, ctx, agentID, newRuntimeID, channelID,
		"queued", "", "")

	task, body := claimChannelTaskForTest(t, newRuntimeID)
	if task == nil {
		t.Fatalf("expected queued channel task to be claimed, got nil: %s", body)
	}
	if task.PriorSessionID != "" {
		t.Fatalf("prior_session_id = %q, want empty — a session from another runtime is machine-local state and must not be resumed",
			task.PriorSessionID)
	}
	if task.PriorWorkDir != "" {
		t.Fatalf("prior_work_dir = %q, want empty — cross-runtime workdir paths don't exist on this machine",
			task.PriorWorkDir)
	}
}

func TestClaimTaskByRuntime_ChannelMentionForceFreshSkipsResume(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	runtimeID := createClaimReclaimRuntime(t, ctx, "Channel session force-fresh")
	agentID, _ := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "Channel session force-fresh agent")
	channelID := uuid.NewString()

	createChannelMentionFixtureTask(t, ctx, agentID, runtimeID, channelID,
		"completed", "sess-should-not-resume", "/tmp/should-not-reuse")
	queuedID := createChannelMentionFixtureTask(t, ctx, agentID, runtimeID, channelID,
		"queued", "", "")
	if _, err := testPool.Exec(ctx,
		`UPDATE agent_task_queue SET force_fresh_session = true WHERE id = $1`, queuedID); err != nil {
		t.Fatalf("setup: flag force_fresh_session: %v", err)
	}

	task, body := claimChannelTaskForTest(t, runtimeID)
	if task == nil {
		t.Fatalf("expected queued channel task to be claimed, got nil: %s", body)
	}
	if task.PriorSessionID != "" {
		t.Fatalf("prior_session_id = %q, want empty when force_fresh_session is set", task.PriorSessionID)
	}
}
