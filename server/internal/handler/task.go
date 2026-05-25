package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Tasks are issue rows with kind='task'. They share the same backbone as
// issues (assignees, comments, parent/child, project association, activity
// log, inbox notifications, agent runtime) but render through a lighter
// human-centric surface. See migration 116 for the schema split and
// task.sql for the query-layer enforcement.

// TaskResponse is the JSON shape returned for a task. Distinct from
// IssueResponse on purpose:
//   - Tasks have no `number` / `identifier` (migration 117 makes the column
//     nullable; tasks stay NULL).
//   - Tasks have no `priority` — the lighter surface intentionally drops it.
// Other fields mirror IssueResponse so frontend code that handles assignees
// or labels can share helpers across the two surfaces.
type TaskResponse struct {
	ID            string           `json:"id"`
	WorkspaceID   string           `json:"workspace_id"`
	Kind          string           `json:"kind"` // always "task"
	Title         string           `json:"title"`
	Description   *string          `json:"description"`
	Status        string           `json:"status"`
	AssigneeType  *string          `json:"assignee_type"`
	AssigneeID    *string          `json:"assignee_id"`
	CreatorType   string           `json:"creator_type"`
	CreatorID     string           `json:"creator_id"`
	ParentIssueID *string          `json:"parent_issue_id"`
	ProjectID     *string          `json:"project_id"`
	Position      float64          `json:"position"`
	StartDate     *string          `json:"start_date"`
	DueDate       *string          `json:"due_date"`
	CreatedAt     string           `json:"created_at"`
	UpdatedAt     string           `json:"updated_at"`
	Metadata      map[string]any   `json:"metadata"`
	Labels        *[]LabelResponse `json:"labels,omitempty"`
}

// Valid task statuses. The CHECK constraint on issue.status admits the
// broader issue lifecycle (backlog/in_review/done/cancelled/...); tasks
// intentionally use a smaller subset, enforced here at the application
// layer rather than as a kind-aware DB CHECK. See PR-2 design doc: status
// enforcement option A.
var validTaskStatuses = map[string]struct{}{
	"todo":        {},
	"in_progress": {},
	"done":        {},
	"cancelled":   {},
}

func isValidTaskStatus(s string) bool {
	_, ok := validTaskStatuses[s]
	return ok
}

func taskFromIssue(i db.Issue) TaskResponse {
	return TaskResponse{
		ID:            uuidToString(i.ID),
		WorkspaceID:   uuidToString(i.WorkspaceID),
		Kind:          i.Kind,
		Title:         i.Title,
		Description:   textToPtr(i.Description),
		Status:        i.Status,
		AssigneeType:  textToPtr(i.AssigneeType),
		AssigneeID:    uuidToPtr(i.AssigneeID),
		CreatorType:   i.CreatorType,
		CreatorID:     uuidToString(i.CreatorID),
		ParentIssueID: uuidToPtr(i.ParentIssueID),
		ProjectID:     uuidToPtr(i.ProjectID),
		Position:      i.Position,
		StartDate:     timestampToPtr(i.StartDate),
		DueDate:       timestampToPtr(i.DueDate),
		CreatedAt:     timestampToString(i.CreatedAt),
		UpdatedAt:     timestampToString(i.UpdatedAt),
		Metadata:      parseIssueMetadata(i.Metadata),
	}
}

func taskFromListRow(r db.ListTasksRow) TaskResponse {
	return TaskResponse{
		ID:            uuidToString(r.ID),
		WorkspaceID:   uuidToString(r.WorkspaceID),
		Kind:          r.Kind,
		Title:         r.Title,
		Description:   textToPtr(r.Description),
		Status:        r.Status,
		AssigneeType:  textToPtr(r.AssigneeType),
		AssigneeID:    uuidToPtr(r.AssigneeID),
		CreatorType:   r.CreatorType,
		CreatorID:     uuidToString(r.CreatorID),
		ParentIssueID: uuidToPtr(r.ParentIssueID),
		ProjectID:     uuidToPtr(r.ProjectID),
		Position:      r.Position,
		StartDate:     timestampToPtr(r.StartDate),
		DueDate:       timestampToPtr(r.DueDate),
		CreatedAt:     timestampToString(r.CreatedAt),
		UpdatedAt:     timestampToString(r.UpdatedAt),
		Metadata:      parseIssueMetadata(r.Metadata),
	}
}

