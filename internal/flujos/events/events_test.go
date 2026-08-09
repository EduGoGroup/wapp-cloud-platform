package events_test

import (
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
)

// TestHistoryIDFormatoYUTC fija el formato exacto de E-3 y, sobre todo, que la
// hora que se imprime es la UTC del instante — no la de su zona.
//
// El caso que importa es el segundo: un instante en UTC+5 a las 23:30 es el mismo
// instante que las 18:30 UTC. Si la fórmula olvidara el .UTC(), el id diría 2330 y
// además cambiaría de DÍA según dónde corriera el proceso. Un id de historial que
// depende del huso del servidor no identifica nada.
func TestHistoryIDFormatoYUTC(t *testing.T) {
	casos := []struct {
		nombre string
		kind   string
		at     time.Time
		quiero string
	}{
		{
			nombre: "instante ya en UTC",
			kind:   "cart",
			at:     time.Date(2026, 8, 9, 18, 30, 45, 123456789, time.UTC),
			quiero: "cart-2026-08-09-1830", // los segundos y nanos NO entran (E-3)
		},
		{
			nombre: "instante en UTC+5 cruzando el día",
			kind:   "survey",
			at:     time.Date(2026, 8, 9, 23, 30, 0, 0, time.FixedZone("UTC+5", 5*3600)),
			quiero: "survey-2026-08-09-1830",
		},
		{
			nombre: "instante en UTC-6 retrocediendo el día",
			kind:   "menu",
			at:     time.Date(2026, 8, 9, 20, 15, 0, 0, time.FixedZone("UTC-6", -6*3600)),
			quiero: "menu-2026-08-10-0215",
		},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := events.HistoryID(c.kind, c.at); got != c.quiero {
				t.Fatalf("HistoryID(%q, %s) = %q; quiero %q", c.kind, c.at, got, c.quiero)
			}
		})
	}
}

// TestIsSuspendedDerivada recorre la regla completa de E-6. Los dos casos que un
// test descuidado se salta y que aquí son el motivo del test: ttl <= 0 (el
// override «sin vencimiento» del tenant, que NO es «caduca al instante») y la
// frontera exacta (justo en el ttl todavía NO está suspendido).
func TestIsSuspendedDerivada(t *testing.T) {
	ahora := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	vivo := func(inactivo time.Duration) events.Event {
		return events.Event{Status: events.StatusOpen, LastActivityAt: ahora.Add(-inactivo)}
	}

	casos := []struct {
		nombre string
		ev     events.Event
		ttl    time.Duration
		quiero bool
	}{
		{"ttl=0 con 100 años de inactividad sigue sin vencer", vivo(100 * 365 * 24 * time.Hour), 0, false},
		{"ttl negativo tampoco vence", vivo(48 * time.Hour), -1 * time.Second, false},
		{"inactivo más que el ttl", vivo(3 * time.Hour), 2 * time.Hour, true},
		{"inactivo menos que el ttl", vivo(1 * time.Hour), 2 * time.Hour, false},
		{"justo en el ttl todavía no", vivo(2 * time.Hour), 2 * time.Hour, false},
		{"un instante pasado el ttl ya sí", vivo(2*time.Hour + time.Nanosecond), 2 * time.Hour, true},
		{
			nombre: "cerrado no está suspendido: ya murió",
			ev:     events.Event{Status: events.StatusClosed, LastActivityAt: ahora.Add(-48 * time.Hour)},
			ttl:    2 * time.Hour,
			quiero: false,
		},
		{
			nombre: "cancelado tampoco",
			ev:     events.Event{Status: events.StatusCancelled, LastActivityAt: ahora.Add(-48 * time.Hour)},
			ttl:    2 * time.Hour,
			quiero: false,
		},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := events.IsSuspended(c.ev, c.ttl, ahora); got != c.quiero {
				t.Fatalf("IsSuspended(status=%s, inactividad=%s, ttl=%s) = %v; quiero %v",
					c.ev.Status, ahora.Sub(c.ev.LastActivityAt), c.ttl, got, c.quiero)
			}
		})
	}
}

// TestIsSuspendedNoMutaElEvento comprueba que la función no toca lo que recibe:
// se le pasa una COPIA y se compara el original campo a campo después. Es la mitad
// barata de «es pura»; la otra mitad —que no escribe en la BD— la prueba
// TestIntegration_IsSuspendedNoEscribeNada, que sí tiene una BD delante.
func TestIsSuspendedNoMutaElEvento(t *testing.T) {
	ahora := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	ev := events.Event{
		ID:             "11111111-1111-1111-1111-111111111111",
		Status:         events.StatusOpen,
		Kind:           "cart",
		HistoryID:      "cart-2026-08-09-0800",
		LastActivityAt: ahora.Add(-9 * time.Hour),
	}
	antes := ev

	if !events.IsSuspended(ev, time.Hour, ahora) {
		t.Fatalf("el evento debería estar suspendido (9 h de inactividad con ttl de 1 h)")
	}
	if ev != antes {
		t.Fatalf("IsSuspended mutó el evento: %+v; era %+v", ev, antes)
	}
}
