//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation/degradationhelpertest"
)

// Este fichero corre la suite de contrato de degradation.Store (degradationhelpertest.Contrato)
// contra degradation.Postgres sobre una base clonada de la plantilla migrada (T4.31 = T9.25). Como
// receipts_contrato_test.go, no es un proceso de caja negra: es la excepción de R9.4.d que decidió
// D-F1-8, y por eso solo importa la suite y el paquete del adaptador, del que solo usa el
// constructor (candado ProcessImports, regla 3). El aviso que cruza el montaje se nombra por el
// alias de la suite, degradationhelpertest.Notice.
//
// Es la que prueba de verdad contra public.owner_degradation_notices (migración 0075) lo que el
// doble solo imita: que el ON CONFLICT sobre (tenant_id, reason, via, window_start) colapsa sobre
// el índice único ux_owner_degradation_notices_ventana —también con escritores a la vez—, que el
// colapso solo toca occurrences y last_seen_at, y que List ordena por
// (window_start DESC, created_at DESC, id) y pagina con LIMIT/OFFSET.
//
// La tabla guarda el tenant como TEXT sin clave foránea: sembrar un tenant es dar un UUID nuevo.
// Las dos ayudas que el puerto no tiene —leer todas las filas y marcar un aviso como leído— son SQL
// sobre el mismo *sql.DB que usa el adaptador.

// degradationContractTimeout acota cada sentencia de observación y de marcado: la base es local al
// contenedor de la corrida.
const degradationContractTimeout = 15 * time.Second

// degradationContractCases cuenta los Montajes pedidos en esta corrida: Contrato llama a nuevo una
// vez por caso (en serie) y cada uno necesita una base con nombre propio.
var degradationContractCases atomic.Int64

// TestDegradationContrato_Postgres corre las promesas del puerto degradation.Store, las mismas que
// pasan contra degradationhelpertest.Memoria, contra el adaptador Postgres. Cada caso recibe su
// base, vacía de avisos, y un Postgres nuevo.
func TestDegradationContrato_Postgres(t *testing.T) {
	degradationhelpertest.Contrato(t, newDegradationContractMontaje)
}

// newDegradationContractMontaje devuelve el Montaje limpio de un caso: clona una base con nuevaBase
// (que la borra en el Cleanup del subtest), la abre con el arnés —la única conexión de este
// fichero, D-F9-6— y construye el Postgres sobre el mismo *sql.DB con el que Rows lee y MarkRead
// escribe.
func newDegradationContractMontaje(t *testing.T) degradationhelpertest.Montaje {
	t.Helper()
	n := degradationContractCases.Add(1)
	db := nuevaBase(t, fmt.Sprintf("degradation_contrato_%02d", n)).Abrir(t)

	return degradationhelpertest.Montaje{
		Store: degradation.NewPostgres(db),
		// La tabla no referencia a public.tenants: un tenant «sembrado» es un UUID nuevo, sin avisos.
		SeedTenant: uuidAleatorio,
		Rows: func(t *testing.T, tenantID string) []degradationhelpertest.Notice {
			t.Helper()
			return readDegradationContractRows(t, db, tenantID)
		},
		MarkRead: func(t *testing.T, tenantID, id string, at time.Time) {
			t.Helper()
			markDegradationContractRead(t, db, tenantID, id, at)
		},
	}
}

// readDegradationContractRows es el Montaje.Rows de Postgres: TODAS las filas del tenant en
// public.owner_degradation_notices, con sus diez columnas, sin filtro y sin página (el orden no es
// del contrato: la suite las ordena). No pasa por List, que es lo que se prueba. read_at NULL llega
// como instante cero. El motivo se escanea directo al campo del aviso, sin validar. Falla el test
// si no puede leer.
func readDegradationContractRows(t *testing.T, db *sql.DB, tenantID string) []degradationhelpertest.Notice {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), degradationContractTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, `
		SELECT id::text, tenant_id, reason, via, window_start, window_end,
		       occurrences, read_at, created_at, last_seen_at
		  FROM public.owner_degradation_notices
		 WHERE tenant_id = $1`,
		tenantID)
	if err != nil {
		t.Fatalf("Rows(tenant %q): %v", tenantID, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Errorf("Rows(tenant %q): cerrar las filas: %v", tenantID, cerr)
		}
	}()

	found := make([]degradationhelpertest.Notice, 0)
	for rows.Next() {
		var (
			n      degradationhelpertest.Notice
			readAt sql.NullTime
		)
		if err := rows.Scan(&n.ID, &n.TenantID, &n.Reason, &n.Via, &n.WindowStart, &n.WindowEnd,
			&n.Occurrences, &readAt, &n.CreatedAt, &n.LastSeenAt); err != nil {
			t.Fatalf("Rows(tenant %q): leer una fila: %v", tenantID, err)
		}
		n.ReadAt = readAt.Time // NULL ⇒ instante cero ⇒ sin leer
		found = append(found, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("Rows(tenant %q): recorrer las filas: %v", tenantID, err)
	}
	return found
}

// markDegradationContractRead es el Montaje.MarkRead de Postgres: pone read_at = at (en UTC) al
// aviso id del tenant y no toca ninguna otra columna. Falla el test si el tenant no tiene ese aviso
// (0 filas), como pide Montaje.
func markDegradationContractRead(t *testing.T, db *sql.DB, tenantID, id string, at time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), degradationContractTimeout)
	defer cancel()
	res, err := db.ExecContext(ctx, `
		UPDATE public.owner_degradation_notices
		   SET read_at = $3
		 WHERE tenant_id = $1 AND id = $2::uuid`,
		tenantID, id, at.UTC())
	if err != nil {
		t.Fatalf("MarkRead(tenant %q, aviso %q): %v", tenantID, id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("MarkRead(tenant %q, aviso %q): filas afectadas: %v", tenantID, id, err)
	}
	if n != 1 {
		t.Fatalf("MarkRead(tenant %q, aviso %q): tocó %d filas de public.owner_degradation_notices; quería 1", tenantID, id, n)
	}
}
