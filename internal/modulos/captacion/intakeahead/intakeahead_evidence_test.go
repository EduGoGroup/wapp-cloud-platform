//go:build pendiente

package intakeahead_test

// intakeahead_evidence_test.go — el SANEO contra el texto del cliente, visto por el
// contrato de Request: la evidencia sostiene la respuesta (si no aparece, tumba la
// clasificación), los params no (se caen uno a uno), y nada de lo que el cliente
// escribió sale por el log (INV-6).

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intakeahead"
)

const (
	evidenceMissingMsg = "adelanto: la evidencia no aparece en el mensaje; la clasificación se descarta"
	paramsDroppedMsg   = "adelanto: params descartados por el allowlist"
)

// TestRequest_EvidenceMustBeInTheClientText: la evidencia es, por contrato, una COPIA
// LITERAL del mensaje. Se compara con mayúsculas y blancos normalizados —el modelo
// capitaliza y parte líneas a su gusto, y eso no es inventar— y con los acentos SIN
// normalizar: «sabado» por «sábado» es reescribir, no copiar. Es una DECISIÓN: rechazar
// solo significa no adelantar; el error caro es el contrario.
func TestRequest_EvidenceMustBeInTheClientText(t *testing.T) {
	cases := []struct {
		name      string
		intent    string
		evidence  string
		delivered bool
	}{
		{"literal phrase", "intake_request", "quiero 200 sillas", true},
		{"whole message", "intake_request", clientText, true},
		{"different case and blanks", "intake_request", "QUIERO   200\n\tSILLAS", true},
		{"accent dropped by the model", "intake_request", "200 sillas para el sabado", false},
		{"invented phrase", "intake_request", "quiero 500 alfombras persas", false},
		{"words of the text in another order", "intake_request", "sillas 200 quiero", false},
		{"unknown label with evidence from the text", "desconocido", "Hola", true},
		// El parser acepta la evidencia vacía de «no entendí»; el saneo no: no hay frase
		// que la sostenga y ese caso no dispara nada.
		{"unknown label without evidence", "desconocido", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := newBench(t, intakeahead.WithWorkers(1))
				b.prov.setReplies(reply{raw: artifact(tc.intent, 0.93, tc.evidence, nil)})
				b.start()

				b.pool.Request(windowKey(), clientText)
				settle()

				if got := len(b.prov.seen()); got != 1 {
					t.Fatalf("una evidencia que no se sostiene NO es un fallo de calidad y no se reintenta: %d llamadas", got)
				}
				got := b.sink.seen()
				if tc.delivered {
					want := delivery{key: windowKey(), intent: tc.intent, confidence: 0.93}
					if len(got) != 1 || got[0] != want {
						t.Fatalf("entregas = %+v, quiero %+v", got, want)
					}
					if b.log.String() != "" {
						t.Errorf("una clasificación aceptada no loguea:\n%s", b.log.String())
					}
					return
				}
				if len(got) != 0 {
					t.Fatalf("una evidencia que no está en el mensaje descarta la clasificación entera: %+v", got)
				}
				assertOnlyLog(t, b.log, "DEBUG", evidenceMissingMsg,
					"tenant_id="+tenantID, "session_id="+sessionID, "intent="+tc.intent)

				// Rechazar no es un fallo de la vía: la ventana queda libre y el worker vivo.
				b.prov.setReplies(reply{raw: goodArtifact()})
				b.pool.Request(windowKey(), clientText)
				settle()
				if len(b.sink.seen()) != 1 {
					t.Errorf("tras el descarte, la siguiente petición de la ventana debe servirse")
				}
			})
		})
	}
}