// loadTaskForUser fetches a task by UUID, enforcing workspace membership
// and the kind='task' discriminator. Mirrors loadIssueForUser's contract
// (writes the error response on failure and returns ok=false) but accepts
// only UUIDs — tasks have no human-readable identifier to resolve.
//
// Per CLAUDE.md's "Backend Handler UUID Parsing Convention": this is the
// single resolution point. Callers must use the returned db.Issue.ID for
// any subsequent write rather than re-parsing the raw URL string.
func (h *Handler) loadTaskForUser(w http.ResponseWriter, r *http.Request, taskID string) (db.Issue, bool) {
	if _, ok := requireUserID(w, r); !ok {
		return db.Issue{}, false
	}
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return db.Issue{}, false
	}
	taskUUID, err := util.ParseUUID(taskID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return db.Issue{}, false
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workspace_id")
		return db.Issue{}, false
	}
	task, err := h.Queries.GetTask(r.Context(), db.GetTaskParams{
		ID:          taskUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return db.Issue{}, false
	}
	return task, true
}

// CreateTaskRequest is the body for POST /tasks. Mirrors CreateIssueRequest
// minus number-bearing and priority-bearing fields.
type CreateTaskRequest struct {
	Title         string  `json:"title"`
	Description   *string `json:"description"`
	Status        string  `json:"status"`
	AssigneeType  *string `json:"assignee_type"`
	AssigneeID    *string `json:"assignee_id"`
	ParentIssueID *string `json:"parent_issue_id"`
	ProjectID     *string `json:"project_id"`
	StartDate     *string `json:"start_date"`
	DueDate       *string `json:"due_date"`
}

// UpdateTaskRequest carries optional fields. A missing field is "no change";
// a present null on assignee_type/id explicitly clears the assignee.
type UpdateTaskRequest struct {
	Title         *string  `json:"title"`
	Description   *string  `json:"description"`
	Status        *string  `json:"status"`
	AssigneeType  *string  `json:"assignee_type"`
	AssigneeID    *string  `json:"assignee_id"`
	Position      *float64 `json:"position"`
	StartDate     *string  `json:"start_date"`
	DueDate       *string  `json:"due_date"`
	ParentIssueID *string  `json:"parent_issue_id"`
	ProjectID     *string  `json:"project_id"`
}

func (h *Handler) CreateTask(w http.ResponseWriter, r *http.Request) {
	var req CreateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	creatorID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	status := req.Status
	if status == "" {
		status = "todo"
	}
	if !isValidTaskStatus(status) {
		writeError(w, http.StatusBadRequest, "invalid task status")
		return
	}

	var assigneeType pgtype.Text
	var assigneeID pgtype.UUID
	if req.AssigneeType != nil {
		assigneeType = pgtype.Text{String: *req.AssigneeType, Valid: true}
	}
	if req.AssigneeID != nil {
		id, ok := parseUUIDOrBadRequest(w, *req.AssigneeID, "assignee_id")
		if !ok {
			return
		}
		assigneeID = id
	}
	if statusCode, msg := h.validateAssigneePair(r.Context(), r, workspaceID, assigneeType, assigneeID); statusCode != 0 {
		writeError(w, statusCode, msg)
		return
	}

	var parentIssueID pgtype.UUID
	if req.ParentIssueID != nil {
		id, ok := parseUUIDOrBadRequest(w, *req.ParentIssueID, "parent_issue_id")
		if !ok {
			return
		}
		// Parent can be either an issue or another task — both live in `issue`.
		// We use GetIssueInWorkspace (kind-agnostic) here because a task's
		// parent legitimately may be either kind.
		parent, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
			ID:          id,
			WorkspaceID: wsUUID,
		})
		if err != nil || !parent.ID.Valid {
			writeError(w, http.StatusBadRequest, "parent not found in this workspace")
			return
		}
		parentIssueID = id
	}

	var projectID pgtype.UUID
	if req.ProjectID != nil {
		id, ok := parseUUIDOrBadRequest(w, *req.ProjectID, "project_id")
		if !ok {
			return
		}
		projectID = id
	}

	startDate, ok := parseOptionalRFC3339(w, req.StartDate, "start_date")
	if !ok {
		return
	}
	dueDate, ok := parseOptionalRFC3339(w, req.DueDate, "due_date")
	if !ok {
		return
	}

	creatorType, actualCreatorID := h.resolveActor(r, creatorID, workspaceID)

	task, err := h.Queries.CreateTask(r.Context(), db.CreateTaskParams{
		WorkspaceID:   wsUUID,
		Title:         req.Title,
		Description:   ptrToText(req.Description),
		Status:        status,
		AssigneeType:  assigneeType,
		AssigneeID:    assigneeID,
		CreatorType:   creatorType,
		CreatorID:     parseUUID(actualCreatorID),
		ParentIssueID: parentIssueID,
		Position:      0,
		StartDate:     startDate,
		DueDate:       dueDate,
		ProjectID:     projectID,
	})
	if err != nil {
		slog.Warn("create task failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to create task: "+err.Error())
		return
	}

	resp := taskFromIssue(task)
	h.publish(protocol.EventUserTaskCreated, workspaceID, creatorType, actualCreatorID, map[string]any{"task": resp})
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) GetTask(w http.ResponseWriter, r *http.Request) {
	task, ok := h.loadTaskForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, taskFromIssue(task))
}

