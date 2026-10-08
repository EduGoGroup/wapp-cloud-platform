//go:build integracion && pendiente

package procesos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations/integrationshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Este fichero corre la suite de contrato de integrations.Store (integrationshelpertest.Contrato)
// contra integrations.Postgres sobre una base clonada de la plantilla migrada (R6.3.b, T6.12). Como
// tenantvars_contrato_test.go, no es un proceso de caja negra: es la excepción de R9.4.d que
// decidió D-F1-8, y por eso solo importa la suite, el paquete del adaptador (su constructor) y
// crypto (sus argumentos).
//
// 🔴 DEL PAQUETE integrations SOLO SE NOMBRA NewPostgres (candado ProcessImports, regla 3b): el
// puerto no se nombra, porque el campo Montaje.Store ya lo es, y las filas se devuelven con los
// tipos de la suite, integrationshelpertest.OutboxRow e IntegrationRow.
//
// Es la que prueba de verdad contra public.webhook_outbox (migraciones 0046, 0049 y 0050) y
// public.tenant_integrations (0047) lo que el unitario del adaptador, con su driver de mentira,
// solo puede mirar como texto: que el reclamo elige por next_attempt_at, respeta el límite y no
// reparte dos veces la misma fila (FOR UPDATE SKIP LOCKED); que la valla `claimed_at = $2 AND
// status = 'delivering'` casa justo el claim vigente; que el rescate compara el sello con el lease
// y trata el NULL como huérfana; que entregar vacía el payload en la misma sentencia; y que el
// upsert sin secreto conserva el sobre y el otro lo sustituye, cifrado y descifrado con el sobre
// real.
//
// 🔴 HOMÓNIMO: la «DEK» y la KEK de este fichero son las del envelope de dato de negocio
// (internal/platform/crypto), NO la DEK del ADR-0007 que custodia el cliente. Se generan en el test
// y mueren con él; el secreto que cifran es una cadena inventada de la suite.
//
// Lleva la etiqueta `pendiente` además de `integracion` mientras el adaptador esté en rojo: su
// panic abortaría el binario ENTERO de los procesos. El verde de postgres.go se la quita. Escrito y
// compilado, NO corrido: lo corre F6-06 (T6.27).

const (
	// integrationsContractTimeout acota cada sentencia de la siembra y de la observación: la base
	// es local al contenedor.
	integrationsContractTimeout = 15 * time.Second
	// integrationsContractKeyID es el key_id de la única KEK del keyring de un Montaje de la suite.
	integrationsContractKeyID = "integrations-contrato"
	// integrationsContractClockTries es el tope de lecturas del reloj en un Advance (ver
	// tenantvarsClockTries: con una basta; el tope evita un bucle sin fin).
	integrationsContractClockTries = 1000
)

// integrationsContractCases cuenta los Montajes pedidos en esta corrida: Contrato llama a nuevo
// una vez por caso (en serie) y cada uno necesita una base con nombre propio.
var integrationsContractCases atomic.Int64

// TestIntegrationsContrato_Postgres corre las promesas del puerto integrations.Store, las mismas
// que pasan contra integrationshelpertest.Memoria, contra el adaptador Postgres. Cada caso recibe
// su base —con la cola vacía y sin integraciones— y un store nuevo con su propia KEK.
func TestIntegrationsContrato_Postgres(t *testing.T) {
	integrationshelpertest.Contrato(t, integrationsContractNewMontaje)
}