// TestRequest_InventedParamsAreDroppedAndTheIntentSurvives es la otra mitad de la regla,
// y la asimetría es deliberada: un param inventado NO puede tumbar una intención con
// evidencia buena. Los params no salen del paquete; lo que se ve desde fuera es que la
// entrega llega igual y el NÚMERO de descartados en el log.
//
// El texto es «a qué hora abren, quiero 200 sillas»; `consulta` declara `tema` e
// `intake_request` declara `params: []` (D-044.20: ahí todo param es invención).
func TestRequest_InventedParamsAreDroppedAndTheIntentSurvives(t *testing.T) {
	const text = "A qué hora abren, quiero 200 sillas"
	cases := []struct {
		name        string
		intent      string
		evidence    string
		params      map[string]string
		wantDropped int
	}{
		{"declared, value in the text", "consulta", "a qué hora abren", map[string]string{"tema": "hora"}, 0},
		{"declared, value in another case", "consulta", "a qué hora abren", map[string]string{"tema": "HORA  ABREN"}, 0},
		{"declared, empty value means not said", "consulta", "a qué hora abren", map[string]string{"tema": ""}, 0},
		{"declared, value not in the text", "consulta", "a qué hora abren", map[string]string{"tema": "precio"}, 1},
		{"undeclared, dropped without looking at its value", "consulta", "a qué hora abren",
			map[string]string{"cantidad": "200"}, 1},
		{"one kept, two dropped", "consulta", "a qué hora abren",
			map[string]string{"tema": "hora", "cantidad": "200", "producto": "alfombras"}, 2},
		{"published intent declares none", "intake_request", "quiero 200 sillas",
			map[string]string{"cantidad": "200", "producto": "sillas", "sin_valor": ""}, 3},
		{"unknown label declares none", "desconocido", "hora",
			map[string]string{"tema": "hora"}, 1},
		{"no params at all", "consulta", "a qué hora abren", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := newBench(t)
				b.prov.setReplies(reply{raw: artifact(tc.intent, 0.9, tc.evidence, tc.params)})
				b.start()

				b.pool.Request(windowKey(), text)
				settle()

				want := delivery{key: windowKey(), intent: tc.intent, confidence: 0.9}
				if got := b.sink.seen(); len(got) != 1 || got[0] != want {
					t.Fatalf("un param rechazado NUNCA tumba una clasificación con evidencia buena: %+v", got)
				}
				if tc.wantDropped == 0 {
					if b.log.String() != "" {
						t.Errorf("sin descartes no se loguea:\n%s", b.log.String())
					}
					return
				}
				assertOnlyLog(t, b.log, "DEBUG", paramsDroppedMsg,
					"tenant_id="+tenantID, "intent="+tc.intent, fmt.Sprintf("descartados=%d", tc.wantDropped))
			})
		})
	}
}

// TestRequest_BadEvidenceAndBadParamsAreBothReported: las dos mitades del saneo son
// independientes: se cuentan los params descartados aunque la evidencia tumbe la
// clasificación.
func TestRequest_BadEvidenceAndBadParamsAreBothReported(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBench(t)
		b.prov.setReplies(reply{raw: artifact("intake_request", 0.99, "alfombras persas",
			map[string]string{"producto": "alfombras"})})
		b.start()

		b.pool.Request(windowKey(), clientText)
		settle()

		if len(b.sink.seen()) != 0 {
			t.Fatalf("la evidencia inventada tumba la clasificación")
		}
		if len(b.log.lines("DEBUG", paramsDroppedMsg)) != 1 || len(b.log.lines("DEBUG", evidenceMissingMsg)) != 1 {
			t.Errorf("quiero las dos líneas, la de params y la de evidencia:\n%s", b.log.String())
		}
	})
}

// TestLogs_NeverCarryWhatTheClientWrote es INV-6 donde se puede romper solo: este
// paquete tiene el literal del cliente en memoria fuera de un sobre cifrado. Se recorren
// TODOS los caminos que loguean —con el log en Debug— y en ninguno puede aparecer el
// texto del cliente, la evidencia que el modelo copió de él ni el valor de un param.
func TestLogs_NeverCarryWhatTheClientWrote(t *testing.T) {
	const text = "Soy Mariana Zubizarreta, quiero 200 sillas Tiffany para el sábado"
	secrets := []string{"Mariana", "Zubizarreta", "Tiffany", "sillas", "sábado", "alfombras", "persas", "quiero"}
	paths := []struct {
		name  string
		setup func(b *bench)
	}{
		{"queue full", nil}, // se monta aparte, sin Run
		{"catalog read failure", func(b *bench) { b.cfg.set(nil, errors.New("la base no responde")) }},
		{"invalid catalog", func(b *bench) { b.cfg.set(map[string]string{tenantID: `{}`}, nil) }},
		{"selector failure", func(b *bench) { b.sel.setErr(errors.New("vía caída")) }},
		{"provider failure", func(b *bench) { b.prov.setReplies(reply{err: errors.New("el Edge no responde")}) }},
		{"quality failure twice", func(b *bench) { b.prov.setReplies(reply{raw: "{"}) }},
		{"evidence not in the text", func(b *bench) {
			b.prov.setReplies(reply{raw: artifact("intake_request", 0.9, "quiero alfombras persas", nil)})
		}},
		{"params dropped", func(b *bench) {
			b.prov.setReplies(reply{raw: artifact("intake_request", 0.9, "quiero 200 sillas Tiffany",
				map[string]string{"producto": "sillas Tiffany", "otro": "alfombras persas"})})
		}},
	}
	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := newBench(t, intakeahead.WithQueueSize(1))
				if tc.setup == nil {
					b.pool.Request(windowOf("c-1"), text)
					b.pool.Request(windowOf("c-2"), text)
				} else {
					tc.setup(b)
					b.start()
					b.pool.Request(windowKey(), text)
					settle()
				}

				out := b.log.String()
				if out == "" {
					t.Fatalf("este camino tenía que loguear algo: el caso no está mirando nada")
				}
				for _, secret := range secrets {
					if strings.Contains(out, secret) {
						t.Errorf("el log lleva %q, que es texto del cliente (INV-6):\n%s", secret, out)
					}
				}
			})
		})
	}
}
