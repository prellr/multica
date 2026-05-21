// Package ship — PR8 of the Ship Hub rebuild: pipeline config diff +
// classification.
//
// PR8 makes the introspector continuously authoritative: three trigger
// paths (on-demand button, push webhook on a workflow-file change,
// daily scheduled poll) re-run the introspector and diff its output
// against the project's stored pipeline_config. This file is the pure,
// heavily-tested heart of that flow — it takes a (current, proposed)
// pair of PipelineConfig values and classifies the difference.
//
// The classification answers one question: "is it safe to auto-apply?"
//
//   - DiffNone        — the two configs are equivalent. Nothing to do
//     beyond stamping last_introspected_at.
//   - DiffAdditive    — the proposed config only ADDS: one or more new
//     stages appended at the end, and/or one or more
//     new triggers on a stage that already existed
//     under the same id. No stage was renamed,
//     dropped, reordered, or had a trigger removed;
//     the shape string is unchanged. Safe to
//     auto-apply — the operator's existing kanban
//     columns all keep their meaning.
//   - DiffDestructive — anything else. A stage id disappeared, a stage
//     was renamed (same id, different name), the
//     stage order changed, a trigger was removed from
//     an existing stage, or the shape changed. NOT
//     safe to auto-apply: an in-flight release could
//     be sitting at a stage that's about to vanish.
//     The operator must Accept or Reject.
//
// "Additive at the end only" is deliberate. A new stage inserted in the
// MIDDLE shifts every later stage's Position, which is a reorder — the
// kanban columns after the insertion point change meaning. Only an
// append leaves every pre-existing column untouched.
package ship

// DiffKind is the closed classification of a pipeline-config diff.
type DiffKind string

const (
	// DiffNone — current and proposed are equivalent.
	DiffNone DiffKind = "none"
	// DiffAdditive — proposed only appends stages and/or adds triggers
	// to existing stages. Safe to auto-apply.
	DiffAdditive DiffKind = "additive"
	// DiffDestructive — proposed renames/drops/reorders a stage, removes
	// a trigger, or changes the shape. Requires operator approval.
	DiffDestructive DiffKind = "destructive"
)

// PipelineDiff describes the difference between a current and a proposed
// PipelineConfig. The API + UI render this so the operator sees exactly
// what changed before accepting.
//
// The stage-name slices use display names (PipelineStage.Name) because
// that's what the operator recognizes in the kanban. AddedStageIDs
// carries ids too — the in-flight-release blocking check in
// pipeline_refresh.go matches release stages against dropped/renamed
// stage IDs, so the id form has to be available there.
type PipelineDiff struct {
	Kind DiffKind `json:"kind"`

	// ShapeChanged is true when the shape string differs. A shape change
	// is always classified destructive — the kanban specializes copy +
	// chip behavior on shape, so the operator should see it explicitly.
	ShapeChanged bool   `json:"shape_changed"`
	OldShape     string `json:"old_shape,omitempty"`
	NewShape     string `json:"new_shape,omitempty"`

	// AddedStages are stages present in proposed but not current (matched
	// by id). For an additive diff these are all appended at the end.
	AddedStages []DiffStageRef `json:"added_stages,omitempty"`

	// RemovedStages are stages present in current but not proposed
	// (matched by id). Their presence forces a destructive classification.
	RemovedStages []DiffStageRef `json:"removed_stages,omitempty"`

	// RenamedStages are stages whose id is unchanged but whose display
	// name changed. A rename is destructive — the operator's kanban
	// column header changes.
	RenamedStages []DiffRename `json:"renamed_stages,omitempty"`

	// ReorderedStages lists ids whose Position changed without the stage
	// being added or removed. Destructive.
	ReorderedStages []string `json:"reordered_stages,omitempty"`

	// TriggersAddedStages / TriggersRemovedStages list the ids of stages
	// (present in both configs under the same id) that gained or lost a
	// trigger. A trigger gain alone is additive; a trigger loss is
	// destructive.
	TriggersAddedStages   []string `json:"triggers_added_stages,omitempty"`
	TriggersRemovedStages []string `json:"triggers_removed_stages,omitempty"`
}

// DiffStageRef is the (id, name) pair for a stage referenced in a diff.
type DiffStageRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DiffRename captures one stage's id plus its old and new display name.
type DiffRename struct {
	ID      string `json:"id"`
	OldName string `json:"old_name"`
	NewName string `json:"new_name"`
}

