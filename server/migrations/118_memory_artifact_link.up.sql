-- memory_artifact_link: many-to-many graph layer over memory_artifact.
--
-- Why we need this: today every memory_artifact has exactly one anchor
-- (issue / project / agent / channel). The 2026-05-31 RoastConsole
-- mining run made it obvious that real decisions touch many things —
-- ROA-46 (rebrand cutover) spans the repo, the infra, and the rename
-- policy; ROA-404 (wholesale portal v1) cites W-20, W-21, ROA-419, and
-- a parent project. A single anchor is the *primary* anchor; a graph
-- of links captures the rest.
--
-- It also models artifact-to-artifact relationships that the single-
-- anchor model can't express at all: "decision A supersedes decision
-- B" was previously possible only as free-text prose inside the body.
-- With this table the relationship is structured and queryable.
--
-- Open-string `target_type` and `relation_type` mirror the substrate's
-- existing discriminator-validated-in-service-layer convention. Adding
-- a new relation type (e.g. "blocks", "duplicates") is a single line
-- in the handler's allowlist; no migration. target_type currently
-- covers issue / project / agent / channel / memory_artifact — the
-- five things an artifact can usefully reference today.
--
-- Uniqueness: the same artifact can link to the same target via more
-- than one relation (e.g. "cites AND supersedes"), so the natural key
-- INCLUDES relation_type. The same artifact cannot link to the same
-- target via the same relation twice — that's noise, not signal.

CREATE TABLE memory_artifact_link (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,

    -- The artifact that's doing the linking. ON DELETE CASCADE so
    -- archiving / hard-deleting an artifact cleans up its outgoing
    -- links automatically.
    artifact_id     UUID NOT NULL REFERENCES memory_artifact(id) ON DELETE CASCADE,

    -- The thing being linked to. target_type names the entity space
    -- ('issue' / 'project' / 'agent' / 'channel' / 'memory_artifact').
    -- target_id is the entity's UUID; we don't FK because the target
    -- table varies. The handler validates target_type at write time;
    -- a delete of the target entity DOES leak orphan links (mirrors
    -- the existing anchor_id behavior — same tradeoff for the same
    -- reasons).
    target_type     TEXT NOT NULL,
    target_id       UUID NOT NULL,

    -- The semantic relationship. Open-string; allowlist enforced in
    -- the handler. Initial set:
    --   'cites'       — references this thing (informational)
    --   'supersedes'  — replaces a prior decision
    --   'contradicts' — conflicts with another decision
    --   'implements'  — concrete realization of an abstract decision
    --   'scope'       — applies to this entity (broader-than-anchor)
    --   'discussed-in' — relevant conversation lived here
    --   'informs'     — informed but did not commit this artifact
    relation_type   TEXT NOT NULL,

    -- Provenance for who created the link — same author-shape as
    -- memory_artifact so a future "links by Memory Miner" filter is
    -- a one-line query.
    created_by_type TEXT NOT NULL,
    created_by_id   UUID NOT NULL,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The natural key. ON CONFLICT DO NOTHING on insert makes "create
    -- this link if it doesn't already exist" trivially idempotent.
    UNIQUE (artifact_id, target_type, target_id, relation_type)
);

-- Workspace scoping — every list query filters here first.
CREATE INDEX memory_artifact_link_workspace_idx
    ON memory_artifact_link (workspace_id);

-- "Show me the outgoing links for artifact X" — by-artifact lookup is
-- the dominant read path (powers the detail page's Links section).
CREATE INDEX memory_artifact_link_artifact_idx
    ON memory_artifact_link (artifact_id);

-- "Show me the backlinks to issue Y" — incoming-links lookup, also a
-- detail-page surface (an issue page can show every memory artifact
-- that references it). The composite covers the two filter columns
-- in the common access pattern.
CREATE INDEX memory_artifact_link_target_idx
    ON memory_artifact_link (target_type, target_id);
