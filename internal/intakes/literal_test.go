package intakes_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// literal_test.go — QUÉ SALE DEL PAYLOAD Y QUÉ SE QUEDA (Plan 044 · Ola 3 · T3.5).
//
// Es la mitad barata del criterio de T3.5: el barrido por SQL de verdad vive en
// literal_integration_test.go y necesita una BD. Estos tests no la necesitan porque
// prueban la REGLA —qué se considera nivel 2 y qué nivel 1—, y esa regla es la que
// decide qué acaba cifrado.

// payloadDeAmbar imita el contrato §7.4 tal como lo escribe la etapa draft: literal
// del cliente arriba, líneas con su evidencia, y —esto es lo que el test protege—
// una personalización que NO se cifra.
const payloadDeAmbar = `{
	"version": 1,
	"source_text": "### MENSAJES DE LA CONVERSACIÓN (literal, en orden) ###\ncliente: Hola Herminia, te quería pedir un presupuesto\ncliente: Serían 2 tortas, una de chocolate\n### FIN DE LOS MENSAJES ###",
	"message_ts": "2026-07-13T09:55:00Z",
	"analysis": {"provider": "local", "source": "whatsapp", "reanalyzed_from": null},
	"lines": [
		{"kind": "matched", "sku": "torta-choc", "label": "Torta de chocolate", "qty": 1,
		 "unit_price": 25000, "customization": "sin sal",
		 "evidence": "una torta sería con decoración infantil, de bizcocho húmedo de chocolate"},
		{"kind": "unmatched", "label": "tequeños congelados", "qty": 1, "unit_price": null,
		 "evidence": "un paquete de tequeños congelados de 30"},
		{"kind": "shipping", "label": "Envío", "qty": 1, "unit_price": null}
	],
	"suggested_questions": [],
	"warnings": [{"item_pos": 1, "reason": "sin_precio"}]
}`

// TestPartirLiteral_SacaElNivel2YDejaElNivel1 es la regla del ADR-0034 §Decisión 1
// aplicada campo a campo. Los dos lados importan igual: si se cifrara de menos queda
// literal en claro (el fallo de MP-06); si se cifrara de más se destruiría el
// agregado de negocio («cuántos piden sin sal») sin proteger a nadie.
func TestPartirLiteral_SacaElNivel2YDejaElNivel1(t *testing.T) {
	limpio, lit, err := intakes.PartirLiteral(json.RawMessage(payloadDeAmbar))
	if err != nil {
		t.Fatalf("PartirLiteral: %v", err)
	}

	// --- NIVEL 2: sale del payload y acaba en el literal -----------------------
	if !strings.Contains(lit.SourceText, "Hola Herminia") {
		t.Fatalf("el source_text no llegó al literal: %q", lit.SourceText)
	}
	if got := len(lit.Evidence); got != 2 {
		t.Fatalf("evidencias extraídas = %d, se esperaban 2 (la línea de envío no tiene): %v", got, lit.Evidence)
	}
	if !strings.Contains(lit.Evidence["1"], "tequeños congelados de 30") {
		t.Fatalf("la evidencia de la línea 1 no es la suya: %q", lit.Evidence["1"])
	}

	texto := string(limpio)
	for _, prohibido := range []string{
		"Hola Herminia",
		"decoración infantil",
		"un paquete de tequeños congelados de 30",
		"source_text",
		"evidence",
	} {
		if strings.Contains(texto, prohibido) {
			t.Fatalf("el payload que se va a persistir todavía contiene %q:\n%s", prohibido, texto)
		}
	}

	// --- NIVEL 1: se queda, y la personalización es el caso que más se confunde -
	for _, esperado := range []string{
		`"sin sal"`,             // ADR-0034: dato de negocio cuantificable, JAMÁS se cifra
		`"torta-choc"`,          // sku
		`"tequeños congelados"`, // label de la línea unmatched: es el NOMBRE del producto
		`25000`,                 // precio
		`"item_pos":1`,          // aviso, que lleva posición y no texto
	} {
		if !strings.Contains(strings.ReplaceAll(texto, " ", ""), strings.ReplaceAll(esperado, " ", "")) {
			t.Fatalf("la interpretación estructurada perdió %s:\n%s", esperado, texto)
		}
	}
}

// TestPartirYFundir_EsUnRoundTrip comprueba que lo que se guarda vuelve. Sin esto,
// el cifrado podría estar impecable y la bandeja seguir sin poder enseñarle al dueño
// el original al lado de la interpretación, que es la mitad de la razón de ser de la
// revisión (§7.6).
func TestPartirYFundir_EsUnRoundTrip(t *testing.T) {
	limpio, lit, err := intakes.PartirLiteral(json.RawMessage(payloadDeAmbar))
	if err != nil {
		t.Fatalf("PartirLiteral: %v", err)
	}
	vuelto, err := intakes.FundirLiteral(limpio, lit)
	if err != nil {
		t.Fatalf("FundirLiteral: %v", err)
	}

	var antes, despues map[string]any
	if err := json.Unmarshal([]byte(payloadDeAmbar), &antes); err != nil {
		t.Fatalf("payload original ilegible: %v", err)
	}
	if err := json.Unmarshal(vuelto, &despues); err != nil {
		t.Fatalf("payload reconstruido ilegible: %v", err)
	}
	esperado, err := json.Marshal(antes)
	if err != nil {
		t.Fatalf("re-serializando el payload original: %v", err)
	}
	obtenido, err := json.Marshal(despues)
	if err != nil {
		t.Fatalf("re-serializando el payload reconstruido: %v", err)
	}
	if string(esperado) != string(obtenido) {
		t.Fatalf("el round-trip no devolvió el mismo payload:\n antes: %s\ndespués: %s", esperado, obtenido)
	}
}

