//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"net/http"
	"testing"
)

// El colector de flow_events visto en /metrics (internal/platform/metrics/flowlifecycle): la parte
// de P10 que re-expresa collector_integration_test.go. El colector da una vuelta al arrancar y otra
// cada 15 s (constante, trampa T-6), así que todo se espera sondeando /metrics con tope de 60 s; y
// wapp_flow_event_lifecycle_total es un CounterVec: no existe hasta su primer incremento (T-10).
//
// 🔧 POR QUÉ LAS FILAS SE SIEMBRAN POR SQL: flow_events lo escribe el motor de flujos, y los
// recorridos que el arnés sabe provocar hoy (palabra clave a un menú plano, P3) no escriben ninguna
// fila (hallazgo 40 de F9). El colector lee la tabla, no al motor: la fila sembrada es su entrada.
//
// Lo que NO se lleva de collector_integration_test.go, y por qué:
//   - «arranca en max(id): el histórico no se recuenta» pide filas en flow_events ANTES de que el
//     servidor arranque, y arrancar crea la base y lanza el servidor en un solo paso;
//   - «dos réplicas: solo una lidera y cuenta» pide dos servidores sobre la misma base, que el
//     arnés prohíbe (reglas.md §2). Queda la mitad observable: el servidor SOSTIENE el advisory
//     lock y un segundo aspirante no lo gana (collectorLeaderLock);
//   - «Run libera el lock al cancelar el contexto» no se distingue por la puerta: al morir el
//     proceso Postgres suelta los locks de sesión, con defer o sin él.

const (
	// p10MetricLifecycle es el contador del colector, con etiquetas name, event_kind y reason.
	p10MetricLifecycle = "wapp_flow_event_lifecycle_total"
	// p10CollectorLockKey es la clave del advisory lock de sesión con la que las réplicas eligen
	// al único colector (collectorLockKey). Se repite aquí a propósito: es un contrato ENTRE
	// binarios —un servidor viejo y uno nuevo sobre la misma base tienen que disputar la misma
	// clave—, y un cambio de valor en uno solo los pondría a contar dos veces.
	p10CollectorLockKey int64 = 0x37232781758ec913
)

// p10FlowEvent es una fila de public.flow_events: la columna kind («persist» o «event»), el nombre
// del efecto y el payload JSON.
type p10FlowEvent struct{ kind, name, payload string }

// p10SeriesKey es la clave de una serie del colector: name, event_kind y reason.
func p10SeriesKey(name, eventKind, reason string) string {
	return name + "|" + eventKind + "|" + reason
}

// p10LifecycleSeries raspa GET :8100/metrics y devuelve las series de p10MetricLifecycle por clave
// p10SeriesKey. Un mapa vacío es un valor legítimo (T-10). Falla (t.Fatalf) si /metrics no responde
// 200 o no se puede interpretar.
func p10LifecycleSeries(t *testing.T, s *servidor) map[string]float64 {
	t.Helper()
	r := s.Admin("").Get(t, "/metrics", nil)
	if r.Codigo != http.StatusOK {
		t.Fatalf("GET /metrics = %d, quería 200", r.Codigo)
	}
	exp, err := p0ParsearExposicion(string(r.Cuerpo))
	if err != nil {
		t.Fatalf("/metrics no se puede interpretar: %v", err)
	}
	out := map[string]float64{}
	for _, m := range exp.Muestras {
		if m.Nombre == p10MetricLifecycle {
			out[p10SeriesKey(m.Etiquetas["name"], m.Etiquetas["event_kind"], m.Etiquetas["reason"])] += m.Valor
		}
	}
	return out
}

