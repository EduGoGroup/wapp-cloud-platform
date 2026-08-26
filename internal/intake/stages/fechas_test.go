package stages_test

import (
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
)

// ---------------------------------------------------------------------------
// LA TABLA DE FECHAS — TODA EN GO Y SIN UNA SOLA LLAMADA AL MODELO
// ---------------------------------------------------------------------------
//
// Es el criterio de T2.4 al pie de la letra: «tabla de tests de fechas (incluye cruce
// de mes/año) todos en Go sin LLM». Que no haya LLM no es una comodidad del test: es la
// tarea. Si la aritmética estuviera en el modelo, esta tabla no podría existir.

// baseDe construye la fecha del mensaje en la zona por defecto. Las horas son 09:55,
// la del caso Ambar, para que ninguna fila dependa de la hora.
func baseDe(t *testing.T, fecha string) time.Time {
	t.Helper()
	b, err := time.ParseInLocation(time.DateOnly, fecha, stages.ZonaPorDefecto)
	if err != nil {
		t.Fatalf("la base del caso no es una fecha: %v", err)
	}
	return b.Add(9*time.Hour + 55*time.Minute)
}

// TestResolverFecha_LaTablaSinLLM fija, caso por caso, qué fecha sale de qué expresión
// contra qué mensaje. Cada fila es una respuesta a «¿qué tendría que pasar para que
// esto fallara?»: la fecha esperada está escrita a mano y no se deriva de nada que el
// código bajo prueba calcule.
//
// 💥 MUTACIONES EJECUTADAS, cada una compila y cada una la pone roja:
//   - en ResolverFecha, calcular contra `time.Now()` en vez de contra `base` ⇒ caen
//     TODAS las filas relativas;
//   - en offsetDesdeLunes, devolver `int(d)` (semana que empieza en domingo) ⇒ cae la
//     fila del domingo, que es la que separa la semana ISO de la de Go;
//   - en porDiaDeLaSemana, dejar `delta = 0` cuando el día coincide ⇒ cae «el lunes»
//     dicho un lunes, que pasaría a ser hoy;
//   - poner porDiaDeLaSemana ANTES que porSemanaQueViene en reglasDeFecha ⇒ cae el
//     ejemplo del plan: «el miércoles de la semana que viene» daría el 15 y no el 22;
//   - en porFechaConMesEnLetra, no sumar el año cuando la fecha ya pasó ⇒ cae el cruce
//     de año de «el 5 de enero»;
//   - en fechaValida, quitar la comprobación de que la fecha existe ⇒ «31 de febrero»
//     dejaría de ser «sin fecha» y pasaría a ser el 3 de marzo.
func TestResolverFecha_LaTablaSinLLM(t *testing.T) {
	casos := []struct {
		nombre string
		base   string // el día del mensaje (message_ts)
		expr   string // lo que escribió el cliente
		quiero string // la fecha absoluta, o "" si no debe resolverse
	}{
		// —— El ejemplo literal del plan y del caso Ambar ——
		{"🔴 el ejemplo del plan: miércoles de la semana que viene desde un lunes",
			"2026-07-13", "el miércoles de la semana que viene", "2026-07-22"},
		{"la misma expresión sin tildes, que es como se escribe en WhatsApp",
			"2026-07-13", "el miercoles de la semana que viene", "2026-07-22"},
		{"y con la forma «la próxima semana»",
			"2026-07-13", "el miércoles de la próxima semana", "2026-07-22"},

		// —— El día a secas: próxima aparición ESTRICTA ——
		{"el miércoles, dicho un lunes, es el de esta semana",
			"2026-07-13", "para el miércoles", "2026-07-15"},
		{"el lunes, dicho un LUNES, es el que viene y nunca hoy",
			"2026-07-13", "el lunes", "2026-07-20"},
		{"«el miércoles que viene» se resuelve como la próxima aparición (decisión documentada)",
			"2026-07-13", "el miércoles que viene", "2026-07-15"},

		// —— Relativos de día ——
		{"hoy es el día del mensaje", "2026-07-13", "hoy si puede ser", "2026-07-13"},
		{"mañana", "2026-07-13", "para mañana", "2026-07-14"},
		{"pasado mañana gana a mañana, que está contenido en él",
			"2026-07-13", "pasado mañana", "2026-07-15"},

		// —— Cantidades ——
		{"en 5 días", "2026-07-13", "en 5 días", "2026-07-18"},
		{"en una semana", "2026-07-13", "en una semana", "2026-07-20"},
		{"en dos semanas", "2026-07-13", "en dos semanas", "2026-07-27"},

		// —— Fechas explícitas ——
		{"el 22 de julio", "2026-07-13", "para el 22 de julio", "2026-07-22"},
		{"22/07 en la convención día/mes", "2026-07-13", "el 22/07", "2026-07-22"},
		{"22-07-2026 con año", "2026-07-13", "el 22-07-2026", "2026-07-22"},
		{"la fecha explícita gana al día de la semana que la acompaña",
			"2026-07-13", "el miércoles 22 de julio", "2026-07-22"},

		// —— 🔴 CRUCE DE MES ——
		{"🔴 cruce de MES: miércoles de la semana que viene desde el 29 de julio",
			"2026-07-29", "el miércoles de la semana que viene", "2026-08-05"},
		{"🔴 cruce de MES: en 5 días desde el 28 de enero",
			"2026-01-28", "en 5 días", "2026-02-02"},
		{"🔴 cruce de MES en año BISIESTO: en 3 días desde el 27 de febrero de 2028",
			"2028-02-27", "en 3 días", "2028-03-01"},

		// —— 🔴 CRUCE DE AÑO ——
		{"🔴 cruce de AÑO: miércoles de la semana que viene desde el 28 de diciembre",
			"2026-12-28", "el miércoles de la semana que viene", "2027-01-06"},
		{"🔴 cruce de AÑO: pasado mañana desde el 31 de diciembre",
			"2026-12-31", "pasado mañana", "2027-01-02"},
		{"🔴 cruce de AÑO con el año OMITIDO: «el 5 de enero» dicho el 28 de diciembre",
			"2026-12-28", "para el 5 de enero", "2027-01-05"},
		{"el año escrito manda aunque ya haya pasado",
			"2026-12-28", "el 5 de enero de 2027", "2027-01-05"},

		// —— La semana ISO ——
		{"🔴 semana ISO: dicho un DOMINGO, «el lunes de la semana que viene» es mañana",
			"2026-07-19", "el lunes de la semana que viene", "2026-07-20"},
		{"y el domingo de la semana que viene, desde un lunes, son trece días",
			"2026-07-13", "el domingo de la semana que viene", "2026-07-26"},

		// —— Lo que NO se resuelve, y es lo correcto ——
		{"«cuando puedas» no es una fecha", "2026-07-13", "cuando puedas", ""},
		{"«la semana que viene» SIN día es un rango de siete, no una fecha",
			"2026-07-13", "la semana que viene", ""},
		{"una fecha que no existe no se normaliza al día siguiente",
			"2026-07-13", "el 31 de febrero", ""},
		{"el 29 de febrero de un año no bisiesto tampoco",
			"2026-02-20", "el 29 de febrero", ""},
		{"la expresión vacía", "2026-07-13", "", ""},
		{"07/22 no es una fecha en día/mes: el mes 22 no existe",
			"2026-07-13", "el 07/22", ""},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			base := baseDe(t, c.base)
			f, ok := stages.ResolverFecha(c.expr, base)
			if c.quiero == "" {
				if ok {
					t.Fatalf("ResolverFecha(%q, %s) resolvió a %s; NO debía resolver",
						c.expr, c.base, f.Format(time.DateOnly))
				}
				return
			}
			if !ok {
				t.Fatalf("ResolverFecha(%q, %s) no resolvió; se esperaba %s", c.expr, c.base, c.quiero)
			}
			if got := f.Format(time.DateOnly); got != c.quiero {
				t.Fatalf("ResolverFecha(%q, %s) = %s; se esperaba %s", c.expr, c.base, got, c.quiero)
			}
		})
	}
}

// TestResolverFecha_NoDependeDelDiaEnQueSeEjecuta es el corazón del criterio «un job
// reanudado dos días después no cambia la fecha», medido en la pieza más pequeña que
// puede fallar: la misma expresión contra la misma base da la misma fecha, y ninguna de
// las dos cosas tiene que ver con hoy.
//
// El test compara el resultado con la fecha ESCRITA A MANO y no con otra llamada a la
// función: comparar la función consigo misma pasaría también si leyera el reloj.
//
// 💥 MUTACIÓN EJECUTADA: en ResolverFecha, `ancla := anclaDe(time.Now())` ⇒ rojo.
func TestResolverFecha_NoDependeDelDiaEnQueSeEjecuta(t *testing.T) {
	base := baseDe(t, "2026-07-13")
	assertFixtureLejosDeHoy(t, base)

	for i := 0; i < 3; i++ {
		f, ok := stages.ResolverFecha("el miércoles de la semana que viene", base)
		if !ok || f.Format(time.DateOnly) != "2026-07-22" {
			t.Fatalf("pasada %d: got (%v, %v); se esperaba 2026-07-22", i, f.Format(time.DateOnly), ok)
		}
	}
}
