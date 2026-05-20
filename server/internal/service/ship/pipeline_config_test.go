package ship

import (
	"encoding/json"
	"strings"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestPipelineConfig_Validate pins the structural invariants the
// kanban renderer in PR5b relies on. Each subcase encodes one rule
// the validator MUST enforce.
func TestPipelineConfig_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		cfg        PipelineConfig
		wantErrSub string // empty => expect no error
	}{
		{
			name:       "empty shape",
			cfg:        PipelineConfig{Shape: "", Stages: validStages()},
			wantErrSub: "shape is required",
		},
		{
			name:       "no stages",
			cfg:        PipelineConfig{Shape: "x"},
			wantErrSub: "stages must be non-empty",
		},
		{
			name: "missing stage id",
			cfg: PipelineConfig{Shape: "x", Stages: []PipelineStage{
				{ID: "", Name: "n", Position: 0, IsTerminal: true},
			}},
			wantErrSub: "id is required",
		},
		{
			name: "duplicate stage id",
			cfg: PipelineConfig{Shape: "x", Stages: []PipelineStage{
				{ID: "a", Name: "n1", Position: 0},
				{ID: "a", Name: "n2", Position: 1, IsTerminal: true},
			}},
			wantErrSub: "duplicate",
		},
		{
			name: "missing stage name",
			cfg: PipelineConfig{Shape: "x", Stages: []PipelineStage{
				{ID: "a", Name: "", Position: 0, IsTerminal: true},
			}},
			wantErrSub: "name is required",
		},
		{
			name: "position doesn't match index",
			cfg: PipelineConfig{Shape: "x", Stages: []PipelineStage{
				{ID: "a", Name: "n", Position: 5, IsTerminal: true}, // index is 0
			}},
			wantErrSub: "position must equal index",
		},
		{
			name: "zero terminal stages",
			cfg: PipelineConfig{Shape: "x", Stages: []PipelineStage{
				{ID: "a", Name: "n", Position: 0},
			}},
			wantErrSub: "exactly one terminal stage required",
		},
		{
			name: "two terminal stages",
			cfg: PipelineConfig{Shape: "x", Stages: []PipelineStage{
				{ID: "a", Name: "n", Position: 0, IsTerminal: true},
				{ID: "b", Name: "n", Position: 1, IsTerminal: true},
			}},
			wantErrSub: "exactly one terminal",
		},
		{
			name: "trigger missing required field",
			cfg: PipelineConfig{Shape: "x", Stages: []PipelineStage{
				{
					ID: "a", Name: "n", Position: 0,
					Triggers: []PipelineTrigger{
						{Kind: TriggerPushBranch}, // branch missing
					},
				},
				{ID: "z", Name: "done", Position: 1, IsTerminal: true},
			}},
			wantErrSub: "branch is required",
		},
		{
			name: "unknown trigger kind",
			cfg: PipelineConfig{Shape: "x", Stages: []PipelineStage{
				{
					ID: "a", Name: "n", Position: 0,
					Triggers: []PipelineTrigger{
						{Kind: "made_up"},
					},
				},
				{ID: "z", Name: "done", Position: 1, IsTerminal: true},
			}},
			wantErrSub: "unknown kind",
		},
		{
			name: "ok minimal",
			cfg: PipelineConfig{Shape: "x", Stages: []PipelineStage{
				{ID: "a", Name: "n", Position: 0, IsTerminal: true},
			}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate()
			if c.wantErrSub == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantErrSub)
			}
			if !strings.Contains(err.Error(), c.wantErrSub) {
				t.Errorf("expected error containing %q, got %v", c.wantErrSub, err)
			}
		})
	}
}

func validStages() []PipelineStage {
	return []PipelineStage{
		{ID: "a", Name: "n", Position: 0, IsTerminal: true},
	}
}

// TestDefaultConfigs_AllValidate proves every shipped default config
// passes Validate. If a future change breaks a default it'll surface
// here rather than in a kanban render error.
func TestDefaultConfigs_AllValidate(t *testing.T) {
	t.Parallel()
	cases := map[string]PipelineConfig{
		"direct_to_prod": DefaultDirectToProdConfig(),
		"staged_strict":  DefaultStagedStrictConfig(),
		"manual_only":    DefaultManualOnlyConfig(),
		"library":        DefaultLibraryConfig(),
		"manual_compose": DefaultManualComposeConfig(),
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if err := cfg.Validate(); err != nil {
				t.Errorf("default %s config failed validate: %v", name, err)
			}
			if cfg.Shape != name {
				t.Errorf("default %s config has shape %q (want %q)", name, cfg.Shape, name)
			}
		})
	}
}

// TestFromJSON_RoundTripsValidConfig — toJSON then fromJSON yields the
// same config. The JSONB column relies on this round-trip being lossless.
func TestFromJSON_RoundTripsValidConfig(t *testing.T) {
	t.Parallel()
	original := DefaultStagedStrictConfig()
	raw, err := original.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	decoded, err := FromJSON(raw)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	if decoded.Shape != original.Shape {
		t.Errorf("shape mismatch: got %q, want %q", decoded.Shape, original.Shape)
	}
	if len(decoded.Stages) != len(original.Stages) {
		t.Fatalf("stage count mismatch: got %d, want %d", len(decoded.Stages), len(original.Stages))
	}
	for i := range original.Stages {
		if decoded.Stages[i].ID != original.Stages[i].ID {
			t.Errorf("stages[%d].id mismatch", i)
		}
		if len(decoded.Stages[i].Triggers) != len(original.Stages[i].Triggers) {
			t.Errorf("stages[%d] trigger count mismatch", i)
		}
	}
}

