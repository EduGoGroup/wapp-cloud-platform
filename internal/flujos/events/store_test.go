package events

// Tests INTERNOS del store: los de aquí afirman sobre el SQL mismo (la forma de la
// consulta), que es justo lo que un test de caja negra no puede mirar. Lo que la
// consulta DEVUELVE contra Postgres real se prueba en store_integration_test.go.

import (
	"strings"
	"testing"
)

// TestINV19_ElWhereDeRescatablesNoCompararaFechas es el test de regresión de
// INV-19: la marca «vencido» INFORMA, no filtra.
//
// Se mira el WHERE y no el resultado a propósito. Un test de datos que compruebe
// que un evento vencido sigue saliendo pasa también si alguien mete la comparación
// de fechas en el WHERE con el signo al revés; este no. Y el fallo que previene es
// caro y silencioso: bastaría un `AND now() - e.last_activity_at > ...` para que el
// rescate dejara de ofrecer justo lo que se dejó a medias hace un rato.
func TestINV19_ElWhereDeRescatablesNoCompararaFechas(t *testing.T) {
	desde := strings.Index(selectRescuableSQL, "WHERE")
	hasta := strings.Index(selectRescuableSQL, "ORDER BY")
	if desde < 0 || hasta < 0 || hasta < desde {
		t.Fatalf("no se puede aislar el WHERE de la consulta:\n%s", selectRescuableSQL)
	}
	where := selectRescuableSQL[desde:hasta]

	for _, prohibido := range []string{"last_activity_at", "now(", "make_interval", "$4", "interval", "ttl"} {
		if strings.Contains(where, prohibido) {
			t.Fatalf("el WHERE de rescatables contiene %q: el vencimiento marca, no filtra (INV-19). WHERE:\n%s",
				prohibido, where)
		}
	}
	// Y la otra mitad: el filtro por el contenido SÍ tiene que estar ahí (REQ-26c),
	// escrito sobre la vista event_content y su vocabulario genérico (D-043.22).
	if !strings.Contains(where, "c.event_id IS NULL OR c.state = 'alive'") {
		t.Fatalf("el WHERE debe filtrar por el contenido (INV-17). WHERE:\n%s", where)
	}
	t.Logf("WHERE de rescatables:%s", where)
}

// TestMarcaVencidoRespetaElOverrideSinVencimiento comprueba en el SQL la mitad que
// un test de datos solo ve si a alguien se le ocurre el caso: la expresión de
// «vencido» exige ttl > 0, porque 0 es «sin vencimiento» (E-6) y no un plazo de
// cero segundos que caducaría todo al instante.
func TestMarcaVencidoRespetaElOverrideSinVencimiento(t *testing.T) {
	expr := rescuableStale
	if !strings.Contains(expr, "> 0\n        AND") {
		t.Fatalf("la marca «vencido» debe exigir ttl > 0 (0 = sin vencimiento); expr:\n%s", expr)
	}
	if !strings.Contains(expr, "$4::timestamptz") {
		t.Fatalf("el instante debe venir por parámetro (reloj inyectable), no de now(); expr:\n%s", expr)
	}
	if strings.Contains(expr, "now()") {
		t.Fatalf("la marca no puede depender del reloj del servidor: no se puede fijar en un test; expr:\n%s", expr)
	}
}

// TestQualify_LaListaCualificadaEsLaMISMA compara columna a columna las dos listas
// —la de conversation_events a secas y la cualificada con el alias `e`— porque las
// dos alimentan el MISMO scan. Si alguien añade una columna a una y no a la otra,
// scanEvent leería una fila desalineada, y una fila desalineada no da error de
// compilación: da datos cambiados de sitio.
//
// La derivación (poner el prefijo) vive aquí, en el test, y no en el código: así la
// consulta que las usa sigue siendo una constante que se lee entera en el fuente.
func TestQualify_LaListaCualificadaEsLaMISMA(t *testing.T) {
	original := strings.Split(eventColumns, ",")
	cualificada := strings.Split(eventColumnsE, ",")
	if len(original) != len(cualificada) {
		t.Fatalf("%d columnas cualificadas frente a %d originales", len(cualificada), len(original))
	}
	for i := range original {
		quiero := "e." + strings.Join(strings.Fields(original[i]), " ")
		if got := strings.TrimSpace(cualificada[i]); got != quiero {
			t.Fatalf("columna %d: %q, quiero %q", i, got, quiero)
		}
	}
	if len(original) != 12 {
		t.Fatalf("conversation_events tiene 12 columnas y la lista trae %d: si añadiste una, "+
			"añádela también a eventDest o el scan se desalinea", len(original))
	}
}

// TestQualify_NoSeCuelaUnAliasEnLaListaSinCualificar: la lista original NO puede
// llevar ya un alias, porque entonces qualify produciría «e.e.id» y la consulta
// dejaría de compilar en Postgres (donde falla en producción, no aquí).
func TestQualify_NoSeCuelaUnAliasEnLaListaSinCualificar(t *testing.T) {
	if strings.Contains(eventColumns, ".") {
		t.Fatalf("eventColumns debe ir SIN cualificar (la cualifica qualify): %s", eventColumns)
	}
}
