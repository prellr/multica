"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Search, Star } from "lucide-react";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { mcpServerDirectorySearchOptions } from "@multica/core/mcp-servers/queries";
import type { MCPDirectoryEntry } from "@multica/core/types";
import { useT } from "../../i18n";

const ALL_TRANSPORTS = "__all__";

function useDebouncedValue(value: string, delayMs: number) {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timeout = window.setTimeout(() => setDebounced(value), delayMs);
    return () => window.clearTimeout(timeout);
  }, [delayMs, value]);
  return debounced;
}

function relativeTime(value: string | null) {
  if (!value) return "never";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const seconds = Math.round((date.getTime() - Date.now()) / 1000);
  const abs = Math.abs(seconds);
  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  if (abs < 60) return rtf.format(seconds, "second");
  if (abs < 3600) return rtf.format(Math.round(seconds / 60), "minute");
  if (abs < 86400) return rtf.format(Math.round(seconds / 3600), "hour");
  return rtf.format(Math.round(seconds / 86400), "day");
}

function DirectoryCard({
  entry,
  onConnect,
}: {
  entry: MCPDirectoryEntry;
  onConnect: (entry: MCPDirectoryEntry) => void;
}) {
  const { t } = useT("settings");
  return (
    <div className="grid gap-3 border border-border bg-card p-3">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="truncate text-sm font-semibold text-foreground">{entry.name}</h3>
          <p className="mt-1 line-clamp-2 text-xs text-muted-foreground">
            {entry.description || entry.homepage}
          </p>
        </div>
        <Button size="sm" onClick={() => onConnect(entry)}>
          {t(($) => $.connected_apps.directory_connect)}
        </Button>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        {entry.transport_types.map((transport) => (
          <Badge key={transport} variant="outline" className="font-mono text-[11px]">
            {transport}
          </Badge>
        ))}
        {entry.publisher_name && (
          <span className="text-xs text-muted-foreground">{entry.publisher_name}</span>
        )}
        <span className="ml-auto inline-flex items-center gap-1 text-xs text-muted-foreground">
          <Star className="size-3.5" />
          {entry.stars.toLocaleString()}
        </span>
      </div>
    </div>
  );
}

export function MCPDirectoryBrowserModal({
  open,
  onOpenChange,
  onConnect,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConnect: (entry: MCPDirectoryEntry) => void;
}) {
  const { t } = useT("settings");
  const [search, setSearch] = useState("");
  const [transport, setTransport] = useState(ALL_TRANSPORTS);
  const debouncedSearch = useDebouncedValue(search, 300);
  const queryParams = useMemo(
    () => ({
      q: debouncedSearch,
      transport: transport === ALL_TRANSPORTS ? "" : transport,
    }),
    [debouncedSearch, transport],
  );
  const { data, isFetching } = useQuery({
    ...mcpServerDirectorySearchOptions(queryParams),
    enabled: open,
  });
  const entries = data?.entries ?? [];

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[90vh] flex-col sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t(($) => $.connected_apps.directory_modal_title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.connected_apps.directory_modal_subtitle, { total: data?.total ?? 0 })}
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-3 sm:flex-row">
          <div className="relative min-w-0 flex-1">
            <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              className="pl-9"
              autoFocus
            />
          </div>
          <Select value={transport} onValueChange={(value) => setTransport(value || ALL_TRANSPORTS)}>
            <SelectTrigger className="w-full sm:w-36">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_TRANSPORTS}>
                {t(($) => $.connected_apps.directory_filter_all)}
              </SelectItem>
              <SelectItem value="stdio">
                {t(($) => $.connected_apps.transport_badge_stdio)}
              </SelectItem>
              <SelectItem value="http">
                {t(($) => $.connected_apps.transport_badge_http)}
              </SelectItem>
              <SelectItem value="sse">
                {t(($) => $.connected_apps.transport_badge_sse)}
              </SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div className="min-h-64 flex-1 overflow-y-auto">
          {isFetching && entries.length === 0 ? (
            <div className="flex min-h-64 items-center justify-center text-sm text-muted-foreground">
              {t(($) => $.connected_apps.directory_loading)}
            </div>
          ) : entries.length === 0 ? (
            <div className="flex min-h-64 items-center justify-center text-sm text-muted-foreground">
              {t(($) => $.connected_apps.directory_empty)}
            </div>
          ) : (
            <div className="grid gap-2">
              {entries.map((entry) => (
                <DirectoryCard key={entry.id} entry={entry} onConnect={onConnect} />
              ))}
            </div>
          )}
        </div>

        <p className="border-t border-border pt-3 text-xs text-muted-foreground">
          {t(($) => $.connected_apps.directory_source_note, {
            date: relativeTime(data?.last_fetched_at ?? null),
          })}
        </p>
      </DialogContent>
    </Dialog>
  );
}
