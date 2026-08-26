package intakes_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// retencion_test.go — LA PODA PEREZOSA CON RELOJ FAKE (Plan 044 · Ola 3 · T3.5).
//
// El criterio de T3.5 lo pide literal: «test de poda con reloj fake: revisión más
// vieja que el TTL ⇒ campos cifrados eliminados en el siguiente acceso,
// interpretación estructurada intacta y evento de poda logueado».
//
// 🔴 POR QUÉ CONTRA EL MemoryStore Y NO CONTRA POSTGRES. Porque el reloj tiene que
// ser falso de arriba abajo, y en el store real NO LO ES ni puede serlo: allí el
// `created_at` lo pone la BD y la edad se calcula EN SQL (`now() - created_at`),
// precisamente para no comparar dos relojes. Un «reloj fake» inyectado en Go contra
// un created_at de Postgres sería la comparación cruzada que ese diseño evita. La
// versión contra Postgres de verdad existe y está en literal_integration_test.go:
// allí la revisión se ENVEJECE moviendo su created_at, y quien mide es la BD.
//
// Los dos tests juntos cubren el criterio entero: éste prueba la POLÍTICA con un
// reloj que controla el test, aquél prueba que la POLÍTICA está cableada en el SQL.

// revisionInterpretada devuelve el payload §7.4 mínimo que hace falta para hablar de
// retención: una interpretación estructurada (nivel 1) y un literal (nivel 2).
func revisionInterpretada(t *testing.T) json.RawMessage {
	t.Helper()
	const payload = `{
		"version": 1,
		"source_text": "cliente: Hola Herminia, quería dos tortas para el miércoles",
		"lines": [
			{"kind":"matched","sku":"torta-choc","label":"Torta de chocolate","qty":2,
			 "unit_price":25000,"customization":"sin sal",
			 "evidence":"quería dos tortas para el miércoles"}
		],
		"suggested_questions": []
	}`
	return json.RawMessage(payload)
}

// storeConRelojFake monta el store en memoria con un reloj que el test mueve a mano
// y un espía del log por el que tiene que salir el evento de poda.
func storeConRelojFake(t *testing.T, ttl time.Duration) (*intakes.MemoryStore, *logSpy, *time.Time) {
	t.Helper()
	ahora := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	reloj := &ahora
	spy := &logSpy{}

	st := intakes.NewMemoryStore()
	st.SetClock(func() time.Time { return *reloj })
	st.SetLiteralTTL(ttl)
	st.SetLogDeRetencion(spy)
	return st, spy, reloj
}

