package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/secrets"
)

type MCPServerEnvData struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	URL       string            `json:"url,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Allowlist []string          `json:"allowlist,omitempty"`
	Required  bool              `json:"required"`
	ReadOnly  bool              `json:"read_only"`
}

type MCPServerResponse struct {
	ID                  string   `json:"id"`
	WorkspaceID         string   `json:"workspace_id"`
	Name                string   `json:"name"`
	Transport           string   `json:"transport"`
	URL                 *string  `json:"url"`
	Command             *string  `json:"command"`
	Args                []string `json:"args"`
	Scope               string   `json:"scope"`
	AgentID             *string  `json:"agent_id"`
	Required            bool     `json:"required"`
	ReadOnly            bool     `json:"read_only"`
	ApprovalRequiredFor string   `json:"approval_required_for"`
	LastConnectedAt     *string  `json:"last_connected_at"`
	SecretKeys          []string `json:"secret_keys"`
	ToolAllowlist       []string `json:"tool_allowlist"`
	CreatedAt           string   `json:"created_at"`
	UpdatedAt           string   `json:"updated_at"`
}

type CreateMCPServerRequest struct {
	Name                string   `json:"name"`
	Transport           string   `json:"transport"`
	URL                 *string  `json:"url"`
	Command             *string  `json:"command"`
	Args                []string `json:"args"`
	Scope               string   `json:"scope"`
	AgentID             *string  `json:"agent_id"`
	Required            bool     `json:"required"`
	ReadOnly            bool     `json:"read_only"`
	ApprovalRequiredFor string   `json:"approval_required_for"`
}

type UpdateMCPServerRequest struct {
	Name                *string  `json:"name"`
	Transport           *string  `json:"transport"`
	URL                 *string  `json:"url"`
	Command             *string  `json:"command"`
	Args                []string `json:"args"`
	Scope               *string  `json:"scope"`
	AgentID             *string  `json:"agent_id"`
	Required            *bool    `json:"required"`
	ReadOnly            *bool    `json:"read_only"`
	ApprovalRequiredFor *string  `json:"approval_required_for"`
}

type SetMCPServerSecretRequest struct {
	Value string `json:"value"`
}

type AddMCPServerToolAllowlistRequest struct {
	ToolName string `json:"tool_name"`
}

func mcpServerToResponse(s db.McpServer, secretKeys, allowlist []string) MCPServerResponse {
	args := s.Args
	if args == nil {
		args = []string{}
	}
	return MCPServerResponse{
		ID:                  uuidToString(s.ID),
		WorkspaceID:         uuidToString(s.WorkspaceID),
		Name:                s.Name,
		Transport:           s.Transport,
		URL:                 textToPtr(s.Url),
		Command:             textToPtr(s.Command),
		Args:                args,
		Scope:               s.Scope,
		AgentID:             uuidToPtr(s.AgentID),
		Required:            s.Required,
		ReadOnly:            s.ReadOnly,
		ApprovalRequiredFor: s.ApprovalRequiredFor,
		LastConnectedAt:     timestampToPtr(s.LastConnectedAt),
		SecretKeys:          secretKeys,
		ToolAllowlist:       allowlist,
		CreatedAt:           timestampToString(s.CreatedAt),
		UpdatedAt:           timestampToString(s.UpdatedAt),
	}
}

func (h *Handler) requireMCPServerWriteAccess(w http.ResponseWriter, r *http.Request) bool {
	member, ok := middleware.MemberFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return false
	}
	if member.Role != "owner" && member.Role != "admin" {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return false
	}
	return true
}

func validateMCPServerFields(w http.ResponseWriter, transport, scope, approval string, url, command *string, agentID *string) bool {
	if transport != "stdio" && transport != "sse" && transport != "http" {
		writeError(w, http.StatusBadRequest, "transport must be stdio, sse, or http")
		return false
	}
	if scope == "" {
		scope = "workspace"
	}
	if scope != "workspace" && scope != "agent" {
		writeError(w, http.StatusBadRequest, "scope must be workspace or agent")
		return false
	}
	if scope == "agent" && (agentID == nil || *agentID == "") {
		writeError(w, http.StatusBadRequest, "agent_id is required when scope is agent")
		return false
	}
	if approval == "" {
		approval = "none"
	}
	if approval != "none" && approval != "writes" {
		writeError(w, http.StatusBadRequest, "approval_required_for must be none or writes")
		return false
	}
	if transport == "stdio" && (command == nil || *command == "") {
		writeError(w, http.StatusBadRequest, "command is required for stdio transport")
		return false
	}
	if (transport == "sse" || transport == "http") && (url == nil || *url == "") {
		writeError(w, http.StatusBadRequest, "url is required for sse/http transport")
		return false
	}
	return true
}

func (h *Handler) loadMCPServerInWorkspace(w http.ResponseWriter, r *http.Request, serverID, workspaceID string) (db.McpServer, bool) {
	serverUUID, ok := parseUUIDOrBadRequest(w, serverID, "mcp server id")
	if !ok {
		return db.McpServer{}, false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return db.McpServer{}, false
	}
	server, err := h.Queries.GetMCPServer(r.Context(), db.GetMCPServerParams{
		ID:          serverUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "mcp server not found")
		return db.McpServer{}, false
	}
	return server, true
}

func (h *Handler) mcpServerSecretKeys(ctxReq *http.Request, serverID pgtype.UUID) []string {
	rows, err := h.Queries.ListMCPServerSecretKeys(ctxReq.Context(), serverID)
	if err != nil {
		return []string{}
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, row.Key)
	}
	return keys
}

func (h *Handler) mcpServerAllowlist(ctxReq *http.Request, serverID pgtype.UUID) []string {
	rows, err := h.Queries.ListMCPServerToolAllowlist(ctxReq.Context(), serverID)
	if err != nil {
		return []string{}
	}
	items := make([]string, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.ToolName)
	}
	return items
}

func (h *Handler) ListMCPServers(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	var name pgtype.Text
	if n := r.URL.Query().Get("name"); n != "" {
		name = pgtype.Text{String: n, Valid: true}
	}
	servers, err := h.Queries.ListMCPServers(r.Context(), db.ListMCPServersParams{WorkspaceID: wsUUID, Name: name})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list mcp servers")
		return
	}

	resp := make([]MCPServerResponse, len(servers))
	for i, server := range servers {
		resp[i] = mcpServerToResponse(server, h.mcpServerSecretKeys(r, server.ID), h.mcpServerAllowlist(r, server.ID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"mcp_servers": resp, "total": len(resp)})
}

func (h *Handler) GetMCPServer(w http.ResponseWriter, r *http.Request) {
	server, ok := h.loadMCPServerInWorkspace(w, r, chi.URLParam(r, "serverId"), h.resolveWorkspaceID(r))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mcp_server": mcpServerToResponse(server, h.mcpServerSecretKeys(r, server.ID), h.mcpServerAllowlist(r, server.ID)),
	})
}

func (h *Handler) CreateMCPServer(w http.ResponseWriter, r *http.Request) {
	if !h.requireMCPServerWriteAccess(w, r) {
		return
	}
	var req CreateMCPServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Scope == "" {
		req.Scope = "workspace"
	}
	if req.ApprovalRequiredFor == "" {
		req.ApprovalRequiredFor = "none"
	}
	if !validateMCPServerFields(w, req.Transport, req.Scope, req.ApprovalRequiredFor, req.URL, req.Command, req.AgentID) {
		return
	}

	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return
	}
	var agentUUID pgtype.UUID
	if req.AgentID != nil && *req.AgentID != "" {
		agentUUID, ok = parseUUIDOrBadRequest(w, *req.AgentID, "agent_id")
		if !ok {
			return
		}
		if _, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: agentUUID, WorkspaceID: wsUUID}); err != nil {
			writeError(w, http.StatusBadRequest, "agent_id must reference an agent in this workspace")
			return
		}
	}

	server, err := h.Queries.InsertMCPServer(r.Context(), db.InsertMCPServerParams{
		WorkspaceID:         wsUUID,
		Name:                req.Name,
		Transport:           req.Transport,
		Url:                 ptrToText(req.URL),
		Command:             ptrToText(req.Command),
		Args:                req.Args,
		Scope:               req.Scope,
		AgentID:             agentUUID,
		Required:            req.Required,
		ReadOnly:            req.ReadOnly,
		ApprovalRequiredFor: req.ApprovalRequiredFor,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create mcp server")
		return
	}
	writeJSON(w, http.StatusCreated, mcpServerToResponse(server, []string{}, []string{}))
}

func (h *Handler) UpdateMCPServer(w http.ResponseWriter, r *http.Request) {
	if !h.requireMCPServerWriteAccess(w, r) {
		return
	}
	server, ok := h.loadMCPServerInWorkspace(w, r, chi.URLParam(r, "serverId"), h.resolveWorkspaceID(r))
	if !ok {
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	var req UpdateMCPServerRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(body, &raw)

	transport := server.Transport
	if req.Transport != nil {
		transport = *req.Transport
	}
	scope := server.Scope
	if req.Scope != nil {
		scope = *req.Scope
	}
	approval := server.ApprovalRequiredFor
	if req.ApprovalRequiredFor != nil {
		approval = *req.ApprovalRequiredFor
	}
	url := textToPtr(server.Url)
	if _, changed := raw["url"]; changed {
		url = req.URL
	}
	command := textToPtr(server.Command)
	if _, changed := raw["command"]; changed {
		command = req.Command
	}
	agentID := uuidToPtr(server.AgentID)
	if _, changed := raw["agent_id"]; changed {
		agentID = req.AgentID
	}
	if !validateMCPServerFields(w, transport, scope, approval, url, command, agentID) {
		return
	}

	params := db.UpdateMCPServerParams{ID: server.ID, WorkspaceID: server.WorkspaceID}
	if req.Name != nil {
		params.Name = pgtype.Text{String: *req.Name, Valid: true}
	}
	if req.Transport != nil {
		params.Transport = pgtype.Text{String: *req.Transport, Valid: true}
	}
	if _, changed := raw["url"]; changed {
		params.UrlSet = pgtype.Bool{Bool: true, Valid: true}
		params.Url = ptrToText(req.URL)
	}
	if _, changed := raw["command"]; changed {
		params.CommandSet = pgtype.Bool{Bool: true, Valid: true}
		params.Command = ptrToText(req.Command)
	}
	if _, changed := raw["args"]; changed {
		params.ArgsSet = pgtype.Bool{Bool: true, Valid: true}
		params.Args = req.Args
	}
	if req.Scope != nil {
		params.Scope = pgtype.Text{String: *req.Scope, Valid: true}
	}
	if _, changed := raw["agent_id"]; changed {
		params.AgentIDSet = pgtype.Bool{Bool: true, Valid: true}
		if req.AgentID != nil && *req.AgentID != "" {
			agentUUID, ok := parseUUIDOrBadRequest(w, *req.AgentID, "agent_id")
			if !ok {
				return
			}
			if _, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: agentUUID, WorkspaceID: server.WorkspaceID}); err != nil {
				writeError(w, http.StatusBadRequest, "agent_id must reference an agent in this workspace")
				return
			}
			params.AgentID = agentUUID
		}
	}
	if req.Required != nil {
		params.Required = pgtype.Bool{Bool: *req.Required, Valid: true}
	}
	if req.ReadOnly != nil {
		params.ReadOnly = pgtype.Bool{Bool: *req.ReadOnly, Valid: true}
	}
	if req.ApprovalRequiredFor != nil {
		params.ApprovalRequiredFor = pgtype.Text{String: *req.ApprovalRequiredFor, Valid: true}
	}

	updated, err := h.Queries.UpdateMCPServer(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update mcp server")
		return
	}
	writeJSON(w, http.StatusOK, mcpServerToResponse(updated, h.mcpServerSecretKeys(r, updated.ID), h.mcpServerAllowlist(r, updated.ID)))
}

func (h *Handler) DeleteMCPServer(w http.ResponseWriter, r *http.Request) {
	if !h.requireMCPServerWriteAccess(w, r) {
		return
	}
	server, ok := h.loadMCPServerInWorkspace(w, r, chi.URLParam(r, "serverId"), h.resolveWorkspaceID(r))
	if !ok {
		return
	}
	if err := h.Queries.DeleteMCPServer(r.Context(), db.DeleteMCPServerParams{ID: server.ID, WorkspaceID: server.WorkspaceID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete mcp server")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListMCPServerSecretKeys(w http.ResponseWriter, r *http.Request) {
	server, ok := h.loadMCPServerInWorkspace(w, r, chi.URLParam(r, "serverId"), h.resolveWorkspaceID(r))
	if !ok {
		return
	}
	rows, err := h.Queries.ListMCPServerSecretKeys(r.Context(), server.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list mcp server secrets")
		return
	}
	type secretKeyResponse struct {
		ID        string `json:"id"`
		ServerID  string `json:"server_id"`
		Key       string `json:"key"`
		UpdatedAt string `json:"updated_at"`
	}
	resp := make([]secretKeyResponse, len(rows))
	for i, row := range rows {
		resp[i] = secretKeyResponse{ID: uuidToString(row.ID), ServerID: uuidToString(row.ServerID), Key: row.Key, UpdatedAt: timestampToString(row.UpdatedAt)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"secrets": resp, "total": len(resp)})
}

func (h *Handler) UpsertMCPServerSecret(w http.ResponseWriter, r *http.Request) {
	if !h.requireMCPServerWriteAccess(w, r) {
		return
	}
	server, ok := h.loadMCPServerInWorkspace(w, r, chi.URLParam(r, "serverId"), h.resolveWorkspaceID(r))
	if !ok {
		return
	}
	key := chi.URLParam(r, "key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	var req SetMCPServerSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	encrypted, err := secrets.EncryptString(req.Value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encrypt secret")
		return
	}
	row, err := h.Queries.UpsertMCPServerSecret(r.Context(), db.UpsertMCPServerSecretParams{
		ServerID:       server.ID,
		Key:            key,
		ValueEncrypted: base64.StdEncoding.EncodeToString(encrypted),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store mcp server secret")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"secret": map[string]any{
			"id":         uuidToString(row.ID),
			"server_id":  uuidToString(row.ServerID),
			"key":        row.Key,
			"updated_at": timestampToString(row.UpdatedAt),
		},
	})
}

func (h *Handler) DeleteMCPServerSecret(w http.ResponseWriter, r *http.Request) {
	if !h.requireMCPServerWriteAccess(w, r) {
		return
	}
	server, ok := h.loadMCPServerInWorkspace(w, r, chi.URLParam(r, "serverId"), h.resolveWorkspaceID(r))
	if !ok {
		return
	}
	if err := h.Queries.DeleteMCPServerSecret(r.Context(), db.DeleteMCPServerSecretParams{ServerID: server.ID, Key: chi.URLParam(r, "key")}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete mcp server secret")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListMCPServerToolAllowlist(w http.ResponseWriter, r *http.Request) {
	server, ok := h.loadMCPServerInWorkspace(w, r, chi.URLParam(r, "serverId"), h.resolveWorkspaceID(r))
	if !ok {
		return
	}
	rows, err := h.Queries.ListMCPServerToolAllowlist(r.Context(), server.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list mcp server tool allowlist")
		return
	}
	items := make([]string, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.ToolName)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tool_allowlist": items, "total": len(items)})
}

func (h *Handler) AddMCPServerToolAllowlistEntry(w http.ResponseWriter, r *http.Request) {
	if !h.requireMCPServerWriteAccess(w, r) {
		return
	}
	server, ok := h.loadMCPServerInWorkspace(w, r, chi.URLParam(r, "serverId"), h.resolveWorkspaceID(r))
	if !ok {
		return
	}
	var req AddMCPServerToolAllowlistRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ToolName == "" {
		writeError(w, http.StatusBadRequest, "tool_name is required")
		return
	}
	if _, err := h.Queries.UpsertMCPServerToolAllowlist(r.Context(), db.UpsertMCPServerToolAllowlistParams{ServerID: server.ID, ToolName: req.ToolName}); err != nil && err != pgx.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "failed to update mcp server tool allowlist")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tool_allowlist": h.mcpServerAllowlist(r, server.ID)})
}

func (h *Handler) RemoveMCPServerToolAllowlistEntry(w http.ResponseWriter, r *http.Request) {
	if !h.requireMCPServerWriteAccess(w, r) {
		return
	}
	server, ok := h.loadMCPServerInWorkspace(w, r, chi.URLParam(r, "serverId"), h.resolveWorkspaceID(r))
	if !ok {
		return
	}
	if err := h.Queries.DeleteMCPServerToolAllowlistEntry(r.Context(), db.DeleteMCPServerToolAllowlistEntryParams{ServerID: server.ID, ToolName: chi.URLParam(r, "toolName")}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove mcp server tool allowlist entry")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) TestMCPServer(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.loadMCPServerInWorkspace(w, r, chi.URLParam(r, "serverId"), h.resolveWorkspaceID(r)); !ok {
		return
	}
	writeError(w, http.StatusNotImplemented, "mcp server test is not implemented yet")
}

func (h *Handler) loadMCPServersForClaim(ctx context.Context, workspaceID, agentID pgtype.UUID) []MCPServerEnvData {
	servers, err := h.Queries.ListMCPServersForAgent(ctx, db.ListMCPServersForAgentParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
	})
	if err != nil || len(servers) == 0 {
		return nil
	}
	out := make([]MCPServerEnvData, 0, len(servers))
	for _, server := range servers {
		entry := MCPServerEnvData{
			Name:      server.Name,
			Transport: server.Transport,
			URL:       server.Url.String,
			Command:   server.Command.String,
			Args:      server.Args,
			Required:  server.Required,
			ReadOnly:  server.ReadOnly,
			Allowlist: []string{},
		}
		if allowlist, err := h.Queries.ListMCPServerToolAllowlist(ctx, server.ID); err == nil {
			for _, row := range allowlist {
				entry.Allowlist = append(entry.Allowlist, row.ToolName)
			}
		}
		if secretRows, err := h.Queries.ListMCPServerSecretsForServer(ctx, server.ID); err == nil {
			for _, row := range secretRows {
				ciphertext, decErr := base64.StdEncoding.DecodeString(row.ValueEncrypted)
				if decErr != nil {
					continue
				}
				plaintext, decErr := secrets.DecryptString(ciphertext)
				if decErr != nil {
					continue
				}
				if server.Transport == "stdio" {
					if entry.Env == nil {
						entry.Env = map[string]string{}
					}
					entry.Env[row.Key] = plaintext
				} else {
					if entry.Headers == nil {
						entry.Headers = map[string]string{}
					}
					key := row.Key
					if strings.EqualFold(key, "access_token") || strings.EqualFold(key, "token") || strings.EqualFold(key, "bearer_token") {
						entry.Headers["Authorization"] = "Bearer " + plaintext
					} else {
						entry.Headers[key] = plaintext
					}
				}
			}
		}
		out = append(out, entry)
	}
	return out
}
