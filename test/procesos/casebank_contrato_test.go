//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/casebank"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/casebank/casebankhelpertest"
)

// Este fichero corre la suite de contrato de casebank.Store (casebankhelpertest.Contrato) contra
// casebank.Postgres sobre una base clonada de la plantilla migrada (T7.27 = T9.28). Como
// tenantvars_contrato_test.go, no es un proceso de caja negra: es la excepción de R9.4.d que
// decidió D-F1-8, y por eso solo importa la suite y el paquete del adaptador, del que solo usa el
// constructor (candado ProcessImports, regla 3).
//
// Es la que prueba de verdad contra public.intake_case_bank (migración 0082) lo que el doble solo
// imita: que el CHECK intake_case_bank_consented_check rechaza el caso sin consentimiento con su
// nombre en el error y sin escribir nada (hallazgo 9 de F7), que la columna JSONB rechaza un
// expected que no es JSON, que un expected vacío queda en NULL y que Exists casa por tenant Y por
// texto exacto.
//
// El puerto no tiene lectura —ningún camino de producción lee esta tabla—: el observador es SQL
// sobre el mismo *sql.DB que usa el adaptador. tenant_id es TEXT sin clave foránea: no hay fila
// de tenants que sembrar.

// casebankContractTimeout acota cada sentencia de observación: la base es local al contenedor de
// la corrida.
const casebankContractTimeout = 15 * time.Second

// casebankContractCases cuenta los Montajes pedidos en esta corrida: Contrato llama a montar una
// vez por caso (en serie) y cada uno necesita una base con nombre propio.
var casebankContractCases atomic.Int64

// TestCasebankContrato_Postgres corre las promesas del puerto casebank.Store, las mismas que pasan
// contra casebankhelpertest.Memory, contra el adaptador Postgres. Cada caso recibe su base, con el
// banco vacío, y un Postgres nuevo.
func TestCasebankContrato_Postgres(t *testing.T) {
	casebankhelpertest.Contrato(t, casebankContractNewMontaje)
}

// casebankContractNewMontaje devuelve el Montaje limpio de un caso: clona una base con nuevaBase
// (que la borra en el Cleanup del subtest), la abre con el arnés —la única conexión de este
// fichero, D-F9-6— y construye el Postgres sobre el mismo *sql.DB con el que Rows lee. Los dos
// tenants llevan el número del caso, aunque con una base por caso ya vendrían sin filas.
func casebankContractNewMontaje(t *testing.T) casebankhelpertest.Montaje {
	t.Helper()
	n := casebankContractCases.Add(1)
	db := nuevaBase(t, fmt.Sprintf("casebank_contrato_%02d", n)).Abrir(t)

	return casebankhelpertest.Montaje{
		Store:   casebank.NewPostgres(db),
		TenantA: fmt.Sprintf("casebank-contrato-%02d-a", n),
		TenantB: fmt.Sprintf("casebank-contrato-%02d-b", n),
		Rows: func(t *testing.T, tenantID string) []casebankhelpertest.Row {
			t.Helper()
			return casebankContractRows(t, db, tenantID)
		},
	}
}

// casebankContractRows es el Montaje.Rows de Postgres: las filas del tenant en
// public.intake_case_bank, en orden de id, con las cuatro columnas que Insert escribe más el id
// (created_at no lo consulta nadie). expected NULL llega como nil; si no, el texto del JSONB tal
// como lo devuelve la base (normalizado: la suite compara el documento, no sus bytes). No pasa por
// el adaptador, que es lo que se prueba. Falla el test si no puede leer.
func casebankContractRows(t *testing.T, db *sql.DB, tenantID string) []casebankhelpertest.Row {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), casebankContractTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, `
		SELECT id, tenant_id, consented, source_text, expected::text
		  FROM public.intake_case_bank
		 WHERE tenant_id = $1
		 ORDER BY id`,
		tenantID)
	if err != nil {
		t.Fatalf("Rows(tenant %q): %v", tenantID, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Errorf("Rows(tenant %q): cerrar las filas: %v", tenantID, cerr)
		}
	}()

	found := make([]casebankhelpertest.Row, 0)
	for rows.Next() {
		var (
			r        casebankhelpertest.Row
			expected sql.NullString
		)
		if err := rows.Scan(&r.ID, &r.TenantID, &r.Consented, &r.SourceText, &expected); err != nil {
			t.Fatalf("Rows(tenant %q): leer una fila: %v", tenantID, err)
		}
		if expected.Valid { // NULL ⇒ nil ⇒ aún sin curar
			r.Expected = json.RawMessage(expected.String)
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("Rows(tenant %q): recorrer las filas: %v", tenantID, err)
	}
	return found
}
