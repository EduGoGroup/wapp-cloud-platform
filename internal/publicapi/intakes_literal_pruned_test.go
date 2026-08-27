package publicapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// intakes_literal_pruned_test.go — LA CLAVE QUE SEPARA «NUNCA HUBO TEXTO» DE «LO
// HUBO Y SE PODÓ» (Plan 044 · Ola 4 · Tanda 4, D-044.52 §3).
//
// Lo que se prueba aquí NO es que un campo de Go llegue al DTO: es que el consumidor
// pueda DISTINGUIR tres estados que hasta ahora colapsaban en dos. Por eso los tests
// miran el árbol JSON crudo del cuerpo y no un struct tipado —un struct ya sabe qué
// campos existen y no puede afirmar que una clave está AUSENTE—, que es exactamente
// la diferencia que este campo viene a publicar.

const (
	intakeSinLiteral    = "88888888-1111-4000-8000-000000000001"
	intakeLiteralVivo   = "88888888-1111-4000-8000-000000000002"
	intakeLiteralPodado = "88888888-1111-4000-8000-000000000003"
)

// payloadCarrito es una revisión del carrito numérico: interpretación estructurada y
// NADA de nivel 2. Es el caso «nunca hubo texto», y no depende del TTL ni del reloj
// —sin literal no hay nada que podar—, así que este fixture no puede volverse
// intermitente el día que pase un plazo.
func payloadCarrito() json.RawMessage {
	return json.RawMessage(`{"version":1,"lines":[` +
		`{"kind":"matched","sku":"torta-choc","label":"Torta de chocolate","qty":1,"unit_price":18000}]}`)
}

// payloadConLiteral es una revisión del pipeline LLM: lleva `source_text` (nivel 2,
// lo que la poda destruye) y `suggested_questions` (nivel 1, pero clave del gate de
// `llm_intake`). Las dos juntas a propósito: es lo que permite comprobar, sobre UN
// solo cuerpo, que el gate filtró lo suyo y aun así no se llevó el sello de poda.
func payloadConLiteral() json.RawMessage {
	return json.RawMessage(`{"version":1,` +
		`"source_text":"hola! quiero una torta para el sábado",` +
		`"lines":[{"kind":"matched","sku":"torta-choc","label":"Torta de chocolate","qty":1,` +
		`"unit_price":18000,"evidence":"una torta para el sábado"}],` +
		`"suggested_questions":["¿De cuántas porciones la quieres?"]}`)
}

// bandejaDeRetención siembra las TRES solicitudes que hacen falta para hablar de este
// campo, en un solo store y con un solo TTL (una hora):
//
//   - sin literal: revisión `cart`, nunca tuvo texto.
//   - literal vivo: revisión `interpreted` fechada hace un minuto, dentro del plazo.
//   - literal podado: revisión `interpreted` fechada en un pasado FIJO (2026-08-07),
//     que la primera lectura poda. La fecha es fija y el tiempo solo avanza, así que
//     el caso no puede dejar de cumplirse con el paso de los días.
//
// Que los tres compartan store importa: si el sello viniera de un TTL global y no de
// la revisión, los tres saldrían iguales y los tests de abajo se caerían.
func bandejaDeRetención(t *testing.T) *intakes.MemoryStore {
	t.Helper()
	st := intakes.NewMemoryStore()
	st.SetLiteralTTL(time.Hour)

	sembrar := func(id string, kind string, payload json.RawMessage, creada time.Time) {
		st.Add(tenantA, intakes.Intake{
			ID: id, ContactID: contactoOpaco, SessionID: "sess-a",
			Status: intakes.StatusPendingApproval, Total: 18000,
			CreatedAt: día(7), UpdatedAt: día(7),
		}, intakes.Item{SKU: "torta-choc", Label: "Torta de chocolate", Qty: 1, UnitPrice: 18000})

		if _, err := st.InsertRevision(context.Background(), intakes.Revision{
			IntakeID: id, Kind: kind, Payload: payload,
			CreatedBy: intakes.RevisionBySystem, CreatedAt: creada,
		}); err != nil {
			t.Fatalf("sembrar la revisión de %s: %v", id, err)
		}
	}

	sembrar(intakeSinLiteral, intakes.RevisionKindCart, payloadCarrito(), día(7))
	sembrar(intakeLiteralVivo, intakes.RevisionKindInterpreted, payloadConLiteral(), time.Now().Add(-time.Minute))
	sembrar(intakeLiteralPodado, intakes.RevisionKindInterpreted, payloadConLiteral(), día(7))
	return st
}

// revisiónCrudaDelDetalle devuelve el objeto JSON de LA revisión del detalle, tal
// cual viaja. Devuelve el objeto entero —y no su payload— porque lo que hay que
// mirar es una clave HERMANA de `created_at`, fuera del payload.
func revisiónCrudaDelDetalle(t *testing.T, body []byte) map[string]any {
	t.Helper()
	revs := listaDe(t, arbolJSON(t, body)["revisions"], "revisions")
	if len(revs) != 1 {
		t.Fatalf("revisions=%d, quiero 1 (el fixture siembra una sola)", len(revs))
	}
	return objetoDe(t, revs[0], "la revisión")
}

