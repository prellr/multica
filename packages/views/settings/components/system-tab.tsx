"use client";

import { queryOptions, useQuery } from "@tanstack/react-query";
import { AlertCircle, CheckCircle, XCircle } from "lucide-react";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useT } from "../../i18n";

function backupStatusOptions(wsId: string) {
  return queryOptions({
    queryKey: ["admin", "backup-status", wsId],
    queryFn: () => api.getBackupStatus(),
    enabled: !!wsId,
    staleTime: 5 * 60 * 1000,
    retry: false,
  });
}

function formatAge(seconds: number): string {
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}

export function SystemTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const { data, isLoading } = useQuery(backupStatusOptions(wsId));

  let icon = <AlertCircle className="h-4 w-4 text-muted-foreground" />;
  let label = "Unknown";
  let subtext = "No status available";

  if (!isLoading && data) {
    if (!data.configured) {
      label = "Not configured";
      subtext = "Set BACKUP_STATUS_DIR in .env and mount ~/multica-backups";
    } else if (data.healthy && data.last_backup_at && data.age_seconds != null) {
      icon = <CheckCircle className="h-4 w-4 text-green-500" />;
      label = `Last backup: ${formatAge(data.age_seconds)}`;
      const mb = data.postgres_dump_size
        ? `${(data.postgres_dump_size / 1_000_000).toFixed(0)} MB`
        : "";
      subtext = `Completed at ${new Date(data.last_backup_at).toLocaleString()}${mb ? ` - ${mb} postgres dump` : ""}`;
    } else {
      icon = <XCircle className="h-4 w-4 text-red-500" />;
      label = data.last_backup_at && data.age_seconds != null
        ? `Backup stale - ${formatAge(data.age_seconds)}`
        : "No backup found";
      subtext = data.error ?? "Check ~/multica-backups/backup.log";
    }
  }

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-sm font-medium mb-1">{t(($) => $.system_tab.backup_health)}</h2>
        <p className="text-xs text-muted-foreground mb-4">
          {t(($) => $.system_tab.backup_threshold)}
        </p>
        <div className="rounded-md border p-4 flex items-start gap-3">
          <div className="mt-0.5">{icon}</div>
          <div>
            <p className="text-sm font-medium">{label}</p>
            <p className="text-xs text-muted-foreground">{subtext}</p>
          </div>
        </div>
      </div>
    </div>
  );
}
