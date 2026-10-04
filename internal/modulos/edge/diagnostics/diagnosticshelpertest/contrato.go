// Package diagnosticshelpertest es la suite de contrato del puerto diagnostics.Store y su doble
// en memoria, Memoria (D-F3-1: el doble vivía en el paquete de producción viejo, diagnostics.go).
// Ningún código de producción lo importa: arrastra "testing".
//
//   - contrato.go: la entrada. Montaje, ContratoStore, la tabla de casos y sus ayudas.
//   - lifecycle_contrato.go: los casos del ciclo solicitud ⇒ bundle ⇒ descarga.
//   - expiry_contrato.go: los casos del vencimiento, la purga y el borrado.
//   - memoria.go: Memoria, el Store en memoria que usan los tests de los consumidores del puerto.
//
// La suite la corren las dos implementaciones del puerto: Memoria en unitario (memoria_test.go) y
// diagnostics.Postgres en los procesos de F9, con el arnés de testcontainers (F3-05).
//
// Nuevo: no tiene fichero viejo. Los casos salen de plan/F3-edge/diseno.md §2 y de los tests
// viejos de internal/diagnostics @ 8896f13 (diagnostics_internal_test.go y
// postgres_integration_test.go), leídos, no portados.
package diagnosticshelpertest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
)

// Montaje es lo que cada implementación entrega a la suite para UN caso: ContratoStore llama a
// nuevo una vez por caso.
//
// ⚠️ La purga de CreateRequest es GLOBAL (borra las vencidas de cualquier tenant): mientras corre
// la suite, nadie más puede crear solicitudes en el mismo almacén, o purgaría las vencidas que un
// caso acaba de sembrar. Con Postgres, la base es de la suite.
type Montaje struct {
	// Store es la implementación bajo prueba.
	Store diagnostics.Store
	// TenantA y TenantB son dos tenants que EXISTEN, distintos y con forma de UUID (con Postgres,
	// filas de public.tenants: las dos tablas los referencian por clave foránea). Nacen SIN fila
	// de consentimiento y sin solicitudes.
	TenantA, TenantB string
	// SetConsent fija el consentimiento del tenant, que el puerto solo deja leer: enabled=false
	// es el opt-out (con Postgres, la fila enabled=FALSE de public.tenant_diagnostics_consent) y
	// enabled=true lo vuelve a consentir. Falla el test t si no puede.
	SetConsent func(t *testing.T, tenantID string, enabled bool)
	// Expire hace vencer una solicitud que YA existe, pendiente o lista, sin tocar nada más (con
	// Postgres, lleva su expires_at al pasado). El puerto solo deja fijar el vencimiento al
	// crearla. Falla el test t si el tenant no tiene esa solicitud.
	Expire func(t *testing.T, tenantID, commandID string)
}

// ContratoStore ejecuta las promesas de diagnostics.Store (y de diagnostics.BundleReceiver, que
// es parte de él) contra la implementación que devuelve nuevo, con un Montaje limpio por caso
// (nuevo se llama una vez por t.Run). No salta nada.
//
// Los command_id de la suite son UUID nuevos en cada caso: con Postgres son la clave primaria de
// una tabla que los casos comparten.
//
// Lo que la suite NO afirma, a propósito, porque las dos implementaciones divergen o no lo
// comparten:
//
//   - repetir un command_id en CreateRequest: Postgres falla (clave primaria) y la Memoria pisa
//     la solicitud anterior;
//   - el valor de RequestedAt y ReceivedAt: son el now() de la base en Postgres y el reloj del
//     proceso en la Memoria. Solo se afirma que no son cero;
//   - el instante exacto del vencimiento (los dos relojes de diagnostics.Postgres): los
//     vencimientos de la suite quedan a una hora de «ahora», por delante o por detrás.
func ContratoStore(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("diagnosticshelpertest.ContratoStore: nuevo es nil; hace falta una función que devuelva un Montaje")
	}
	for _, c := range cases() {
		t.Run(c.name, func(t *testing.T) {
			m := nuevo(t)
			validateMontaje(t, m)
			c.run(t, m)
		})
	}
}

// contractCase es una promesa del puerto: su nombre (el del t.Run) y la función que la afirma.
type contractCase struct {
	name string
	run  func(t *testing.T, m Montaje)
}

