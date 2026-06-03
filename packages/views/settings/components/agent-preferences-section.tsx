"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  memoryListOptions,
  useCreateMemoryArtifact,
  useDeleteMemoryArtifact,
} from "@multica/core/memory";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Label } from "@multica/ui/components/ui/label";
import { useT } from "../../i18n";

// Per-user "things agents should know about me" — kind='preference'
// memory artifacts anchored to the current member. The substrate piece
// landed in the same PR; this is the surface that lets a member curate
// their own preferences without using the generic memory create modal
// (which deliberately doesn't offer preference as a creatable kind —
// the create picker is for workspace-shared kinds only).
//
// Runtime injection wiring (auto-include these preferences in the
// dispatching member's CLAUDE.md context) is a deliberate follow-up;
// this PR ships the data model + the curation surface. Members can
// already create + edit preferences and they're round-tripped via the
// existing memory APIs.

const PREFERENCE_LIST_LIMIT = 200;

export function AgentPreferencesSection() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const user = useAuthStore((s) => s.user);

  // Resolve the current user's member.id in this workspace — the
  // anchor_id we'll write + the filter for the read query.
  const { data: members = [], isLoading: membersLoading } = useQuery(
    memberListOptions(wsId),
  );
  const member = useMemo(
    () => members.find((m) => m.user_id === user?.id) ?? null,
    [members, user?.id],
  );

  // Memory list filtered to (kind=preference, anchor_type=member,
  // anchor_id=current member). Server already restricts to this
  // workspace; member filter narrows to "me only."
  const listEnabled = !!wsId && !!member;
  const { data: artifacts = [], isLoading: prefsLoading } = useQuery({
    ...memoryListOptions(
      wsId,
      member
        ? {
            kind: "preference",
            anchor_type: "member",
            anchor_id: member.id,
            limit: PREFERENCE_LIST_LIMIT,
          }
        : undefined,
    ),
    enabled: listEnabled,
  });

  const createPref = useCreateMemoryArtifact();
  const deletePref = useDeleteMemoryArtifact();

  const [title, setTitle] = useState("");
  const [content, setContent] = useState("");
  const canSubmit =
    !!member && title.trim().length > 0 && content.trim().length > 0;

  const handleAdd = (e: React.FormEvent) => {
    e.preventDefault();
    if (!member || !canSubmit) return;
    createPref.mutate(
      {
        kind: "preference",
        title: title.trim(),
        content: content.trim(),
        anchor_type: "member",
        anchor_id: member.id,
      },
      {
        onSuccess: () => {
          setTitle("");
          setContent("");
          toast.success(t(($) => $.agent_prefs.toast_created));
        },
        onError: (err) =>
          toast.error(
            err instanceof Error
              ? err.message
              : t(($) => $.agent_prefs.toast_create_failed),
          ),
      },
    );
  };

  const handleDelete = (id: string) => {
    deletePref.mutate(id, {
      onSuccess: () => toast.success(t(($) => $.agent_prefs.toast_deleted)),
      onError: (err) =>
        toast.error(
          err instanceof Error
            ? err.message
            : t(($) => $.agent_prefs.toast_delete_failed),
        ),
    });
  };

  if (membersLoading || (listEnabled && prefsLoading)) {
    return (
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="h-3.5 w-3.5 animate-spin" />
        {t(($) => $.agent_prefs.loading)}
      </div>
    );
  }
  if (!member) {
    return (
      <p className="text-sm text-muted-foreground">
        {t(($) => $.agent_prefs.no_membership)}
      </p>
    );
  }

  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <h3 className="text-sm font-semibold">
          {t(($) => $.agent_prefs.heading)}
        </h3>
        <p className="text-xs text-muted-foreground">
          {t(($) => $.agent_prefs.description)}
        </p>
      </div>

      {artifacts.length > 0 && (
        <ul className="space-y-2">
          {artifacts.map((a) => (
            <li
              key={a.id}
              className="flex items-start gap-3 rounded-md border bg-card p-3 text-sm"
            >
              <div className="min-w-0 flex-1 space-y-1">
                <div className="font-medium">{a.title}</div>
                <p className="whitespace-pre-wrap text-xs text-muted-foreground">
                  {a.content}
                </p>
              </div>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label={t(($) => $.agent_prefs.delete_aria)}
                onClick={() => handleDelete(a.id)}
                disabled={deletePref.isPending}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </Button>
            </li>
          ))}
        </ul>
      )}

      <form
        onSubmit={handleAdd}
        className="space-y-2 rounded-md border bg-card p-3"
      >
        <div className="space-y-1">
          <Label htmlFor="agent-pref-title" className="text-xs">
            {t(($) => $.agent_prefs.field_title)}
          </Label>
          <Input
            id="agent-pref-title"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder={t(($) => $.agent_prefs.placeholder_title)}
          />
        </div>
        <div className="space-y-1">
          <Label htmlFor="agent-pref-content" className="text-xs">
            {t(($) => $.agent_prefs.field_content)}
          </Label>
          <Textarea
            id="agent-pref-content"
            value={content}
            onChange={(e) => setContent(e.target.value)}
            placeholder={t(($) => $.agent_prefs.placeholder_content)}
            rows={3}
          />
        </div>
        <div className="flex justify-end">
          <Button
            type="submit"
            size="sm"
            disabled={!canSubmit || createPref.isPending}
          >
            <Plus className="mr-1 h-3 w-3" />
            {t(($) => $.agent_prefs.add_button)}
          </Button>
        </div>
      </form>
    </div>
  );
}