// TestFromJSON_RejectsMalformed — invalid JSONB is reported, not
// returned silently. EffectivePipelineConfig then falls back to the
// enum-derived default.
func TestFromJSON_RejectsMalformed(t *testing.T) {
	t.Parallel()
	if _, err := FromJSON([]byte(`{not json`)); err == nil {
		t.Error("expected parse error for malformed JSON")
	}
	if _, err := FromJSON([]byte(`{"shape": "", "stages": []}`)); err == nil {
		t.Error("expected validation error for empty shape")
	}
}

// TestEffectivePipelineConfig_PrefersJSONBWhenPresent — the read shim
// uses the stored JSONB when it parses cleanly.
func TestEffectivePipelineConfig_PrefersJSONBWhenPresent(t *testing.T) {
	t.Parallel()
	want := DefaultManualComposeConfig()
	raw, err := want.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	p := db.Project{
		PipelineConfig: raw,
		// Legacy enum says staged — shim should ignore this because
		// the JSONB is present and valid.
		PipelineKind: db.ProjectPipelineKindStaged,
	}
	got, err := EffectivePipelineConfig(p)
	if err != nil {
		t.Fatalf("EffectivePipelineConfig: %v", err)
	}
	if got.Shape != "manual_compose" {
		t.Errorf("shape: got %q, want manual_compose (JSONB should win)", got.Shape)
	}
}

// TestEffectivePipelineConfig_FallsBackToLegacyEnum — no JSONB stored
// (the common state for un-introspected projects pre-PR8) — shim
// synthesizes from pipeline_kind.
func TestEffectivePipelineConfig_FallsBackToLegacyEnum(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind      db.ProjectPipelineKind
		wantShape string
	}{
		{db.ProjectPipelineKindDirectToProd, "direct_to_prod"},
		{db.ProjectPipelineKindStaged, "staged_strict"},
	}
	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			p := db.Project{PipelineKind: c.kind}
			got, err := EffectivePipelineConfig(p)
			if err != nil {
				t.Fatalf("EffectivePipelineConfig: %v", err)
			}
			if got.Shape != c.wantShape {
				t.Errorf("kind=%q: shape=%q, want %q", c.kind, got.Shape, c.wantShape)
			}
		})
	}
}

// TestEffectivePipelineConfig_FallsBackOnCorruptJSONB — operator
// manual override leaves a corrupt blob in the column. Shim returns
// the legacy default rather than crashing the kanban.
func TestEffectivePipelineConfig_FallsBackOnCorruptJSONB(t *testing.T) {
	t.Parallel()
	p := db.Project{
		PipelineConfig: []byte(`{not json`),
		PipelineKind:   db.ProjectPipelineKindStaged,
	}
	got, err := EffectivePipelineConfig(p)
	if err != nil {
		t.Fatalf("EffectivePipelineConfig: %v (must recover from corrupt blob)", err)
	}
	if got.Shape != "staged_strict" {
		t.Errorf("got shape %q on corrupt blob, want staged_strict fallback", got.Shape)
	}
}

// TestEffectivePipelineConfig_UnknownLegacyKindDefaultsToStaged —
// defensive default for an unrecognized enum value (e.g. a future
// migration adds a new value that this code hasn't been taught yet).
// Should NOT crash; should return a usable config.
func TestEffectivePipelineConfig_UnknownLegacyKindDefaultsToStaged(t *testing.T) {
	t.Parallel()
	p := db.Project{PipelineKind: db.ProjectPipelineKind("future_kind")}
	got, err := EffectivePipelineConfig(p)
	if err != nil {
		t.Fatalf("EffectivePipelineConfig: %v", err)
	}
	if got.Shape != "staged_strict" {
		t.Errorf("unknown legacy kind: shape=%q, want staged_strict fallback", got.Shape)
	}
}

// TestDefaultConfigs_JSONStability — the JSON of each default config
// is the same shape every time. Catches an accidental field rename
// before it lands in production and breaks deserialization of stored
// blobs.
func TestDefaultConfigs_JSONStability(t *testing.T) {
	t.Parallel()
	cfgs := []PipelineConfig{
		DefaultDirectToProdConfig(),
		DefaultStagedStrictConfig(),
		DefaultManualOnlyConfig(),
		DefaultLibraryConfig(),
		DefaultManualComposeConfig(),
	}
	for _, cfg := range cfgs {
		t.Run(cfg.Shape, func(t *testing.T) {
			raw, err := cfg.ToJSON()
			if err != nil {
				t.Fatalf("ToJSON: %v", err)
			}
			// Top-level fields must be present.
			var m map[string]json.RawMessage
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("re-parse: %v", err)
			}
			for _, want := range []string{"shape", "stages"} {
				if _, ok := m[want]; !ok {
					t.Errorf("missing top-level field %q in %s", want, cfg.Shape)
				}
			}
		})
	}
}
