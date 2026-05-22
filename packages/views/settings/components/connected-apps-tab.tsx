"use client";

import { useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  ChevronDown,
  ChevronRight,
  KeyRound,
  Pencil,
  Plus,
  Search,
  ShieldCheck,
  Trash2,
} from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { Button } from "@multica/ui/components/ui/button";
import { Badge } from "@multica/ui/components/ui/badge";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Switch } from "@multica/ui/components/ui/switch";
import { RadioGroup, RadioGroupItem } from "@multica/ui/components/ui/radio-group";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  agentListOptions,
  memberListOptions,
} from "@multica/core/workspace/queries";
import { mcpServerListOptions } from "@multica/core/mcp-servers/queries";
import {
  useAddToolAllowlistEntry,
  useCreateMCPServer,
  useDeleteMCPServer,
  useDeleteMCPServerSecret,
  useRemoveToolAllowlistEntry,
  useUpdateMCPServer,
  useUpsertMCPServerSecret,
} from "@multica/core/mcp-servers/mutations";
import type { Agent, MCPDirectoryEntry, MCPServer } from "@multica/core/types";
import { useT } from "../../i18n";
import { MCPDirectoryBrowserModal } from "./mcp-directory-browser";

type Transport = MCPServer["transport"];
type Scope = MCPServer["scope"];
type ApprovalRequiredFor = MCPServer["approval_required_for"];

const TRANSPORTS: Transport[] = ["stdio", "sse", "http"];
const APPROVAL_OPTIONS: ApprovalRequiredFor[] = ["none", "writes"];
const EMPTY_AGENT = "__none__";

interface FormState {
  name: string;
  transport: Transport;
  url: string;
  command: string;
  argsText: string;
  scope: Scope;
  agentId: string;
  required: boolean;
  readOnly: boolean;
  approvalRequiredFor: ApprovalRequiredFor;
}

function emptyFormState(): FormState {
  return {
    name: "",
    transport: "sse",
    url: "",
    command: "",
    argsText: "",
    scope: "workspace",
    agentId: "",
    required: false,
    readOnly: false,
    approvalRequiredFor: "none",
  };
}

function stateFromServer(server: MCPServer | null): FormState {
  if (!server) return emptyFormState();
  return {
    name: server.name,
    transport: server.transport,
    url: server.url ?? "",
    command: server.command ?? "",
    argsText: server.args.join(" "),
    scope: server.scope,
    agentId: server.agent_id ?? "",
    required: server.required,
    readOnly: server.read_only,
    approvalRequiredFor: server.approval_required_for,
  };
}

function seedFormState(initialValues?: Partial<FormState>): FormState {
  return { ...emptyFormState(), ...(initialValues ?? {}) };
}

