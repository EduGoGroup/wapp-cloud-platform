//go:build pendiente

package intakehelpertest_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake/intakehelpertest"
)

// ROJO de T8.40 (D-F7-9, D-F8-13) contra el doble en memoria: el job del re-análisis NACE con el
// sobre de su petición. El adaptador Postgres tiene el suyo en postgres_reanalysis_envelope_test.go.
//
// 🔧 Este fichero es PROVISIONAL. En el verde, los casos de ContratoReanalysisEnvelope se mudan a
// la tabla de ContratoReanalysis (y los corre TestMachineMemory_ContratoReanalysis, sin etiqueta),
// y los tests propios del doble de aquí abajo pasan a reanalysis_memory_test.go, que es el test de
// reanalysis_memory.go.

// TestMachineMemory_ContratoReanalysisEnvelope corre las promesas del job que nace con su sobre
// contra el doble, con el mismo Montaje que la suite del segundo productor.
func TestMachineMemory_ContratoReanalysisEnvelope(t *testing.T) {
	intakehelpertest.ContratoReanalysisEnvelope(t, func(*testing.T) intakehelpertest.ReanalysisMontaje {
		store, table := newTable()
		return intakehelpertest.ReanalysisMontaje{Store: store, Table: table}
	})
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
