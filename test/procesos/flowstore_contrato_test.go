//go:build integracion

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

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store/storehelpertest"
)

// Este fichero corre la suite de contrato de la persistencia del motor de flujos
// (storehelpertest.Contrato) contra store.PostgresRepository sobre una base clonada de la
// plantilla migrada (T8.6). Como intakes_contrato_test.go, no es un proceso de caja negra: es la
// excepción de R9.4.d que decidió D-F1-8, y por eso solo importa la suite y el paquete del
// adaptador (candado ProcessImports, regla 3).
//
// Del paquete store SOLO se nombra NewPostgresRepository (regla 3b del candado). Los tipos del
// puerto que cruzan el Montaje —lo que devuelve cada observador— se nombran por los alias de la
// suite (storehelpertest.FlowEvent, .Intake, .Settings…), como fleethelpertest.TenantProfiles.
//
// Es la que prueba de verdad contra public.flow_state, public.flow_definitions, public.flow_events,
// survey_results, public.tenant_content, public.tenant_content_versions, public.intakes,
// public.intake_items, public.tenant_settings y public.conversation_welcomes lo que los unitarios
// del adaptador, con su driver de mentira, solo pueden mirar como forma: que el upsert del estado
// escribe TODAS sus columnas, que el versionado numera por (tenant, ref) y archiva el blob viejo,
// que el cierre encuentra la solicitud abierta de ESE contacto y no duplica sus líneas, que
// TouchContact devuelve la fila previa y que el compare-and-set de la bienvenida casa justo la
// marca vigente. Y las tres carreras, que aquí las serializa la base y no un mutex.
//
// Llevó la etiqueta `pendiente` además de `integracion` mientras el adaptador estuvo en rojo (un
// panic aborta el binario ENTERO de los procesos). F8-01 lo puso en verde y se la quitó.
//
// El puerto no deja ver todo lo que escribe (no hay lectura de flow_events ni de las versiones
// archivadas, y las lecturas de una solicitud no traen la nota), así que los observadores son SQL
// de este fichero. Y dos siembras: el evento conversacional que una solicitud, una respuesta y un
// puntero de dueño tienen que declarar (clave foránea a public.conversation_events) y la fila de
// configuración del tenant.

const (
	// flowstoreTimeout acota cada sentencia de la siembra y de la observación: la base es local al
	// contenedor.
	flowstoreTimeout = 15 * time.Second
	// flowstoreClockTries es el tope de lecturas del reloj en un Advance (con una basta; el tope
	// evita un bucle sin fin).
	flowstoreClockTries = 1000
	// flowstorePoolSize son las conexiones que el Montaje deja abiertas de antemano: más que las
	// llamadas simultáneas de una carrera de la suite (ver flowstoreWarmPool).
	flowstorePoolSize = 16
)

// flowstoreCases cuenta los Montajes pedidos en esta corrida: Contrato llama a nuevo una vez por
// caso (en serie) y cada uno necesita una base con nombre propio.
var flowstoreCases atomic.Int64

// TestFlowStoreContrato_Postgres corre las promesas de la persistencia del motor de flujos, las
// mismas que pasan contra store.MemoryRepository, contra el adaptador Postgres. Cada caso recibe
// su base, con dos tenants recién creados y vacíos, y un repositorio nuevo.
func TestFlowStoreContrato_Postgres(t *testing.T) {
	storehelpertest.Contrato(t, flowstoreNewMontaje)
}

// flowstoreNewMontaje devuelve el Montaje limpio de un caso: clona una base con nuevaBase (que la
// borra en el Cleanup del subtest), la abre con el arnés, siembra dos tenants en public.tenants
// (flow_state, flow_definitions, conversation_welcomes y conversation_events los exigen por clave
// foránea) y construye el adaptador sobre ese *sql.DB. Los dos tenants nacen SIN fila en
// public.tenant_settings: es el «sin fila de configuración» que pide el Montaje.
func flowstoreNewMontaje(t *testing.T) storehelpertest.Montaje {
	t.Helper()
	n := flowstoreCases.Add(1)
	db := nuevaBase(t, fmt.Sprintf("flowstore_contrato_%02d", n)).Abrir(t)
	flowstoreWarmPool(t, db)

	return storehelpertest.Montaje{
		Store:    store.NewPostgresRepository(db),
		TenantA:  flowstoreSeedTenant(t, db, fmt.Sprintf("flowstore-contrato-%02d-a", n)),
		TenantB:  flowstoreSeedTenant(t, db, fmt.Sprintf("flowstore-contrato-%02d-b", n)),
		NewEvent: func(t *testing.T, tenantID string) string { return flowstoreNewEvent(t, db, tenantID) },
		SetSettings: func(t *testing.T, s storehelpertest.Settings) {
			flowstoreSetSettings(t, db, s)
		},
		FlowEvents: func(t *testing.T, tenantID string) []storehelpertest.FlowEvent {
			return flowstoreFlowEvents(t, db, tenantID)
		},
		SurveyResults: func(t *testing.T, tenantID string) []storehelpertest.SurveyResult {
			return flowstoreSurveyResults(t, db, tenantID)
		},
		ContentVersions: func(t *testing.T, tenantID, ref string) []storehelpertest.ContentVersion {
			return flowstoreContentVersions(t, db, tenantID, ref)
		},
		Intakes: func(t *testing.T, tenantID string) []storehelpertest.Intake {
			return flowstoreIntakes(t, db, tenantID)
		},
		IntakeItems: func(t *testing.T, intakeID string) []storehelpertest.IntakeItem {
			return flowstoreIntakeItems(t, db, intakeID)
		},
		Welcome: func(t *testing.T, tenantID, sessionID, contactID string) storehelpertest.WelcomeMark {
			return flowstoreWelcome(t, db, tenantID, sessionID, contactID)
		},
		Now:     func(t *testing.T) time.Time { return flowstoreNow(t, db) },
		Advance: func(t *testing.T) { flowstoreAdvance(t, db) },
	}
}

