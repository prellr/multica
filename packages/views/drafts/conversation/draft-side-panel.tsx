"use client";

import { useState } from "react";
import type { DraftAnnotation } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import { useT } from "../../i18n";
import {
  AnnotationThreadPanel,
  openAnnotationCount,
} from "../annotations/annotation-thread-panel";
import type { AnchoredAnnotation } from "../annotations/use-annotation-anchoring";
import { ConversationRail } from "./conversation-rail";

type SidePanelTab = "conversation" | "annotations";

interface DraftSidePanelProps {
  wsId: string;
  draftId: string;
  // Annotation-tab props, threaded straight to AnnotationThreadPanel.
  anchored: AnchoredAnnotation[];
  orphaned: DraftAnnotation[];
  /** The currently expanded annotation id (pin → thread), or null. */
  activeId: string | null;
  onSelect: (id: string | null) => void;
}

/**
 * The draft editor's right rail: a two-tab wrapper over the draft-level
 * conversation (Rail-1, default) and the anchored annotation overlay. The
 * conversation is the document-wide back-and-forth; the annotations are margin
 * notes pinned to spans. The wrapper owns the `w-96` rail chrome (width +
 * border) and the tab strip; each tab renders its surface embedded (no double
 * chrome).
 *
 * The active tab is ephemeral UI state — local `useState`, not a store (per the
 * state-management rules: transient selection that shouldn't persist across
 * restarts).
 */
export function DraftSidePanel({
  wsId,
  draftId,
  anchored,
  orphaned,
  activeId,
  onSelect,
}: DraftSidePanelProps) {
  const { t } = useT("drafts");
  const [tab, setTab] = useState<SidePanelTab>("conversation");

  const openCount = openAnnotationCount(anchored);

  return (
    <Tabs
      value={tab}
      onValueChange={(value) => setTab(value as SidePanelTab)}
      className="flex h-full w-96 flex-col gap-0 border-l"
    >
      <TabsList variant="line" className="shrink-0 justify-start border-b px-2">
        <TabsTrigger value="conversation">
          {t(($) => $.conversation.tab_conversation)}
        </TabsTrigger>
        <TabsTrigger value="annotations">
          {t(($) => $.conversation.tab_annotations)}
          {openCount > 0 && (
            <Badge
              variant="secondary"
              aria-label={t(($) => $.annotations.open_count)}
            >
              {openCount}
            </Badge>
          )}
        </TabsTrigger>
      </TabsList>

      <TabsContent value="conversation" className="min-h-0 flex-1">
        <ConversationRail wsId={wsId} draftId={draftId} />
      </TabsContent>

      <TabsContent value="annotations" className="min-h-0 flex-1">
        <AnnotationThreadPanel
          wsId={wsId}
          draftId={draftId}
          anchored={anchored}
          orphaned={orphaned}
          activeId={activeId}
          onSelect={onSelect}
          embedded
        />
      </TabsContent>
    </Tabs>
  );
}
