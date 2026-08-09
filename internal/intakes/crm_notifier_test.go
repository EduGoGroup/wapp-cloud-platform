package intakes

import (
	"strings"
	"testing"
)

// TestCRMStatusTemplates_LosCuatroCanonicosTienenTexto es la vigilancia que el propio
// NotifyCRMStatus asume al tratar un estado sin plantilla como ANOMALÍA (y no como el
// silencio normal de statusTemplates).
//
// Sin este test, añadir un quinto estado canónico al contrato dejaría al cliente sin
// enterarse: el callback respondería 200, el reflejo se aplicaría, y el aviso se
// perdería en una línea de log que nadie mira.
func TestCRMStatusTemplates_LosCuatroCanonicosTienenTexto(t *testing.T) {
	for _, st := range crmCanonicalStatuses {
		tpl, ok := crmStatusTemplates[st]
		if !ok {
			t.Errorf("el estado canónico %q no tiene texto: el cliente no se enteraría de su cambio", st)
			continue
		}
		if strings.TrimSpace(tpl) == "" {
			t.Errorf("el texto de %q está vacío", st)
		}
	}
	if len(crmStatusTemplates) != len(crmCanonicalStatuses) {
		t.Errorf("hay %d textos para %d estados canónicos: sobra uno que ningún callback puede disparar",
			len(crmStatusTemplates), len(crmCanonicalStatuses))
	}
}

// TestCRMStatusTemplates_RejectedNoSeDuplica fija la decisión de reutilizar el texto
// del ciclo de vida para el único literal que comparten los dos vocabularios. Si
// alguien le escribe uno propio, tendremos dos redacciones del mismo hecho que se
// separarán con el tiempo — y el cliente recibirá una u otra según por dónde venga el
// rechazo, que es exactamente lo que no queremos.
func TestCRMStatusTemplates_RejectedNoSeDuplica(t *testing.T) {
	if crmStatusTemplates[CRMStatusRejected] != statusTemplates[StatusRejected] {
		t.Fatalf("el rechazo del CRM y el del ciclo de vida deben decir LO MISMO:\n crm=%q\n ciclo=%q",
			crmStatusTemplates[CRMStatusRejected], statusTemplates[StatusRejected])
	}
}

// TestCRMStatusTemplates_NoPrometenLoQueNadiePuedeCumplir aplica al vocabulario del
// CRM el mismo criterio que el 041 exige a sus textos: ni fechas inventadas, ni
// marcadores que estas plantillas no resuelven.
func TestCRMStatusTemplates_NoPrometenLoQueNadiePuedeCumplir(t *testing.T) {
	for st, tpl := range crmStatusTemplates {
		for _, marcador := range []string{placeholderPlazo, placeholderFechaLímite} {
			if strings.Contains(tpl, marcador) {
				t.Errorf("el texto de %q usa %s, que solo resuelve el cobro del dueño: %q", st, marcador, tpl)
			}
		}
	}
}
