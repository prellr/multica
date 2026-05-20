package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func newMCPRequest(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	req := newRequest(method, path, body)
	member, err := testHandler.getWorkspaceMember(req.Context(), testUserID, testWorkspaceID)
	if err != nil {
		t.Fatalf("load workspace member: %v", err)
	}
	return req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, member))
}

func TestMCPServerSecretValueNeverReturned(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test setup unavailable")
	}

	createReq := newMCPRequest(t, http.MethodPost, "/api/mcp-servers", map[string]any{
		"name":      "secret-redaction-test",
		"transport": "sse",
		"url":       "https://example.test/sse",
	})
	createW := httptest.NewRecorder()
	testHandler.CreateMCPServer(createW, createReq)
	if createW.Code != http.StatusCreated {
		t.Fatalf("CreateMCPServer: got %d: %s", createW.Code, createW.Body.String())
	}

	var created MCPServerResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	t.Cleanup(func() {
		_ = testHandler.Queries.DeleteMCPServer(context.Background(), db.DeleteMCPServerParams{
			ID:          parseUUID(created.ID),
			WorkspaceID: parseUUID(testWorkspaceID),
		})
	})

	secretReq := newMCPRequest(t, http.MethodPut, "/api/mcp-servers/"+created.ID+"/secrets/ACCESS_TOKEN", map[string]any{
		"value": "super-secret-token",
	})
	secretReq = withURLParam(secretReq, "serverId", created.ID)
	secretReq = withURLParam(secretReq, "key", "ACCESS_TOKEN")
	secretW := httptest.NewRecorder()
	testHandler.UpsertMCPServerSecret(secretW, secretReq)
	if secretW.Code != http.StatusOK {
		t.Fatalf("UpsertMCPServerSecret: got %d: %s", secretW.Code, secretW.Body.String())
	}
	if strings.Contains(secretW.Body.String(), "super-secret-token") {
		t.Fatal("secret write response leaked plaintext")
	}

	getReq := newMCPRequest(t, http.MethodGet, "/api/mcp-servers/"+created.ID, nil)
	getReq = withURLParam(getReq, "serverId", created.ID)
	getW := httptest.NewRecorder()
	testHandler.GetMCPServer(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("GetMCPServer: got %d: %s", getW.Code, getW.Body.String())
	}
	body := getW.Body.String()
	if strings.Contains(body, "super-secret-token") {
		t.Fatal("get response leaked plaintext secret")
	}
	if !strings.Contains(body, "ACCESS_TOKEN") {
		t.Fatalf("get response did not include secret key name: %s", body)
	}
}
