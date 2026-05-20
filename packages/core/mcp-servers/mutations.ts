import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import type {
  CreateMCPServerRequest,
  GetMCPServerResponse,
  ListMCPServersResponse,
  MCPServer,
  UpdateMCPServerRequest,
} from "../types";
import { mcpServerKeys } from "./queries";

export function useCreateMCPServer() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (data: CreateMCPServerRequest) => api.createMCPServer(data),
    onSuccess: (newServer) => {
      qc.setQueryData<ListMCPServersResponse>(mcpServerKeys.list(wsId), (old) =>
        old && !old.mcp_servers.some((s) => s.id === newServer.id)
          ? {
              ...old,
              mcp_servers: [...old.mcp_servers, newServer],
              total: old.total + 1,
            }
          : old,
      );
      qc.setQueryData<GetMCPServerResponse>(mcpServerKeys.detail(wsId, newServer.id), {
        mcp_server: newServer,
      });
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: mcpServerKeys.list(wsId) });
    },
  });
}

export function useUpdateMCPServer() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ id, ...data }: { id: string } & UpdateMCPServerRequest) =>
      api.updateMCPServer(id, data),
    onMutate: async ({ id, ...data }) => {
      await qc.cancelQueries({ queryKey: mcpServerKeys.list(wsId) });
      await qc.cancelQueries({ queryKey: mcpServerKeys.detail(wsId, id) });
      const prevList = qc.getQueryData<ListMCPServersResponse>(mcpServerKeys.list(wsId));
      const prevDetail = qc.getQueryData<GetMCPServerResponse>(mcpServerKeys.detail(wsId, id));
      qc.setQueryData<ListMCPServersResponse>(mcpServerKeys.list(wsId), (old) =>
        old
          ? {
              ...old,
              mcp_servers: old.mcp_servers.map((server) =>
                server.id === id ? ({ ...server, ...data } as MCPServer) : server,
              ),
            }
          : old,
      );
      qc.setQueryData<GetMCPServerResponse>(mcpServerKeys.detail(wsId, id), (old) =>
        old
          ? { ...old, mcp_server: { ...old.mcp_server, ...data } as MCPServer }
          : old,
      );
      return { prevList, prevDetail, id };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prevList) qc.setQueryData(mcpServerKeys.list(wsId), ctx.prevList);
      if (ctx?.prevDetail) qc.setQueryData(mcpServerKeys.detail(wsId, ctx.id), ctx.prevDetail);
    },
    onSuccess: (server) => {
      qc.setQueryData<GetMCPServerResponse>(mcpServerKeys.detail(wsId, server.id), {
        mcp_server: server,
      });
      qc.setQueryData<ListMCPServersResponse>(mcpServerKeys.list(wsId), (old) =>
        old
          ? {
              ...old,
              mcp_servers: old.mcp_servers.map((item) =>
                item.id === server.id ? server : item,
              ),
            }
          : old,
      );
    },
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: mcpServerKeys.detail(wsId, vars.id) });
      qc.invalidateQueries({ queryKey: mcpServerKeys.list(wsId) });
    },
  });
}

export function useDeleteMCPServer() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.deleteMCPServer(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: mcpServerKeys.list(wsId) });
      const prevList = qc.getQueryData<ListMCPServersResponse>(mcpServerKeys.list(wsId));
      qc.setQueryData<ListMCPServersResponse>(mcpServerKeys.list(wsId), (old) =>
        old
          ? {
              ...old,
              mcp_servers: old.mcp_servers.filter((server) => server.id !== id),
              total: Math.max(0, old.total - 1),
            }
          : old,
      );
      qc.removeQueries({ queryKey: mcpServerKeys.detail(wsId, id) });
      return { prevList };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prevList) qc.setQueryData(mcpServerKeys.list(wsId), ctx.prevList);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: mcpServerKeys.list(wsId) });
    },
  });
}

export function useUpsertMCPServerSecret() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ id, key, value }: { id: string; key: string; value: string }) =>
      api.upsertMCPServerSecret(id, key, value),
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: mcpServerKeys.detail(wsId, vars.id) });
      qc.invalidateQueries({ queryKey: mcpServerKeys.list(wsId) });
    },
  });
}

export function useDeleteMCPServerSecret() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ id, key }: { id: string; key: string }) =>
      api.deleteMCPServerSecret(id, key),
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: mcpServerKeys.detail(wsId, vars.id) });
      qc.invalidateQueries({ queryKey: mcpServerKeys.list(wsId) });
    },
  });
}

export function useAddToolAllowlistEntry() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ id, toolName }: { id: string; toolName: string }) =>
      api.addMCPServerToolAllowlistEntry(id, toolName),
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: mcpServerKeys.detail(wsId, vars.id) });
      qc.invalidateQueries({ queryKey: mcpServerKeys.list(wsId) });
    },
  });
}

export function useRemoveToolAllowlistEntry() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ id, toolName }: { id: string; toolName: string }) =>
      api.removeMCPServerToolAllowlistEntry(id, toolName),
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: mcpServerKeys.detail(wsId, vars.id) });
      qc.invalidateQueries({ queryKey: mcpServerKeys.list(wsId) });
    },
  });
}