// integrationsContractNewMontaje devuelve el Montaje limpio de un caso: clona una base con
// nuevaBase (que la borra en el Cleanup del subtest), la abre con el arnés —la única conexión que
// usa el caso (D-F9-6)— y construye sobre ese *sql.DB el store con un cifrador de campo de keyring
// propio. La clave del índice ciego va EXPLÍCITA aunque este store no la use: sin IndexB64 el
// proveedor la derivaría de la KEK current con un aviso (hallazgo 30 de F3).
//
// No hay nada que sembrar: tenant_id es TEXT sin clave foránea en las dos tablas, y con una base
// por caso la cola viene vacía (la suite lo comprueba antes de cada caso).
func integrationsContractNewMontaje(t *testing.T) integrationshelpertest.Montaje {
	t.Helper()
	n := integrationsContractCases.Add(1)
	db := nuevaBase(t, fmt.Sprintf("integrations_contrato_%03d", n)).Abrir(t)

	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		KeyringB64: integrationsContractKeyID + ":" + clavesSecretoB64(t),
		CurrentID:  integrationsContractKeyID,
		IndexB64:   clavesSecretoB64(t),
	})
	if err != nil {
		t.Fatalf("KeyProvider del contrato (current %q): %v", integrationsContractKeyID, err)
	}

	return integrationshelpertest.Montaje{
		Store:   integrations.NewPostgres(db, crypto.NewFieldCipher(kp)),
		TenantA: fmt.Sprintf("integrations-contrato-%03d-a", n),
		TenantB: fmt.Sprintf("integrations-contrato-%03d-b", n),
		Now:     func(t *testing.T) time.Time { return integrationsContractNow(t, db) },
		Advance: func(t *testing.T) { integrationsContractAdvance(t, db) },
		OutboxRow: func(t *testing.T, id int64) (integrationshelpertest.OutboxRow, bool) {
			return integrationsContractOutboxRow(t, db, id)
		},
		IntegrationRow: func(t *testing.T, tenantID string) (integrationshelpertest.IntegrationRow, bool) {
			return integrationsContractIntegrationRow(t, db, tenantID)
		},
		SetClaimedAt: func(t *testing.T, id int64, at time.Time) {
			integrationsContractSetClaimedAt(t, db, id, at)
		},
	}
}

// integrationsContractOutboxRow es el observador de la cola: lee POR SQL las diez columnas de la
// fila. last_error NULL llega como "" y claimed_at NULL como el instante cero, que es como el
// puerto las da. found es false si no existe; falla el test si la lectura falla por otra causa.
func integrationsContractOutboxRow(t *testing.T, db *sql.DB, id int64) (row integrationshelpertest.OutboxRow, found bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), integrationsContractTimeout)
	defer cancel()
	var (
		payload   []byte
		claimedAt sql.NullTime
	)
	err := db.QueryRowContext(ctx, `
		SELECT id, tenant_id, kind, payload::text, status, attempts,
		       next_attempt_at, created_at, COALESCE(last_error, ''), claimed_at
		FROM public.webhook_outbox
		WHERE id = $1`,
		id,
	).Scan(&row.ID, &row.TenantID, &row.Kind, &payload, &row.Status, &row.Attempts,
		&row.NextAttemptAt, &row.CreatedAt, &row.LastError, &claimedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return integrationshelpertest.OutboxRow{}, false
	}
	if err != nil {
		t.Fatalf("leer la fila %d de public.webhook_outbox: %v", id, err)
	}
	row.Payload = payload
	row.ClaimedAt = claimedAt.Time // NULL ⇒ cero
	return row, true
}

