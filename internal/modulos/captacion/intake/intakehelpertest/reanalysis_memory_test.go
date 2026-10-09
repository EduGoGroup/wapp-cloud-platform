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
