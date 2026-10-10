//go:build integracion && pendiente

package procesos

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events/eventshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Este fichero corre la suite de contrato del almacén del evento conversacional
// (eventshelpertest.Contrato) contra events.Store sobre una base clonada de la plantilla migrada
// (T8.12). Como flowstore_contrato_test.go, no es un proceso de caja negra: es la excepción de
// R9.4.d que decidió D-F1-8, y por eso solo importa la suite, el paquete del adaptador e
// internal/platform/crypto (candado ProcessImports, regla 3).
//
// Del paquete events SOLO se nombra NewStore (regla 3b del candado). El tipo que cruza el Montaje
// se nombra por el alias de la suite (eventshelpertest.Event) y el reloj se inyecta con
// eventshelpertest.ClockOption, que es events.WithClock.
//
// Es la que prueba de verdad contra public.conversation_events, public.conversation_event_messages,
// la vista public.event_content y public.tenant_settings lo que los unitarios del adaptador, con su
// driver de mentira, solo pueden mirar como forma: que el único parcial deja UN vivo por tipo y
// conversación, que el guard de la transición no pisa una muerte ya sellada, que la consulta de
// rescatables aparta lo que cuajó o murió y marca lo vencido sin filtrarlo, que el listado del
// dueño casa sus ocho argumentos con sus casts, que el historial se numera sin huecos bajo
// escritores simultáneos y que el cuerpo vuelve entero después de cifrado. Y las cuatro carreras,
// que aquí las serializa la base y no un mutex.
//
// 🔴 Lleva la etiqueta `pendiente` además de `integracion` mientras events.Store esté en rojo: un
// panic aborta el binario ENTERO de los procesos. El verde del adaptador se la quita.
//
// El reloj NO es el de la base: events.Store fecha con el reloj que se le inyecta, así que Now y
// Advance mueven un reloj de este fichero y nada duerme.
//
// Las claves de cifrado de aquí son las del ENVELOPE DE PII DE NEGOCIO (internal/platform/crypto),
// NO la DEK del ADR-0007 que custodia el cliente. Se generan en el test y mueren con él.

// eventsCases cuenta los Montajes pedidos en esta corrida: Contrato llama a nuevo una vez por caso
// (en serie) y cada uno necesita una base con nombre propio.
var eventsCases atomic.Int64

// eventsClock es el reloj inyectado en el adaptador: solo avanza cuando la suite lo pide.
type eventsClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *eventsClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *eventsClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// TestEventsContrato_Postgres corre las promesas del almacén del evento, las mismas que pasan
// contra el doble en memoria, contra el adaptador Postgres. Cada caso recibe su base, con dos
// tenants recién creados y sin eventos, y dos adaptadores nuevos sobre ella: con cipher y sin él.
func TestEventsContrato_Postgres(t *testing.T) {
	eventshelpertest.Contrato(t, eventsNewMontaje)
}

// eventsNewMontaje devuelve el Montaje limpio de un caso: clona una base con nuevaBase (que la
// borra en el Cleanup del subtest), la abre con el arnés, precalienta el pool (sin eso las
// carreras saldrían escalonadas: ver flowstoreWarmPool), siembra dos tenants en public.tenants
// (conversation_events los exige por clave foránea) y construye los dos adaptadores sobre ese
// *sql.DB con el MISMO reloj. Los dos tenants nacen SIN fila en public.tenant_settings.
func eventsNewMontaje(t *testing.T) eventshelpertest.Montaje {
	t.Helper()
	n := eventsCases.Add(1)
	db := nuevaBase(t, fmt.Sprintf("events_contrato_%02d", n)).Abrir(t)
	flowstoreWarmPool(t, db)

	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		MasterB64: clavesSecretoB64(t),
		IndexB64:  clavesSecretoB64(t),
	})
	if err != nil {
		t.Fatalf("KeyProvider del contrato: %v", err)
	}
	clock := &eventsClock{now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	option := eventshelpertest.ClockOption(clock.Now)

	return eventshelpertest.Montaje{
		Store:    events.NewStore(db, crypto.NewFieldCipher(kp), option),
		NoCipher: events.NewStore(db, nil, option),
		TenantA:  flowstoreSeedTenant(t, db, fmt.Sprintf("events-contrato-%02d-a", n)),
		TenantB:  flowstoreSeedTenant(t, db, fmt.Sprintf("events-contrato-%02d-b", n)),
		Events:   func(t *testing.T, tenantID string) []eventshelpertest.Event { return eventsRows(t, db, tenantID) },
		Entries:  func(t *testing.T, eventID string) []eventshelpertest.Entry { return eventsEntries(t, db, eventID) },
		SetContent: func(t *testing.T, eventID, state string) string {
			return eventsSetContent(t, db, eventID, state)
		},
		SetInactivityTTL: func(t *testing.T, tenantID string, seconds int) {
			eventsSetInactivityTTL(t, db, tenantID, seconds)
		},
		CorruptEntry: func(t *testing.T, eventID string, seq int) { eventsCorruptEntry(t, db, eventID, seq) },
		Now:          func(*testing.T) time.Time { return clock.Now() },
		Advance:      func(_ *testing.T, d time.Duration) { clock.Advance(d) },
	}
}