// TestPartirLiteral_NoTocaLosNUMEROS es la razón por la que PartirLiteral trabaja
// sobre json.RawMessage y no sobre map[string]any. Un round-trip por `any` pasa todo
// número por float64, y ahí un entero grande —un id, un precio en la moneda sin
// decimales de algún país— se muerde SIN ERROR.
func TestPartirLiteral_NoTocaLosNumeros(t *testing.T) {
	const conEnteroGrande = `{"version":1,"source_text":"hola","total":9007199254740993,"lines":[]}`
	limpio, _, err := intakes.PartirLiteral(json.RawMessage(conEnteroGrande))
	if err != nil {
		t.Fatalf("PartirLiteral: %v", err)
	}
	if !strings.Contains(string(limpio), "9007199254740993") {
		t.Fatalf("el entero se degradó al partir el payload: %s", limpio)
	}
}

// TestPartirLiteral_UnPayloadSinLiteralNoSeToca cubre el caso MAYORITARIO de la
// tabla: las revisiones del carrito numérico. Si el partido las reescribiera, cada
// cierre de carrito pagaría dos serializaciones para no cambiar nada — y peor, el
// payload guardado dejaría de ser byte a byte el que produjo CartRevisionPayload.
func TestPartirLiteral_UnPayloadSinLiteralNoSeToca(t *testing.T) {
	original, err := intakes.CartRevisionPayload(5000, []intakes.RevisionLine{
		{SKU: "emp-pino", Label: "Empanada de pino", Qty: 2, UnitPrice: 2500},
	})
	if err != nil {
		t.Fatalf("CartRevisionPayload: %v", err)
	}
	limpio, lit, err := intakes.PartirLiteral(original)
	if err != nil {
		t.Fatalf("PartirLiteral: %v", err)
	}
	if !lit.Vacio() {
		t.Fatalf("una revisión de carrito no tiene literal, y se extrajo: %+v", lit)
	}
	if string(limpio) != string(original) {
		t.Fatalf("el payload sin literal se reescribió:\n antes: %s\ndespués: %s", original, limpio)
	}
}

// TestFundirLiteral_RechazaUnaEvidenciaQueNoCasaConSuLINEA. Devolver la evidencia a
// la línea equivocada es PEOR que no devolverla: el dueño leería como prueba de una
// línea una frase que sostiene otra, y decidiría el pedido con eso.
func TestFundirLiteral_RechazaUnaEvidenciaHuerfana(t *testing.T) {
	const dosLineas = `{"version":1,"lines":[{"sku":"a"},{"sku":"b"}]}`
	_, err := intakes.FundirLiteral(json.RawMessage(dosLineas), intakes.LiteralRevision{
		Evidence: map[string]string{"7": "una frase de una línea que ya no existe"},
	})
	if err == nil {
		t.Fatal("se fundió una evidencia sobre una línea inexistente sin protestar")
	}
}

// TestPartirLiteral_UnSourceTextQueNoEsCadenaFALLA. La alternativa —dejarlo pasar—
// persistiría en claro algo que se llama `source_text`, que es justo lo que esta
// tarea existe para impedir.
func TestPartirLiteral_UnSourceTextQueNoEsCadenaFalla(t *testing.T) {
	const raro = `{"version":1,"source_text":{"texto":"hola Herminia"}}`
	if _, _, err := intakes.PartirLiteral(json.RawMessage(raro)); err == nil {
		t.Fatal("un source_text que no es cadena se aceptó en silencio")
	}
}

// TestLiteralVencido_ElCeroEsRETENCIONINDEFINIDA. La lectura contraria —«0 = vencido
// siempre»— destruiría el literal de todo tenant que dejara la clave a cero, y lo
// haría en la primera lectura, sin aviso y sin vuelta atrás.
func TestLiteralVencido_ElCeroEsRetencionIndefinida(t *testing.T) {
	const unSiglo = 100 * 365 * 24 * 3600
	if intakes.LiteralVencido(unSiglo, 0) {
		t.Fatal("con TTL 0 se dio por vencido un literal: 0 significa SIN PODA")
	}
	if !intakes.LiteralVencido(intakes.TTLLiteralPorDefecto, intakes.TTLLiteralPorDefecto) {
		t.Fatal("una edad EXACTAMENTE igual al TTL tiene que estar vencida (>=, no >)")
	}
	if intakes.LiteralVencido(intakes.TTLLiteralPorDefecto-1, intakes.TTLLiteralPorDefecto) {
		t.Fatal("un literal un nanosegundo más joven que el TTL no está vencido")
	}
}