// cases es la tabla de la suite. El comentario de cada fila es la promesa que fija.
func cases() []contractCase {
	return []contractCase{
		// Consentimiento.
		{"Consent_DefaultIsOn", caseConsentDefaultOn},       // sin fila ⇒ consentido (opt-out)
		{"Consent_OptOutAndBack", caseConsentOptOutAndBack}, // enabled=false ⇒ off; true ⇒ on
		{"Consent_StaysInItsTenant", caseConsentIsolation},  // el opt-out de uno no toca al otro
		// Ciclo solicitud ⇒ bundle ⇒ descarga (lifecycle_contrato.go).
		{"Request_WithoutBundle_IsPending", casePending},                           // viva y sin bundle ⇒ ErrPending
		{"Request_Bundle_Download", caseRoundTrip},                                 // correlada por command_id + (tenant, sesión)
		{"Bundle_EmptyParts_AreKept", caseEmptyBundle},                             // un bundle vacío también es un bundle
		{"Bundle_Orphan_NotFoundWithoutError", caseOrphanBundle},                   // sin solicitud ⇒ found=false
		{"Bundle_FromAnotherTenant_DoesNotCorrelate", caseBundleFromOtherTenant},   // INV-8
		{"Bundle_FromAnotherSession_DoesNotCorrelate", caseBundleFromOtherSession}, // identidad mTLS del stream
		{"Bundle_SecondForTheSameRequest_IsOrphan", caseSecondBundle},              // solo casa una PENDIENTE
		{"Download_UnknownCommand_NotFound", caseUnknownCommand},                   // nunca se pidió ⇒ ErrNotFound
		{"Download_FromAnotherTenant_NotFound", caseDownloadFromOtherTenant},       // 404 opaco, INV-8
		// Vencimiento, purga y borrado (expiry_contrato.go).
		{"Expired_Pending_ExpiredThenGone", caseExpiredPending},             // ErrExpired y borrado perezoso
		{"Expired_Ready_ExpiredThenGone", caseExpiredReady},                 // vencida gana a lista
		{"Expired_BundleDoesNotCorrelate", caseBundleForExpired},            // un bundle tardío es huérfano
		{"Expired_FromCreation_ExpiredThenGone", caseCreatedExpired},        // el vencimiento es el que se pidió
		{"Create_PurgesExpiredRequests", caseCreatePurges},                  // crear purga las vencidas
		{"Create_KeepsLiveRequests", caseCreateKeepsLive},                   // y solo las vencidas
		{"Delete_RemovesTheRequest", caseDelete},                            // rollback
		{"Delete_FromAnotherTenant_DoesNothing", caseDeleteFromOtherTenant}, // INV-8
		{"Delete_UnknownCommand_NoError", caseDeleteUnknown},                // borrar lo que no hay no es error
		{"Delete_ThenBundle_IsOrphan", caseBundleAfterDelete},               // tras el rollback no casa nada
	}
}

// validateMontaje exige lo que la suite da por hecho de un Montaje.
func validateMontaje(t *testing.T, m Montaje) {
	t.Helper()
	switch {
	case m.Store == nil:
		t.Fatal("Montaje.Store es nil")
	case m.SetConsent == nil:
		t.Fatal("Montaje.SetConsent es nil: la suite necesita fijar el consentimiento")
	case m.Expire == nil:
		t.Fatal("Montaje.Expire es nil: la suite necesita hacer vencer una solicitud")
	case m.TenantA == m.TenantB:
		t.Fatalf("Montaje: TenantA y TenantB son el mismo (%q); deben ser distintos", m.TenantA)
	}
	for _, tenant := range []string{m.TenantA, m.TenantB} {
		if _, err := uuid.Parse(tenant); err != nil {
			t.Fatalf("Montaje: el tenant %q no es un UUID bien formado: %v", tenant, err)
		}
	}
}

// Los valores fijos de la suite.
const (
	sessionMain  = "contract-session-main"
	sessionOther = "contract-session-other"
	requester    = "contract-user-1"
)

// sampleBundle es un bundle con sus tres partes.
var sampleBundle = diagnostics.Bundle{
	LogTail:        "contract log tail",
	GoroutineDump:  "contract goroutine dump",
	SubsystemsJSON: `{"contract":1}`,
}

// alive y expired son vencimientos a una hora de ahora: lejos del borde para los dos relojes.
func alive() time.Time   { return time.Now().Add(time.Hour) }
func expired() time.Time { return time.Now().Add(-time.Hour) }

// newCommand devuelve un command_id nuevo.
func newCommand() string { return uuid.NewString() }