// integrationsContractIntegrationRow es el observador de la configuración: lee POR SQL la fila del
// tenant SIN traer el secreto. Del sobre saca solo cuáles de sus tres columnas están rellenas y un
// resumen SHA-256 de las tres juntas, calculado en la base: ni el blob cifrado ni su DEK envuelta
// salen de ella. Si el sobre está a medias, falla el test: ese estado no lo puede escribir el
// puerto.
func integrationsContractIntegrationRow(t *testing.T, db *sql.DB, tenantID string) (row integrationshelpertest.IntegrationRow, found bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), integrationsContractTimeout)
	defer cancel()
	var hasEnc, hasDEK, hasKEKID bool
	err := db.QueryRowContext(ctx, `
		SELECT tenant_id, catalog_adapter, events_adapter, COALESCE(endpoint_url, ''), enabled,
		       created_at, updated_at,
		       secret_enc    IS NOT NULL,
		       secret_dek    IS NOT NULL,
		       secret_kek_id IS NOT NULL,
		       encode(sha256(COALESCE(secret_enc, ''::bytea) || '|'::bytea ||
		                     COALESCE(secret_dek, ''::bytea) || '|'::bytea ||
		                     convert_to(COALESCE(secret_kek_id, ''), 'UTF8')), 'hex')
		FROM public.tenant_integrations
		WHERE tenant_id = $1`,
		tenantID,
	).Scan(&row.TenantID, &row.CatalogAdapter, &row.EventsAdapter, &row.EndpointURL, &row.Enabled,
		&row.CreatedAt, &row.UpdatedAt, &hasEnc, &hasDEK, &hasKEKID, &row.SecretSeal)
	if errors.Is(err, sql.ErrNoRows) {
		return integrationshelpertest.IntegrationRow{}, false
	}
	if err != nil {
		t.Fatalf("leer la fila de public.tenant_integrations del tenant %s: %v", tenantID, err)
	}
	if hasEnc != hasDEK || hasDEK != hasKEKID {
		t.Fatalf("el sobre del secreto del tenant %s está a medias (enc=%v, dek=%v, kek_id=%v): las tres columnas van juntas",
			tenantID, hasEnc, hasDEK, hasKEKID)
	}
	row.HasSecret = hasEnc
	if !row.HasSecret {
		row.SecretSeal = "" // sin secreto no hay marca: el resumen de «nada» no es una marca
	}
	return row, true
}

// integrationsContractSetClaimedAt es la siembra cruda del Montaje: deja la fila con ese
// claimed_at (el instante cero es NULL) sin tocar ninguna otra columna. Falla el test si la fila
// no existe.
func integrationsContractSetClaimedAt(t *testing.T, db *sql.DB, id int64, at time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), integrationsContractTimeout)
	defer cancel()
	res, err := db.ExecContext(ctx,
		`UPDATE public.webhook_outbox SET claimed_at = $2 WHERE id = $1`,
		id, sql.NullTime{Time: at, Valid: !at.IsZero()},
	)
	if err != nil {
		t.Fatalf("fijar claimed_at de la entrega %d: %v", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("fijar claimed_at de la entrega %d: contar filas: %v", id, err)
	}
	if n != 1 {
		t.Fatalf("fijar claimed_at de la entrega %d tocó %d filas, quería 1", id, n)
	}
}

// integrationsContractNow es el Now del Montaje: el reloj con el que el adaptador decide y fecha,
// que es el de la base.
func integrationsContractNow(t *testing.T, db *sql.DB) time.Time {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), integrationsContractTimeout)
	defer cancel()
	return integrationsContractClock(ctx, t, db)
}

// integrationsContractAdvance es el Advance del Montaje contra Postgres: vuelve cuando el reloj de
// la BASE ha pasado del instante en que estaba al entrar.
//
// El adaptador sella y fecha con now(), que es el instante en que empezó la sentencia. Toda
// escritura anterior a esta llamada empezó antes de la primera lectura de aquí, y toda escritura
// posterior empezará después de la última, que es estrictamente mayor: sus marcas no pueden
// coincidir. No se duerme: se le pregunta la hora a la base hasta que cambia, y con resolución de
// microsegundo y un viaje de red por lectura cambia a la primera.
func integrationsContractAdvance(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), integrationsContractTimeout)
	defer cancel()
	start := integrationsContractClock(ctx, t, db)
	for range integrationsContractClockTries {
		if integrationsContractClock(ctx, t, db).After(start) {
			return
		}
	}
	t.Fatalf("el reloj de la base no pasó de %v en %d lecturas", start, integrationsContractClockTries)
}

// integrationsContractClock lee el reloj de pared de la base (clock_timestamp, que avanza dentro
// de una misma transacción, no now()).
func integrationsContractClock(ctx context.Context, t *testing.T, db *sql.DB) time.Time {
	t.Helper()
	var now time.Time
	if err := db.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatalf("leer el reloj de la base: %v", err)
	}
	return now
}
