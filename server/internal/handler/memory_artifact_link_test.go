package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// memory_artifact_link handler coverage. The relation graph is the
// substrate piece that unlocks "decision A supersedes decision B"
// and "this artifact spans multiple issues" — both real shapes the
// 2026-05-31 RoastConsole mining surfaced that the single-anchor
// model couldn't express.

// mkLinkableArtifact creates a kind=decision artifact in the test
// workspace and returns its ID. Used as the "source" end of each
// link test below. Cleans itself up via t.Cleanup so tests don't
// bleed state.
func mkLinkableArtifact(t *testing.T, title string) MemoryArtifactResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/memory?workspace_id="+testWorkspaceID, map[string]any{
		"kind": "decision", "title": title, "content": "x",
	})
	testHandler.CreateMemoryArtifact(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create %q: %d %s", title, w.Code, w.Body.String())
	}
	var a MemoryArtifactResponse
	json.NewDecoder(w.Body).Decode(&a)
	t.Cleanup(func() {
		req := newRequest("DELETE", "/api/memory/"+a.ID, nil)
		req = withURLParam(req, "id", a.ID)
		testHandler.DeleteMemoryArtifact(httptest.NewRecorder(), req)
	})
	return a
}

// postLink — small helper that issues POST /api/memory/{id}/links
// and returns the parsed response. Most tests need a setup link;
// extracting the call keeps each test focused on its assertions.
func postLink(t *testing.T, artifactID string, body map[string]any) (MemoryArtifactLinkResponse, int) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/memory/"+artifactID+"/links?workspace_id="+testWorkspaceID, body)
	req = withURLParam(req, "id", artifactID)
	testHandler.CreateMemoryArtifactLink(w, req)
	var resp MemoryArtifactLinkResponse
	if w.Body.Len() > 0 {
		_ = json.NewDecoder(w.Body).Decode(&resp)
	}
	return resp, w.Code
}

func TestMemoryArtifactLink_CreateAndList(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	source := mkLinkableArtifact(t, "Source decision")
	target := mkLinkableArtifact(t, "Target decision")

	// artifact-to-artifact "supersedes" — the killer use case the
	// single-anchor model couldn't express.
	link, code := postLink(t, source.ID, map[string]any{
		"target_type":   "memory_artifact",
		"target_id":     target.ID,
		"relation_type": "supersedes",
	})
	if code != http.StatusCreated {
		t.Fatalf("create link: %d", code)
	}
	if link.TargetType != "memory_artifact" || link.TargetID != target.ID || link.RelationType != "supersedes" {
		t.Fatalf("link round-trip: %+v", link)
	}
	if link.ArtifactID != source.ID {
		t.Errorf("artifact_id: want %s, got %s", source.ID, link.ArtifactID)
	}

	// Outgoing links — the detail page's Links section query.
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/memory/"+source.ID+"/links?workspace_id="+testWorkspaceID, nil)
	req = withURLParam(req, "id", source.ID)
	testHandler.ListMemoryArtifactLinks(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list links: %d %s", w.Code, w.Body.String())
	}
	var listResp struct {
		Links []MemoryArtifactLinkResponse `json:"links"`
	}
	json.NewDecoder(w.Body).Decode(&listResp)
	if len(listResp.Links) != 1 || listResp.Links[0].ID != link.ID {
		t.Fatalf("expected 1 outgoing link with id %s, got %+v", link.ID, listResp.Links)
	}
}

func TestMemoryArtifactLink_Idempotent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	source := mkLinkableArtifact(t, "Source")
	target := mkLinkableArtifact(t, "Target")
	body := map[string]any{
		"target_type":   "memory_artifact",
		"target_id":     target.ID,
		"relation_type": "cites",
	}
	first, firstCode := postLink(t, source.ID, body)
	if firstCode != http.StatusCreated {
		t.Fatalf("first create: %d", firstCode)
	}
	// Same body again — should return the canonical row with 200.
	second, secondCode := postLink(t, source.ID, body)
	if secondCode != http.StatusOK {
		t.Fatalf("re-create: expected 200 (idempotent), got %d", secondCode)
	}
	if second.ID != first.ID {
		t.Errorf("idempotent re-create should return same id; first=%s second=%s", first.ID, second.ID)
	}
}