func (h *Handler) ListTasks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var statusFilter pgtype.Text
	if s := r.URL.Query().Get("status"); s != "" {
		statusFilter = pgtype.Text{String: s, Valid: true}
	}
	var assigneeFilter pgtype.UUID
	if a := r.URL.Query().Get("assignee_id"); a != "" {
		id, ok := parseUUIDOrBadRequest(w, a, "assignee_id")
		if !ok {
			return
		}
		assigneeFilter = id
	}
	var creatorFilter pgtype.UUID
	if c := r.URL.Query().Get("creator_id"); c != "" {
		id, ok := parseUUIDOrBadRequest(w, c, "creator_id")
		if !ok {
			return
		}
		creatorFilter = id
	}
	var projectFilter pgtype.UUID
	if p := r.URL.Query().Get("project_id"); p != "" {
		id, ok := parseUUIDOrBadRequest(w, p, "project_id")
		if !ok {
			return
		}
		projectFilter = id
	}
	var parentFilter pgtype.UUID
	if p := r.URL.Query().Get("parent_issue_id"); p != "" {
		id, ok := parseUUIDOrBadRequest(w, p, "parent_issue_id")
		if !ok {
			return
		}
		parentFilter = id
	}

	limit := 100
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil {
			limit = v
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil {
			offset = v
		}
	}

	rows, err := h.Queries.ListTasks(ctx, db.ListTasksParams{
		WorkspaceID:   wsUUID,
		Limit:         int32(limit),
		Offset:        int32(offset),
		Status:        statusFilter,
		AssigneeID:    assigneeFilter,
		CreatorID:     creatorFilter,
		ProjectID:     projectFilter,
		ParentIssueID: parentFilter,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list tasks")
		return
	}

	total, err := h.Queries.CountTasks(ctx, db.CountTasksParams{
		WorkspaceID:   wsUUID,
		Status:        statusFilter,
		AssigneeID:    assigneeFilter,
		CreatorID:     creatorFilter,
		ProjectID:     projectFilter,
		ParentIssueID: parentFilter,
	})
	if err != nil {
		total = int64(len(rows))
	}

	// Reuse the issue-side bulk label loader — labels live on issue.id and
	// tasks are issue rows, so the same query works without modification.
	ids := make([]pgtype.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	labelsMap := h.labelsByIssue(ctx, wsUUID, ids)

	resp := make([]TaskResponse, len(rows))
	for i, row := range rows {
		resp[i] = taskFromListRow(row)
		labels := labelsMap[resp[i].ID]
		if labels == nil {
			labels = []LabelResponse{}
		}
		resp[i].Labels = &labels
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tasks": resp,
		"total": total,
	})
}