// flowstoreWarmPool deja flowstorePoolSize conexiones ya abiertas y en reposo en el pool. Sin
// esto, las llamadas simultáneas de una carrera de la suite saldrían escalonadas —cada una
// esperando a que se abra SU conexión, que cuesta más que la transacción entera— y la carrera que
// el caso quiere provocar no llegaría a darse: un adaptador sin su bloqueo pasaría en verde.
func flowstoreWarmPool(t *testing.T, db *sql.DB) {
	t.Helper()
	db.SetMaxIdleConns(flowstorePoolSize)
	ctx, cancel := context.WithTimeout(t.Context(), flowstoreTimeout)
	defer cancel()
	conns := make([]*sql.Conn, 0, flowstorePoolSize)
	for range flowstorePoolSize {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("abrir una conexión del pool: %v", err)
		}
		conns = append(conns, conn)
	}
	for _, conn := range conns {
		if err := conn.Close(); err != nil {
			t.Fatalf("devolver una conexión al pool: %v", err)
		}
	}
}

// flowstoreSeedTenant inserta un tenant con el slug dado en public.tenants y devuelve su id (un
// UUID). Solo rellena las columnas NOT NULL sin valor por defecto (0001_tenants.sql).
func flowstoreSeedTenant(t *testing.T, db *sql.DB, slug string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), flowstoreTimeout)
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

// flowstoreNewEvent crea un evento conversacional `open` del tenant y devuelve su id. Nace con una
// sesión y un contacto PROPIOS (gen_random_uuid()): así dos eventos del mismo tenant no chocan
// contra el único parcial «un evento vivo por tipo» de la 0051, y la suite puede pedir los que
// quiera. Lo único que la suite usa de él es el id, como destino de tres claves foráneas.
func flowstoreNewEvent(t *testing.T, db *sql.DB, tenantID string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), flowstoreTimeout)
	defer cancel()
	var id string
	err := db.QueryRowContext(ctx, `
		INSERT INTO public.conversation_events
			(tenant_id, session_id, contact_id, kind, history_id, status, flow_id, flow_version)
		VALUES ($1::uuid, 'flowstore-sess-' || gen_random_uuid(), gen_random_uuid(), 'cart',
		        'flowstore-' || gen_random_uuid(), 'open', 'flujo-contrato', 1)
		RETURNING id::text`, tenantID).Scan(&id)
	if err != nil {
		t.Fatalf("sembrar un evento del tenant %s: %v", tenantID, err)
	}
	return id
}

// flowstoreSeconds convierte una duración de la configuración en los segundos de su columna.
func flowstoreSeconds(d time.Duration) int64 { return int64(d / time.Second) }