// TestPoda_RevisionMasViejaQueElTTL es el criterio de T3.5, entero y en un test.
func TestPoda_RevisionMasViejaQueElTTL(t *testing.T) {
	ctx := context.Background()
	st, spy, reloj := storeConRelojFake(t, intakes.TTLLiteralPorDefecto)
	intakeID := uuid.NewString()

	escrita, err := st.InsertRevision(ctx, intakes.Revision{
		IntakeID:  intakeID,
		Kind:      intakes.RevisionKindInterpreted,
		Payload:   revisionInterpretada(t),
		CreatedBy: intakes.RevisionBySystem,
	})
	if err != nil {
		t.Fatalf("InsertRevision: %v", err)
	}

	// --- PRIMERO: EL TEXTO SÍ ESTÁ. Sin esta mitad, todo lo de abajo saldría verde
	// midiendo cero — un seed que no siembra deja la verificación corriendo sobre
	// nada y no protesta.
	antes := st.Revisions(intakeID)
	if len(antes) != 1 {
		t.Fatalf("revisiones escritas = %d, se esperaba 1", len(antes))
	}
	if !strings.Contains(string(antes[0].Payload), "Hola Herminia") {
		t.Fatalf("el literal no vuelve al leer una revisión VIGENTE: %s", antes[0].Payload)
	}
	if !antes[0].LiteralPrunedAt.IsZero() {
		t.Fatal("una revisión recién escrita no puede estar podada")
	}
	// Y no está en lo PERSISTIDO: el payload que devolvió la escritura es el que fue
	// a parar al almacén, y ahí el literal no puede aparecer.
	if strings.Contains(string(escrita.Payload), "Hola Herminia") {
		t.Fatalf("el literal se persistió dentro del payload: %s", escrita.Payload)
	}

	// --- EL RELOJ AVANZA 12 MESES Y UN DÍA -------------------------------------
	*reloj = reloj.Add(intakes.TTLLiteralPorDefecto + 24*time.Hour)

	despues := st.Revisions(intakeID)
	if len(despues) != 1 {
		t.Fatalf("la poda se llevó la revisión entera: quedan %d", len(despues))
	}
	rev := despues[0]

	// (1) Campos cifrados eliminados.
	if strings.Contains(string(rev.Payload), "Hola Herminia") ||
		strings.Contains(string(rev.Payload), "para el miércoles") {
		t.Fatalf("el literal sobrevivió al vencimiento del TTL: %s", rev.Payload)
	}
	if rev.LiteralPrunedAt.IsZero() {
		t.Fatal("la revisión se podó pero no quedó sellada con literal_pruned_at")
	}

	// (2) Interpretación estructurada INTACTA. Es la mitad que distingue una poda de
	// una pérdida de datos.
	sinEspacios := strings.ReplaceAll(string(rev.Payload), " ", "")
	for _, debeSeguir := range []string{`"torta-choc"`, `"sinsal"`, `"qty":2`, `25000`} {
		if !strings.Contains(sinEspacios, debeSeguir) {
			t.Fatalf("la poda se llevó por delante la interpretación (%s):\n%s", debeSeguir, rev.Payload)
		}
	}

	// (3) Evento de poda logueado — y SIN una palabra del texto que acaba de
	// destruir: dejarlo en un fichero de log sería sacarlo de la base para meterlo en
	// un sitio que no se cifra ni se rota.
	log := spy.all()
	if !strings.Contains(log, "podado por TTL vencido") {
		t.Fatalf("la poda no dejó evento en el log:\n%s", log)
	}
	if strings.Contains(log, "Herminia") || strings.Contains(log, "miércoles") {
		t.Fatalf("el evento de poda arrastró literal del cliente:\n%s", log)
	}
}

// TestPoda_NoSeRepiteNiMuevesuFecha: la segunda lectura de una revisión ya podada no
// vuelve a anunciar una destrucción que ya ocurrió, y `literal_pruned_at` conserva el
// instante REAL en vez de moverse con cada visita a la bandeja.
func TestPoda_NoSeRepiteNiMueveSuFecha(t *testing.T) {
	ctx := context.Background()
	st, spy, reloj := storeConRelojFake(t, intakes.TTLLiteralPorDefecto)
	intakeID := uuid.NewString()

	if _, err := st.InsertRevision(ctx, intakes.Revision{
		IntakeID: intakeID, Kind: intakes.RevisionKindInterpreted,
		Payload: revisionInterpretada(t), CreatedBy: intakes.RevisionBySystem,
	}); err != nil {
		t.Fatalf("InsertRevision: %v", err)
	}

	*reloj = reloj.Add(intakes.TTLLiteralPorDefecto + time.Hour)
	primera := st.Revisions(intakeID)[0]

	*reloj = reloj.Add(30 * 24 * time.Hour)
	segunda := st.Revisions(intakeID)[0]

	if !segunda.LiteralPrunedAt.Equal(primera.LiteralPrunedAt) {
		t.Fatalf("literal_pruned_at se movió en la segunda lectura: %s -> %s",
			primera.LiteralPrunedAt, segunda.LiteralPrunedAt)
	}
	if n := strings.Count(spy.all(), "podado por TTL vencido"); n != 1 {
		t.Fatalf("el evento de poda se emitió %d veces, se esperaba 1", n)
	}
}

