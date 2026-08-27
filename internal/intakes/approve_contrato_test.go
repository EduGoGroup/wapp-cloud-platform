package intakes_test

// approve_contrato_test.go — LAS CLAVES QUE LEE LA PRECONDICIÓN SON LAS DEL
// CONTRATO §7.4 (Plan 044 · T4.3).
//
// LinesWithoutPrice usa literales a propósito: lo que lee es el payload que ya está
// escrito en `intake_revisions`, o sea el CABLE, y el cable no cambia de forma porque
// alguien renombre un campo de Go. Eso deja una grieta exacta: si el productor
// renombrara `unit_price`, el pipeline escribiría otra clave, la precondición no
// encontraría NINGUNA línea sin precio y todos los tests de conducta seguirían verdes
// —porque siembran el payload con el nombre nuevo y comprueban el viejo—. El
// resultado sería aprobar borradores a medio precificar sin un solo error.
//
// Este test cierra esa grieta mirando las etiquetas JSON reales por reflexión, que es
// el mismo molde con el que T4.1 ata su gate por campo
// (publicapi/intakes_llm_gate_test.go: TestGateLLM_LasClavesSonLasDelContrato).
//
// Importar `stages` desde aquí es legal y no es un ciclo: éste es el paquete de test
// EXTERNO de `intakes` (intakes_test), y `stages` sí puede importar `intakes`.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

func TestAprobar_LasClavesSonLasDelContrato(t *testing.T) {
	campo := func(tipo reflect.Type, nombre string) reflect.StructField {
		t.Helper()
		f, ok := tipo.FieldByName(nombre)
		if !ok {
			t.Fatalf("%s no tiene el campo %s: el contrato §7.4 cambió de forma y la precondición de "+
				"T4.3 se quedó leyendo un payload que ya no existe", tipo, nombre)
		}
		return f
	}
	etiqueta := func(f reflect.StructField) string {
		nombre, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		return nombre
	}

	precio := campo(reflect.TypeOf(stages.Linea{}), "UnitPrice")
	if got := etiqueta(precio); got != intakes.ClaveLineaUnitPrice {
		t.Fatalf("stages.Linea.UnitPrice se serializa como %q y la precondición de la aprobación busca %q: "+
			"actualiza internal/intakes/approve.go", got, intakes.ClaveLineaUnitPrice)
	}
	if got := etiqueta(campo(reflect.TypeOf(stages.Linea{}), "Label")); got != intakes.ClaveLineaLabel {
		t.Fatalf("stages.Linea.Label se serializa como %q y el detalle del 400 busca %q", got, intakes.ClaveLineaLabel)
	}
	if got := etiqueta(campo(reflect.TypeOf(stages.PayloadRevision{}), "Lines")); got != intakes.ClavePayloadLines {
		t.Fatalf("stages.PayloadRevision.Lines se serializa como %q y la precondición busca %q", got, intakes.ClavePayloadLines)
	}

	// 🔴 EL PUNTERO ES LA MITAD DE LA PRECONDICIÓN. `unit_price` es *float64 y sin
	// `omitempty` porque design §7.4 escribe `"unit_price": null` para la línea que el
	// dueño tiene que precificar. Si alguien lo convirtiera en float64, esa línea
	// pasaría a serializarse como 0 —que aquí significa «artículo de regalo»— y
	// aprobar dejaría de distinguir «sin precio» de «gratis»: el cliente recibiría una
	// cotización con un renglón regalado.
	if precio.Type.Kind() != reflect.Pointer {
		t.Fatalf("stages.Linea.UnitPrice dejó de ser puntero (%s): sin él, «sin precio» y «gratis» son "+
			"el mismo 0 y la precondición de T4.3 no puede existir", precio.Type)
	}
	if strings.Contains(precio.Tag.Get("json"), "omitempty") {
		t.Fatal("stages.Linea.UnitPrice ganó omitempty: una línea sin precio dejaría de emitir la clave. " +
			"LinesWithoutPrice trata la ausencia como «sin precio», así que hoy no rompe — pero la decisión " +
			"tiene que ser explícita y no un efecto de una etiqueta")
	}
}
