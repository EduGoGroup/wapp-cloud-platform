//go:build pendiente

package stages_test

import (
	"testing"
	"time"
	_ "time/tzdata" // la zona con cambio de hora del test no depende de la máquina

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
)

// ---------------------------------------------------------------------------
// LA TABLA DE FECHAS — TODA EN GO Y SIN UNA SOLA LLAMADA AL MODELO
//
// Cada fecha esperada está ESCRITA A MANO y no se deriva de nada que el código bajo
// prueba calcule; las bases son días fijos del pasado, así que una implementación que
// leyera el reloj no podría acertar ninguna fila relativa.
// ---------------------------------------------------------------------------

// dateCase es una fila: qué fecha sale de qué expresión contra qué mensaje.
type dateCase struct {
	name string
	base string // el día del mensaje (message_ts)
	expr string // lo que escribió el cliente
	want string // la fecha absoluta, o "" si no debe resolverse
}

// baseAt construye el instante del mensaje en la zona dada, a las 09:55 (la hora del
// caso Ambar), para que ninguna fila dependa de la hora.
func baseAt(t *testing.T, day string, zone *time.Location) time.Time {
	t.Helper()
	base, err := time.ParseInLocation(time.DateOnly, day, zone)
	if err != nil {
		t.Fatalf("la base del caso no es una fecha: %v", err)
	}
	return base.Add(9*time.Hour + 55*time.Minute)
}

// runDateCases corre una tabla contra ResolveDate, en la zona por defecto.
func runDateCases(t *testing.T, cases []dateCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := stages.ResolveDate(c.expr, baseAt(t, c.base, stages.DefaultZone))
			if c.want == "" {
				if ok || !got.IsZero() {
					t.Fatalf("ResolveDate(%q, %s) = (%s, %v); NO debía resolver, y devuelve el instante cero",
						c.expr, c.base, got.Format(time.DateOnly), ok)
				}
				return
			}
			if !ok {
				t.Fatalf("ResolveDate(%q, %s) no resolvió; se esperaba %s", c.expr, c.base, c.want)
			}
			if day := got.Format(time.DateOnly); day != c.want {
				t.Fatalf("ResolveDate(%q, %s) = %s; se esperaba %s", c.expr, c.base, day, c.want)
			}
			if got.Hour() != 0 || got.Minute() != 0 || got.Second() != 0 || got.Nanosecond() != 0 {
				t.Fatalf("ResolveDate(%q, %s) = %s; la fecha vuelve a MEDIANOCHE", c.expr, c.base, got)
			}
		})
	}
}

// TestResolveDate_TheTable fija, caso por caso, las reglas del contrato y su orden.
func TestResolveDate_TheTable(t *testing.T) {
	runDateCases(t, []dateCase{
		// —— El ejemplo del plan y del caso Ambar ——
		{"wednesday of next week from a monday", "2026-07-13", "el miércoles de la semana que viene", "2026-07-22"},
		{"the same without accents", "2026-07-13", "el miercoles de la semana que viene", "2026-07-22"},
		{"the proxima semana mark", "2026-07-13", "el miércoles de la próxima semana", "2026-07-22"},

		// —— El día a secas: próxima aparición ESTRICTA ——
		{"bare wednesday said on a monday is this week", "2026-07-13", "para el miércoles", "2026-07-15"},
		{"bare monday said on a monday is next monday never today", "2026-07-13", "el lunes", "2026-07-20"},
		{"wednesday que viene is the next occurrence", "2026-07-13", "el miércoles que viene", "2026-07-15"},
		{"proximo wednesday is the next occurrence", "2026-07-13", "el próximo miércoles", "2026-07-15"},

		// —— Relativos de día ——
		{"today is the message day", "2026-07-13", "hoy si puede ser", "2026-07-13"},
		{"tomorrow", "2026-07-13", "para mañana", "2026-07-14"},
		{"day after tomorrow wins over tomorrow", "2026-07-13", "pasado mañana", "2026-07-15"},

		// —— Cantidades ——
		{"in 5 days", "2026-07-13", "en 5 días", "2026-07-18"},
		{"in one week", "2026-07-13", "en una semana", "2026-07-20"},
		{"in two weeks", "2026-07-13", "en dos semanas", "2026-07-27"},
		{"in ten days in letters", "2026-07-13", "en diez dias", "2026-07-23"},

		// —— Fechas explícitas ——
		{"lettered month", "2026-07-13", "para el 22 de julio", "2026-07-22"},
		{"numeric day/month", "2026-07-13", "el 22/07", "2026-07-22"},
		{"numeric with dashes and year", "2026-07-13", "el 22-07-2026", "2026-07-22"},
		{"explicit date wins over the weekday next to it", "2026-07-13", "el miércoles 22 de julio", "2026-07-22"},
		{"explicit date wins over the week mark", "2026-07-13", "el 30 de julio, la semana que viene no", "2026-07-30"},

		// —— El orden de las reglas: gana la primera que resuelve ——
		{"lettered date wins over a numeric one", "2026-07-13", "el 22 de julio o el 23/07", "2026-07-22"},
		{"numeric date wins over the week mark", "2026-07-13", "el 23/07 o el lunes de la semana que viene", "2026-07-23"},
		{"week mark wins over a relative day", "2026-07-13", "mañana no, el lunes de la semana que viene", "2026-07-20"},
		{"relative day wins over an amount", "2026-07-13", "hoy, o en 5 días", "2026-07-13"},
		{"amount wins over a bare weekday", "2026-07-13", "el viernes o en 2 semanas", "2026-07-27"},

		// —— Cruce de MES ——
		{"month crossing with the week mark", "2026-07-29", "el miércoles de la semana que viene", "2026-08-05"},
		{"month crossing in 5 days", "2026-01-28", "en 5 días", "2026-02-02"},
		{"month crossing in a leap year", "2028-02-27", "en 3 días", "2028-03-01"},

		// —— Cruce de AÑO ——
		{"year crossing with the week mark", "2026-12-28", "el miércoles de la semana que viene", "2027-01-06"},
		{"year crossing day after tomorrow", "2026-12-31", "pasado mañana", "2027-01-02"},
		{"year crossing with the year omitted", "2026-12-28", "para el 5 de enero", "2027-01-05"},
		{"written year rules even when it is the next one", "2026-12-28", "el 5 de enero de 2027", "2027-01-05"},
		{"written year rules even when already past", "2026-07-13", "el 5 de enero de 2026", "2026-01-05"},

		// —— La semana ISO ——
		{"said on a sunday monday of next week is tomorrow", "2026-07-19", "el lunes de la semana que viene", "2026-07-20"},
		{"sunday of next week from a monday is thirteen days", "2026-07-13", "el domingo de la semana que viene", "2026-07-26"},

		// —— Lo que NO se resuelve, y es lo correcto ——
		{"cuando puedas is not a date", "2026-07-13", "cuando puedas", ""},
		{"next week without a day is a range not a date", "2026-07-13", "la semana que viene", ""},
		{"a date that does not exist is not normalized forward", "2026-07-13", "el 31 de febrero", ""},
		{"29 february of a non leap year", "2026-02-20", "el 29 de febrero", ""},
		{"the empty expression", "2026-07-13", "", ""},
		{"07/22 is not day/month", "2026-07-13", "el 07/22", ""},
	})
}

