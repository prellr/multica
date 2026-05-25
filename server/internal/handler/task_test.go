package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Tasks are issue rows with kind='task'. PR 1 added the discriminator and
// scoped every broad SELECT to kind='issue'; PR 2 adds the task CRUD surface.
// These tests cover:
//   - TestTaskCRUD: round-trip through create/get/list/update/delete
//   - TestTaskDoesNotLeakIntoListIssues: the central leakage invariant —
//     a task row must never appear in an issue list response
//   - TestTaskStatusValidation: handler rejects statuses outside the
//     task-status subset enforced at the app layer (see isValidTaskStatus)
//   - TestTaskResponseHasNoNumber: the wire contract has no `number` field
//     because tasks have no human-readable identifier (migration 117 makes
//     the column nullable; CreateTask leaves it NULL)

func TestTaskCRUD(t *testing.T) {
	// Create
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/tasks?workspace_id="+testWorkspaceID, map[string]any{
		"title":  "task crud",
		"status": "todo",
	})
	testHandler.CreateTask(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateTask: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created TaskResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Kind != "task" {
		t.Fatalf("CreateTask: expected kind 'task', got %q", created.Kind)
	}
	if created.Status != "todo" {
		t.Fatalf("CreateTask: expected status 'todo', got %q", created.Status)
	}
	taskID := created.ID
	t.Cleanup(func() {
		// Belt and suspenders — the DELETE step below should remove this, but
		// a failing assertion mid-test would otherwise leak the row.
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, taskID)
	})

	// Get
	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/tasks/"+taskID, nil)
	req = withURLParam(req, "id", taskID)
	testHandler.GetTask(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetTask: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var fetched TaskResponse
	if err := json.NewDecoder(w.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if fetched.ID != taskID {
		t.Fatalf("GetTask: expected id %s, got %s", taskID, fetched.ID)
	}

	// Update — partial (status only)
	w = httptest.NewRecorder()
	req = newRequest("PATCH", "/api/tasks/"+taskID, map[string]any{
		"status": "in_progress",
	})
	req = withURLParam(req, "id", taskID)
	testHandler.UpdateTask(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateTask: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updated TaskResponse
	if err := json.NewDecoder(w.Body).Decode(&updated); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if updated.Status != "in_progress" {
		t.Fatalf("UpdateTask: expected status 'in_progress', got %q", updated.Status)
	}
	if updated.Title != "task crud" {
		t.Fatalf("UpdateTask: title should be preserved across partial update, got %q", updated.Title)
	}

	// List — should include our task
	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/tasks?workspace_id="+testWorkspaceID, nil)
	testHandler.ListTasks(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListTasks: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var listResp map[string]any
	json.NewDecoder(w.Body).Decode(&listResp)
	tasks, _ := listResp["tasks"].([]any)
	found := false
	for _, item := range tasks {
		m, _ := item.(map[string]any)
		if m["id"] == taskID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ListTasks: created task %s not present in response", taskID)
	}

	// Delete
	w = httptest.NewRecorder()
	req = newRequest("DELETE", "/api/tasks/"+taskID, nil)
	req = withURLParam(req, "id", taskID)
	testHandler.DeleteTask(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DeleteTask: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Verify deleted
	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/tasks/"+taskID, nil)
	req = withURLParam(req, "id", taskID)
	testHandler.GetTask(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GetTask after delete: expected 404, got %d", w.Code)
	}
}

// TestTaskDoesNotLeakIntoListIssues is the central invariant for the
// kind discriminator: a task row exists in the same table as issues, but
// must never appear in an issue list. If this test ever fails, a query in
// issue.sql has lost its `kind = 'issue'` filter (or the constant
// kindIssue is being passed wrong).
func TestTaskDoesNotLeakIntoListIssues(t *testing.T) {
	// Seed a task.
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/tasks?workspace_id="+testWorkspaceID, map[string]any{
		"title": "leak probe",
	})
	testHandler.CreateTask(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateTask seed: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var task TaskResponse
	json.NewDecoder(w.Body).Decode(&task)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, task.ID)
	})

	// ListIssues — task must NOT appear.
	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/issues?workspace_id="+testWorkspaceID, nil)
	testHandler.ListIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListIssues: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var listResp map[string]any
	json.NewDecoder(w.Body).Decode(&listResp)
	issues, _ := listResp["issues"].([]any)
	for _, item := range issues {
		m, _ := item.(map[string]any)
		if m["id"] == task.ID {
			t.Fatalf("ListIssues leaked task %s — kind filter broken", task.ID)
		}
		if k, _ := m["kind"].(string); k == "task" {
			t.Fatalf("ListIssues returned row with kind=task: %+v", m)
		}
	}

	// ListOpenIssues — same invariant. Task with default status 'todo' is
	// "open" (not in done/cancelled), so a missing kind filter would surface
	// it here too.
	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/issues?workspace_id="+testWorkspaceID+"&open_only=true", nil)
	testHandler.ListIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListOpenIssues: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var openResp map[string]any
	json.NewDecoder(w.Body).Decode(&openResp)
	openIssues, _ := openResp["issues"].([]any)
	for _, item := range openIssues {
		m, _ := item.(map[string]any)
		if m["id"] == task.ID {
			t.Fatalf("ListOpenIssues leaked task %s — kind filter broken", task.ID)
		}
	}
}

func TestTaskStatusValidation(t *testing.T) {
	// Reject a status that's valid for issues but not for tasks.
	for _, badStatus := range []string{"backlog", "in_review", "blocked", "bogus"} {
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/tasks?workspace_id="+testWorkspaceID, map[string]any{
			"title":  "rejected status probe",
			"status": badStatus,
		})
		testHandler.CreateTask(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("CreateTask with status %q: expected 400, got %d: %s", badStatus, w.Code, w.Body.String())
		}
	}

	// Accept a valid status. Cleanup the created row.
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/tasks?workspace_id="+testWorkspaceID, map[string]any{
		"title":  "accepted status probe",
		"status": "in_progress",
	})
	testHandler.CreateTask(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateTask with status 'in_progress': expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var ok TaskResponse
	json.NewDecoder(w.Body).Decode(&ok)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, ok.ID)
	})
}

// TestTaskResponseHasNoNumber confirms the wire contract excludes `number`
// and `identifier`. JSON-marshal the response and verify neither key is
// present — frontend code must not assume tasks have either.
func TestTaskResponseHasNoNumber(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/tasks?workspace_id="+testWorkspaceID, map[string]any{
		"title": "no number probe",
	})
	testHandler.CreateTask(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateTask: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	// Re-decode as a raw map so we can introspect the field set.
	var raw map[string]any
	if err := json.NewDecoder(w.Body).Decode(&raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, ok := raw["number"]; ok {
		t.Errorf("TaskResponse leaked `number` field: %v", raw["number"])
	}
	if _, ok := raw["identifier"]; ok {
		t.Errorf("TaskResponse leaked `identifier` field: %v", raw["identifier"])
	}
	id, _ := raw["id"].(string)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, id)
	})
}