// TestIntakeDetail_LosTresCasosDelLiteralSonDistinguibles es el criterio entero en un
// test: el consumidor tiene que poder separar «hay texto», «nunca lo hubo» y «se
// podó», y las dos únicas claves con las que puede hacerlo son `payload.source_text`
// y `literal_pruned_at`.
//
// Sin la tercera fila esto no probaría nada nuevo: «hay texto» y «no hay» ya se
// distinguían antes, y lo que faltaba era partir el «no hay» en dos.
func TestIntakeDetail_LosTresCasosDelLiteralSonDistinguibles(t *testing.T) {
	deps := depsIntakesLLM(bandejaDeRetención(t), entitlements.FeatureLLMIntake)

	casos := []struct {
		nombre      string
		intakeID    string
		quiereTexto bool
		quiereSello bool
	}{
		{"literal vivo: el texto viene y no hay sello", intakeLiteralVivo, true, false},
		{"nunca hubo literal: ni texto ni sello", intakeSinLiteral, false, false},
		{"literal podado: sin texto, CON sello", intakeLiteralPodado, false, true},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			rev := revisiónCrudaDelDetalle(t, detalleDe(t, deps, c.intakeID))
			payload := objetoDe(t, rev["payload"], "el payload de la revisión")

			if _, hay := payload["source_text"]; hay != c.quiereTexto {
				t.Fatalf("source_text presente=%t, quiero %t; payload=%v", hay, c.quiereTexto, payload)
			}
			sello, hay := rev["literal_pruned_at"]
			if hay != c.quiereSello {
				t.Fatalf("literal_pruned_at presente=%t, quiero %t; revisión=%v", hay, c.quiereSello, rev)
			}
			if !c.quiereSello {
				return
			}
			// El sello no vale por estar: tiene que ser un instante legible y
			// posterior a la revisión, o el consumidor no puede contar NADA con él.
			marca, ok := sello.(string)
			if !ok {
				t.Fatalf("literal_pruned_at=%T, quiero una cadena RFC3339", sello)
			}
			at, err := time.Parse(time.RFC3339, marca)
			if err != nil {
				t.Fatalf("literal_pruned_at=%q no es RFC3339: %v", marca, err)
			}
			if !at.After(día(7)) {
				t.Fatalf("literal_pruned_at=%s no es posterior al created_at de la revisión (%s)", at, día(7))
			}
		})
	}
}

// TestIntakeDetail_SinPoda_LaClaveLiteralPrunedAtNoAparece fija la mitad barata de
// equivocarse: que la ausencia sea la CLAVE AUSENTE y no un cero publicado.
//
// Un `LiteralPrunedAt` sin `omitempty` —o formateado con Format a secas— sacaría
// `"0001-01-01T00:00:00Z"` en TODA revisión sin podar, y el consumidor que preguntara
// «¿tiene la clave?» volvería a ver podadas las 100 % de las revisiones. El caso que
// este campo viene a separar se cerraría otra vez, y en verde.
func TestIntakeDetail_SinPoda_LaClaveLiteralPrunedAtNoAparece(t *testing.T) {
	deps := depsIntakesLLM(bandejaDeRetención(t), entitlements.FeatureLLMIntake)

	for _, id := range []string{intakeSinLiteral, intakeLiteralVivo} {
		body := detalleDe(t, deps, id)
		if _, hay := revisiónCrudaDelDetalle(t, body)["literal_pruned_at"]; hay {
			t.Fatalf("%s: la revisión sin podar publica literal_pruned_at: %s", id, body)
		}
		// Y no está escondido en ninguna otra parte del cuerpo con otro nombre: el
		// cero de time.Time no puede salir al cable por ningún camino.
		if strings.Contains(string(body), "0001-01-01") {
			t.Fatalf("%s: el cuerpo publica el cero de time.Time: %s", id, body)
		}
	}
}

// TestIntakeDetail_ElSelloDePodaNoLoTapaElGateLLM es la decisión de contrato que este
// campo obligaba a tomar: `llm_intake` NO lo gatea.
//
// El gate del 044 borra claves DEL PAYLOAD, y `literal_pruned_at` vive fuera, hermano
// de `created_at`. Que pase no es un descuido de la implementación sino lo correcto:
// es un hecho de RETENCIÓN —«el texto original de tu cliente se destruyó, y este
// día»—, no una capacidad que se compre; y el literal mismo (`source_text`) tampoco
// está gateado, así que taparlo le contaría menos a un tenant sobre un texto que ya
// puede ver.
//
// 🔴 EL TEST COMPRUEBA QUE EL GATE CORRIÓ. Sin la primera mitad —`suggested_questions`
// tapada en el cuerpo sin la feature— esto pasaría igual con el gate desconectado, y
// entonces no afirmaría nada sobre el gate.
func TestIntakeDetail_ElSelloDePodaNoLoTapaElGateLLM(t *testing.T) {
	store := bandejaDeRetención(t)

	sin := revisiónCrudaDelDetalle(t, detalleDe(t, depsIntakesLLM(store), intakeLiteralPodado))
	if _, hay := objetoDe(t, sin["payload"], "el payload")["suggested_questions"]; hay {
		t.Fatal("suggested_questions viaja SIN llm_intake: el gate no corrió, así que este test no prueba nada")
	}
	selloSin, hay := sin["literal_pruned_at"]
	if !hay {
		t.Fatalf("el gate de llm_intake se llevó por delante literal_pruned_at: %v", sin)
	}

	// El mismo dato para quien sí compró el nivel: el sello es el MISMO valor en los
	// dos lados. Si algún día difiere, es que alguien lo metió en el gate.
	con := revisiónCrudaDelDetalle(t, detalleDe(t,
		depsIntakesLLM(store, entitlements.FeatureLLMIntake), intakeLiteralPodado))
	if selloCon := con["literal_pruned_at"]; selloCon != selloSin {
		t.Fatalf("literal_pruned_at difiere con y sin llm_intake: %v vs %v", selloCon, selloSin)
	}
}