// flowstoreSetSettings deja al tenant con EXACTAMENTE esa configuración: escribe las diez columnas
// que el puerto lee, creando la fila si no la tenía. Un checklist vacío se guarda como `[]`.
func flowstoreSetSettings(t *testing.T, db *sql.DB, s storehelpertest.Settings) {
	t.Helper()
	buyerFields := []byte(`[]`)
	if len(s.BuyerFields) > 0 {
		raw, err := json.Marshal(s.BuyerFields)
		if err != nil {
			t.Fatalf("serializar el checklist del comprador: %v", err)
		}
		buyerFields = raw
	}
	ctx, cancel := context.WithTimeout(t.Context(), flowstoreTimeout)
	defer cancel()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_settings
			(tenant_id, page_size, order_ttl_seconds, conversation_ttl_seconds, buyer_fields,
			 event_inactivity_ttl_seconds, event_history_ttl_seconds, aggregation_window_seconds,
			 aggregation_max_seconds, welcome_text, welcome_silence_seconds)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (tenant_id) DO UPDATE
		   SET page_size = EXCLUDED.page_size,
		       order_ttl_seconds = EXCLUDED.order_ttl_seconds,
		       conversation_ttl_seconds = EXCLUDED.conversation_ttl_seconds,
		       buyer_fields = EXCLUDED.buyer_fields,
		       event_inactivity_ttl_seconds = EXCLUDED.event_inactivity_ttl_seconds,
		       event_history_ttl_seconds = EXCLUDED.event_history_ttl_seconds,
		       aggregation_window_seconds = EXCLUDED.aggregation_window_seconds,
		       aggregation_max_seconds = EXCLUDED.aggregation_max_seconds,
		       welcome_text = EXCLUDED.welcome_text,
		       welcome_silence_seconds = EXCLUDED.welcome_silence_seconds`,
		s.TenantID, s.PageSize, flowstoreSeconds(s.OrderTTL), flowstoreSeconds(s.ConversationTTL), string(buyerFields),
		flowstoreSeconds(s.EventInactivityTTL), flowstoreSeconds(s.EventHistoryTTL), flowstoreSeconds(s.AggregationWindow),
		flowstoreSeconds(s.AggregationMax), s.WelcomeText, flowstoreSeconds(s.WelcomeSilence),
	); err != nil {
		t.Fatalf("fijar la configuración del tenant %s: %v", s.TenantID, err)
	}
}

// flowstoreRows corre una consulta de observación y devuelve una fila de tipo T por cada fila de
// la base, leída con scan. Si la consulta, una lectura, el recorrido o el cierre fallan, falla el
// test: la suite solo observa lo que ella misma escribió.
func flowstoreRows[T any](t *testing.T, db *sql.DB, what, query string, scan func(*sql.Rows, *T) error, args ...any) []T {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), flowstoreTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		t.Fatalf("leer %s: %v", what, err)
	}
	out := make([]T, 0)
	for rows.Next() {
		var row T
		if err := scan(rows, &row); err != nil {
			t.Fatalf("leer una fila de %s: %v", what, errors.Join(err, rows.Close()))
		}
		out = append(out, row)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatalf("recorrer %s: %v", what, err)
	}
	return out
}

// flowstoreFlowEvents devuelve los efectos del tenant en el orden en que se escribieron (id), con
// el payload JSONB deserializado.
func flowstoreFlowEvents(t *testing.T, db *sql.DB, tenantID string) []storehelpertest.FlowEvent {
	t.Helper()
	return flowstoreRows(t, db, "los efectos de "+tenantID, `
		SELECT tenant_id, contact_id, flow_id, flow_version, kind, name, payload
		FROM public.flow_events WHERE tenant_id = $1 ORDER BY id`,
		func(rows *sql.Rows, ev *storehelpertest.FlowEvent) error {
			var payload []byte
			if err := rows.Scan(&ev.TenantID, &ev.ContactID, &ev.FlowID, &ev.FlowVersion, &ev.Kind, &ev.Name, &payload); err != nil {
				return err
			}
			return json.Unmarshal(payload, &ev.Payload)
		}, tenantID)
}

// flowstoreSurveyResults devuelve las respuestas del tenant en el orden en que se escribieron
// (id), con su evento (NULL = cadena vacía) y su fecha.
func flowstoreSurveyResults(t *testing.T, db *sql.DB, tenantID string) []storehelpertest.SurveyResult {
	t.Helper()
	return flowstoreRows(t, db, "las respuestas de "+tenantID, `
		SELECT tenant_id, contact_id, flow_id, flow_version, question_id, answer_code,
		       COALESCE(event_id::text, ''), created_at
		FROM public.survey_results WHERE tenant_id = $1 ORDER BY id`,
		func(rows *sql.Rows, r *storehelpertest.SurveyResult) error {
			return rows.Scan(&r.TenantID, &r.ContactID, &r.FlowID, &r.FlowVersion, &r.QuestionID, &r.AnswerCode,
				&r.EventID, &r.CreatedAt)
		}, tenantID)
}

// flowstoreContentVersions devuelve las versiones archivadas de (tenant, ref) por número.
func flowstoreContentVersions(t *testing.T, db *sql.DB, tenantID, ref string) []storehelpertest.ContentVersion {
	t.Helper()
	return flowstoreRows(t, db, "las versiones de "+ref, `
		SELECT version, content, source, created_at
		FROM public.tenant_content_versions WHERE tenant_id = $1 AND ref = $2 ORDER BY version`,
		func(rows *sql.Rows, v *storehelpertest.ContentVersion) error {
			return rows.Scan(&v.Version, &v.Content, &v.Source, &v.CreatedAt)
		}, tenantID, ref)
}

// flowstoreIntakes devuelve TODAS las solicitudes del tenant con la fila entera: los diez campos
// que el puerto enseña más la nota del cliente. expires_at y event_id en NULL salen a cero.
func flowstoreIntakes(t *testing.T, db *sql.DB, tenantID string) []storehelpertest.Intake {
	t.Helper()
	return flowstoreRows(t, db, "las solicitudes de "+tenantID, `
		SELECT id::text, tenant_id, contact_id, session_id, status, total::float8,
		       COALESCE(event_id::text, ''), created_at, updated_at, expires_at, customer_note
		FROM public.intakes WHERE tenant_id = $1 ORDER BY created_at, id`,
		func(rows *sql.Rows, in *storehelpertest.Intake) error {
			var expires sql.NullTime
			if err := rows.Scan(&in.ID, &in.TenantID, &in.ContactID, &in.SessionID, &in.Status, &in.Total,
				&in.EventID, &in.CreatedAt, &in.UpdatedAt, &expires, &in.CustomerNote); err != nil {
				return err
			}
			in.ExpiresAt = expires.Time
			return nil
		}, tenantID)
}

// flowstoreIntakeItems devuelve las líneas de la solicitud en el orden en que las ve el cliente
// (added_at, id). El id se compara como texto para que uno que no sea UUID sea «sin líneas» y no
// un error de sintaxis.
func flowstoreIntakeItems(t *testing.T, db *sql.DB, intakeID string) []storehelpertest.IntakeItem {
	t.Helper()
	return flowstoreRows(t, db, "las líneas de "+intakeID, `
		SELECT intake_id::text, sku, label, customization, qty, unit_price::float8, added_at
		FROM public.intake_items WHERE intake_id::text = $1 ORDER BY added_at, id`,
		func(rows *sql.Rows, it *storehelpertest.IntakeItem) error {
			return rows.Scan(&it.IntakeID, &it.SKU, &it.Label, &it.Customization, &it.Qty, &it.UnitPrice, &it.AddedAt)
		}, intakeID)
}

// flowstoreWelcome devuelve la fila de la bienvenida de esa conversación, o la marca cero si no
// existe. welcomed_at en NULL sale a cero.
func flowstoreWelcome(t *testing.T, db *sql.DB, tenantID, sessionID, contactID string) storehelpertest.WelcomeMark {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), flowstoreTimeout)
	defer cancel()
	var (
		mark     storehelpertest.WelcomeMark
		welcomed sql.NullTime
	)
	err := db.QueryRowContext(ctx, `
		SELECT last_incoming_at, welcomed_at FROM public.conversation_welcomes
		WHERE tenant_id::text = $1 AND session_id = $2 AND contact_id::text = $3`,
		tenantID, sessionID, contactID).Scan(&mark.LastIncomingAt, &welcomed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return storehelpertest.WelcomeMark{}
	case err != nil:
		t.Fatalf("leer la bienvenida de (%s, %s, %s): %v", tenantID, sessionID, contactID, err)
	}
	mark.WelcomedAt = welcomed.Time
	return mark
}

// flowstoreNow es el Now del Montaje: el reloj con el que el adaptador fecha lo que escribe, que
// es el de la base.
func flowstoreNow(t *testing.T, db *sql.DB) time.Time {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), flowstoreTimeout)
	defer cancel()
	return flowstoreClock(ctx, t, db)
}

// flowstoreAdvance es el Advance del Montaje contra Postgres: vuelve cuando el reloj de la BASE ha
// pasado del instante en que estaba al entrar.
//
// El adaptador fecha con now(), que es el instante en que empezó la transacción (o la sentencia
// suelta). Toda escritura anterior a esta llamada empezó antes de la primera lectura de aquí, y
// toda escritura posterior empezará después de la última, que es estrictamente mayor: sus marcas
// no pueden coincidir. No se duerme: se le pregunta la hora a la base hasta que cambia, y con
// resolución de microsegundo y un viaje de red por lectura cambia a la primera.
func flowstoreAdvance(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), flowstoreTimeout)
	defer cancel()
	start := flowstoreClock(ctx, t, db)
	for range flowstoreClockTries {
		if flowstoreClock(ctx, t, db).After(start) {
			return
		}
	}
	t.Fatalf("el reloj de la base no pasó de %v en %d lecturas", start, flowstoreClockTries)
}

// flowstoreClock lee el reloj de pared de la base (clock_timestamp, que avanza dentro de una
// misma transacción, no now()).
func flowstoreClock(ctx context.Context, t *testing.T, db *sql.DB) time.Time {
	t.Helper()
	var now time.Time
	if err := db.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatalf("leer el reloj de la base: %v", err)
	}
	return now
}
