"use client";

import { useState, useMemo, useEffect, useRef } from "react";
import {
  Plus,
  BookOpen,
  Search as SearchIcon,
  X,
  Check,
  CheckCircle2,
  Tag as TagIcon,
  Archive,
  Bot,
  CircleDashed,
} from "lucide-react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import {
  memoryListInfiniteOptions,
  memorySearchInfiniteOptions,
  memoryTagsOptions,
  MEMORY_KINDS,
  MEMORY_SYSTEM_KINDS,
  MEMORY_KIND_LABELS,
} from "@multica/core/memory";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useModalStore } from "@multica/core/modals";
import { useActorName } from "@multica/core/workspace/hooks";
import type {
  MemoryArtifact,
  MemoryArtifactKind,
  ListMemoryArtifactsParams,
} from "@multica/core/types";
import { AppLink } from "../../navigation";
import { ActorAvatar } from "../../common/actor-avatar";
import { PageHeader } from "../../layout/page-header";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Button } from "@multica/ui/components/ui/button";
import {
  Popover,
  PopoverTrigger,
  PopoverContent,
} from "@multica/ui/components/ui/popover";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

// Same relative-time helper as projects/issues — kept inline rather than
// shared because the bar for promoting it to a util is "third caller."
function formatRelativeDate(date: string): string {
  const diff = Date.now() - new Date(date).getTime();
  const days = Math.floor(diff / (1000 * 60 * 60 * 24));
  if (days < 1) return "Today";
  if (days === 1) return "1d ago";
  if (days < 30) return `${days}d ago`;
  const months = Math.floor(days / 30);
  return `${months}mo ago`;
}

// Tailwind classes for each kind's badge — picked to be visually distinct
// without being loud. Stays here (not in @multica/core) because it's pure
// presentation; the canonical kind list lives in core.
const KIND_BADGE: Record<MemoryArtifactKind, string> = {
  wiki_page: "bg-sky-500/10 text-sky-600 dark:text-sky-400",
  agent_note: "bg-violet-500/10 text-violet-600 dark:text-violet-400",
  runbook: "bg-amber-500/10 text-amber-600 dark:text-amber-400",
  decision: "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
  session: "bg-slate-500/10 text-slate-600 dark:text-slate-400",
  dispatch_event: "bg-slate-500/10 text-slate-600 dark:text-slate-400",
};

const STALE_THRESHOLD_DAYS = 30;

function isStale(artifact: MemoryArtifact): boolean {
  const freshDate =
    artifact.verified_at && artifact.verified_at > artifact.updated_at
      ? artifact.verified_at
      : artifact.updated_at;
  const days = Math.floor(
    (Date.now() - new Date(freshDate).getTime()) / (1000 * 60 * 60 * 24),
  );
  return days >= STALE_THRESHOLD_DAYS;
}

function MemoryRow({ artifact }: { artifact: MemoryArtifact }) {
  const wsPaths = useWorkspacePaths();
  const { getActorName } = useActorName();
  const { t } = useT("memory");
  const authorName = getActorName(artifact.author_type, artifact.author_id);

  return (
    <AppLink
      href={wsPaths.memoryDetail(artifact.id)}
      className="group/row flex h-11 items-center gap-3 px-5 text-sm transition-colors hover:bg-accent/40"
    >
      <span
        className={cn(
          "inline-flex items-center rounded-md px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide shrink-0 w-20 justify-center",
          KIND_BADGE[artifact.kind],
        )}
      >
        {MEMORY_KIND_LABELS[artifact.kind]}
      </span>

      <span className="min-w-0 flex-1 truncate font-medium">
        {artifact.title}
      </span>

      {artifact.tags.length > 0 && (
        <div className="hidden md:flex items-center gap-1 shrink-0 max-w-[40%] overflow-hidden">
          {artifact.tags.slice(0, 3).map((tag) => (
            <span
              key={tag}
              className="rounded-full border px-2 py-0.5 text-[10px] text-muted-foreground truncate max-w-[120px]"
            >
              {tag}
            </span>
          ))}
          {artifact.tags.length > 3 && (
            <span className="text-[10px] text-muted-foreground">
              +{artifact.tags.length - 3}
            </span>
          )}
        </div>
      )}

      {artifact.verified_at && (
        <span className="hidden md:inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[10px] font-medium bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 shrink-0">
          <CheckCircle2 className="h-3 w-3" />
          {t(($) => $.page.verified_badge)}
        </span>
      )}

      {isStale(artifact) && (
        <span className="hidden md:inline-flex items-center rounded-md px-1.5 py-0.5 text-[10px] font-medium bg-amber-500/10 text-amber-600 dark:text-amber-400 shrink-0">
          {t(($) => $.page.stale_badge)}
        </span>
      )}

      <span className="hidden lg:flex w-32 shrink-0 items-center gap-1.5 text-xs text-muted-foreground">
        <ActorAvatar
          actorType={artifact.author_type}
          actorId={artifact.author_id}
          size={16}
        />
        <span className="truncate">{authorName}</span>
      </span>

      <span className="w-20 shrink-0 text-right text-xs text-muted-foreground tabular-nums">
        {formatRelativeDate(artifact.updated_at)}
      </span>
    </AppLink>
  );
}