// TestPoda_ConTTLCeroNoSePodaNunca. El 0 de esta clave significa RETENCIÓN
// INDEFINIDA, igual que el de event_history_ttl_seconds. Leerlo al revés destruiría
// el literal de todo tenant que la dejara a cero, en la primera lectura y sin vuelta.
func TestPoda_ConTTLCeroNoSePodaNunca(t *testing.T) {
	ctx := context.Background()
	st, spy, reloj := storeConRelojFake(t, 0)
	intakeID := uuid.NewString()

	if _, err := st.InsertRevision(ctx, intakes.Revision{
		IntakeID: intakeID, Kind: intakes.RevisionKindInterpreted,
		Payload: revisionInterpretada(t), CreatedBy: intakes.RevisionBySystem,
	}); err != nil {
		t.Fatalf("InsertRevision: %v", err)
	}

	*reloj = reloj.Add(100 * 365 * 24 * time.Hour) // un siglo
	rev := st.Revisions(intakeID)[0]

	if !strings.Contains(string(rev.Payload), "Hola Herminia") {
		t.Fatalf("con TTL 0 se podó el literal, y 0 significa SIN PODA: %s", rev.Payload)
	}
	if strings.Contains(spy.all(), "podado por TTL vencido") {
		t.Fatalf("con TTL 0 se emitió un evento de poda:\n%s", spy.all())
	}
}

// TestPoda_UnaRevisionSinLiteralNoSePodaNiSeAnuncia cubre el caso mayoritario de la
// tabla: las del carrito numérico. Anunciar su «poda» llenaría el log de destrucciones
// que nunca ocurrieron y haría inútil el propio evento.
func TestPoda_UnaRevisionSinLiteralNoSeAnuncia(t *testing.T) {
	ctx := context.Background()
	st, spy, reloj := storeConRelojFake(t, intakes.TTLLiteralPorDefecto)
	intakeID := uuid.NewString()

	payload, err := intakes.CartRevisionPayload(5000, []intakes.RevisionLine{
		{SKU: "emp-pino", Label: "Empanada de pino", Qty: 2, UnitPrice: 2500},
	})
	if err != nil {
		t.Fatalf("CartRevisionPayload: %v", err)
	}
	if _, err := st.InsertRevision(ctx, intakes.Revision{
		IntakeID: intakeID, Kind: intakes.RevisionKindCart,
		Payload: payload, CreatedBy: intakes.RevisionBySystem,
	}); err != nil {
		t.Fatalf("InsertRevision: %v", err)
	}

	*reloj = reloj.Add(10 * intakes.TTLLiteralPorDefecto)
	rev := st.Revisions(intakeID)[0]

	if !rev.LiteralPrunedAt.IsZero() {
		t.Fatal("se selló como podada una revisión que nunca tuvo literal")
	}
	if !strings.Contains(string(rev.Payload), "emp-pino") {
		t.Fatalf("la revisión del carrito perdió su contenido: %s", rev.Payload)
	}
	if strings.Contains(spy.all(), "podado por TTL vencido") {
		t.Fatalf("se anunció la poda de una revisión sin literal:\n%s", spy.all())
	}
}

// TestGet_DevuelveElLiteralAlDuenoAutorizado es la otra mitad del criterio («la API
// de detalle sí devuelve el texto descifrado al dueño autorizado») en su versión sin
// BD: el camino de lectura del DETALLE, no el mirador de tests. Que el detalle esté
// acotado al tenant es INV-8 y ya lo prueban los tests del handler; lo que se
// comprueba aquí es que ese camino trae el literal.
func TestGet_DevuelveElLiteralAlDuenoAutorizado(t *testing.T) {
	ctx := context.Background()
	st, _, _ := storeConRelojFake(t, intakes.TTLLiteralPorDefecto)
	tenant, intakeID := uuid.NewString(), uuid.NewString()

	st.Add(tenant, intakes.Intake{ID: intakeID, Status: intakes.StatusPendingApproval})
	if _, err := st.InsertRevision(ctx, intakes.Revision{
		IntakeID: intakeID, Kind: intakes.RevisionKindInterpreted,
		Payload: revisionInterpretada(t), CreatedBy: intakes.RevisionBySystem,
	}); err != nil {
		t.Fatalf("InsertRevision: %v", err)
	}

	detalle, err := st.Get(ctx, tenant, intakeID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(detalle.Revisions) != 1 {
		t.Fatalf("el detalle trae %d revisiones, se esperaba 1", len(detalle.Revisions))
	}
	if !strings.Contains(string(detalle.Revisions[0].Payload), "Hola Herminia") {
		t.Fatalf("el detalle no devolvió el texto descifrado: %s", detalle.Revisions[0].Payload)
	}
}
