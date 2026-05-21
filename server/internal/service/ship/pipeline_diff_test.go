package ship

import (
	"sort"
	"testing"
)

// pipeline_diff_test.go — exhaustive coverage of the PR8 diff
// classifier. The classifier is the testable heart of PR8: it decides
// whether an introspected config change is safe to auto-apply
// (additive) or must be parked for operator review (destructive).

// --- test fixtures -------------------------------------------------

// baseConfig is a minimal 3-stage config used as the "current" side of
// most diff cases.
func baseConfig() PipelineConfig {
	return PipelineConfig{
		Shape: "staged_strict",
		Stages: []PipelineStage{
			{ID: "in_review", Name: "In Review", Position: 0},
			{ID: "in_staging", Name: "In Staging", Position: 1, Triggers: []PipelineTrigger{
				{Kind: TriggerWorkflowRun, Config: TriggerConfig{Workflow: "deploy-staging.yml"}},
			}},
			{ID: "done", Name: "Done", Position: 2, IsTerminal: true},
		},
	}
}

// --- core classification cases -------------------------------------

func TestDiffPipelineConfig_None(t *testing.T) {
	t.Parallel()
	cur := baseConfig()
	prop := baseConfig()
	diff := DiffPipelineConfig(cur, prop)
	if diff.Kind != DiffNone {
		t.Fatalf("identical configs: want DiffNone, got %s", diff.Kind)
	}
	if diff.ShapeChanged {
		t.Errorf("identical configs: ShapeChanged should be false")
	}
}

func TestDiffPipelineConfig_AdditiveStageAppended(t *testing.T) {
	t.Parallel()
	cur := baseConfig()
	prop := baseConfig()
	// Append a new stage at the END — strictly after every survivor.
	// Flipping the old terminal's IsTerminal off and the new stage's on
	// does NOT rename/remove a survivor (IsTerminal is not a tracked
	// diff field), and the old `done` stage keeps its id/name/position/
	// triggers — so the only structural change is the new tail stage.
	prop.Stages[2].IsTerminal = false
	prop.Stages = append(prop.Stages, PipelineStage{
		ID: "post_deploy_smoke", Name: "Post-deploy smoke", Position: 3, IsTerminal: true,
	})
	diff := DiffPipelineConfig(cur, prop)
	if diff.Kind != DiffAdditive {
		t.Fatalf("appended tail stage: want DiffAdditive, got %s", diff.Kind)
	}
}

func TestDiffPipelineConfig_AdditiveTailStageSimple(t *testing.T) {
	t.Parallel()
	cur := baseConfig()
	prop := baseConfig()
	prop.Stages = append(prop.Stages, PipelineStage{
		ID: "archived", Name: "Archived", Position: 3,
	})
	diff := DiffPipelineConfig(cur, prop)
	if diff.Kind != DiffAdditive {
		t.Fatalf("appended tail stage: want DiffAdditive, got %s", diff.Kind)
	}
	if len(diff.AddedStages) != 1 || diff.AddedStages[0].ID != "archived" {
		t.Errorf("AddedStages: want [archived], got %+v", diff.AddedStages)
	}
	if len(diff.RemovedStages) != 0 {
		t.Errorf("RemovedStages should be empty, got %+v", diff.RemovedStages)
	}
}

func TestDiffPipelineConfig_AdditiveTriggerAdded(t *testing.T) {
	t.Parallel()
	cur := baseConfig()
	prop := baseConfig()
	// Add a second trigger to the existing in_staging stage.
	prop.Stages[1].Triggers = append(prop.Stages[1].Triggers, PipelineTrigger{
		Kind: TriggerDeploymentStatus, Config: TriggerConfig{Environment: "staging"},
	})
	diff := DiffPipelineConfig(cur, prop)
	if diff.Kind != DiffAdditive {
		t.Fatalf("added trigger to existing stage: want DiffAdditive, got %s", diff.Kind)
	}
	if !equalStrSlice(diff.TriggersAddedStages, []string{"in_staging"}) {
		t.Errorf("TriggersAddedStages: want [in_staging], got %v", diff.TriggersAddedStages)
	}
	if len(diff.TriggersRemovedStages) != 0 {
		t.Errorf("TriggersRemovedStages should be empty, got %v", diff.TriggersRemovedStages)
	}
}