func (h *Handler) UpdateTask(w http.ResponseWriter, r *http.Request) {
	task, ok := h.loadTaskForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	var req UpdateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	workspaceID := uuidToString(task.WorkspaceID)
	actorUserID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, actorUserID, workspaceID)

	var title pgtype.Text
	if req.Title != nil {
		title = pgtype.Text{String: *req.Title, Valid: true}
	}
	var description pgtype.Text
	if req.Description != nil {
		description = pgtype.Text{String: *req.Description, Valid: true}
	}
	var status pgtype.Text
	if req.Status != nil {
		if !isValidTaskStatus(*req.Status) {
			writeError(w, http.StatusBadRequest, "invalid task status")
			return
		}
		status = pgtype.Text{String: *req.Status, Valid: true}
	}

	// Assignee semantics: pointer present + non-empty string → set;
	// pointer present + empty string → explicit clear; pointer absent →
	// leave unchanged. The sqlc.narg type makes "leave unchanged" represented
	// as the zero pgtype, so this maps cleanly: a non-Valid pgtype.Text means
	// "do not touch this column" in the UPDATE query.
	var assigneeType pgtype.Text
	var assigneeID pgtype.UUID
	assigneePresent := req.AssigneeType != nil || req.AssigneeID != nil
	if assigneePresent {
		if req.AssigneeType != nil && *req.AssigneeType != "" {
			assigneeType = pgtype.Text{String: *req.AssigneeType, Valid: true}
		}
		if req.AssigneeID != nil && *req.AssigneeID != "" {
			id, ok := parseUUIDOrBadRequest(w, *req.AssigneeID, "assignee_id")
			if !ok {
				return
			}
			assigneeID = id
		}
		if statusCode, msg := h.validateAssigneePair(r.Context(), r, workspaceID, assigneeType, assigneeID); statusCode != 0 {
			writeError(w, statusCode, msg)
			return
		}
	}

	var position pgtype.Float8
	if req.Position != nil {
		position = pgtype.Float8{Float64: *req.Position, Valid: true}
	}

	startDate, ok := parseOptionalRFC3339(w, req.StartDate, "start_date")
	if !ok {
		return
	}
	dueDate, ok := parseOptionalRFC3339(w, req.DueDate, "due_date")
	if !ok {
		return
	}

	var parentIssueID pgtype.UUID
	if req.ParentIssueID != nil && *req.ParentIssueID != "" {
		id, ok := parseUUIDOrBadRequest(w, *req.ParentIssueID, "parent_issue_id")
		if !ok {
			return
		}
		parentIssueID = id
	}
	var projectID pgtype.UUID
	if req.ProjectID != nil && *req.ProjectID != "" {
		id, ok := parseUUIDOrBadRequest(w, *req.ProjectID, "project_id")
		if !ok {
			return
		}
		projectID = id
	}

	updated, err := h.Queries.UpdateTask(r.Context(), db.UpdateTaskParams{
		ID:            task.ID,
		WorkspaceID:   task.WorkspaceID,
		Title:         title,
		Description:   description,
		Status:        status,
		AssigneeType:  assigneeType,
		AssigneeID:    assigneeID,
		Position:      position,
		StartDate:     startDate,
		DueDate:       dueDate,
		ParentIssueID: parentIssueID,
		ProjectID:     projectID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update task")
		return
	}

	resp := taskFromIssue(updated)
	h.publish(protocol.EventUserTaskUpdated, workspaceID, actorType, actorID, map[string]any{"task": resp})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) DeleteTask(w http.ResponseWriter, r *http.Request) {
	task, ok := h.loadTaskForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	workspaceID := uuidToString(task.WorkspaceID)
	actorUserID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, actorUserID, workspaceID)

	if err := h.Queries.DeleteTask(r.Context(), db.DeleteTaskParams{
		ID:          task.ID,
		WorkspaceID: task.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete task")
		return
	}

	h.publish(protocol.EventUserTaskDeleted, workspaceID, actorType, actorID, map[string]any{"task_id": uuidToString(task.ID)})
	w.WriteHeader(http.StatusNoContent)
}

// parseOptionalRFC3339 parses an optional RFC3339 timestamp from a request
// body. A nil pointer or an empty string returns a zero-value Timestamptz
// (Valid=false), which sqlc treats as "no change" / "set null" depending on
// query semantics. A non-empty malformed string writes a 400 and returns
// ok=false. Lives in this file because it's task-specific (the issue handler
// inlines the same parse two-up for create and update).
func parseOptionalRFC3339(w http.ResponseWriter, s *string, field string) (pgtype.Timestamptz, bool) {
	if s == nil || *s == "" {
		return pgtype.Timestamptz{}, true
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid "+field+" format, expected RFC3339")
		return pgtype.Timestamptz{}, false
	}
	return pgtype.Timestamptz{Time: t, Valid: true}, true
}