// p10InsertFlowEvents inserta las filas en UNA transacción, en orden: el colector las ve todas en
// la misma vuelta o no ve ninguna. Falla (t.Fatalf) si el SQL falla.
func p10InsertFlowEvents(t *testing.T, db *sql.DB, tenant string, events []p10FlowEvent) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), topeFixture)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("sembrar flow_events: abrir la transacción: %v", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			t.Errorf("sembrar flow_events: rollback: %v", rerr)
		}
	}()
	for _, e := range events {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO public.flow_events (tenant_id, contact_id, flow_id, flow_version, kind, name, payload)
			VALUES ($1, 'contacto-opaco-p10', 'flujo-p10', 1, $2, $3, $4::jsonb)`, tenant, e.kind, e.name, e.payload); err != nil {
			t.Fatalf("sembrar flow_events (%s): %v", e.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("sembrar flow_events: confirmar: %v", err)
	}
}

// waitSeries espera, con tope, a que las series del colector sean EXACTAMENTE want, y las deja
// apuntadas en el mundo. Falla (t.Fatalf) con la diferencia si no llegan a serlo.
func (w *p10World) waitSeries(t *testing.T, want map[string]float64) {
	t.Helper()
	var got map[string]float64
	p10Poll(t, p10PollTimeout,
		func() string { return "las series de " + p10MetricLifecycle + ": " + p10Diff(got, want) },
		func() bool {
			got = p10LifecycleSeries(t, w.sc.S)
			return maps.Equal(got, want)
		})
	w.metrics = want
}

// p10CollectorRows son las filas del subtest collector_series y p10CollectorWant las series que
// tienen que dar. Cada grupo lleva su regla.
var p10CollectorRows = []p10FlowEvent{
	// El predicado es LIKE 'event\_%' con el guion bajo ESCAPADO: sin el escape, «eventXfoo»
	// casaría (el «_» sería un comodín). Ni ese ni un efecto de otra familia cuentan.
	{"persist", "eventXfoo", `{"kind":"cart"}`},
	{"persist", "survey_answer", `{"kind":"survey"}`},
	// Adversarios del predicado: mayúsculas, un espacio delante, un espacio Unicode en vez del
	// guion bajo y «event» sin separador no casan.
	{"event", "EVENT_started", `{"kind":"cart"}`},
	{"event", " event_started", `{"kind":"cart"}`},
	{"event", "event started", `{"kind":"cart"}`},
	{"event", "event", `{"kind":"cart"}`},
	// Se agrupa por name y por payload->>'kind', NUNCA por la columna kind de la fila: las dos
	// filas de event_started/cart tienen columnas kind distintas y van a la misma serie.
	{"event", "event_started", `{"kind":"cart"}`},
	{"persist", "event_started", `{"kind":"cart"}`},
	{"event", "event_closed", `{"kind":"cart"}`},
	{"event", "event_started", `{"kind":"survey"}`},
	// Las tres causas de event_escaped son tres series; el efecto sin causa sigue contándose.
	{"event", "event_escaped", `{"kind":"cart","reason":"client_escape"}`},
	{"event", "event_escaped", `{"kind":"cart","reason":"client_escape"}`},
	{"event", "event_escaped", `{"kind":"cart","reason":"owner_flow_finished"}`},
	{"event", "event_escaped", `{"kind":"cart","reason":"orphan_menu"}`},
	// La clave reason ausente y la clave vacía van a la MISMA serie, la de reason vacío.
	{"event", "event_escaped", `{"kind":"menu"}`},
	{"event", "event_escaped", `{"kind":"menu","reason":""}`},
	// Sin payload.kind la serie sale con event_kind vacío.
	{"event", "event_started", `{}`},
	// Adversarios que SÍ casan: el separador repetido, el prefijo solo, y etiquetas con «@@»,
	// dígitos no ASCII y un espacio Unicode, que llegan a /metrics tal cual.
	{"event", "event__started", `{"kind":"a@@b","reason":"١٢٣"}`},
	{"event", "event_", `{"kind":"cart x"}`},
}

var p10CollectorWant = map[string]float64{
	p10SeriesKey("event_started", "cart", ""):                    2,
	p10SeriesKey("event_closed", "cart", ""):                     1,
	p10SeriesKey("event_started", "survey", ""):                  1,
	p10SeriesKey("event_escaped", "cart", "client_escape"):       2,
	p10SeriesKey("event_escaped", "cart", "owner_flow_finished"): 1,
	p10SeriesKey("event_escaped", "cart", "orphan_menu"):         1,
	p10SeriesKey("event_escaped", "menu", ""):                    2,
	p10SeriesKey("event_started", "", ""):                        1,
	p10SeriesKey("event__started", "a@@b", "١٢٣"):                1,
	p10SeriesKey("event_", "cart x", ""):                         1,
}

// collectorSeries: sin filas en flow_events el contador no existe (T-10); con las filas de
// p10CollectorRows, las series son exactamente p10CollectorWant. La etiqueta de empresa no existe:
// el contador es de plataforma, y las filas de dos empresas se suman.
func (w *p10World) collectorSeries(t *testing.T) {
	if n := consultaEntero(t, w.sc.DB, `SELECT count(*) FROM public.flow_events`); n != 0 {
		t.Fatalf("flow_events tiene %d filas antes de sembrar: el subtest contaba con una tabla vacía", n)
	}
	if before := p10LifecycleSeries(t, w.sc.S); len(before) != 0 {
		t.Errorf("%s ya tiene series sin ninguna fila en flow_events: %v", p10MetricLifecycle, before)
	}
	half := len(p10CollectorRows) / 2
	p10InsertFlowEvents(t, w.sc.DB, w.sc.Tenant, p10CollectorRows[:half])
	p10InsertFlowEvents(t, w.sc.DB, w.otherTenant, p10CollectorRows[half:])
	w.waitSeries(t, p10CollectorWant)
}

// collectorNoRecount: el cursor avanza. Una fila nueva sube SU serie en uno y nada más: si la
// vuelta que la cuenta volviera a leer desde el principio, todas las series anteriores se
// doblarían. Y como esa serie solo aparece tras una vuelta posterior a collector_series, cuando
// se ve ya hubo al menos una vuelta sin que nada se recontara.
func (w *p10World) collectorNoRecount(t *testing.T) {
	want := maps.Clone(w.metrics)
	p10InsertFlowEvents(t, w.sc.DB, w.sc.Tenant, []p10FlowEvent{{"event", "event_closed", `{"kind":"survey"}`}})
	want[p10SeriesKey("event_closed", "survey", "")] = 1
	w.waitSeries(t, want)

	// Y otra vuelta más, con una fila sobre una serie que ya existía: sube en uno, no se reinicia.
	want = maps.Clone(w.metrics)
	p10InsertFlowEvents(t, w.sc.DB, w.sc.Tenant, []p10FlowEvent{{"event", "event_started", `{"kind":"cart"}`}})
	want[p10SeriesKey("event_started", "cart", "")]++
	w.waitSeries(t, want)
}

// collectorLeaderLock: el servidor sostiene el advisory lock del colector —una sola sesión de su
// base lo tiene concedido— y un segundo aspirante que lo pide sin bloquear no lo gana. Es lo que
// impide que dos réplicas cuenten dos veces la misma fila.
func (w *p10World) collectorLeaderLock(t *testing.T) {
	const held = `SELECT count(*) FROM pg_locks l
		WHERE l.locktype = 'advisory' AND l.granted AND l.objsubid = 1
		  AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
		  AND ((l.classid::bigint << 32) | l.objid::bigint) = $1`
	if n := consultaEntero(t, w.sc.DB, held, p10CollectorLockKey); n != 1 {
		t.Errorf("hay %d sesiones con el advisory lock del colector concedido, quería 1", n)
	}

	ctx, cancel := context.WithTimeout(t.Context(), topeFixture)
	defer cancel()
	conn, err := w.sc.DB.Conn(ctx)
	if err != nil {
		t.Fatalf("reservar la sesión del segundo aspirante: %v", err)
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("devolver la sesión del segundo aspirante: %v", cerr)
		}
	}()
	var won bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, p10CollectorLockKey).Scan(&won); err != nil {
		t.Fatalf("pedir el advisory lock del colector: %v", err)
	}
	if won {
		t.Errorf("un segundo aspirante ganó el advisory lock del colector con el servidor vivo: dos réplicas contarían dos veces")
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, p10CollectorLockKey); err != nil {
			t.Errorf("soltar el lock ganado por error: %v", err)
		}
	}
	// Perder la carrera no cambia nada: las series siguen como estaban.
	if got := p10LifecycleSeries(t, w.sc.S); !maps.Equal(got, w.metrics) {
		t.Errorf("las series cambiaron tras disputar el lock: %s", p10Diff(got, w.metrics))
	}
}