func TestDiffPipelineConfig_DestructiveStageRemoved(t *testing.T) {
	t.Parallel()
	cur := baseConfig()
	prop := PipelineConfig{
		Shape: "staged_strict",
		Stages: []PipelineStage{
			{ID: "in_review", Name: "In Review", Position: 0},
			{ID: "done", Name: "Done", Position: 1, IsTerminal: true},
		},
	}
	diff := DiffPipelineConfig(cur, prop)
	if diff.Kind != DiffDestructive {
		t.Fatalf("removed stage: want DiffDestructive, got %s", diff.Kind)
	}
	if len(diff.RemovedStages) != 1 || diff.RemovedStages[0].ID != "in_staging" {
		t.Errorf("RemovedStages: want [in_staging], got %+v", diff.RemovedStages)
	}
}

func TestDiffPipelineConfig_DestructiveStageRenamed(t *testing.T) {
	t.Parallel()
	cur := baseConfig()
	prop := baseConfig()
	prop.Stages[1].Name = "Staging Environment"
	diff := DiffPipelineConfig(cur, prop)
	if diff.Kind != DiffDestructive {
		t.Fatalf("renamed stage: want DiffDestructive, got %s", diff.Kind)
	}
	if len(diff.RenamedStages) != 1 {
		t.Fatalf("RenamedStages: want 1, got %+v", diff.RenamedStages)
	}
	r := diff.RenamedStages[0]
	if r.ID != "in_staging" || r.OldName != "In Staging" || r.NewName != "Staging Environment" {
		t.Errorf("RenamedStages[0]: unexpected %+v", r)
	}
}

func TestDiffPipelineConfig_DestructiveStageReordered(t *testing.T) {
	t.Parallel()
	cur := baseConfig()
	prop := PipelineConfig{
		Shape: "staged_strict",
		Stages: []PipelineStage{
			// in_staging and in_review swapped positions.
			{ID: "in_staging", Name: "In Staging", Position: 0, Triggers: []PipelineTrigger{
				{Kind: TriggerWorkflowRun, Config: TriggerConfig{Workflow: "deploy-staging.yml"}},
			}},
			{ID: "in_review", Name: "In Review", Position: 1},
			{ID: "done", Name: "Done", Position: 2, IsTerminal: true},
		},
	}
	diff := DiffPipelineConfig(cur, prop)
	if diff.Kind != DiffDestructive {
		t.Fatalf("reordered stages: want DiffDestructive, got %s", diff.Kind)
	}
	got := append([]string{}, diff.ReorderedStages...)
	sort.Strings(got)
	if !equalStrSlice(got, []string{"in_review", "in_staging"}) {
		t.Errorf("ReorderedStages: want [in_review in_staging], got %v", got)
	}
}

func TestDiffPipelineConfig_DestructiveMidInsert(t *testing.T) {
	t.Parallel()
	// A new stage inserted in the MIDDLE pushes `done` from position 2 to
	// 3 — that's a reorder of a survivor, so it must be destructive.
	cur := baseConfig()
	prop := PipelineConfig{
		Shape: "staged_strict",
		Stages: []PipelineStage{
			{ID: "in_review", Name: "In Review", Position: 0},
			{ID: "in_staging", Name: "In Staging", Position: 1, Triggers: []PipelineTrigger{
				{Kind: TriggerWorkflowRun, Config: TriggerConfig{Workflow: "deploy-staging.yml"}},
			}},
			{ID: "staging_verified", Name: "Staging verified", Position: 2},
			{ID: "done", Name: "Done", Position: 3, IsTerminal: true},
		},
	}
	diff := DiffPipelineConfig(cur, prop)
	if diff.Kind != DiffDestructive {
		t.Fatalf("mid-insert: want DiffDestructive, got %s", diff.Kind)
	}
}

func TestDiffPipelineConfig_DestructiveTriggerRemoved(t *testing.T) {
	t.Parallel()
	cur := baseConfig()
	prop := baseConfig()
	// Drop the only trigger off in_staging.
	prop.Stages[1].Triggers = nil
	diff := DiffPipelineConfig(cur, prop)
	if diff.Kind != DiffDestructive {
		t.Fatalf("removed trigger: want DiffDestructive, got %s", diff.Kind)
	}
	if !equalStrSlice(diff.TriggersRemovedStages, []string{"in_staging"}) {
		t.Errorf("TriggersRemovedStages: want [in_staging], got %v", diff.TriggersRemovedStages)
	}
}

