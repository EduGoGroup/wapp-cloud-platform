//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg/intentcfghelpertest"
)

// Este fichero corre la suite de contrato de intentcfg.Store (intentcfghelpertest.Contrato) contra
// intentcfg.PostgresStore sobre una base clonada de la plantilla migrada (T7.27 = T9.28). Como
// tenantvars_contrato_test.go, no es un proceso de caja negra: es la excepción de R9.4.d que
// decidió D-F1-8, y por eso solo importa la suite y el paquete del adaptador, del que solo usa el
// constructor (candado ProcessImports, regla 3).
//
// Es la que prueba de verdad contra public.intent_configs (migración 0033) lo que el unitario del
// adaptador, con su driver de mentira, solo puede mirar como texto: que el ON CONFLICT (tenant_id)
// reemplaza versión y blob en vez de duplicar la fila, que updated_at se refresca SIEMPRE —también
// reescribiendo lo mismo, al revés que tenantvars (hallazgo 8 de F7)—, que las dos sentencias van
// acotadas al tenant y que el blob vuelve equivalente como JSON aunque el JSONB lo normalice.
//
// El puerto es su propia siembra (Upsert) y su propio observador (Get), y tenant_id es TEXT sin
// clave foránea: el Montaje solo pide dos tenant_id sin config y Advance.

const (
	// intentcfgContractTimeout acota cada lectura del reloj de la base: es local al contenedor.
	intentcfgContractTimeout = 15 * time.Second
	// intentcfgContractClockTries es el tope de lecturas del reloj en un Advance (ver
	// tenantvarsClockTries: con una basta; el tope evita un bucle sin fin).
	intentcfgContractClockTries = 1000
)

// intentcfgContractCases cuenta los Montajes pedidos en esta corrida: Contrato llama a nuevo una
// vez por caso (en serie) y cada uno necesita una base con nombre propio.
var intentcfgContractCases atomic.Int64

// TestIntentcfgContrato_Postgres corre las promesas del puerto intentcfg.Store, las mismas que
// pasan contra intentcfg.MemoryStore, contra el adaptador Postgres. Cada caso recibe su base, sin
// ninguna config, y un PostgresStore nuevo.
func TestIntentcfgContrato_Postgres(t *testing.T) {
	intentcfghelpertest.Contrato(t, intentcfgContractNewMontaje)
}

// intentcfgContractNewMontaje devuelve el Montaje limpio de un caso: clona una base con nuevaBase
// (que la borra en el Cleanup del subtest), la abre con el arnés y construye el adaptador sobre
// ese *sql.DB. No hay nada que sembrar; los dos tenants llevan el número del caso, aunque con una
// base por caso ya vendrían sin config.
func intentcfgContractNewMontaje(t *testing.T) intentcfghelpertest.Montaje {
	t.Helper()
	n := intentcfgContractCases.Add(1)
	db := nuevaBase(t, fmt.Sprintf("intentcfg_contrato_%02d", n)).Abrir(t)

	return intentcfghelpertest.Montaje{
		Store:   intentcfg.NewPostgresStore(db),
		TenantA: fmt.Sprintf("intentcfg-contrato-%02d-a", n),
		TenantB: fmt.Sprintf("intentcfg-contrato-%02d-b", n),
		Advance: func(t *testing.T) { intentcfgContractAdvance(t, db) },
	}
}

// intentcfgContractAdvance es el Advance del Montaje contra Postgres: vuelve cuando el reloj de la
// BASE ha pasado del instante en que estaba al entrar.
//
// El adaptador marca updated_at con now(), que es el instante en que empezó la sentencia del
// Upsert. Todo Upsert anterior a esta llamada empezó antes de la primera lectura de aquí, y todo
// Upsert posterior empezará después de la última, que es estrictamente mayor: sus marcas no pueden
// coincidir. No se duerme: se le pregunta la hora a la base hasta que cambia, y con resolución de
// microsegundo y un viaje de red por lectura cambia a la primera.
func intentcfgContractAdvance(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), intentcfgContractTimeout)
	defer cancel()
	start := intentcfgContractClock(ctx, t, db)
	for range intentcfgContractClockTries {
		if intentcfgContractClock(ctx, t, db).After(start) {
			return
		}
	}
	t.Fatalf("el reloj de la base no pasó de %v en %d lecturas", start, intentcfgContractClockTries)
}

// intentcfgContractClock lee el reloj de pared de la base (clock_timestamp, que avanza dentro de
// una misma transacción, no now()).
func intentcfgContractClock(ctx context.Context, t *testing.T, db *sql.DB) time.Time {
	t.Helper()
	var now time.Time
	if err := db.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatalf("leer el reloj de la base: %v", err)
	}
	return now
}
