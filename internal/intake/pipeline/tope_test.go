package pipeline

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
)

// ════════════════════════════════════════════════════════════════════════════
// EL TOPE DE ÍTEMS, VISTO DESDE EL WORKER (Plan 044 · T2.6)
//
// El banco de `stages` prueba que P3 llama 10 veces y marca el resto. Aquí se prueba la
// otra mitad del criterio, que desde `stages` NO SE PUEDE VER: **el job llega a `done`,
// no a `failed`**. Es una afirmación con contenido —la implementación obvia y
// equivocada es rechazar el pedido que no cabe, y ésa daría `failed`— y solo la máquina
// de estados la puede responder.
//
// Por eso este test cablea la **P3 REAL** (no `p3Falsa`) dentro del worker, con el
// mismo precedente que TestElWorkerPasaSuPlazoPorLlamadaALasEtapas: un doble de P3 no
// tiene tope que ejercitar.
// ════════════════════════════════════════════════════════════════════════════

// literalDeDoce es el texto en claro que el descifrador del banco devuelve, con una
// línea por ítem para que el anclaje de P3 acepte las doce evidencias.
func literalDeDoce() string {
	var b strings.Builder
	b.WriteString("### MENSAJES DE LA CONVERSACIÓN (literal, en orden) ###\n")
	for i := range 12 {
		b.WriteString("cliente: quiero la torta " + strconv.Itoa(i) + " de chocolate\n")
	}
	b.WriteString("### FIN DE LOS MENSAJES ###")
	return b.String()
}

// cifraDeDoce abre el sobre a un pedido de doce ítems.
type cifraDeDoce struct{}

func (cifraDeDoce) Decrypt(_, _ []byte, _ string) (string, error) { return literalDeDoce(), nil }

// provDeDoce contesta bien a cualquier ítem y CUENTA las llamadas. Es el contador del
// criterio: si el tope no se aplicara, aquí se verían doce.
type provDeDoce struct {
	llm.LLMProvider
	llamadas int
}

func (p *provDeDoce) ExtractItemSpecs(_ context.Context, in llm.ExtractItemSpecsInput,
	_ llm.Options) (json.RawMessage, error) {
	p.llamadas++
	// La evidencia es la línea del literal que corresponde a ESTA idea, así que el
	// anclaje la acepta y ningún ítem se aísla por un motivo que no sea el tope.
	return json.Marshal(map[string]any{
		"version": llm.ArtifactVersion,
		"items": []any{map[string]any{
			"product": "torta", "evidence": "quiero la " + in.Idea + " de chocolate",
		}},
	})
}

// TestWorker_PedidoDeDoceItems_LLEGA_A_DONE_ConLosDosSobrantesMarcados es el resto del
// criterio de T2.6: el pedido que no cabe se atiende hasta el tope y TERMINA BIEN.
//
// # QUÉ TENDRÍA QUE PASAR PARA QUE FALLARA
//
// Que superar el tope se tratara como un error del job — que es la implementación que
// sale sola si uno lee «tope» y piensa «rechazar». El job acabaría en `failed`, el
// cliente se quedaría sin presupuesto, y el criterio de esta tarea dice literalmente lo
// contrario. También falla si los sobrantes se descartan (10 líneas en vez de 12) o si
// se gastan las 12 llamadas.
//
// 💥 MUTACIONES EJECUTADAS, las dos rojas (las dos COMPILAN):
//   - en `stages.Run`, devolver un error cuando `sobrantes > 0` ⇒ el job acaba en
//     `failed` con la causa escrita;
//   - en `stages.acotarAlTope`, `return ideas, 0` (sin tope) ⇒ 12 llamadas.
func TestWorker_PedidoDeDoceItems_LLEGA_A_DONE_ConLosDosSobrantesMarcados(t *testing.T) {
	const ideasDelPedido = 12
	const llamadasEsperadas = 10

	rel := nuevoReloj()
	store := NuevoStoreEnMemoria(rel.ahora)
	log := &captor{}
	prov := &provDeDoce{}

	p3, err := stages.NewP3(log, selectorFijo{prov: prov}, store)
	if err != nil {
		t.Fatalf("construir P3: %v", err)
	}
	wants := make([]llm.Want, 0, ideasDelPedido)
	for i := range ideasDelPedido {
		wants = append(wants, llm.Want{
			Idea:     "torta " + strconv.Itoa(i),
			Evidence: "quiero la torta " + strconv.Itoa(i) + " de chocolate",
		})
	}
	p2 := &p2Falsa{etapaBase: etapaBase{rel: rel}, store: store, wants: wants}
	p4 := &p4Falsa{etapaBase: etapaBase{rel: rel}, store: store}

	w, err := NewWorker(log, store, p2, p3, p4, cifraDeDoce{}, Config{})
	if err != nil {
		t.Fatalf("cablear el worker: %v", err)
	}
	w.ahora = rel.ahora

	id := store.Sembrar(Fila{
		Key:        intake.WindowKey{TenantID: "tenant-1", SessionID: "sess-1", ContactID: "c-1"},
		SourceText: intake.SourceText{Enc: []byte("cifrado"), DEK: []byte("dek"), KEKID: "kek-1"},
		MessageTS:  rel.ahora(), CreatedAt: rel.ahora(),
	})

	if hubo, cerr := w.UnaVuelta(context.Background()); cerr != nil || !hubo {
		t.Fatalf("UnaVuelta: hubo=%v err=%v", hubo, cerr)
	}

	fila, ok := store.Ver(id)
	if !ok {
		t.Fatalf("la fila %s desapareció", id)
	}
	if fila.Status != intake.StatusDone {
		t.Fatalf("el job acabó en %q (error: %q); un pedido que supera el tope se atiende hasta el tope "+
			"y TERMINA BIEN: superar el tope no es un fallo", fila.Status, fila.Error)
	}
	if prov.llamadas != llamadasEsperadas {
		t.Fatalf("llamadas de P3 = %d para %d ítems; se esperaban %d",
			prov.llamadas, ideasDelPedido, llamadasEsperadas)
	}

	var art stages.ArtefactoP3
	if uerr := json.Unmarshal(fila.Artifacts[intake.StageP3], &art); uerr != nil {
		t.Fatalf("el artefacto de P3 no decodifica: %v", uerr)
	}
	if lineas := len(art.Items) + len(art.Isolated); lineas != ideasDelPedido {
		t.Fatalf("líneas del borrador = %d (%d items + %d aislados); el pedido del cliente entró con %d",
			lineas, len(art.Items), len(art.Isolated), ideasDelPedido)
	}
	for _, it := range art.Isolated {
		if it.Reason != "over_limit" {
			t.Fatalf("un ítem quedó aislado por %q y no por el tope: %+v", it.Reason, art.Isolated)
		}
	}
}
