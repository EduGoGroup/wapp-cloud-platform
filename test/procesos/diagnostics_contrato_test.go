//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics/diagnosticshelpertest"
)

// Este fichero corre la suite de contrato de diagnostics.Store
// (diagnosticshelpertest.ContratoStore) contra diagnostics.Postgres sobre una base clonada de la
// plantilla migrada (T3.30 = T9.24, sesión F3-05, hallazgo 12 de F3). Como
// contact_contrato_test.go, no es un proceso de caja negra: es la excepción de R9.4.d que decidió
// D-F1-8, y por eso solo importa la suite y el paquete del adaptador, del que solo usa el
// constructor (candado ProcessImports, regla 3).
//
// Además lleva a Postgres la prueba de D-F3-11, que hasta hoy solo existía con el doble en
// memoria (TestMountDiagnostics_Request_RollbackSurvivesTheClientLeaving, en internal/apipublica):
// el borrado del rollback de D5 ocurre de verdad aunque la petición ya esté cancelada.

const (
	// diagnosticsContractTimeout acota cada sentencia de siembra y de observación: la base es local
	// al contenedor de la corrida.
	diagnosticsContractTimeout = 15 * time.Second

	// diagnosticsContractExpiredBy es cuánto por detrás de «ahora» deja Expire el vencimiento: una
	// hora, como los vencimientos de la suite, lejos del borde para los dos relojes de
	// diagnostics.Postgres (el now() de la base y el del proceso; hallazgo 14 de F3).
	diagnosticsContractExpiredBy = "1 hour"

	// diagnosticsContractSession y diagnosticsContractRequester rellenan session_id y requested_by
	// de la solicitud del test de D-F3-11: el borrado no los mira.
	diagnosticsContractSession   = "rollback-session"
	diagnosticsContractRequester = "rollback-user"
)

// diagnosticsContractCases cuenta las bases pedidas en esta corrida: ContratoStore llama a nuevo
// una vez por caso (en serie) y cada uno necesita una base con nombre propio mientras la anterior
// siga viva. El test de D-F3-11 toma la suya del mismo contador.
var diagnosticsContractCases atomic.Int64

// TestDiagnosticsContrato_Postgres corre las 22 promesas del puerto diagnostics.Store, las mismas
// que pasan contra diagnosticshelpertest.Memoria, contra el adaptador Postgres. Cada caso recibe
// su base —la purga de CreateRequest es GLOBAL: borra las vencidas de cualquier tenant, así que
// dos casos sobre la misma base se pisarían—, sus dos tenants (filas reales de public.tenants: las
// dos tablas de la migración 0036 los referencian por clave foránea) y un Postgres nuevo.
func TestDiagnosticsContrato_Postgres(t *testing.T) {
	diagnosticshelpertest.ContratoStore(t, newDiagnosticsContractMontaje)
}

// newDiagnosticsContractMontaje devuelve el Montaje limpio de un caso: clona una base, siembra los
// dos tenants por SQL (sin fila de consentimiento y sin solicitudes, como pide Montaje) y construye
// el Postgres sobre el mismo *sql.DB con el que SetConsent y Expire escriben.
func newDiagnosticsContractMontaje(t *testing.T) diagnosticshelpertest.Montaje {
	t.Helper()
	db := newDiagnosticsContractDB(t)
	return diagnosticshelpertest.Montaje{
		Store:   diagnostics.NewPostgres(db),
		TenantA: seedDiagnosticsContractTenant(t, db, "diagnostico-a"),
		TenantB: seedDiagnosticsContractTenant(t, db, "diagnostico-b"),
		SetConsent: func(t *testing.T, tenantID string, enabled bool) {
			t.Helper()
			setDiagnosticsContractConsent(t, db, tenantID, enabled)
		},
		Expire: func(t *testing.T, tenantID, commandID string) {
			t.Helper()
			expireDiagnosticsContractRequest(t, db, tenantID, commandID)
		},
	}
}

// newDiagnosticsContractDB clona una base con nuevaBase (que la borra en el Cleanup del test que
// la pide) y la abre con el arnés: es la única conexión que este fichero usa (D-F9-6).
func newDiagnosticsContractDB(t *testing.T) *sql.DB {
	t.Helper()
	proceso := fmt.Sprintf("diagnostics_contrato_%02d", diagnosticsContractCases.Add(1))
	return nuevaBase(t, proceso).Abrir(t)
}

// seedDiagnosticsContractTenant inserta un tenant con el slug dado en public.tenants y devuelve su
// id. Solo rellena las columnas NOT NULL sin valor por defecto (0001_tenants.sql). Falla el test
// si no puede.
func seedDiagnosticsContractTenant(t *testing.T, db *sql.DB, slug string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), diagnosticsContractTimeout)
	defer cancel()
	var id string
	err := db.QueryRowContext(ctx,
		`INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id::text`,
		slug, "Tenant "+slug,
	).Scan(&id)
	if err != nil {
		t.Fatalf("sembrar el tenant %q: %v", slug, err)
	}
	return id
}