func TestMemoryArtifactLink_RejectsSelfLink(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	source := mkLinkableArtifact(t, "Self-linker")
	_, code := postLink(t, source.ID, map[string]any{
		"target_type":   "memory_artifact",
		"target_id":     source.ID,
		"relation_type": "cites",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 on self-link, got %d", code)
	}
}

func TestMemoryArtifactLink_RejectsBadTargetType(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	source := mkLinkableArtifact(t, "Source")
	_, code := postLink(t, source.ID, map[string]any{
		"target_type":   "not_a_real_type",
		"target_id":     source.ID, // any uuid — never reached
		"relation_type": "cites",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 on invalid target_type, got %d", code)
	}
}

func TestMemoryArtifactLink_RejectsBadRelationType(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	source := mkLinkableArtifact(t, "Source")
	target := mkLinkableArtifact(t, "Target")
	_, code := postLink(t, source.ID, map[string]any{
		"target_type":   "memory_artifact",
		"target_id":     target.ID,
		"relation_type": "fnord",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 on invalid relation_type, got %d", code)
	}
}

func TestMemoryArtifactLink_TargetByIssueIdentifier(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// Create an issue with an identifier, then link to it via the
	// identifier form ("MUL-N") rather than the UUID. Mirrors the
	// memory_artifact create + by-anchor handling for the primary
	// anchor.
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{"title": "Link target issue"})
	testHandler.CreateIssue(w, req)
	var issue IssueResponse
	json.NewDecoder(w.Body).Decode(&issue)
	t.Cleanup(func() {
		req := newRequest("DELETE", "/api/issues/"+issue.ID, nil)
		req = withURLParam(req, "id", issue.ID)
		testHandler.DeleteIssue(httptest.NewRecorder(), req)
	})
	source := mkLinkableArtifact(t, "Linker")

	link, code := postLink(t, source.ID, map[string]any{
		"target_type":   "issue",
		"target_id":     issue.Identifier, // <- the identifier form
		"relation_type": "cites",
	})
	if code != http.StatusCreated {
		t.Fatalf("create with identifier target: %d", code)
	}
	if link.TargetID != issue.ID {
		t.Errorf("target_id should resolve to issue UUID: got %s want %s", link.TargetID, issue.ID)
	}
}

func TestMemoryArtifactLink_Backlinks(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	source := mkLinkableArtifact(t, "Linker A")
	target := mkLinkableArtifact(t, "Common target")
	source2 := mkLinkableArtifact(t, "Linker B")

	for _, srcID := range []string{source.ID, source2.ID} {
		_, code := postLink(t, srcID, map[string]any{
			"target_type":   "memory_artifact",
			"target_id":     target.ID,
			"relation_type": "cites",
		})
		if code != http.StatusCreated {
			t.Fatalf("seed link from %s: %d", srcID, code)
		}
	}

	// "Who links to <target>?" — both source artifacts should appear.
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/memory/backlinks/memory_artifact/"+target.ID+"?workspace_id="+testWorkspaceID, nil)
	req = withURLParams(req, "targetType", "memory_artifact", "targetId", target.ID)
	testHandler.ListMemoryArtifactBacklinks(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("backlinks: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Links []MemoryArtifactLinkResponse `json:"links"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	gotSources := map[string]bool{}
	for _, l := range resp.Links {
		gotSources[l.ArtifactID] = true
	}
	if !gotSources[source.ID] || !gotSources[source2.ID] {
		t.Fatalf("backlinks should include both source artifacts; got %v", gotSources)
	}
}

func TestMemoryArtifactLink_Delete(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	source := mkLinkableArtifact(t, "Source")
	target := mkLinkableArtifact(t, "Target")
	link, _ := postLink(t, source.ID, map[string]any{
		"target_type":   "memory_artifact",
		"target_id":     target.ID,
		"relation_type": "supersedes",
	})

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/memory/links/"+link.ID+"?workspace_id="+testWorkspaceID, nil)
	req = withURLParam(req, "linkId", link.ID)
	testHandler.DeleteMemoryArtifactLink(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}

	// Verify gone — outgoing list should be empty.
	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/memory/"+source.ID+"/links?workspace_id="+testWorkspaceID, nil)
	req = withURLParam(req, "id", source.ID)
	testHandler.ListMemoryArtifactLinks(w, req)
	var listResp struct {
		Links []MemoryArtifactLinkResponse `json:"links"`
	}
	json.NewDecoder(w.Body).Decode(&listResp)
	if len(listResp.Links) != 0 {
		t.Fatalf("after delete: expected 0 links, got %d", len(listResp.Links))
	}
}