// DiffPipelineConfig compares a current config against a proposed one
// and returns the classified diff. Pure — no IO, no clock, fully
// deterministic — so the classifier is exhaustively unit-testable.
//
// Matching is by stage ID, not by position: a stage keeps its identity
// across an introspection even if other stages shift around it. The
// position comparison then detects reorders among the stages that
// survived in both configs.
func DiffPipelineConfig(current, proposed PipelineConfig) PipelineDiff {
	diff := PipelineDiff{
		Kind:         DiffNone,
		OldShape:     current.Shape,
		NewShape:     proposed.Shape,
		ShapeChanged: current.Shape != proposed.Shape,
	}

	currByID := indexStagesByID(current.Stages)
	propByID := indexStagesByID(proposed.Stages)

	// Removed: in current, not in proposed.
	for _, s := range current.Stages {
		if _, ok := propByID[s.ID]; !ok {
			diff.RemovedStages = append(diff.RemovedStages, DiffStageRef{ID: s.ID, Name: s.Name})
		}
	}

	// Added: in proposed, not in current.
	for _, s := range proposed.Stages {
		if _, ok := currByID[s.ID]; !ok {
			diff.AddedStages = append(diff.AddedStages, DiffStageRef{ID: s.ID, Name: s.Name})
		}
	}

	// Surviving stages (present in both): detect rename, reorder, and
	// per-stage trigger add/remove.
	for _, propStage := range proposed.Stages {
		curStage, ok := currByID[propStage.ID]
		if !ok {
			continue
		}
		if curStage.Name != propStage.Name {
			diff.RenamedStages = append(diff.RenamedStages, DiffRename{
				ID:      propStage.ID,
				OldName: curStage.Name,
				NewName: propStage.Name,
			})
		}
		if curStage.Position != propStage.Position {
			diff.ReorderedStages = append(diff.ReorderedStages, propStage.ID)
		}
		added, removed := diffTriggers(curStage.Triggers, propStage.Triggers)
		if added {
			diff.TriggersAddedStages = append(diff.TriggersAddedStages, propStage.ID)
		}
		if removed {
			diff.TriggersRemovedStages = append(diff.TriggersRemovedStages, propStage.ID)
		}
	}

	diff.Kind = classifyDiff(current, proposed, diff)
	return diff
}

// classifyDiff folds the structural observations into a single DiffKind.
//
// Destructive wins over additive: if a diff both appends a stage AND
// drops one, the drop is the dangerous part, so the whole diff is
// destructive. A diff is additive only when EVERY change is purely
// additive.
func classifyDiff(current, proposed PipelineConfig, d PipelineDiff) DiffKind {
	destructive := d.ShapeChanged ||
		len(d.RemovedStages) > 0 ||
		len(d.RenamedStages) > 0 ||
		len(d.ReorderedStages) > 0 ||
		len(d.TriggersRemovedStages) > 0
	if destructive {
		return DiffDestructive
	}

	// At this point nothing was removed, renamed, reordered, or had a
	// trigger removed, and the shape is unchanged. The only possible
	// changes are added stages and added triggers.
	additive := len(d.AddedStages) > 0 || len(d.TriggersAddedStages) > 0
	if !additive {
		return DiffNone
	}

	// A pure append is additive; a stage inserted in the middle is a
	// reorder of everything after it. We already know no SURVIVING stage
	// was reordered (ReorderedStages is empty) — but a survivor keeps the
	// same position only if every added stage sits at a position >= the
	// count of surviving stages. Equivalently: the added stages must
	// occupy the final positions of the proposed list. Because the
	// surviving stages kept their exact positions and proposed positions
	// form a gapless 0..N-1 sequence (Validate guarantees), any added
	// stage that isn't at the tail would have displaced a survivor —
	// which would have shown up in ReorderedStages. So reaching here with
	// ReorderedStages empty already implies a tail append. Still, guard
	// explicitly so a future Validate relaxation can't silently turn a
	// mid-insert into a false "additive".
	if !addedStagesAreAtTail(current, proposed) {
		return DiffDestructive
	}
	return DiffAdditive
}

// addedStagesAreAtTail reports whether every stage in proposed that is
// NOT in current sits strictly after every stage that IS in current.
// This is the "appended at the end" guarantee that makes an added stage
// safe to auto-apply.
func addedStagesAreAtTail(current, proposed PipelineConfig) bool {
	currByID := indexStagesByID(current.Stages)
	lastSurvivorPos := -1
	firstAddedPos := len(proposed.Stages)
	for _, s := range proposed.Stages {
		if _, survived := currByID[s.ID]; survived {
			if s.Position > lastSurvivorPos {
				lastSurvivorPos = s.Position
			}
		} else {
			if s.Position < firstAddedPos {
				firstAddedPos = s.Position
			}
		}
	}
	// No added stages — vacuously true.
	if firstAddedPos == len(proposed.Stages) {
		return true
	}
	return firstAddedPos > lastSurvivorPos
}

// diffTriggers compares two trigger slices for one stage and reports
// whether the proposed slice ADDED any trigger and/or REMOVED any.
// Triggers are compared by value (kind + config) — a trigger whose
// config changed counts as a removal of the old one plus an addition of
// the new one, which (because removal is destructive) correctly forces
// a destructive classification for a config edit.
func diffTriggers(current, proposed []PipelineTrigger) (added, removed bool) {
	curSet := triggerMultiset(current)
	propSet := triggerMultiset(proposed)
	for key, n := range propSet {
		if curSet[key] < n {
			added = true
			break
		}
	}
	for key, n := range curSet {
		if propSet[key] < n {
			removed = true
			break
		}
	}
	return added, removed
}

// triggerMultiset folds a trigger slice into a count-keyed map so the
// comparison tolerates ordering differences but still notices a
// duplicate trigger being added or removed.
func triggerMultiset(triggers []PipelineTrigger) map[string]int {
	m := make(map[string]int, len(triggers))
	for _, t := range triggers {
		m[triggerKey(t)]++
	}
	return m
}

// triggerKey is a stable string identity for a trigger value. Every
// field that Validate / the kanban renderer reads is folded in so a
// config-field change (e.g. branch main → master) is detected.
func triggerKey(t PipelineTrigger) string {
	return string(t.Kind) + "|" +
		t.Config.Branch + "|" +
		t.Config.Workflow + "|" +
		t.Config.ParentWorkflow + "|" +
		t.Config.Environment + "|" +
		t.Config.TagPattern
}

func indexStagesByID(stages []PipelineStage) map[string]PipelineStage {
	m := make(map[string]PipelineStage, len(stages))
	for _, s := range stages {
		m[s.ID] = s
	}
	return m
}
