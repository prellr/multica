import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const mcpServerKeys = {
  all: (wsId: string) => ["mcp-servers", wsId] as const,
  list: (wsId: string) => [...mcpServerKeys.all(wsId), "list"] as const,
  detail: (wsId: string, id: string) =>
    [...mcpServerKeys.all(wsId), "detail", id] as const,
  directory: (params: { q: string; transport: string }) =>
    ["mcp-server-directory", params] as const,
};

export function mcpServerListOptions(wsId: string) {
  return queryOptions({
    queryKey: mcpServerKeys.list(wsId),
    queryFn: () => api.listMCPServers(),
    select: (data) => data.mcp_servers ?? [],
  });
}

export function mcpServerDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: mcpServerKeys.detail(wsId, id),
    queryFn: () => api.getMCPServer(id),
    select: (data) => data.mcp_server,
  });
}

export function mcpServerDirectorySearchOptions(params: { q: string; transport: string }) {
  return queryOptions({
    queryKey: mcpServerKeys.directory(params),
    queryFn: () =>
      api.searchMCPServerDirectory({
        q: params.q || undefined,
        transport: params.transport || undefined,
      }),
    staleTime: 5 * 60 * 1000,
  });
}