func TestDiffPipelineConfig_DestructiveTriggerConfigChanged(t *testing.T) {
	t.Parallel()
	// Changing a trigger's config (workflow filename) counts as removing
	// the old trigger + adding the new one → destructive.
	cur := baseConfig()
	prop := baseConfig()
	prop.Stages[1].Triggers[0].Config.Workflow = "deploy-staging-v2.yml"
	diff := DiffPipelineConfig(cur, prop)
	if diff.Kind != DiffDestructive {
		t.Fatalf("changed trigger config: want DiffDestructive, got %s", diff.Kind)
	}
}

func TestDiffPipelineConfig_DestructiveShapeChange(t *testing.T) {
	t.Parallel()
	cur := baseConfig()
	prop := baseConfig()
	prop.Shape = "direct_to_prod"
	diff := DiffPipelineConfig(cur, prop)
	if diff.Kind != DiffDestructive {
		t.Fatalf("shape change: want DiffDestructive, got %s", diff.Kind)
	}
	if !diff.ShapeChanged || diff.OldShape != "staged_strict" || diff.NewShape != "direct_to_prod" {
		t.Errorf("shape fields wrong: %+v", diff)
	}
}

func TestDiffPipelineConfig_DestructiveWinsOverAdditive(t *testing.T) {
	t.Parallel()
	// A diff that BOTH appends a stage AND drops one must classify as
	// destructive — the drop is the dangerous part.
	cur := baseConfig()
	prop := PipelineConfig{
		Shape: "staged_strict",
		Stages: []PipelineStage{
			{ID: "in_review", Name: "In Review", Position: 0},
			{ID: "done", Name: "Done", Position: 1, IsTerminal: true},
			{ID: "post_archive", Name: "Post Archive", Position: 2},
		},
	}
	diff := DiffPipelineConfig(cur, prop)
	if diff.Kind != DiffDestructive {
		t.Fatalf("append+drop: want DiffDestructive, got %s", diff.Kind)
	}
	if len(diff.AddedStages) != 1 || len(diff.RemovedStages) != 1 {
		t.Errorf("expected 1 added + 1 removed, got added=%d removed=%d",
			len(diff.AddedStages), len(diff.RemovedStages))
	}
}

func TestDiffPipelineConfig_DefaultConfigsAreStable(t *testing.T) {
	t.Parallel()
	// Diffing each canonical default against itself must be DiffNone —
	// guards against a non-deterministic field creeping into triggerKey.
	cases := []struct {
		name string
		cfg  PipelineConfig
	}{
		{"direct_to_prod", DefaultDirectToProdConfig()},
		{"staged_strict", DefaultStagedStrictConfig()},
		{"manual_only", DefaultManualOnlyConfig()},
		{"library", DefaultLibraryConfig()},
		{"manual_compose", DefaultManualComposeConfig()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diff := DiffPipelineConfig(tc.cfg, tc.cfg)
			if diff.Kind != DiffNone {
				t.Errorf("%s diffed against itself: want DiffNone, got %s", tc.name, diff.Kind)
			}
		})
	}
}

func TestDiffPipelineConfig_TriggerOrderInsensitive(t *testing.T) {
	t.Parallel()
	// Two triggers on a stage, same set but different order — must be
	// DiffNone (the multiset comparison ignores order).
	cur := baseConfig()
	cur.Stages[1].Triggers = []PipelineTrigger{
		{Kind: TriggerWorkflowRun, Config: TriggerConfig{Workflow: "a.yml"}},
		{Kind: TriggerDeploymentStatus, Config: TriggerConfig{Environment: "staging"}},
	}
	prop := baseConfig()
	prop.Stages[1].Triggers = []PipelineTrigger{
		{Kind: TriggerDeploymentStatus, Config: TriggerConfig{Environment: "staging"}},
		{Kind: TriggerWorkflowRun, Config: TriggerConfig{Workflow: "a.yml"}},
	}
	diff := DiffPipelineConfig(cur, prop)
	if diff.Kind != DiffNone {
		t.Fatalf("trigger reorder only: want DiffNone, got %s", diff.Kind)
	}
}

// --- helpers -------------------------------------------------------

func equalStrSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
