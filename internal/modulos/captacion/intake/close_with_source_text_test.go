package intake_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// Los tests propios del gemelo para la cuarta operación de la cola,
// intake.JobStore.CloseWithSourceText (D-F7-9, D-F8-13): su contador, su gancho de fallo y el
// orden de sus rechazos. Van aparte de memory_test.go por tamaño (E-13). Las promesas del puerto
// las corre TestMemoryStore_ContratoQueue, con el resto de la suite de la cola.

// liveWindow abre una ventana en un store nuevo y la devuelve como la ve el barrido, con el reloj
// ya adelantado: lo que se escriba después lleva un instante posterior al de la lectura.
func liveWindow(t *testing.T) (*intake.MemoryStore, intake.OpenJob) {
	t.Helper()
	clock := newMemoryClock()
	store := intake.NewMemoryStore(clock.Now)
	ctx := context.Background()
	if err := store.OpenOrAppend(ctx, intake.Append{Key: key("e1"), Refs: []string{"one"}}); err != nil {
		t.Fatalf("OpenOrAppend: error inesperado %v", err)
	}
	live, err := store.ListAggregating(ctx, 10)
	if err != nil || len(live) != 1 {
		t.Fatalf("ListAggregating = (%+v, %v), quería la ventana recién abierta", live, err)
	}
	clock.Advance(time.Second)
	store.ResetCounters()
	return store, live[0]
}

// TestMemoryStore_CloseWithSourceText_CountsCallsNotEffects: el presupuesto de I/O cuenta las
// LLAMADAS a CloseWithSourceText en su propio contador —la que cierra, la que ya no encuentra la
// ventana y las rechazadas—, y no en el de CloseWindow.
func TestMemoryStore_CloseWithSourceText_CountsCallsNotEffects(t *testing.T) {
	store, seen := liveWindow(t)
	ctx := context.Background()
	if ok, err := store.CloseWithSourceText(ctx, seen, fullEnvelope()); err != nil || !ok {
		t.Fatalf("CloseWithSourceText = (%v, %v), quería (true, nil)", ok, err)
	}
	if ok, err := store.CloseWithSourceText(ctx, seen, fullEnvelope()); err != nil || ok { // ya cerrada, y cuenta
		t.Fatalf("segundo CloseWithSourceText = (%v, %v), quería (false, nil)", ok, err)
	}
	if _, err := store.CloseWithSourceText(ctx, seen, intake.SourceText{Enc: []byte("enc")}); err == nil { // sobre a medias, y cuenta
		t.Fatal("CloseWithSourceText con un sobre a medias: quería error")
	}
	noID := seen
	noID.ID = ""
	if _, err := store.CloseWithSourceText(ctx, noID, fullEnvelope()); err == nil { // sin id, y cuenta
		t.Fatal("CloseWithSourceText sin id: quería error")
	}
	if got, want := store.Counters(), (intake.Counters{CloseWithSourceText: 4}); got != want {
		t.Errorf("Counters = %+v, quería %+v", got, want)
	}
}

// TestMemoryStore_CloseWithSourceText_IncompleteEnvelopeMessage: el rechazo del sobre a medias
// lleva el texto del gemelo (el del gemelo viejo), byte a byte; y la ventana sigue viva y sin
// sobre.
func TestMemoryStore_CloseWithSourceText_IncompleteEnvelopeMessage(t *testing.T) {
	store, seen := liveWindow(t)
	ok, err := store.CloseWithSourceText(context.Background(), seen, intake.SourceText{Enc: []byte("enc")})
	const want = "intake: sobre del literal incompleto (son las tres o ninguna)"
	if ok || err == nil || err.Error() != want {
		t.Errorf("CloseWithSourceText = (%v, %v), quería (false, %q)", ok, err, want)
	}
	if job := store.Jobs()[0]; job.Status != intake.StatusAggregating || !job.SourceText.Empty() {
		t.Errorf("tras el rechazo la fila es (status %q, sobre %+v), quería aggregating y sin sobre", job.Status, job.SourceText)
	}
}

// TestMemoryStore_FailCloseWithSourceTextWith_LeavesTheWindowLive: con el fallo puesto,
// CloseWithSourceText devuelve (false, ESE error) —antes incluso de mirar el sobre— y la ventana
// se queda VIVA, sin sobre y sin tocar: al ser una sola sentencia no hay medio cierre. nil vuelve
// a la normalidad, y el mismo `seen` todavía sirve.
func TestMemoryStore_FailCloseWithSourceTextWith_LeavesTheWindowLive(t *testing.T) {
	store, seen := liveWindow(t)
	ctx := context.Background()
	before := store.Jobs()[0]
	boom := errors.New("la base no contesta")
	store.FailCloseWithSourceTextWith(boom)
	envelopes := map[string]intake.SourceText{"completo": fullEnvelope(), "vacío": {}, "a medias": {Enc: []byte("enc")}}
	for name, env := range envelopes {
		if ok, err := store.CloseWithSourceText(ctx, seen, env); !errors.Is(err, boom) || ok {
			t.Errorf("CloseWithSourceText (sobre %s) con el fallo puesto = (%v, %v), quería (false, %v)", name, ok, err, boom)
		}
	}
	if job := store.Jobs()[0]; job.Status != intake.StatusAggregating || !job.SourceText.Empty() || !job.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("tras el fallo la fila es (status %q, sobre %+v, updated_at %v), quería aggregating, sin sobre y con la marca de antes (%v)",
			job.Status, job.SourceText, job.UpdatedAt, before.UpdatedAt)
	}
	store.FailCloseWithSourceTextWith(nil)
	if ok, err := store.CloseWithSourceText(ctx, seen, fullEnvelope()); err != nil || !ok {
		t.Errorf("CloseWithSourceText tras quitar el fallo = (%v, %v), quería (true, nil)", ok, err)
	}
}

// TestMemoryStore_CloseWithSourceText_IncompleteWindow_ComesBeforeTheInjectedFailure: la clave a
// medias y el id vacío se miran antes que el fallo inyectado, que simula la base: a Postgres esa
// llamada ni le llega.
func TestMemoryStore_CloseWithSourceText_IncompleteWindow_ComesBeforeTheInjectedFailure(t *testing.T) {
	store, seen := liveWindow(t)
	boom := errors.New("la base no contesta")
	store.FailCloseWithSourceTextWith(boom)
	noEvent, noID := seen, seen
	noEvent.Key.EventID = ""
	noID.ID = ""
	const want = "intake: ventana incompleta al cerrar con el literal"
	for name, job := range map[string]intake.OpenJob{"sin evento": noEvent, "sin id": noID} {
		ok, err := store.CloseWithSourceText(context.Background(), job, fullEnvelope())
		if ok || err == nil || err.Error() != want {
			t.Errorf("CloseWithSourceText (%s) = (%v, %v), quería (false, %q) y no el fallo inyectado", name, ok, err, want)
		}
	}
	if job := store.Jobs()[0]; job.Status != intake.StatusAggregating {
		t.Errorf("las llamadas rechazadas dejaron la ventana en %q, quería aggregating", job.Status)
	}
}