func caseConsentDefaultOn(t *testing.T, m Montaje) {
	requireConsent(t, m, m.TenantA, true)
	requireConsent(t, m, m.TenantB, true)
}

func caseConsentOptOutAndBack(t *testing.T, m Montaje) {
	m.SetConsent(t, m.TenantA, false)
	requireConsent(t, m, m.TenantA, false)
	m.SetConsent(t, m.TenantA, true)
	requireConsent(t, m, m.TenantA, true)
}

func caseConsentIsolation(t *testing.T, m Montaje) {
	m.SetConsent(t, m.TenantA, false)
	requireConsent(t, m, m.TenantA, false)
	requireConsent(t, m, m.TenantB, true)
}

// requireConsent afirma que ConsentEnabled devuelve (want, nil).
func requireConsent(t *testing.T, m Montaje, tenantID string, want bool) {
	t.Helper()
	got, err := m.Store.ConsentEnabled(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("ConsentEnabled(%q): error inesperado %v", tenantID, err)
	}
	if got != want {
		t.Errorf("ConsentEnabled(%q) = %v, quería %v", tenantID, got, want)
	}
}

// create crea una solicitud de la sesión principal con ese vencimiento, o falla el test.
func create(t *testing.T, m Montaje, tenantID, commandID string, expiresAt time.Time) {
	t.Helper()
	err := m.Store.CreateRequest(context.Background(), tenantID, sessionMain, commandID, requester, expiresAt)
	if err != nil {
		t.Fatalf("CreateRequest(%q, %q): error inesperado %v", tenantID, commandID, err)
	}
}

// requireSave afirma que SaveBundle devuelve (want, nil).
func requireSave(t *testing.T, m Montaje, tenantID, sessionID, commandID string, b diagnostics.Bundle, want bool) {
	t.Helper()
	found, err := m.Store.SaveBundle(context.Background(), tenantID, sessionID, commandID, b)
	if err != nil {
		t.Fatalf("SaveBundle(%q, %q, %q): error inesperado %v (un huérfano no es un error)", tenantID, sessionID, commandID, err)
	}
	if found != want {
		t.Errorf("SaveBundle(%q, %q, %q): found = %v, quería %v", tenantID, sessionID, commandID, found, want)
	}
}

// requireGetError afirma que GetBundle devuelve ese centinela y un Record vacío.
func requireGetError(t *testing.T, m Montaje, tenantID, commandID string, want error) {
	t.Helper()
	rec, err := m.Store.GetBundle(context.Background(), tenantID, commandID)
	if !errors.Is(err, want) {
		t.Fatalf("GetBundle(%q, %q): err = %v, quería %v", tenantID, commandID, err, want)
	}
	if rec != (diagnostics.Record{}) {
		t.Errorf("GetBundle(%q, %q) con error devolvió %+v, quería el Record vacío", tenantID, commandID, rec)
	}
}

// requireRecord afirma que GetBundle devuelve la solicitud lista, con ese bundle.
func requireRecord(t *testing.T, m Montaje, tenantID, commandID string, want diagnostics.Bundle) {
	t.Helper()
	rec, err := m.Store.GetBundle(context.Background(), tenantID, commandID)
	if err != nil {
		t.Fatalf("GetBundle(%q, %q): error inesperado %v", tenantID, commandID, err)
	}
	if rec.CommandID != commandID || rec.SessionID != sessionMain || rec.RequestedBy != requester {
		t.Errorf("GetBundle: (command %q, sesión %q, pedido por %q), quería (%q, %q, %q)",
			rec.CommandID, rec.SessionID, rec.RequestedBy, commandID, sessionMain, requester)
	}
	if rec.Bundle != want {
		t.Errorf("GetBundle: bundle = %+v, quería %+v", rec.Bundle, want)
	}
	if rec.RequestedAt.IsZero() {
		t.Error("GetBundle: RequestedAt es cero")
	}
	if rec.ReceivedAt.IsZero() {
		t.Error("GetBundle: ReceivedAt es cero: una solicitud lista lleva cuándo llegó su bundle")
	}
}

// deleteRequest borra la solicitud o falla el test.
func deleteRequest(t *testing.T, m Montaje, tenantID, commandID string) {
	t.Helper()
	if err := m.Store.DeleteRequest(context.Background(), tenantID, commandID); err != nil {
		t.Fatalf("DeleteRequest(%q, %q): error inesperado %v", tenantID, commandID, err)
	}
}
