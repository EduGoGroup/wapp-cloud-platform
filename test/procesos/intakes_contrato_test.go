//go:build integracion && pendiente

package procesos

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/intakeshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Este fichero corre la suite de contrato de la persistencia de solicitudes
// (intakeshelpertest.Contrato) contra intakes.Postgres sobre una base clonada de la plantilla
// migrada (T6.8). Como tenantvars_contrato_test.go, no es un proceso de caja negra: es la
// excepción de R9.4.d que decidió D-F1-8, y por eso solo importa la suite y el paquete del
// adaptador (candado ProcessImports, regla 3).
//
// Del paquete intakes SOLO se nombra NewPostgres (regla 3b del candado). La opción del cifrador
// del literal no empieza por «New», así que llega por la suite: intakeshelpertest.WithLiteralCipher
// la reexporta (hallazgo 15 de F6, decisión de Jhoan del 2026-10-08) y el Montaje la cablea con un
// crypto.FieldCipher de keyring propio, como tenantllm_contrato_test.go. Sin ella el adaptador se
// niega, con razón, a escribir el literal en claro y el caso
// InsertRevision_LiteralLeavesThePayloadAndReturnsOnRead no podría pasar.
//
// Queda un rodeo: el tipo de las zonas de envío del Montaje no se nombra, y
// intakesContractBindShippingZones lo deduce del propio campo. Si la suite publica un alias (el
// precedente es fleethelpertest.TenantProfiles, hallazgo 75 de F3), se nombra por él y sobra.
//
// Es la que prueba de verdad contra public.intakes, public.intake_items, public.intake_revisions,
// public.tenant_settings y public.conversation_events lo que los unitarios del adaptador, con su
// driver de mentira, solo pueden mirar como forma: que el predicado compartido de List y
// ListDetails filtra lo que dice, que el compare-and-swap del estado y los de los recordatorios
// casan justo las filas que prometen, que el total se recalcula con el envío dentro, que el
// numerador de revisiones cuenta por solicitud y que el literal sale del payload y vuelve.
//
// 🔴 LLEVA LA ETIQUETA `pendiente` ADEMÁS DE `integracion` mientras el adaptador esté en rojo: sus
// métodos son `panic(pendiente.Implementar…)` y un panic aborta el binario ENTERO de los procesos.
// Quien ponga el adaptador en verde (F6-03) o corra la suite (F6-06) quita `&& pendiente` de la
// primera línea. Hasta entonces este fichero se compila
// (`go vet -tags 'integracion pendiente' ./test/procesos/...`) y no se corre.
//
// El puerto no tiene alta de solicitudes —las pare el proyector del carrito o el pipeline—, así
// que la siembra es SQL de este fichero: el evento padre (0051), la cabecera (0041 y siguientes)
// y sus líneas (0012).

const (
	// intakesContractTimeout acota cada sentencia de la siembra y de la observación: la base es
	// local al contenedor.
	intakesContractTimeout = 15 * time.Second
	// intakesContractKeyID es el key_id de la única KEK del keyring de un Montaje de la suite.
	intakesContractKeyID = "intakes-contrato"
	// intakesContractClockTries es el tope de lecturas del reloj en un Advance (ver
	// tenantvarsClockTries: con una basta; el tope evita un bucle sin fin).
	intakesContractClockTries = 1000
)

// intakesContractCases cuenta los Montajes pedidos en esta corrida: Contrato llama a nuevo una vez
// por caso (en serie) y cada uno necesita una base con nombre propio.
var intakesContractCases atomic.Int64

// TestIntakesContrato_Postgres corre las promesas de la persistencia de solicitudes, las mismas
// que pasan contra intakes.MemoryStore, contra el adaptador Postgres. Cada caso recibe su base,
// con dos tenants recién creados y sin solicitudes, y un Postgres nuevo.
func TestIntakesContrato_Postgres(t *testing.T) {
	intakeshelpertest.Contrato(t, intakesContractNewMontaje)
}

