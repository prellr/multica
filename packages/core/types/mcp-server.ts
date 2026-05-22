export interface MCPServer {
  id: string;
  workspace_id: string;
  name: string;
  transport: "stdio" | "sse" | "http";
  url: string | null;
  command: string | null;
  args: string[];
  scope: "workspace" | "agent";
  agent_id: string | null;
  required: boolean;
  read_only: boolean;
  approval_required_for: "none" | "writes";
  last_connected_at: string | null;
  secret_keys: string[];
  tool_allowlist: string[];
  created_at: string;
  updated_at: string;
}

export interface ListMCPServersResponse {
  mcp_servers: MCPServer[];
  total: number;
}

export interface GetMCPServerResponse {
  mcp_server: MCPServer;
}

export interface CreateMCPServerRequest {
  name: string;
  transport: string;
  url?: string | null;
  command?: string | null;
  args?: string[];
  scope?: string;
  agent_id?: string | null;
  required?: boolean;
  read_only?: boolean;
  approval_required_for?: string;
}

export interface UpdateMCPServerRequest {
  name?: string;
  transport?: string;
  url?: string | null;
  command?: string | null;
  args?: string[];
  scope?: string;
  agent_id?: string | null;
  required?: boolean;
  read_only?: boolean;
  approval_required_for?: string;
}

export interface MCPDirectoryEntry {
  id: string;
  name: string;
  slug: string;
  description: string | null;
  transport_types: string[];
  publisher_name: string | null;
  homepage: string | null;
  stars: number;
  last_fetched_at: string;
}

export interface MCPServerDirectoryResponse {
  entries: MCPDirectoryEntry[];
  total: number;
  last_fetched_at: string | null;
}
