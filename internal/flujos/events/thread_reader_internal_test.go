// thread_reader_internal_test.go — Plan 044 · Ola 1 · T1.4.
//
// ⏳ NO SE HA EJECUTADO: se escribió en un entorno sin Go. Cada test lleva su
// MUTACIÓN, y la mutación COMPILA.
//
// Es un test INTERNO (package events) porque lo que fija es `entryText`, que no se
// exporta: la resolución del GRADO de una entrada a texto plano. La otra mitad de
// `ListThread` —el SQL y el descifrado— necesita Postgres y una KEK, y por eso no
// está aquí; queda dicho en la lista de lo no verificado del compositor.
package events

import (
	"database/sql"
	"strings"
	"testing"
)

// TestEntryText_ElResumenSeRenderizaComoLoLeeElCliente: una entrada de NIVEL 1
// —estructura en claro en `payload`— sale como el texto que el cliente leyó al
// reanudar, no como JSON.
//
// Importa más de lo que parece: ese texto acaba DENTRO de un prompt. Un
// `{"kind":"cart","lines":[…]}` metido en un prompt es una invitación a que el
// modelo lo trate como datos ya validados; una frase en el mismo idioma que el
// resto del hilo, no.
//
// MUTACIÓN (compila): en thread_reader.go, dentro de entryText, sustituir
//
//	return sum.Render(), nil
//
// por
//
//	return string(payload), nil
func TestEntryText_ElResumenSeRenderizaComoLoLeeElCliente(t *testing.T) {
	body, err := Summary{
		Kind:  "cart",
		Lines: []SummaryLine{{SKU: "TORTA-CHOC", Label: "Torta de chocolate", Qty: 2, UnitPrice: 10}},
	}.Encode()
	if err != nil {
		t.Fatalf("serializar el resumen: %v", err)
	}

	s := &Store{}
	got, err := s.entryText(body, nil, nil, sql.NullString{})
	if err != nil {
		t.Fatalf("entryText del nivel 1 no puede fallar: %v", err)
	}
	if !strings.Contains(got, "Torta de chocolate") {
		t.Fatalf("el resumen no se renderizó como prosa: %q", got)
	}
	if strings.Contains(got, `"sku"`) || strings.Contains(got, "{") {
		t.Fatalf("salió JSON en bruto y no el texto que lee el cliente: %q", got)
	}
}

// TestEntryText_LoQueNoEsResumenNoAportaTexto: una `decision` —el otro habitante
// del nivel 1— no tiene render, y un payload que no se pueda leer tampoco. Los dos
// devuelven cadena vacía SIN error: no aportan texto, y eso no es un fallo del
// hilo. Tratarlo como error dejaría a un cliente sin presupuesto por una fila vieja
// mal formada.
//
// MUTACIÓN (compila): en thread_reader.go, dentro de entryText, sustituir
//
//	if err := json.Unmarshal(payload, &sum); err != nil {
//		return "", nil
//	}
//
// por la misma condición devolviendo `return "", err`. Compila —`err` es la
// variable del propio `if`— y el sub-caso "un payload roto" se pone rojo. ⚠️ Al
// mutarla hay que quitar también el `//nolint:nilerr` de esa rama: con el `return
// "", err` deja de haber nada que silenciar y `nolintlint` protestaría. Es lint, no
// compilación, pero la mutación se aplica para ver un test rojo, no para ver un
// linter rojo.
func TestEntryText_LoQueNoEsResumenNoAportaTexto(t *testing.T) {
	s := &Store{}
	for nombre, payload := range map[string][]byte{
		"una decision":      []byte(`{"sku":"TORTA-CHOC","qty":2}`),
		"un payload roto":   []byte(`{no soy json`),
		"un payload vacío":  nil,
		"un objeto sin uso": []byte(`{"kind":"cart"}`),
	} {
		got, err := s.entryText(payload, nil, nil, sql.NullString{})
		if err != nil {
			t.Fatalf("%s: entryText devolvió error y no puede: %v", nombre, err)
		}
		if got != "" {
			t.Fatalf("%s: aportó texto al source_text y no debía: %q", nombre, got)
		}
	}
}
