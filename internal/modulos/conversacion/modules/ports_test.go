package modules_test

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// recordingProjector es un Projector de prueba: materializa solo un nombre de efecto
// y anota lo que recibe.
type recordingProjector struct {
	handled string
	meta    modules.EffectMeta
	effect  modules.Effect
	err     error
}

func (p *recordingProjector) Handles(effectName string) bool { return effectName == p.handled }

func (p *recordingProjector) Project(_ context.Context, meta modules.EffectMeta, eff modules.Effect) error {
	p.meta, p.effect = meta, eff
	return p.err
}

// fixedPolicy es una ResumePolicy de prueba: reinicia si las Vars traen "closed" y
// siembra el tamaño de página.
type fixedPolicy struct{}

func (fixedPolicy) Restart(_ context.Context, _, _ string, vars map[string]any) (bool, string, []modules.Effect, error) {
	_, closed := vars["closed"]
	return closed, "", nil, nil
}

func (fixedPolicy) Seed(_ context.Context, _ string, vars map[string]any) error {
	vars["page_size"] = 5
	return nil
}

// Aserciones de compilación: las firmas de los dos puertos.
var (
	_ modules.Projector    = (*recordingProjector)(nil)
	_ modules.ResumePolicy = fixedPolicy{}
)

// El proyector recibe la identidad sin PII y el efecto ENTERO (con sus claves
// privadas: podarlas es cosa del sink, no del puerto), y su error sube tal cual.
func TestProjector_ReceivesMetaAndWholeEffect(t *testing.T) {
	boom := errors.New("boom")
	recorder := &recordingProjector{handled: "survey_answer", err: boom}
	var projector modules.Projector = recorder

	if !projector.Handles("survey_answer") || projector.Handles("item_added") {
		t.Fatal("Handles no distingue el efecto propio del ajeno")
	}

	meta := modules.EffectMeta{
		TenantID:    "t1",
		ContactID:   "c-opaco",
		SessionID:   "s1",
		FlowID:      "encuesta",
		FlowVersion: 3,
		EventID:     "ev-1",
	}
	effect := modules.Effect{
		Kind:        modules.KindPrivate,
		Name:        "survey_answer",
		Payload:     map[string]any{"answer": "2", "note": "texto"},
		PrivateKeys: []string{"note"},
	}
	if err := projector.Project(context.Background(), meta, effect); !errors.Is(err, boom) {
		t.Errorf("error = %v, quiero el del proyector", err)
	}
	if recorder.meta != meta {
		t.Errorf("meta = %+v, quiero %+v", recorder.meta, meta)
	}
	if recorder.effect.Payload["note"] != "texto" {
		t.Errorf("Payload = %v, quiero el efecto entero", recorder.effect.Payload)
	}
}

// EventID vacío es «sin evento»: el cero de EffectMeta no inventa identidad.
func TestEffectMeta_ZeroValue(t *testing.T) {
	var meta modules.EffectMeta
	if meta.EventID != "" || meta.TenantID != "" || meta.FlowVersion != 0 {
		t.Errorf("EffectMeta cero = %+v", meta)
	}
}

func TestResumePolicy_RestartAndSeed(t *testing.T) {
	var policy modules.ResumePolicy = fixedPolicy{}
	ctx := context.Background()

	restart, notice, effects, err := policy.Restart(ctx, "t1", "c1", map[string]any{"closed": true})
	if !restart || notice != "" || effects != nil || err != nil {
		t.Errorf("Restart = %v, %q, %v, %v; quiero reinicio sin aviso ni efectos", restart, notice, effects, err)
	}

	vars := map[string]any{}
	if err := policy.Seed(ctx, "t1", vars); err != nil || vars["page_size"] != 5 {
		t.Errorf("Seed = %v con vars %v, quiero el mapa sembrado", err, vars)
	}
}
