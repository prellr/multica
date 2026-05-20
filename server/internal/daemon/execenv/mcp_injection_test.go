package execenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInjectMCPServersWritesClaudeConfig(t *testing.T) {
	dir := t.TempDir()
	err := InjectMCPServers([]MCPServerEnvEntry{
		{
			Name:      "square",
			Transport: "sse",
			URL:       "https://mcp.squareup.test/sse",
			Headers:   map[string]string{"Authorization": "Bearer test-token"},
			Allowlist: []string{"orders.search"},
			Required:  true,
			ReadOnly:  true,
		},
		{
			Name:      "local",
			Transport: "stdio",
			Command:   "node",
			Args:      []string{"server.js"},
			Env:       map[string]string{"API_KEY": "test-key"},
		},
	}, dir)
	if err != nil {
		t.Fatalf("InjectMCPServers: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	var cfg map[string]map[string]map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("decode .mcp.json: %v", err)
	}
	if got := cfg["mcpServers"]["square"]["url"]; got != "https://mcp.squareup.test/sse" {
		t.Fatalf("square url = %v", got)
	}
	if got := cfg["mcpServers"]["local"]["command"]; got != "node" {
		t.Fatalf("local command = %v", got)
	}
}

func TestInjectMCPServersRemovesStaleConfigWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InjectMCPServers(nil, dir); err != nil {
		t.Fatalf("InjectMCPServers empty: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected stale .mcp.json removed, got err=%v", err)
	}
}