// intakesContractNewMontaje devuelve el Montaje limpio de un caso: clona una base con nuevaBase
// (que la borra en el Cleanup del subtest), la abre con el arnés, siembra dos tenants en
// public.tenants (conversation_events los exige por clave foránea) y construye el adaptador sobre
// ese *sql.DB, con el cifrador del literal de un keyring propio (ver la cabecera del fichero). La
// clave del índice ciego va EXPLÍCITA aunque este store no la use: sin IndexB64 el proveedor la
// derivaría de la KEK current con un aviso (hallazgo 30 de F3).
//
// Los dos tenants nacen SIN fila en public.tenant_settings: es el «sin zonas de envío y sin
// configuración de la seña» que pide el Montaje.
func intakesContractNewMontaje(t *testing.T) intakeshelpertest.Montaje {
	t.Helper()
	n := intakesContractCases.Add(1)
	db := nuevaBase(t, fmt.Sprintf("intakes_contrato_%02d", n)).Abrir(t)

	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		KeyringB64: intakesContractKeyID + ":" + clavesSecretoB64(t),
		CurrentID:  intakesContractKeyID,
		IndexB64:   clavesSecretoB64(t),
	})
	if err != nil {
		t.Fatalf("KeyProvider del contrato (current %q): %v", intakesContractKeyID, err)
	}

	m := intakeshelpertest.Montaje{
		Store:   intakes.NewPostgres(db, intakeshelpertest.WithLiteralCipher(crypto.NewFieldCipher(kp))),
		TenantA: intakesContractSeedTenant(t, db, fmt.Sprintf("intakes-contrato-%02d-a", n)),
		TenantB: intakesContractSeedTenant(t, db, fmt.Sprintf("intakes-contrato-%02d-b", n)),
		Seed: func(t *testing.T, tenantID string, s intakeshelpertest.Seed) string {
			return intakesContractSeed(t, db, tenantID, s)
		},
		SetDepositTemplate: func(t *testing.T, tenantID, template string, dueDays int) {
			intakesContractSetDepositTemplate(t, db, tenantID, template, dueDays)
		},
		StoredStatus: func(t *testing.T, tenantID, intakeID string) string {
			return intakesContractStoredStatus(t, db, tenantID, intakeID)
		},
		EventStatus: func(t *testing.T, eventID string) string {
			return intakesContractEventStatus(t, db, eventID)
		},
		Now:     func(t *testing.T) time.Time { return intakesContractNow(t, db) },
		Advance: func(t *testing.T) { intakesContractAdvance(t, db) },
	}
	intakesContractBindShippingZones(db, &m.SetShippingZones)
	return m
}

// intakesContractSeedTenant inserta un tenant con el slug dado en public.tenants y devuelve su id
// (un UUID). Solo rellena las columnas NOT NULL sin valor por defecto (0001_tenants.sql).
func intakesContractSeedTenant(t *testing.T, db *sql.DB, slug string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), intakesContractTimeout)
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

// intakesContractNullTime convierte un instante en lo que la columna anulable espera: el cero es
// NULL.
func intakesContractNullTime(at time.Time) sql.NullTime {
	return sql.NullTime{Time: at, Valid: !at.IsZero()}
}