// TestResolveDate_IsAPureFunctionOfItsArguments es el corazón de «un job reanudado dos
// días después no cambia la fecha», en la pieza más pequeña que puede fallar. Se compara
// con la fecha ESCRITA A MANO: comparar la función consigo misma pasaría también si
// leyera el reloj.
func TestResolveDate_IsAPureFunctionOfItsArguments(t *testing.T) {
	base := baseAt(t, "2026-07-13", stages.DefaultZone)
	for i := range 3 {
		got, ok := stages.ResolveDate("el miércoles de la semana que viene", base)
		if !ok || got.Format(time.DateOnly) != "2026-07-22" {
			t.Fatalf("pasada %d: (%s, %v); se esperaba 2026-07-22", i, got.Format(time.DateOnly), ok)
		}
	}
}

// TestResolveDate_ReturnsMidnightInTheZoneOfTheBase: el día se cuenta en la zona de
// `base` —no en UTC ni en la local de la máquina— y la fecha vuelve en esa misma zona.
func TestResolveDate_ReturnsMidnightInTheZoneOfTheBase(t *testing.T) {
	minus3 := time.FixedZone("-03", -3*60*60)
	// Lunes 13 a las 23:30 en UTC−3: en UTC ya es martes 14.
	base := time.Date(2026, 7, 13, 23, 30, 0, 0, minus3)

	got, ok := stages.ResolveDate("mañana", base)
	if !ok {
		t.Fatal("«mañana» no resolvió")
	}
	if want := time.Date(2026, 7, 14, 0, 0, 0, 0, minus3); !got.Equal(want) || got.Location() != minus3 {
		t.Fatalf("ResolveDate = %s (zona %s); se esperaba %s, a medianoche en la zona de la base", got, got.Location(), want)
	}
}

// TestResolveDate_DaylightSavingChangeDoesNotMoveTheDay: los días se suman sobre el
// mediodía, así que la noche que el reloj salta una hora no se pierde ni se repite un
// día. En Europe/Madrid el reloj se adelanta el 29/03/2026 y se atrasa el 25/10/2026.
func TestResolveDate_DaylightSavingChangeDoesNotMoveTheDay(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatalf("no se pudo cargar la zona del caso: %v", err)
	}
	cases := []struct {
		name string
		base time.Time
		expr string
		want string
	}{
		{"tomorrow is the spring forward day", time.Date(2026, 3, 28, 23, 30, 0, 0, madrid), "mañana", "2026-03-29"},
		{"day after tomorrow across spring forward", time.Date(2026, 3, 28, 0, 30, 0, 0, madrid), "pasado mañana", "2026-03-30"},
		{"in one week across fall back", time.Date(2026, 10, 24, 23, 30, 0, 0, madrid), "en una semana", "2026-10-31"},
		{"tomorrow is the fall back day", time.Date(2026, 10, 24, 0, 30, 0, 0, madrid), "mañana", "2026-10-25"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := stages.ResolveDate(c.expr, c.base)
			if !ok || got.Format(time.DateOnly) != c.want {
				t.Fatalf("ResolveDate(%q, %s) = (%s, %v); se esperaba %s", c.expr, c.base, got.Format(time.DateOnly), ok, c.want)
			}
			if got.Hour() != 0 || got.Minute() != 0 || got.Location() != madrid {
				t.Fatalf("ResolveDate(%q) = %s; se esperaba la medianoche de Madrid", c.expr, got)
			}
		})
	}
}