// setDiagnosticsContractConsent es el Montaje.SetConsent de Postgres: un upsert sobre la clave
// primaria de public.tenant_diagnostics_consent (tenant_id). enabled=false deja la fila del
// opt-out y enabled=true la vuelve a consentir sobre esa misma fila (no la borra: «fila con TRUE»
// y «sin fila» son los dos consentidos, y la suite ya afirma el segundo en Consent_DefaultIsOn).
func setDiagnosticsContractConsent(t *testing.T, db *sql.DB, tenantID string, enabled bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), diagnosticsContractTimeout)
	defer cancel()
	_, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_diagnostics_consent (tenant_id, enabled, updated_at)
		VALUES ($1::uuid, $2, now())
		ON CONFLICT (tenant_id) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()`,
		tenantID, enabled)
	if err != nil {
		t.Fatalf("SetConsent(tenant %q, %v): %v", tenantID, enabled, err)
	}
}

// expireDiagnosticsContractRequest es el Montaje.Expire de Postgres: lleva al pasado el expires_at
// de la solicitud del tenant, pendiente o lista, y no toca ninguna otra columna. Se calcula con el
// now() de la base, el mismo reloj que usan la purga de CreateRequest y el filtro de SaveBundle.
// Falla el test si el tenant no tiene esa solicitud (0 filas), como pide Montaje.
func expireDiagnosticsContractRequest(t *testing.T, db *sql.DB, tenantID, commandID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), diagnosticsContractTimeout)
	defer cancel()
	res, err := db.ExecContext(ctx, `
		UPDATE public.diagnostics_bundles
		SET expires_at = now() - $3::interval
		WHERE tenant_id = $1::uuid AND command_id = $2`,
		tenantID, commandID, diagnosticsContractExpiredBy)
	if err != nil {
		t.Fatalf("Expire(tenant %q, command %q): %v", tenantID, commandID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("Expire(tenant %q, command %q): filas afectadas: %v", tenantID, commandID, err)
	}
	if n != 1 {
		t.Fatalf("Expire(tenant %q, command %q): tocó %d filas de public.diagnostics_bundles; quería 1", tenantID, commandID, n)
	}
}

// countDiagnosticsContractRequests cuenta por SQL las filas de public.diagnostics_bundles con ese
// tenant y ese command_id: 1 si la solicitud sigue ahí, 0 si se borró. Usa un contexto propio, no
// el del caso, para que la observación no dependa del contexto que se está probando.
func countDiagnosticsContractRequests(t *testing.T, db *sql.DB, tenantID, commandID string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), diagnosticsContractTimeout)
	defer cancel()
	var n int
	err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM public.diagnostics_bundles
		WHERE tenant_id = $1::uuid AND command_id = $2`,
		tenantID, commandID).Scan(&n)
	if err != nil {
		t.Fatalf("contar las solicitudes (tenant %q, command %q): %v", tenantID, commandID, err)
	}
	return n
}

// TestDiagnosticsContract_DeleteRequest_SurvivesTheCancelledRequest es D-F3-11 contra Postgres. D5
// registra la solicitud, intenta empujarla al Edge y, si el empuje falla, la borra (rollback). El
// caso que necesita ese borrado es justo el del cliente que ya se fue, y ahí el contexto de la
// petición está cancelado. El test fija las dos mitades con una fila real:
//
//  1. con el contexto YA cancelado, DeleteRequest falla con context.Canceled y la fila SIGUE en
//     la tabla: es el defecto que tenía la cara vieja (hallazgo 71 de F3), y lo que el doble en
//     memoria no puede enseñar, porque ignora el contexto;
//  2. con ESE MISMO contexto desenganchado de la cancelación y acotado por un plazo —la forma
//     exacta de rollbackRequest en internal/apipublica/diagnostics.go:
//     dbCtx(context.WithoutCancel(ctx), dbTimeout), o sea context.WithTimeout sobre
//     context.WithoutCancel— el borrado ocurre y la fila desaparece de Postgres.
//
// No pasa por HTTP ni por rollbackRequest (un *_contrato_test.go no importa apipublica, R9.4.d):
// prueba el hecho del que rollbackRequest depende, que el adaptador obedece al contexto que recibe.
func TestDiagnosticsContract_DeleteRequest_SurvivesTheCancelledRequest(t *testing.T) {
	db := newDiagnosticsContractDB(t)
	store := diagnostics.NewPostgres(db)
	tenant := seedDiagnosticsContractTenant(t, db, "diagnostico-rollback")
	command := uuid.NewString()

	// La solicitud que D5 acaba de registrar, viva y pendiente.
	seedCtx, cancelSeed := context.WithTimeout(context.Background(), diagnosticsContractTimeout)
	defer cancelSeed()
	err := store.CreateRequest(seedCtx, tenant, diagnosticsContractSession, command, diagnosticsContractRequester, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateRequest: error inesperado %v", err)
	}
	if n := countDiagnosticsContractRequests(t, db, tenant, command); n != 1 {
		t.Fatalf("tras CreateRequest hay %d filas de la solicitud; quería 1", n)
	}

	// El contexto de la petición de un cliente que ya se fue.
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()

	// 1 · Con él, el borrado no llega a la base.
	err = store.DeleteRequest(requestCtx, tenant, command)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteRequest con el contexto cancelado: err = %v, quería context.Canceled", err)
	}
	if n := countDiagnosticsContractRequests(t, db, tenant, command); n != 1 {
		t.Fatalf("tras el DeleteRequest fallido hay %d filas de la solicitud; quería 1 (la fila sigue pendiente)", n)
	}

	// 2 · Desenganchado de la cancelación y con plazo, como rollbackRequest, sí.
	rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(requestCtx), diagnosticsContractTimeout)
	defer cancelRollback()
	if err := store.DeleteRequest(rollbackCtx, tenant, command); err != nil {
		t.Fatalf("DeleteRequest con el contexto desenganchado: error inesperado %v", err)
	}
	if n := countDiagnosticsContractRequests(t, db, tenant, command); n != 0 {
		t.Errorf("tras el rollback hay %d filas de la solicitud; quería 0 (el borrado tiene que ocurrir en Postgres)", n)
	}
}