// intakesContractSeed siembra la solicitud con su evento padre y sus líneas, en UNA transacción, y
// devuelve el id del evento.
//
//   - El evento nace con un contact_id PROPIO (gen_random_uuid()): la columna del evento es un UUID
//     y la de la solicitud es texto opaco, y así dos solicitudes de la misma sesión con el evento
//     `open` no chocan contra el único parcial «un evento vivo por tipo» de la 0051.
//   - La cabecera se guarda TAL CUAL la pide la suite, estado almacenado incluido (puede ser el
//     legado `closed`); las tres fechas de los recordatorios en cero viajan como NULL.
//   - Cada línea lleva su added_at: es su orden.
//
// Todo parámetro va con su tipo EXPLÍCITO: en un INSERT … SELECT Postgres no deduce el tipo de un
// parámetro por la columna de destino, y el tenant viaja dos veces porque es uuid en el evento y
// texto en la solicitud.
func intakesContractSeed(t *testing.T, db *sql.DB, tenantID string, s intakeshelpertest.Seed) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), intakesContractTimeout)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("abrir la transacción de la siembra: %v", err)
	}
	// Si la siembra falla a medias, el t.Fatalf de abajo corta el test: el rollback deja la base
	// como estaba. Tras el commit devuelve sql.ErrTxDone, que no es un fallo.
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			t.Errorf("revertir la siembra de la solicitud %s: %v", s.Intake.ID, rerr)
		}
	}()

	in := s.Intake
	var eventID string
	err = tx.QueryRowContext(ctx, `
		WITH evento AS (
			INSERT INTO public.conversation_events
				(tenant_id, session_id, contact_id, kind, history_id, status, flow_id, flow_version, closed_at)
			VALUES ($1::uuid, $5::text, gen_random_uuid(), 'cart', 'cart-2026-08-01-0000', $14::text, 'flujo-contrato', 1,
			        CASE WHEN $14::text = 'open' THEN NULL ELSE now() END)
			RETURNING id
		)
		INSERT INTO public.intakes
			(id, tenant_id, contact_id, session_id, status, total, created_at, updated_at, customer_note,
			 deposit_due_at, deposit_reminded_at, expiry_reminded_at, event_id)
		SELECT $3::uuid, $2::text, $4::text, $5::text, $6::text, $7::numeric, $8::timestamptz, $9::timestamptz,
		       $10::text, $11::timestamptz, $12::timestamptz, $13::timestamptz, evento.id
		FROM evento
		RETURNING event_id::text`,
		tenantID, tenantID, in.ID, in.ContactID, in.SessionID, in.Status, in.Total, in.CreatedAt, in.UpdatedAt, in.CustomerNote,
		intakesContractNullTime(in.DepositDueAt), intakesContractNullTime(in.DepositRemindedAt),
		intakesContractNullTime(in.ExpiryRemindedAt), s.EventStatus,
	).Scan(&eventID)
	if err != nil {
		t.Fatalf("sembrar la solicitud %s con su evento %q: %v", in.ID, s.EventStatus, err)
	}

	for _, it := range s.Items {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO public.intake_items (intake_id, sku, label, customization, qty, unit_price, added_at)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)`,
			in.ID, it.SKU, it.Label, it.Customization, it.Qty, it.UnitPrice, it.AddedAt,
		); err != nil {
			t.Fatalf("sembrar la línea %q de la solicitud %s: %v", it.SKU, in.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("confirmar la siembra de la solicitud %s: %v", in.ID, err)
	}
	return eventID
}

// intakesContractBindShippingZones rellena el campo SetShippingZones del Montaje: deja al tenant
// con EXACTAMENTE esas zonas en tenant_settings.shipping_zones (ninguna = `[]`), creando su fila de
// config si no la tenía. Las demás columnas de la fila conservan su valor, o nacen con el de por
// defecto de la tabla.
//
// Es genérica para NO nombrar el tipo de las zonas, que es del paquete del puerto (candado
// ProcessImports, regla 3b): Z se deduce del tipo del campo. Aquí no hace falta saber qué es una
// zona, solo serializarla como la serializa el adaptador al leerla: con sus etiquetas json.
func intakesContractBindShippingZones[Z any](db *sql.DB, field *func(t *testing.T, tenantID string, zones ...Z)) {
	*field = func(t *testing.T, tenantID string, zones ...Z) {
		t.Helper()
		if zones == nil {
			zones = []Z{}
		}
		raw, err := json.Marshal(zones)
		if err != nil {
			t.Fatalf("serializar las zonas de envío: %v", err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), intakesContractTimeout)
		defer cancel()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO public.tenant_settings (tenant_id, shipping_zones) VALUES ($1, $2::jsonb)
			ON CONFLICT (tenant_id) DO UPDATE SET shipping_zones = EXCLUDED.shipping_zones`,
			tenantID, string(raw)); err != nil {
			t.Fatalf("fijar las zonas de envío del tenant %s: %v", tenantID, err)
		}
	}
}

