//go:build pendiente

package ingest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/ingest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/ingest/ingesthelpertest"
)

// ingestDeduper es, copiada, la interfaz que declara el runtime del motor de flujos
// (runtime.IngestDeduper): la consume estructuralmente, así que la firma de Seen no puede cambiar.
type ingestDeduper interface {
	Seen(ctx context.Context, sessionID, waMessageID string) (bool, error)
}

// Las dos implementaciones son Deduper, y un Deduper sirve donde el runtime pide el suyo: lo
// comprueba el compilador.
var (
	_ ingest.Deduper = (*ingest.PostgresDeduper)(nil)
	_ ingest.Deduper = (*ingesthelpertest.Memoria)(nil)
	_ ingestDeduper  = ingest.Deduper(nil)
)

// failingDeduper es un Deduper cuya base está caída: cumple el contrato del error (false + error).
type failingDeduper struct{ err error }

func (d failingDeduper) Seen(context.Context, string, string) (bool, error) { return false, d.err }

// admit es el uso que el contrato describe para el consumidor: procesa el entrante salvo que el
// Deduper diga, SIN error, que ya lo vio (fail-open).
func admit(d ingest.Deduper, sessionID, waMessageID string) bool {
	seen, err := d.Seen(context.Background(), sessionID, waMessageID)
	return err != nil || !seen
}

// TestDeduper_ConsumerReadsFalseAsProcess: con el contrato de Seen, un consumidor fail-open
// procesa el primer avistamiento, ignora el duplicado y procesa también cuando el dedupe falla.
func TestDeduper_ConsumerReadsFalseAsProcess(t *testing.T) {
	var d ingest.Deduper = ingesthelpertest.NewMemoria()
	if !admit(d, "session-1", "wamid.A") {
		t.Error("el primer avistamiento no se procesó: Seen tenía que devolver false")
	}
	if admit(d, "session-1", "wamid.A") {
		t.Error("el duplicado se procesó: Seen tenía que devolver true")
	}
	if !admit(failingDeduper{err: errors.New("base caída")}, "session-1", "wamid.A") {
		t.Error("con el dedupe caído el entrante no se procesó: el error viaja con false (fail-open)")
	}
}
