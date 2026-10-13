package intakehelpertest_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake/intakehelpertest"
)

// TestMachineMemory_ContratoReanalysis corre la suite del segundo productor de jobs contra el
// doble, sin BD y sin reloj real.
func TestMachineMemory_ContratoReanalysis(t *testing.T) {
	intakehelpertest.ContratoReanalysis(t, func(*testing.T) intakehelpertest.ReanalysisMontaje {
		store, table := newTable()
		return intakehelpertest.ReanalysisMontaje{Store: store, Table: table}
	})
}

// TestMachineMemory_OpenReanalysis_TheWorkerClaimsItWithItsContext: es la MISMA tabla. El job que
// abre el segundo productor es el que el worker reclama, y le llega con el contexto del dueño.
func TestMachineMemory_OpenReanalysis_TheWorkerClaimsItWithItsContext(t *testing.T) {
	store := intakehelpertest.NewMachineMemory(newTestClock().Now)
	req := intake.ReanalysisRequest{
		Key:      intake.WindowKey{TenantID: "tenant-a", SessionID: "s", ContactID: "c", EventID: "event-1"},
		IntakeID: "intake-1",
		Context:  intake.Reanalysis{RequestedBy: intake.RequestedByOwner, Via: "api", Source: "both", From: 4},
	}
	id, err := store.OpenReanalysis(context.Background(), req)
	if err != nil {
		t.Fatalf("OpenReanalysis: error inesperado %v", err)
	}
	job, ok, err := store.ClaimNext(context.Background())
	if err != nil || !ok || job.ID != id {
		t.Fatalf("ClaimNext = (%q, %v, %v), quería llevarse el job del re-análisis (%q)", job.ID, ok, err, id)
	}
	if job.Reanalysis != req.Context || !job.Reanalysis.IsFromOwner() {
		t.Errorf("contexto del job reclamado = %+v, quería %+v", job.Reanalysis, req.Context)
	}
	if job.SourceText.Complete() {
		t.Errorf("el job del re-análisis nació con sobre: %+v", job.SourceText)
	}
}

// envelopeRequest es una petición de re-análisis completa que trae ese sobre.
func envelopeRequest(env intake.SourceText) intake.ReanalysisRequest {
	return intake.ReanalysisRequest{
		Key:        intake.WindowKey{TenantID: "tenant-a", SessionID: "s", ContactID: "c", EventID: "event-1"},
		IntakeID:   "intake-1",
		Context:    intake.Reanalysis{RequestedBy: intake.RequestedByOwner, Via: "api", Source: "both", From: 4},
		SourceText: env,
	}
}

// TestMachineMemory_OpenReanalysis_TheWorkerClaimsItWithItsEnvelope: es la MISMA tabla. El job que
// abre el segundo productor con su sobre es el que el worker reclama, y le llega con las tres
// piezas: el reclamo va INMEDIATAMENTE después de abrirlo, sin ninguna escritura en medio.
func TestMachineMemory_OpenReanalysis_TheWorkerClaimsItWithItsEnvelope(t *testing.T) {
	store := intakehelpertest.NewMachineMemory(newTestClock().Now)
	want := intake.SourceText{Enc: []byte("enc-born"), DEK: []byte("dek-born"), KEKID: "kek-born"}
	id, err := store.OpenReanalysis(context.Background(), envelopeRequest(want))
	if err != nil {
		t.Fatalf("OpenReanalysis: error inesperado %v", err)
	}
	job, ok, err := store.ClaimNext(context.Background())
	if err != nil || !ok || job.ID != id {
		t.Fatalf("ClaimNext = (%q, %v, %v), quería llevarse el job del re-análisis (%q)", job.ID, ok, err, id)
	}
	got := job.SourceText
	if string(got.Enc) != string(want.Enc) || string(got.DEK) != string(want.DEK) || got.KEKID != want.KEKID {
		t.Errorf("sobre del job reclamado = %+v, quería el de la petición (%+v)", got, want)
	}
}

// TestMachineMemory_OpenReanalysis_HalfEnvelopeMessage: el rechazo del sobre a medias lleva el
// MISMO texto que el adaptador Postgres, byte a byte —como ya lo lleva el de la petición
// incompleta—: dice QUÉ falta sin citar el contenido. Y no deja fila: no hay nada que reclamar.
func TestMachineMemory_OpenReanalysis_HalfEnvelopeMessage(t *testing.T) {
	cases := map[string]intake.SourceText{
		"intake: sobre del literal incompleto (enc=0 dek=3 kek_id=true): son las tres o ninguna":  {DEK: []byte("dek"), KEKID: "k1"},
		"intake: sobre del literal incompleto (enc=7 dek=0 kek_id=true): son las tres o ninguna":  {Enc: []byte("secreto"), KEKID: "k1"},
		"intake: sobre del literal incompleto (enc=7 dek=3 kek_id=false): son las tres o ninguna": {Enc: []byte("secreto"), DEK: []byte("dek")},
	}
	store := intakehelpertest.NewMachineMemory(newTestClock().Now)
	for want, env := range cases {
		id, err := store.OpenReanalysis(context.Background(), envelopeRequest(env))
		if id != "" || err == nil || err.Error() != want {
			t.Errorf("OpenReanalysis = (%q, %v), quería (\"\", %q)", id, err, want)
		}
	}
	if job, ok, err := store.ClaimNext(context.Background()); err != nil || ok {
		t.Errorf("ClaimNext tras los rechazos = (%q, %v, %v), quería que no hubiera ningún job", job.ID, ok, err)
	}
}
