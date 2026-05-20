package execenv

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type MCPServerEnvEntry struct {
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

type claudeMCPConfig struct {
	MCPServers map[string]claudeMCPServer `json:"mcpServers"`
}

type claudeMCPServer struct {
	Transport string            `json:"transport,omitempty"`
	URL       string            `json:"url,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Allowlist []string          `json:"allowlist,omitempty"`
	ReadOnly  bool              `json:"read_only,omitempty"`
	Required  bool              `json:"required,omitempty"`
}

func InjectMCPServers(servers []MCPServerEnvEntry, workDir string) error {
	path := filepath.Join(workDir, ".mcp.json")
	if len(servers) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	cfg := claudeMCPConfig{MCPServers: map[string]claudeMCPServer{}}
	for _, server := range servers {
		if server.Name == "" {
			continue
		}
		cfg.MCPServers[server.Name] = claudeMCPServer{
			Transport: server.Transport,
			URL:       server.URL,
			Command:   server.Command,
			Args:      server.Args,
			Env:       server.Env,
			Headers:   server.Headers,
			Allowlist: server.Allowlist,
			ReadOnly:  server.ReadOnly,
			Required:  server.Required,
		}
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}