function formatTimestamp(value: string | null) {
  if (!value) return null;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function splitArgs(value: string) {
  return value.split(/\s+/).map((part) => part.trim()).filter(Boolean);
}

function TransportBadge({ transport }: { transport: Transport }) {
  const { t } = useT("settings");
  return (
    <Badge variant="outline" className="font-mono text-[11px]">
      {t(($) => $.connected_apps[`transport_badge_${transport}`])}
    </Badge>
  );
}

function ScopeBadge({ scope }: { scope: Scope }) {
  const { t } = useT("settings");
  return (
    <Badge variant="secondary" className="text-[11px]">
      {t(($) => $.connected_apps[`scope_badge_${scope}`])}
    </Badge>
  );
}

function ServerRow({
  server,
  expanded,
  canManage,
  onToggle,
  onEdit,
  onRemove,
}: {
  server: MCPServer;
  expanded: boolean;
  canManage: boolean;
  onToggle: () => void;
  onEdit: () => void;
  onRemove: () => void;
}) {
  const { t } = useT("settings");
  const lastConnected = formatTimestamp(server.last_connected_at);

  return (
    <div className="flex items-center justify-between gap-3 p-3">
      <button
        type="button"
        onClick={onToggle}
        className="flex min-w-0 flex-1 items-center gap-3 text-left"
      >
        {expanded ? (
          <ChevronDown className="size-4 shrink-0 text-muted-foreground" />
        ) : (
          <ChevronRight className="size-4 shrink-0 text-muted-foreground" />
        )}
        <span
          className={cn(
            "size-2 shrink-0 rounded-full",
            server.last_connected_at ? "bg-emerald-500" : "bg-muted-foreground/40",
          )}
        />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-sm font-medium text-foreground">
            {server.name}
          </span>
          <span className="block truncate text-xs text-muted-foreground">
            {lastConnected
              ? `${t(($) => $.connected_apps.status_connected)} · ${lastConnected}`
              : t(($) => $.connected_apps.status_never)}
          </span>
        </span>
      </button>

      <div className="flex shrink-0 items-center gap-2">
        <TransportBadge transport={server.transport} />
        <ScopeBadge scope={server.scope} />
        {canManage && (
          <>
            <Button size="icon-sm" variant="ghost" onClick={onEdit} aria-label={t(($) => $.connected_apps.edit_server)}>
              <Pencil className="size-4" />
            </Button>
            <Button size="icon-sm" variant="ghost" onClick={onRemove} aria-label={t(($) => $.connected_apps.remove)}>
              <Trash2 className="size-4" />
            </Button>
          </>
        )}
      </div>
    </div>
  );
}

function MCPServerDialog({
  open,
  onOpenChange,
  editing,
  agents,
  initialValues,
  showDirectoryUrlNote = false,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  editing: MCPServer | null;
  agents: Agent[];
  initialValues?: Partial<FormState>;
  showDirectoryUrlNote?: boolean;
}) {
  const { t } = useT("settings");
  const [form, setForm] = useState<FormState>(() => editing ? stateFromServer(editing) : seedFormState(initialValues));
  const createMutation = useCreateMCPServer();
  const updateMutation = useUpdateMCPServer();
  const isSaving = createMutation.isPending || updateMutation.isPending;

  useEffect(() => {
    if (open) setForm(editing ? stateFromServer(editing) : seedFormState(initialValues));
  }, [editing, initialValues, open]);

  function update<K extends keyof FormState>(key: K, value: FormState[K]) {
    setForm((current) => ({ ...current, [key]: value }));
  }

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const payload = {
      name: form.name.trim(),
      transport: form.transport,
      url: form.transport === "stdio" ? null : form.url.trim(),
      command: form.transport === "stdio" ? form.command.trim() : null,
      args: form.transport === "stdio" ? splitArgs(form.argsText) : [],
      scope: form.scope,
      agent_id: form.scope === "agent" ? form.agentId : null,
      required: form.required,
      read_only: form.readOnly,
      approval_required_for: form.approvalRequiredFor,
    };

    const onSuccess = () => {
      toast.success(
        editing
          ? t(($) => $.connected_apps.toast_updated)
          : t(($) => $.connected_apps.toast_created),
      );
      onOpenChange(false);
    };
    const onError = () => toast.error(t(($) => $.connected_apps.toast_error));

    if (editing) {
      updateMutation.mutate({ id: editing.id, ...payload }, { onSuccess, onError });
    } else {
      createMutation.mutate(payload, { onSuccess, onError });
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {editing
              ? t(($) => $.connected_apps.edit_server)
              : t(($) => $.connected_apps.add_server)}
          </DialogTitle>
          <DialogDescription>
            {t(($) => $.connected_apps.section_servers_description)}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="mcp-server-name">{t(($) => $.connected_apps.form_name)}</Label>
            <Input
              id="mcp-server-name"
              value={form.name}
              onChange={(event) => update("name", event.target.value)}
              required
            />
          </div>

          <div className="space-y-2">
            <Label>{t(($) => $.connected_apps.form_transport)}</Label>
            <Select value={form.transport} onValueChange={(value) => value && update("transport", value as Transport)}>
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {TRANSPORTS.map((transport) => (
                  <SelectItem key={transport} value={transport}>
                    {t(($) => $.connected_apps[`transport_badge_${transport}`])}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {form.transport === "stdio" ? (
            <>
              <div className="space-y-2">
                <Label htmlFor="mcp-server-command">{t(($) => $.connected_apps.form_command)}</Label>
                <Input
                  id="mcp-server-command"
                  value={form.command}
                  onChange={(event) => update("command", event.target.value)}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="mcp-server-args">{t(($) => $.connected_apps.form_args)}</Label>
                <Input
                  id="mcp-server-args"
                  value={form.argsText}
                  onChange={(event) => update("argsText", event.target.value)}
                  placeholder={t(($) => $.connected_apps.form_args_hint)}
                />
              </div>
            </>
          ) : (
            <div className="space-y-2">
              <Label htmlFor="mcp-server-url">{t(($) => $.connected_apps.form_url)}</Label>
              <Input
                id="mcp-server-url"
                value={form.url}
                onChange={(event) => update("url", event.target.value)}
                required
                type="url"
              />
              {showDirectoryUrlNote && !editing && (
                <p className="text-xs text-muted-foreground">
                  {t(($) => $.connected_apps.directory_url_note)}
                </p>
              )}
            </div>
          )}

          <div className="space-y-2">
            <Label>{t(($) => $.connected_apps.form_scope)}</Label>
            <RadioGroup
              value={form.scope}
              onValueChange={(value) => value && update("scope", value as Scope)}
              className="grid grid-cols-2 gap-3"
            >
              <label className="flex items-center gap-2 rounded-lg border border-border p-3 text-sm">
                <RadioGroupItem value="workspace" />
                {t(($) => $.connected_apps.form_scope_workspace)}
              </label>
              <label className="flex items-center gap-2 rounded-lg border border-border p-3 text-sm">
                <RadioGroupItem value="agent" />
                {t(($) => $.connected_apps.form_scope_agent)}
              </label>
            </RadioGroup>
          </div>

          {form.scope === "agent" && (
            <div className="space-y-2">
              <Label>{t(($) => $.connected_apps.form_agent)}</Label>
              <Select value={form.agentId || EMPTY_AGENT} onValueChange={(value) => update("agentId", value === EMPTY_AGENT ? "" : value ?? "")}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={EMPTY_AGENT}>{t(($) => $.connected_apps.form_agent)}</SelectItem>
                  {agents.map((agent) => (
                    <SelectItem key={agent.id} value={agent.id}>
                      {agent.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}

          <div className="space-y-3 rounded-lg border border-border p-3">
            <ToggleRow
              label={t(($) => $.connected_apps.form_required)}
              hint={t(($) => $.connected_apps.form_required_hint)}
              checked={form.required}
              onCheckedChange={(checked) => update("required", checked)}
            />
            <ToggleRow
              label={t(($) => $.connected_apps.form_read_only)}
              hint={t(($) => $.connected_apps.form_read_only_hint)}
              checked={form.readOnly}
              onCheckedChange={(checked) => update("readOnly", checked)}
            />
          </div>

          <div className="space-y-2">
            <Label>{t(($) => $.connected_apps.form_approval)}</Label>
            <Select value={form.approvalRequiredFor} onValueChange={(value) => value && update("approvalRequiredFor", value as ApprovalRequiredFor)}>
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {APPROVAL_OPTIONS.map((option) => (
                  <SelectItem key={option} value={option}>
                    {t(($) => $.connected_apps[`form_approval_${option}`])}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <DialogFooter>
            <Button type="submit" disabled={isSaving || !form.name.trim() || (form.scope === "agent" && !form.agentId)}>
              {isSaving
                ? t(($) => $.connected_apps.form_saving)
                : t(($) => $.connected_apps.form_save)}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ToggleRow({
  label,
  hint,
  checked,
  onCheckedChange,
}: {
  label: string;
  hint: string;
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
}) {
  return (
    <div className="flex items-center justify-between gap-4">
      <div>
        <p className="text-sm font-medium">{label}</p>
        <p className="text-xs text-muted-foreground">{hint}</p>
      </div>
      <Switch checked={checked} onCheckedChange={onCheckedChange} />
    </div>
  );
}

function ServerDetailPanel({
  server,
  canManage,
}: {
  server: MCPServer;
  canManage: boolean;
}) {
  const { t } = useT("settings");
  const [rotatingKey, setRotatingKey] = useState("");
  const [newKey, setNewKey] = useState("");
  const [secretValue, setSecretValue] = useState("");
  const [toolName, setToolName] = useState("");
  const secretMutation = useUpsertMCPServerSecret();
  const deleteSecretMutation = useDeleteMCPServerSecret();
  const addToolMutation = useAddToolAllowlistEntry();
  const removeToolMutation = useRemoveToolAllowlistEntry();
  const lastConnected = formatTimestamp(server.last_connected_at);

  function saveSecret(key: string) {
    const trimmedKey = key.trim();
    if (!trimmedKey || !secretValue) return;
    secretMutation.mutate(
      { id: server.id, key: trimmedKey, value: secretValue },
      {
        onSuccess: () => {
          toast.success(t(($) => $.connected_apps.toast_secret_saved));
          setRotatingKey("");
          setNewKey("");
          setSecretValue("");
        },
        onError: () => toast.error(t(($) => $.connected_apps.toast_error)),
      },
    );
  }

  function addTool() {
    const trimmed = toolName.trim();
    if (!trimmed) return;
    addToolMutation.mutate(
      { id: server.id, toolName: trimmed },
      {
        onSuccess: () => {
          toast.success(t(($) => $.connected_apps.toast_allowlist_updated));
          setToolName("");
        },
        onError: () => toast.error(t(($) => $.connected_apps.toast_error)),
      },
    );
  }

  return (
    <div className="border-t border-border bg-muted/30 p-4">
      <div className="grid gap-6 md:grid-cols-2">
        <section className="space-y-3">
          <div className="flex items-center justify-between gap-3">
            <h3 className="text-sm font-semibold">{t(($) => $.connected_apps.secrets_title)}</h3>
            {canManage && (
              <Button size="sm" variant="outline" onClick={() => setRotatingKey("__new__")}>
                <Plus className="size-3.5" />
                {t(($) => $.connected_apps.secrets_rotate)}
              </Button>
            )}
          </div>
          {server.secret_keys.length === 0 && rotatingKey !== "__new__" ? (
            <p className="text-xs text-muted-foreground">{t(($) => $.connected_apps.secrets_empty)}</p>
          ) : (
            <div className="space-y-2">
              {server.secret_keys.map((key) => (
                <div key={key} className="rounded-lg border border-border bg-card p-2">
                  <div className="flex items-center justify-between gap-2">
                    <span className="flex min-w-0 items-center gap-2 text-sm">
                      <KeyRound className="size-4 shrink-0 text-muted-foreground" />
                      <code className="truncate">{key}</code>
                    </span>
                    {canManage && (
                      <div className="flex shrink-0 gap-1">
                        <Button size="sm" variant="outline" onClick={() => setRotatingKey(key)}>
                          {t(($) => $.connected_apps.secrets_rotate)}
                        </Button>
                        <Button
                          size="icon-sm"
                          variant="ghost"
                          aria-label={t(($) => $.connected_apps.secrets_delete)}
                          onClick={() =>
                            deleteSecretMutation.mutate(
                              { id: server.id, key },
                              {
                                onSuccess: () => toast.success(t(($) => $.connected_apps.toast_secret_deleted)),
                                onError: () => toast.error(t(($) => $.connected_apps.toast_error)),
                              },
                            )
                          }
                        >
                          <Trash2 className="size-4" />
                        </Button>
                      </div>
                    )}
                  </div>
                  {rotatingKey === key && (
                    <SecretForm
                      keyName={key}
                      value={secretValue}
                      onValueChange={setSecretValue}
                      onSave={() => saveSecret(key)}
                      saving={secretMutation.isPending}
                      readonlyKey
                    />
                  )}
                </div>
              ))}
              {rotatingKey === "__new__" && (
                <SecretForm
                  keyName={newKey}
                  onKeyChange={setNewKey}
                  value={secretValue}
                  onValueChange={setSecretValue}
                  onSave={() => saveSecret(newKey)}
                  saving={secretMutation.isPending}
                />
              )}
            </div>
          )}
        </section>

        <section className="space-y-3">
          <h3 className="text-sm font-semibold">{t(($) => $.connected_apps.allowlist_title)}</h3>
          {server.tool_allowlist.length === 0 ? (
            <p className="text-xs text-muted-foreground">{t(($) => $.connected_apps.allowlist_empty)}</p>
          ) : (
            <div className="space-y-2">
              {server.tool_allowlist.map((tool) => (
                <div key={tool} className="flex items-center justify-between gap-2 rounded-lg border border-border bg-card p-2">
                  <code className="min-w-0 truncate text-sm">{tool}</code>
                  {canManage && (
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() =>
                        removeToolMutation.mutate(
                          { id: server.id, toolName: tool },
                          {
                            onSuccess: () => toast.success(t(($) => $.connected_apps.toast_allowlist_updated)),
                            onError: () => toast.error(t(($) => $.connected_apps.toast_error)),
                          },
                        )
                      }
                    >
                      {t(($) => $.connected_apps.allowlist_remove)}
                    </Button>
                  )}
                </div>
              ))}
            </div>
          )}
          {canManage && (
            <div className="flex gap-2">
              <Input
                value={toolName}
                onChange={(event) => setToolName(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    addTool();
                  }
                }}
              />
              <Button type="button" variant="outline" onClick={addTool} disabled={!toolName.trim() || addToolMutation.isPending}>
                {t(($) => $.connected_apps.allowlist_add)}
              </Button>
            </div>
          )}
          {server.transport !== "stdio" && (
            <Tooltip>
              <TooltipTrigger render={<Button variant="outline" disabled />}>
                <ShieldCheck className="size-4" />
                {t(($) => $.connected_apps.oauth_connect)}
              </TooltipTrigger>
              <TooltipContent>{t(($) => $.connected_apps.oauth_coming_soon)}</TooltipContent>
            </Tooltip>
          )}
          {lastConnected && (
            <p className="text-xs text-muted-foreground">
              {t(($) => $.connected_apps.status_connected)} · {lastConnected}
            </p>
          )}
        </section>
      </div>
    </div>
  );
}

function SecretForm({
  keyName,
  value,
  onValueChange,
  onSave,
  saving,
  readonlyKey = false,
  onKeyChange,
}: {
  keyName: string;
  value: string;
  onValueChange: (value: string) => void;
  onSave: () => void;
  saving: boolean;
  readonlyKey?: boolean;
  onKeyChange?: (value: string) => void;
}) {
  const { t } = useT("settings");
  return (
    <div className="mt-3 grid gap-2 rounded-lg bg-muted/40 p-2">
      <Label>{t(($) => $.connected_apps.secrets_rotate_key_label)}</Label>
      <Input
        value={keyName}
        readOnly={readonlyKey}
        onChange={(event) => onKeyChange?.(event.target.value)}
      />
      <Label>{t(($) => $.connected_apps.secrets_rotate_value_label)}</Label>
      <Input
        type="password"
        value={value}
        onChange={(event) => onValueChange(event.target.value)}
      />
      <Button type="button" size="sm" onClick={onSave} disabled={saving || !keyName.trim() || !value}>
        {t(($) => $.connected_apps.secrets_rotate_save)}
      </Button>
    </div>
  );
}

export function ConnectedAppsTab() {
  const wsId = useWorkspaceId();
  const { t } = useT("settings");
  const { data: servers = [], isLoading } = useQuery(mcpServerListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const user = useAuthStore((s) => s.user);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [browsing, setBrowsing] = useState(false);
  const [editing, setEditing] = useState<MCPServer | null>(null);
  const [initialValues, setInitialValues] = useState<Partial<FormState> | undefined>();
  const [showDirectoryUrlNote, setShowDirectoryUrlNote] = useState(false);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<MCPServer | null>(null);
  const deleteMutation = useDeleteMCPServer();

  const canManage = useMemo(() => {
    const currentMember = members.find((member) => member.user_id === user?.id);
    return currentMember?.role === "owner" || currentMember?.role === "admin";
  }, [members, user?.id]);

  function openCreateDialog() {
    setEditing(null);
    setInitialValues(undefined);
    setShowDirectoryUrlNote(false);
    setDialogOpen(true);
  }

  function openEditDialog(server: MCPServer) {
    setEditing(server);
    setInitialValues(undefined);
    setShowDirectoryUrlNote(false);
    setDialogOpen(true);
  }

  function preferredTransport(entry: MCPDirectoryEntry): Transport {
    if (entry.transport_types.includes("http")) return "http";
    if (entry.transport_types.includes("sse")) return "sse";
    if (entry.transport_types.includes("stdio")) return "stdio";
    return "sse";
  }

  function handleDirectoryConnect(entry: MCPDirectoryEntry) {
    const transport = preferredTransport(entry);
    setEditing(null);
    setInitialValues({
      name: entry.name,
      transport,
      url: transport === "stdio" ? "" : entry.homepage ?? "",
      command: "",
      argsText: "",
    });
    setShowDirectoryUrlNote(transport !== "stdio" && !!entry.homepage);
    setBrowsing(false);
    setDialogOpen(true);
  }

  function confirmDelete() {
    if (!deleteTarget) return;
    deleteMutation.mutate(deleteTarget.id, {
      onSuccess: () => {
        toast.success(t(($) => $.connected_apps.toast_deleted));
        setDeleteTarget(null);
      },
      onError: () => toast.error(t(($) => $.connected_apps.toast_error)),
    });
  }

  return (
    <div className="space-y-8">
      <section className="space-y-4">
        <div className="flex items-start justify-between gap-4">
          <div>
            <h2 className="text-sm font-semibold">{t(($) => $.connected_apps.section_servers)}</h2>
            <p className="mt-1 text-sm text-muted-foreground">
              {t(($) => $.connected_apps.section_servers_description)}
            </p>
          </div>
          {canManage && (
            <div className="flex shrink-0 gap-2">
              <Button size="sm" variant="outline" onClick={() => setBrowsing(true)}>
                <Search className="size-4" />
                {t(($) => $.connected_apps.browse_directory)}
              </Button>
              <Button size="sm" onClick={openCreateDialog}>
                <Plus className="size-4" />
                {t(($) => $.connected_apps.add_server)}
              </Button>
            </div>
          )}
        </div>

        <Card>
          <CardContent className="p-0">
            {isLoading ? (
              <div className="p-4 text-sm text-muted-foreground">{t(($) => $.connected_apps.section_servers)}</div>
            ) : servers.length === 0 ? (
              <div className="flex flex-col items-center justify-center gap-3 p-8 text-center">
                <p className="text-sm font-medium">{t(($) => $.connected_apps.empty_title)}</p>
                {canManage && (
                  <Button variant="outline" onClick={openCreateDialog}>
                    <Plus className="size-4" />
                    {t(($) => $.connected_apps.empty_cta)}
                  </Button>
                )}
              </div>
            ) : (
              <div className="divide-y divide-border">
                {servers.map((server) => (
                  <div key={server.id}>
                    <ServerRow
                      server={server}
                      expanded={expanded === server.id}
                      canManage={canManage}
                      onToggle={() => setExpanded((current) => (current === server.id ? null : server.id))}
                      onEdit={() => openEditDialog(server)}
                      onRemove={() => setDeleteTarget(server)}
                    />
                    {expanded === server.id && (
                      <ServerDetailPanel server={server} canManage={canManage} />
                    )}
                  </div>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      </section>

      <MCPServerDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        editing={editing}
        agents={agents}
        initialValues={initialValues}
        showDirectoryUrlNote={showDirectoryUrlNote}
      />

      <MCPDirectoryBrowserModal
        open={browsing}
        onOpenChange={setBrowsing}
        onConnect={handleDirectoryConnect}
      />

      <AlertDialog open={!!deleteTarget} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.connected_apps.remove_confirm_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.connected_apps.remove_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteMutation.isPending}>
              {t(($) => $.connected_apps.remove_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={confirmDelete}
              disabled={deleteMutation.isPending}
            >
              {t(($) => $.connected_apps.remove_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
