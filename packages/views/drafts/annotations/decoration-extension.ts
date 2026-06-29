import { Extension } from "@tiptap/core";
import { Plugin, PluginKey } from "@tiptap/pm/state";
import { Decoration, DecorationSet } from "@tiptap/pm/view";

/**
 * ProseMirror decoration plugin for the draft annotation layer (slice 1).
 *
 * Annotations are a NON-DESTRUCTIVE overlay: the highlighted/annotated ranges
 * render as ProseMirror inline decorations computed from the annotation list
 * (re-anchored through @multica/core/drafts reanchor), NOT as inline marks /
 * CriticMarkup inside the document. The `body` stays clean markdown — nothing
 * about an annotation is ever written into the doc.
 *
 * The decoration set is pushed in from React via the `setAnnotationDecorations`
 * command: the surface recomputes ranges whenever the annotation list changes
 * or the doc is edited, then hands this plugin a fresh list of [from, to)
 * positions to paint. The plugin maps its decorations through document changes
 * automatically (DecorationSet.map) so highlights track edits between
 * recomputes, and a full recompute re-syncs them to the re-anchored truth.
 */

export interface AnnotationDecorationRange {
  /** ProseMirror position of the span start. */
  from: number;
  /** ProseMirror position of the span end. */
  to: number;
  /** Annotation id, surfaced as a data attribute for hit-testing / click. */
  id: string;
  /** Annotation type — drives the highlight color via a data attribute. */
  type: string;
  /** Annotation state — open vs resolved/dismissed styling. */
  state: string;
  /** True when re-anchoring flagged the underlying text as materially changed. */
  changed: boolean;
}

export const annotationDecorationPluginKey = new PluginKey<DecorationSet>(
  "draft-annotation-decorations",
);

const SET_DECORATIONS_META = "setAnnotationDecorations";

function buildDecorationSet(doc: import("@tiptap/pm/model").Node, ranges: AnnotationDecorationRange[]): DecorationSet {
  const decorations = ranges
    // Guard against inverted / out-of-bounds ranges (re-anchor can produce a
    // zero-length span on a degenerate edit; skip rather than throw).
    .filter((r) => r.to > r.from && r.from >= 0 && r.to <= doc.content.size)
    .map((r) =>
      Decoration.inline(r.from, r.to, {
        class: "draft-annotation-highlight",
        "data-annotation-id": r.id,
        "data-annotation-type": r.type,
        "data-annotation-state": r.state,
        ...(r.changed ? { "data-annotation-changed": "true" } : {}),
      }),
    );
  return DecorationSet.create(doc, decorations);
}

declare module "@tiptap/core" {
  interface Commands<ReturnType> {
    draftAnnotationDecorations: {
      /** Replace the annotation decoration set with `ranges`. */
      setAnnotationDecorations: (ranges: AnnotationDecorationRange[]) => ReturnType;
    };
  }
}

export const AnnotationDecorationExtension = Extension.create({
  name: "draftAnnotationDecorations",

  addCommands() {
    return {
      setAnnotationDecorations:
        (ranges: AnnotationDecorationRange[]) =>
        ({ tr, dispatch }) => {
          if (dispatch) {
            tr.setMeta(SET_DECORATIONS_META, ranges);
            dispatch(tr);
          }
          return true;
        },
    };
  },

  addProseMirrorPlugins() {
    return [
      new Plugin<DecorationSet>({
        key: annotationDecorationPluginKey,
        state: {
          init: () => DecorationSet.empty,
          apply(tr, old) {
            const meta = tr.getMeta(SET_DECORATIONS_META) as
              | AnnotationDecorationRange[]
              | undefined;
            if (meta) {
              // Full re-sync to the re-anchored truth.
              return buildDecorationSet(tr.doc, meta);
            }
            // No new ranges — map existing decorations through the edit so they
            // track the change until the next recompute.
            return old.map(tr.mapping, tr.doc);
          },
        },
        props: {
          decorations(state) {
            return annotationDecorationPluginKey.getState(state) ?? DecorationSet.empty;
          },
        },
      }),
    ];
  },
});
