//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/tenantvars"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/tenantvars/tenantvarshelpertest"
)

// Este fichero corre la suite de contrato de tenantvars.Store (tenantvarshelpertest.Contrato)
// contra tenantvars.Postgres sobre una base clonada de la plantilla migrada (R6.3.c, T6.3). Como
// receipts_contrato_test.go, no es un proceso de caja negra: es la excepción de R9.4.d que decidió
// D-F1-8, y por eso solo importa la suite y el paquete del adaptador, del que solo usa el
// constructor (candado ProcessImports, regla 3).
//
// Es la que prueba de verdad contra public.tenant_variables (migración 0043) lo que el unitario
// del adaptador, con su driver de mentira, solo puede mirar como texto: que el
// `key <> ALL($2::text[])` borra justo las claves ausentes y, con el array vacío, todas; que el
// `IS DISTINCT FROM` del upsert deja quieto el updated_at de un valor que no cambia y lo mueve si
// cambia; que las dos sentencias van acotadas al tenant; y que ORDER BY key ordena las claves de
// la suite (minúsculas ASCII) igual que el doble en memoria.
//
// El puerto es su propia siembra (Replace) y su propio observador (List), y tenant_id es TEXT sin
// clave foránea: el Montaje solo pide dos tenant_id sin variables y Advance.

const (
	// tenantvarsTimeout acota cada lectura del reloj de la base: es local al contenedor.
	tenantvarsTimeout = 15 * time.Second
	// tenantvarsClockTries es el tope de lecturas del reloj en un Advance. Cada lectura es un
	// viaje de ida y vuelta a la base y el reloj tiene resolución de microsegundo: con una
	// basta; el tope solo evita un bucle sin fin si el reloj de la base se quedara parado.
	tenantvarsClockTries = 1000
)

// tenantvarsContractCases cuenta los Montajes pedidos en esta corrida: Contrato llama a nuevo una
// vez por caso (en serie) y cada uno necesita una base con nombre propio.
var tenantvarsContractCases atomic.Int64

// TestTenantvarsContrato_Postgres corre las promesas del puerto tenantvars.Store, las mismas que
// pasan contra tenantvars.MemoryStore, contra el adaptador Postgres. Cada caso recibe su base,
// vacía de variables, y un Postgres nuevo.
func TestTenantvarsContrato_Postgres(t *testing.T) {
	tenantvarshelpertest.Contrato(t, tenantvarsContractNewMontaje)
}

// tenantvarsContractNewMontaje devuelve el Montaje limpio de un caso: clona una base con
// nuevaBase (que la borra en el Cleanup del subtest), la abre con el arnés y construye el
// adaptador sobre ese *sql.DB. No hay nada que sembrar; los dos tenants llevan el número del
// caso, aunque con una base por caso ya vendrían sin variables (la suite lo comprueba con List
// antes de cada caso).
func tenantvarsContractNewMontaje(t *testing.T) tenantvarshelpertest.Montaje {
	t.Helper()
	n := tenantvarsContractCases.Add(1)
	db := nuevaBase(t, fmt.Sprintf("tenantvars_contrato_%02d", n)).Abrir(t)

	return tenantvarshelpertest.Montaje{
		Store:   tenantvars.NewPostgres(db),
		TenantA: fmt.Sprintf("tenantvars-contrato-%02d-a", n),
		TenantB: fmt.Sprintf("tenantvars-contrato-%02d-b", n),
		Advance: func(t *testing.T) { tenantvarsContractAdvance(t, db) },
	}
}

// tenantvarsContractAdvance es el Advance del Montaje contra Postgres: vuelve cuando el reloj de
// la BASE ha pasado del instante en que estaba al entrar.
//
// El adaptador marca updated_at con now(), que es el instante en que empezó la transacción del
// Replace. Todo Replace anterior a esta llamada empezó antes de la primera lectura de aquí, y
// todo Replace posterior empezará después de la última, que es estrictamente mayor: sus marcas
// no pueden coincidir. No se duerme: se le pregunta la hora a la base hasta que cambia, y con
// resolución de microsegundo y un viaje de red por lectura cambia a la primera.
func tenantvarsContractAdvance(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), tenantvarsTimeout)
	defer cancel()
	start := tenantvarsContractClock(ctx, t, db)
	for range tenantvarsClockTries {
		if tenantvarsContractClock(ctx, t, db).After(start) {
			return
		}
	}
	t.Fatalf("el reloj de la base no pasó de %v en %d lecturas", start, tenantvarsClockTries)
}

// tenantvarsContractClock lee el reloj de pared de la base (clock_timestamp, que avanza dentro
// de una misma transacción, no now()).
func tenantvarsContractClock(ctx context.Context, t *testing.T, db *sql.DB) time.Time {
	t.Helper()
	var now time.Time
	if err := db.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatalf("leer el reloj de la base: %v", err)
	}
	return now
}
