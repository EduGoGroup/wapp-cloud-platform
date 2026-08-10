// event_pair_test.go — el par contenedor↔contenido sobre el store en MEMORIA
// (Ola 4.5 · T4.5.5): las mismas semánticas que fija event_pair_pg_test.go contra
// Postgres, en el doble que usan los tests de handler. Si divergieran, un test de
// publicapi contra el MemoryStore diría algo falso sobre producción.
package intakes_test

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// parEnMemoria monta tenant + evento + solicitud ligada en el MemoryStore.
func parEnMemoria(estadoIntake, estadoEvento string) (*intakes.MemoryStore, string, string, string) {
	st := intakes.NewMemoryStore()
	tenant, intakeID, eventID := "t45i-tenant", "t45i-intake", "t45i-evento"
	st.Add(tenant, intakes.Intake{
		ID: intakeID, ContactID: "t45i-contacto", SessionID: "t45i-sess",
		Status: estadoIntake, Total: 18000, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	st.SetEvent(eventID, estadoEvento)
	st.BindEvent(intakeID, eventID)
	return st, tenant, intakeID, eventID
}

// TestMemoria_AbandonByEvent_ParidadConPostgres: abandona el `open`, es
// idempotente en el segundo intento y no toca una `settled` — por Service, que es
// la puerta que el bootstrap adapta al puerto IntakeAbandoner del runtime.
func TestMemoria_AbandonByEvent_ParidadConPostgres(t *testing.T) {
	ctx := context.Background()

	st, tenant, intakeID, eventID := parEnMemoria(intakes.StatusOpen, "cancelled")
	svc := intakes.NewService(st)
	if err := svc.AbandonByEvent(ctx, tenant, eventID); err != nil {
		t.Fatalf("AbandonByEvent: %v", err)
	}
	det, err := st.Get(ctx, tenant, intakeID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if det.Status != intakes.StatusAbandoned {
		t.Fatalf("status=%q, quiero abandoned", det.Status)
	}

	// Idempotente: el reintento de la cancelación a medias termina en éxito.
	if err := svc.AbandonByEvent(ctx, tenant, eventID); err != nil {
		t.Fatalf("segundo AbandonByEvent: %v", err)
	}
	// Y un evento sin contenido también (0 coincidencias = nil).
	if err := svc.AbandonByEvent(ctx, tenant, "t45i-evento-sin-hijo"); err != nil {
		t.Fatalf("AbandonByEvent sin contenido: %v", err)
	}

	// El guard del estado: una resuelta por un humano no se abandona.
	st2, tenant2, intakeID2, eventID2 := parEnMemoria(intakes.StatusSettled, "cancelled")
	if err := intakes.NewService(st2).AbandonByEvent(ctx, tenant2, eventID2); err != nil {
		t.Fatalf("AbandonByEvent sobre settled: %v", err)
	}
	det2, err := st2.Get(ctx, tenant2, intakeID2)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if det2.Status != intakes.StatusSettled {
		t.Fatalf("status=%q; una settled no se abandona", det2.Status)
	}
}

// TestMemoria_Discard_MiraElEventoYCierraElContenedor: la guarda `live_event`
// pregunta por el evento que la solicitud declara (DT-043.2 saldada) y el descarte
// consumado deja el contenedor sin vida rescatable (REQ-32e). El intake LEGADO
// —sin ligadura— no tiene evento vivo que mirar y es descartable.
func TestMemoria_Discard_MiraElEventoYCierraElContenedor(t *testing.T) {
	ctx := context.Background()

	// Evento open + intake suyo ⇒ live_event, sin escribir nada.
	st, tenant, intakeID, eventID := parEnMemoria(intakes.StatusOpen, "open")
	out, err := st.Discard(ctx, tenant, intakeID, intakes.DiscardableStatuses())
	if err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if out.Discarded || !out.LiveEvent {
		t.Fatalf("outcome=%+v; quiero LiveEvent=true sin descartar", out)
	}
	if st.EventStatus(eventID) != "open" {
		t.Fatalf("el rechazo no toca el evento: %q", st.EventStatus(eventID))
	}

	// El mismo par con el evento muerto: el huérfano se descarta.
	st, tenant, intakeID, eventID = parEnMemoria(intakes.StatusOpen, "cancelled")
	out, err = st.Discard(ctx, tenant, intakeID, intakes.DiscardableStatuses())
	if err != nil {
		t.Fatalf("Discard del huérfano: %v", err)
	}
	if !out.Discarded || out.LiveEvent {
		t.Fatalf("outcome=%+v; con el evento muerto el descarte procede", out)
	}
	if st.EventStatus(eventID) != "cancelled" {
		t.Fatalf("el contenedor ya estaba terminal y no se pisa: %q", st.EventStatus(eventID))
	}

	// Intake legado sin event_id ⇒ no hay evento vivo ⇒ descartable.
	legado := intakes.NewMemoryStore()
	legado.Add(tenant, intakes.Intake{
		ID: "t45i-legado", ContactID: "c", SessionID: "s", Status: intakes.StatusOpen,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	out, err = legado.Discard(ctx, tenant, "t45i-legado", intakes.DiscardableStatuses())
	if err != nil {
		t.Fatalf("Discard del legado: %v", err)
	}
	if !out.Discarded || out.LiveEvent {
		t.Fatalf("outcome=%+v; el legado sin ligadura es descartable", out)
	}
}
