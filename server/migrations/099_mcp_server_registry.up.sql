-- MCP server registrations
CREATE TABLE mcp_server (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id   UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
  name           TEXT NOT NULL,
  transport      TEXT NOT NULL CHECK (transport IN ('stdio', 'sse', 'http')),
  url            TEXT,
  command        TEXT,
  args           TEXT[],
  scope          TEXT NOT NULL DEFAULT 'workspace' CHECK (scope IN ('workspace', 'agent')),
  agent_id       UUID REFERENCES agent(id) ON DELETE SET NULL,
  required       BOOLEAN NOT NULL DEFAULT false,
  read_only      BOOLEAN NOT NULL DEFAULT false,
  approval_required_for TEXT NOT NULL DEFAULT 'none' CHECK (approval_required_for IN ('none', 'writes')),
  last_connected_at TIMESTAMPTZ,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT mcp_server_name_workspace_unique UNIQUE (workspace_id, name),
  CONSTRAINT mcp_server_transport_fields_check CHECK (
    (transport = 'stdio' AND command IS NOT NULL)
    OR (transport IN ('sse', 'http') AND url IS NOT NULL)
  ),
  CONSTRAINT mcp_server_agent_scope_check CHECK (
    (scope = 'agent' AND agent_id IS NOT NULL)
    OR scope = 'workspace'
  )
);
CREATE INDEX mcp_server_workspace_idx ON mcp_server (workspace_id);
CREATE INDEX mcp_server_agent_idx ON mcp_server (agent_id) WHERE agent_id IS NOT NULL;

-- Encrypted secrets per server (never returned in list output)
CREATE TABLE mcp_server_secret (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  server_id       UUID NOT NULL REFERENCES mcp_server(id) ON DELETE CASCADE,
  key             TEXT NOT NULL,
  value_encrypted TEXT NOT NULL,
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT mcp_server_secret_unique UNIQUE (server_id, key)
);

-- Per-server tool allowlist (empty = all tools allowed)
CREATE TABLE mcp_server_tool_allowlist (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  server_id  UUID NOT NULL REFERENCES mcp_server(id) ON DELETE CASCADE,
  tool_name  TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT mcp_server_tool_allowlist_unique UNIQUE (server_id, tool_name)
);

-- Audit log for every MCP tool call during an agent run
CREATE TABLE mcp_tool_call_log (
  id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id     UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
  server_id        UUID REFERENCES mcp_server(id) ON DELETE SET NULL,
  server_name      TEXT NOT NULL,
  namespaced_tool  TEXT NOT NULL,
  classification   TEXT NOT NULL CHECK (classification IN ('read', 'write')),
  agent_id         UUID,
  run_id           UUID,
  issue_id         UUID,
  channel_id       UUID,
  arguments_json   TEXT,
  result_status    TEXT NOT NULL CHECK (result_status IN ('success', 'error', 'approval_pending', 'approval_denied')),
  approval_status  TEXT CHECK (approval_status IN ('pending', 'approved', 'denied')),
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX mcp_tool_call_log_workspace_idx ON mcp_tool_call_log (workspace_id, created_at DESC);
CREATE INDEX mcp_tool_call_log_server_idx ON mcp_tool_call_log (server_id, created_at DESC) WHERE server_id IS NOT NULL;