// intakesContractSetDepositTemplate deja al tenant con esa plantilla de seña y ese plazo en días,
// creando su fila de config si no la tenía.
func intakesContractSetDepositTemplate(t *testing.T, db *sql.DB, tenantID, template string, dueDays int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), intakesContractTimeout)
	defer cancel()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_settings (tenant_id, deposit_template, deposit_due_days) VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id) DO UPDATE
		   SET deposit_template = EXCLUDED.deposit_template, deposit_due_days = EXCLUDED.deposit_due_days`,
		tenantID, template, dueDays); err != nil {
		t.Fatalf("fijar la plantilla de seña del tenant %s: %v", tenantID, err)
	}
}

// intakesContractStoredStatus devuelve la clave de estado tal como está GUARDADA, sin normalizar.
// Falla el test si la solicitud no existe en ese tenant: la suite solo pregunta por las que sembró.
func intakesContractStoredStatus(t *testing.T, db *sql.DB, tenantID, intakeID string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), intakesContractTimeout)
	defer cancel()
	var status string
	if err := db.QueryRowContext(ctx,
		`SELECT status FROM public.intakes WHERE tenant_id = $1 AND id::text = $2`,
		tenantID, intakeID).Scan(&status); err != nil {
		t.Fatalf("leer el estado guardado de la solicitud %s: %v", intakeID, err)
	}
	return status
}

// intakesContractEventStatus devuelve el estado del evento, o "" si no existe. Compara el id como
// texto para que un id que no sea UUID sea «no existe» y no un error de sintaxis.
func intakesContractEventStatus(t *testing.T, db *sql.DB, eventID string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), intakesContractTimeout)
	defer cancel()
	var status string
	err := db.QueryRowContext(ctx,
		`SELECT status FROM public.conversation_events WHERE id::text = $1`, eventID).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ""
	case err != nil:
		t.Fatalf("leer el estado del evento %s: %v", eventID, err)
	}
	return status
}

// intakesContractNow es el Now del Montaje: el reloj con el que el adaptador fecha lo que escribe,
// que es el now() de la base.
func intakesContractNow(t *testing.T, db *sql.DB) time.Time {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), intakesContractTimeout)
	defer cancel()
	return intakesContractClock(ctx, t, db)
}

// intakesContractAdvance es el Advance del Montaje contra Postgres: vuelve cuando el reloj de la
// BASE ha pasado del instante en que estaba al entrar.
//
// El adaptador fecha con now(), que es el instante en que empezó la transacción (o la sentencia
// suelta). Toda escritura anterior a esta llamada empezó antes de la primera lectura de aquí, y
// toda escritura posterior empezará después de la última, que es estrictamente mayor: sus marcas
// no pueden coincidir. No se duerme: se le pregunta la hora a la base hasta que cambia, y con
// resolución de microsegundo y un viaje de red por lectura cambia a la primera.
func intakesContractAdvance(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), intakesContractTimeout)
	defer cancel()
	start := intakesContractClock(ctx, t, db)
	for range intakesContractClockTries {
		if intakesContractClock(ctx, t, db).After(start) {
			return
		}
	}
	t.Fatalf("el reloj de la base no pasó de %v en %d lecturas", start, intakesContractClockTries)
}

// intakesContractClock lee el reloj de pared de la base (clock_timestamp, que avanza dentro de una
// misma transacción, no now()).
func intakesContractClock(ctx context.Context, t *testing.T, db *sql.DB) time.Time {
	t.Helper()
	var now time.Time
	if err := db.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatalf("leer el reloj de la base: %v", err)
	}
	return now
}