// eventsRows devuelve TODOS los eventos del tenant, en cualquier estado, con sus doce columnas.
// closed_at en NULL sale a cero.
func eventsRows(t *testing.T, db *sql.DB, tenantID string) []eventshelpertest.Event {
	t.Helper()
	return flowstoreRows(t, db, "los eventos de "+tenantID, `
		SELECT id::text, tenant_id::text, session_id, contact_id::text, kind, history_id, status,
		       flow_id, flow_version, created_at, last_activity_at, closed_at
		FROM public.conversation_events WHERE tenant_id = $1::uuid ORDER BY id`,
		func(rows *sql.Rows, ev *eventshelpertest.Event) error {
			var closedAt sql.NullTime
			if err := rows.Scan(&ev.ID, &ev.TenantID, &ev.SessionID, &ev.ContactID, &ev.Kind, &ev.HistoryID,
				&ev.Status, &ev.FlowID, &ev.FlowVersion, &ev.CreatedAt, &ev.LastActivityAt, &closedAt); err != nil {
				return err
			}
			ev.ClosedAt = closedAt.Time
			return nil
		}, tenantID)
}

// eventsEntries devuelve el historial del evento por seq, sin descifrar nada: las tres marcas, el
// payload (nil si es NULL) y si la fila lleva el sobre cifrado COMPLETO. El id se compara como
// texto para que uno desconocido sea «sin entradas».
func eventsEntries(t *testing.T, db *sql.DB, eventID string) []eventshelpertest.Entry {
	t.Helper()
	return flowstoreRows(t, db, "el historial de "+eventID, `
		SELECT seq, role, entry_kind, origin, payload,
		       (body_enc IS NOT NULL AND body_dek IS NOT NULL AND body_kek_id IS NOT NULL)
		FROM public.conversation_event_messages WHERE event_id::text = $1 ORDER BY seq`,
		func(rows *sql.Rows, e *eventshelpertest.Entry) error {
			return rows.Scan(&e.Seq, &e.Role, &e.Kind, &e.Origin, &e.Payload, &e.Sealed)
		}, eventID)
}

// eventsIntakeStatus traduce un estado de la vista event_content al estado de la solicitud que lo
// produce (0054): open → alive, abandoned → discarded y cualquier otro → settled.
func eventsIntakeStatus(t *testing.T, state string) string {
	t.Helper()
	switch state {
	case eventshelpertest.ContentAlive:
		return "open"
	case eventshelpertest.ContentDiscarded:
		return "abandoned"
	case eventshelpertest.ContentSettled:
		return "confirmed"
	default:
		t.Fatalf("estado de contenido desconocido: %q", state)
		return ""
	}
}

// eventsSetContent deja al evento con contenido en ese estado: una solicitud real que declara su
// event_id (la única rama de la vista public.event_content). Si el evento ya tenía solicitud, le
// cambia el estado y conserva su id, que es el ref.
func eventsSetContent(t *testing.T, db *sql.DB, eventID, state string) string {
	t.Helper()
	status := eventsIntakeStatus(t, state)
	ctx := t.Context()
	var ref string
	err := db.QueryRowContext(ctx,
		`UPDATE public.intakes SET status = $2 WHERE event_id = $1::uuid RETURNING id::text`, eventID, status).Scan(&ref)
	if err == nil {
		return ref
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cambiar el contenido del evento %s: %v", eventID, err)
	}
	err = db.QueryRowContext(ctx, `
		INSERT INTO public.intakes (id, tenant_id, contact_id, session_id, status, event_id)
		SELECT gen_random_uuid(), e.tenant_id::text, e.contact_id::text, e.session_id, $2, e.id
		  FROM public.conversation_events e WHERE e.id = $1::uuid
		RETURNING id::text`, eventID, status).Scan(&ref)
	if err != nil {
		t.Fatalf("sembrar el contenido del evento %s: %v", eventID, err)
	}
	return ref
}

// eventsSetInactivityTTL deja al tenant con ese event_inactivity_ttl_seconds, creando su fila de
// public.tenant_settings (tenant_id es TEXT) con el resto de columnas por defecto si no la tenía.
func eventsSetInactivityTTL(t *testing.T, db *sql.DB, tenantID string, seconds int) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO public.tenant_settings (tenant_id, event_inactivity_ttl_seconds) VALUES ($1, $2)
		ON CONFLICT (tenant_id) DO UPDATE SET event_inactivity_ttl_seconds = EXCLUDED.event_inactivity_ttl_seconds`,
		tenantID, seconds); err != nil {
		t.Fatalf("fijar el TTL de inactividad del tenant %s: %v", tenantID, err)
	}
}

// eventsCorruptEntry estropea el cuerpo cifrado de la entrada: le antepone un byte, y el sello de
// autenticidad de GCM deja de casar. La entrada tiene que existir y ser de nivel 2.
func eventsCorruptEntry(t *testing.T, db *sql.DB, eventID string, seq int) {
	t.Helper()
	res, err := db.ExecContext(t.Context(), `
		UPDATE public.conversation_event_messages SET body_enc = '\x00'::bytea || body_enc
		 WHERE event_id = $1::uuid AND seq = $2 AND body_enc IS NOT NULL`, eventID, seq)
	if err != nil {
		t.Fatalf("estropear la entrada %d del evento %s: %v", seq, eventID, err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("estropear la entrada %d del evento %s: %d filas (%v), quería 1", seq, eventID, n, err)
	}
}