export function MemoryPage() {
  const { t } = useT("memory");
  const wsId = useWorkspaceId();
  const [kindFilter, setKindFilter] = useState<MemoryArtifactKind | "all">("all");
  const [selectedTags, setSelectedTags] = useState<string[]>([]);
  const [showArchived, setShowArchived] = useState(false);
  const [showSystem, setShowSystem] = useState(false);
  const [unverifiedOnly, setUnverifiedOnly] = useState(false);
  const [searchInput, setSearchInput] = useState("");

  // Debounce the search box so we don't fire a request per keystroke. 250ms
  // is the usual sweet spot between "feels live" and "not chatty".
  const [trimmedSearch, setTrimmedSearch] = useState("");
  useEffect(() => {
    const id = setTimeout(() => setTrimmedSearch(searchInput.trim()), 250);
    return () => clearTimeout(id);
  }, [searchInput]);
  const isSearching = trimmedSearch.length > 0;

  // A system kind explicitly selected as the filter should still be visible
  // even if the "logs" toggle is off — selecting it IS the request.
  const kindParam = kindFilter === "all" ? undefined : kindFilter;

  const listParams = useMemo<ListMemoryArtifactsParams>(
    () => ({
      kind: kindParam,
      tags: selectedTags.length > 0 ? selectedTags : undefined,
      include_archived: showArchived,
      include_system: showSystem,
      unverified_only: unverifiedOnly,
    }),
    [kindParam, selectedTags, showArchived, showSystem, unverifiedOnly],
  );
  const searchParams = useMemo(
    () => ({
      q: trimmedSearch,
      kind: kindParam,
      tags: selectedTags.length > 0 ? selectedTags : undefined,
      include_system: showSystem,
    }),
    [trimmedSearch, kindParam, selectedTags, showSystem],
  );

  // Search and list are separate cache spaces — switching modes doesn't
  // invalidate the other. We just toggle which infinite query is enabled.
  const listQuery = useInfiniteQuery({
    ...memoryListInfiniteOptions(wsId, listParams),
    enabled: !isSearching,
  });
  const searchQuery = useInfiniteQuery({
    ...memorySearchInfiniteOptions(wsId, searchParams),
    enabled: isSearching,
  });
  const active = isSearching ? searchQuery : listQuery;

  const artifacts = useMemo(
    () => active.data?.pages.flatMap((p) => p.memory_artifacts) ?? [],
    [active.data],
  );
  const total = active.data?.pages[0]?.total ?? 0;
  const isLoading = active.isLoading;
  const hasNextPage = active.hasNextPage ?? false;
  const isFetchingNextPage = active.isFetchingNextPage;
  const fetchNextPage = active.fetchNextPage;

  // IntersectionObserver sentinel — pulls the next page when the user nears
  // the bottom. rootMargin prefetches ~200px early so scrolling stays smooth.
  const sentinelRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    const el = sentinelRef.current;
    if (!el) return;
    const obs = new IntersectionObserver(
      (entries) => {
        if (entries[0]?.isIntersecting && hasNextPage && !isFetchingNextPage) {
          fetchNextPage();
        }
      },
      { rootMargin: "200px" },
    );
    obs.observe(el);
    return () => obs.disconnect();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);

  const tagsQuery = useQuery(memoryTagsOptions(wsId));
  const availableTags = tagsQuery.data ?? [];

  const hasActiveFilters =
    kindFilter !== "all" ||
    selectedTags.length > 0 ||
    showArchived ||
    unverifiedOnly;

  const openCreate = () =>
    useModalStore.getState().open("create-memory-artifact");

  const toggleTag = (tag: string) =>
    setSelectedTags((prev) =>
      prev.includes(tag) ? prev.filter((x) => x !== tag) : [...prev, tag],
    );

  const clearFilters = () => {
    setKindFilter("all");
    setSelectedTags([]);
    setShowArchived(false);
    setUnverifiedOnly(false);
  };

  // Kind pills: human kinds always; system kinds only when the logs toggle
  // is on (so they don't clutter the strip for non-debugging use).
  const kindPills: MemoryArtifactKind[] = showSystem
    ? [...MEMORY_KINDS, ...MEMORY_SYSTEM_KINDS]
    : [...MEMORY_KINDS];

  return (
    <div className="flex h-full flex-col">
      <PageHeader className="justify-between px-5">
        <div className="flex items-center gap-2">
          <BookOpen className="h-4 w-4 text-muted-foreground" />
          <h1 className="text-sm font-medium">{t(($) => $.page.title)}</h1>
          {!isLoading && total > 0 && (
            <span className="text-xs text-muted-foreground tabular-nums">
              {artifacts.length < total
                ? `${artifacts.length} / ${total}`
                : total}
            </span>
          )}
        </div>
        <Button size="sm" variant="outline" onClick={openCreate}>
          <Plus className="h-3.5 w-3.5 mr-1" />
          {t(($) => $.page.new_artifact)}
        </Button>
      </PageHeader>

      {/* Filter strip 1: kind pills + search box. */}
      <div className="flex items-center gap-3 border-b px-5 py-2">
        <div className="flex flex-wrap items-center gap-1">
          <KindPill active={kindFilter === "all"} onClick={() => setKindFilter("all")}>
            {t(($) => $.page.filter_all)}
          </KindPill>
          {kindPills.map((k) => (
            <KindPill
              key={k}
              active={kindFilter === k}
              onClick={() => setKindFilter(k)}
            >
              {MEMORY_KIND_LABELS[k]}
            </KindPill>
          ))}
        </div>
        <div className="ml-auto flex items-center gap-1.5 rounded-md border bg-background px-2 py-1 w-72 max-w-full">
          <SearchIcon className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
          <input
            type="text"
            placeholder={t(($) => $.page.search_placeholder)}
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
            className="flex-1 min-w-0 bg-transparent text-sm placeholder:text-muted-foreground outline-none"
          />
          {searchInput && (
            <button
              type="button"
              onClick={() => setSearchInput("")}
              className="text-muted-foreground hover:text-foreground"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          )}
        </div>
      </div>

      {/* Filter strip 2: tag chips + tag picker on the left; toggles right. */}
      <div className="flex items-center gap-2 border-b px-5 py-2">
        <div className="flex flex-wrap items-center gap-1.5 min-w-0">
          {selectedTags.map((tag) => (
            <span
              key={tag}
              className="inline-flex items-center gap-1 rounded-full border bg-accent/40 px-2 py-0.5 text-[11px]"
            >
              {tag}
              <button
                type="button"
                onClick={() => toggleTag(tag)}
                className="text-muted-foreground hover:text-foreground"
              >
                <X className="h-3 w-3" />
              </button>
            </span>
          ))}
          <Popover>
            <PopoverTrigger
              render={
                <Button variant="outline" size="sm" className="h-6 gap-1 text-xs">
                  <TagIcon className="h-3 w-3" />
                  {t(($) => $.page.filter_tags)}
                </Button>
              }
            />
            <PopoverContent align="start" className="w-56 p-0">
              <div className="max-h-64 overflow-y-auto py-1">
                {availableTags.length === 0 ? (
                  <p className="px-3 py-2 text-xs text-muted-foreground">
                    {t(($) => $.page.filter_no_tags)}
                  </p>
                ) : (
                  availableTags.map(({ tag, count }) => {
                    const checked = selectedTags.includes(tag);
                    return (
                      <button
                        key={tag}
                        type="button"
                        onClick={() => toggleTag(tag)}
                        className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm hover:bg-accent/40"
                      >
                        <span className="flex h-3.5 w-3.5 shrink-0 items-center justify-center">
                          {checked && <Check className="h-3.5 w-3.5" />}
                        </span>
                        <span className="min-w-0 flex-1 truncate">{tag}</span>
                        <span className="text-[10px] text-muted-foreground tabular-nums">
                          {count}
                        </span>
                      </button>
                    );
                  })
                )}
              </div>
            </PopoverContent>
          </Popover>
        </div>

        <div className="ml-auto flex shrink-0 items-center gap-1.5">
          <ToggleChip
            active={unverifiedOnly}
            onClick={() => setUnverifiedOnly((v) => !v)}
            icon={<CircleDashed className="h-3 w-3" />}
            label={t(($) => $.page.needs_review)}
          />
          <ToggleChip
            active={showArchived}
            onClick={() => setShowArchived((v) => !v)}
            icon={<Archive className="h-3 w-3" />}
            label={t(($) => $.page.show_archived)}
          />
          <ToggleChip
            active={showSystem}
            onClick={() => setShowSystem((v) => !v)}
            icon={<Bot className="h-3 w-3" />}
            label={t(($) => $.page.show_logs)}
          />
        </div>
      </div>

      <div className="flex-1 overflow-y-auto">
        {isLoading ? (
          <div className="p-5 space-y-1">
            {Array.from({ length: 5 }).map((_, i) => (
              <Skeleton key={i} className="h-11 w-full" />
            ))}
          </div>
        ) : artifacts.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-24 text-muted-foreground">
            <BookOpen className="h-10 w-10 mb-3 opacity-30" />
            <p className="text-sm">
              {isSearching
                ? t(($) => $.page.empty_search)
                : hasActiveFilters
                  ? t(($) => $.page.empty_filtered)
                  : t(($) => $.page.empty_default)}
            </p>
            {isSearching || hasActiveFilters ? (
              <Button
                size="sm"
                variant="outline"
                className="mt-3"
                onClick={() => {
                  setSearchInput("");
                  clearFilters();
                }}
              >
                {t(($) => $.page.clear_filters)}
              </Button>
            ) : (
              <Button size="sm" variant="outline" className="mt-3" onClick={openCreate}>
                {t(($) => $.page.create_first)}
              </Button>
            )}
          </div>
        ) : (
          <>
            {artifacts.map((a) => (
              <MemoryRow key={a.id} artifact={a} />
            ))}
            {/* Infinite-scroll sentinel + loading affordance. */}
            <div ref={sentinelRef} className="h-px" />
            {isFetchingNextPage && (
              <p className="py-4 text-center text-xs text-muted-foreground">
                {t(($) => $.page.loading_more)}
              </p>
            )}
          </>
        )}
      </div>
    </div>
  );
}

function KindPill({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "rounded-full px-2.5 py-1 text-xs transition-colors cursor-pointer",
        active
          ? "bg-accent text-accent-foreground"
          : "text-muted-foreground hover:bg-accent/40",
      )}
    >
      {children}
    </button>
  );
}

function ToggleChip({
  active,
  onClick,
  icon,
  label,
}: {
  active: boolean;
  onClick: () => void;
  icon: React.ReactNode;
  label: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "inline-flex items-center gap-1 rounded-md border px-2 py-1 text-xs transition-colors cursor-pointer",
        active
          ? "bg-accent text-accent-foreground border-transparent"
          : "text-muted-foreground hover:bg-accent/40",
      )}
    >
      {icon}
      {label}
    </button>
  );
}
