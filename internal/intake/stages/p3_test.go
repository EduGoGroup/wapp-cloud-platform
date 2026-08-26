package stages_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
)

// ---------------------------------------------------------------------------
// Banco de pruebas de P3
//
// El fake de P3 es DISTINTO del de P2 en una cosa que es justo lo que hay que probar:
// aquí se llama al modelo VARIAS veces por job, así que el doble responde POR LLAMADA
// —según la idea que le toca y según si es el primer intento o el reintento— y guarda
// las entradas Y las temperaturas. Un fake de una sola respuesta no podría distinguir
// «reintentó» de «no reintentó».
// ---------------------------------------------------------------------------

// llamadaP3 es lo que el fake anotó de UNA llamada.
type llamadaP3 struct {
	in   llm.ExtractItemSpecsInput
	temp float64
}

// respuestaP3 es lo que el fake contesta a una llamada.
type respuestaP3 struct {
	raw json.RawMessage
	err error
}

// provFakeP3 responde a ExtractItemSpecs con una función de la idea y del número de
// intento sobre ESA idea. Las cuatro etapas restantes devuelven errNoLlamarP3: si P3
// tocara P2 o P4, se vería.
type provFakeP3 struct {
	// responde recibe la Idea y cuántas veces se había llamado YA con esa idea (0
	// en el primer intento, 1 en el reintento).
	responde func(idea string, intento int) respuestaP3
	llamadas []llamadaP3
	// porIdea cuenta los intentos ya hechos por idea.
	porIdea map[string]int
}

func (p *provFakeP3) ExtractItemSpecs(_ context.Context, in llm.ExtractItemSpecsInput, opts llm.Options) (json.RawMessage, error) {
	if p.porIdea == nil {
		p.porIdea = map[string]int{}
	}
	intento := p.porIdea[in.Idea]
	p.porIdea[in.Idea]++
	p.llamadas = append(p.llamadas, llamadaP3{in: in, temp: opts.Temperature})
	r := p.responde(in.Idea, intento)
	return r.raw, r.err
}

var errNoLlamarP3 = errors.New("fake: esta etapa no la llama P3")

func (p *provFakeP3) ClassifyRequest(context.Context, llm.ClassifyRequestInput, llm.Options) (json.RawMessage, error) {
	return nil, errNoLlamarP3
}

func (p *provFakeP3) ExtractMainIdeas(context.Context, llm.ExtractMainIdeasInput, llm.Options) (json.RawMessage, error) {
	return nil, errNoLlamarP3
}

func (p *provFakeP3) NormalizeQuantities(context.Context, llm.NormalizeQuantitiesInput, llm.Options) (json.RawMessage, error) {
	return nil, errNoLlamarP3
}

func (p *provFakeP3) GenerateQuoteText(context.Context, llm.GenerateQuoteTextInput, llm.Options) (json.RawMessage, error) {
	return nil, errNoLlamarP3
}

// ideasDeAmbar son las tres ideas de P2 tal como P3 las recibe: exactamente las de
// wantsDeAmbar(), con su evidencia, porque el orden es el que indexa ItemAislado.IdeaPos.
func ideasDeAmbar() []llm.Want {
	w := wantsDeAmbar()
	out := make([]llm.Want, 0, len(w))
	for _, par := range w {
		out = append(out, llm.Want{Idea: par[0], Evidence: par[1]})
	}
	return out
}

// specDe arma la respuesta del modelo para UN ítem, con el sobre del artefacto §7.2.
func specDe(t *testing.T, producto, variante, evidencia string, addons, custom []string) json.RawMessage {
	t.Helper()
	item := map[string]any{"product": producto, "evidence": evidencia}
	if variante != "" {
		item["variant"] = variante
	}
	if len(addons) > 0 {
		item["addon_candidates"] = addons
	}
	if len(custom) > 0 {
		item["customizations"] = custom
	}
	raw, err := json.Marshal(map[string]any{
		"version": llm.ArtifactVersion,
		"items":   []any{item},
	})
	if err != nil {
		t.Fatalf("marshal del fixture: %v", err)
	}
	return raw
}

// specDeLaIdea responde a cada idea de Ambar con una spec creíble anclada a SU
// evidencia. Es la respuesta «todo va bien» del fake.
func specDeLaIdea(t *testing.T, idea string) json.RawMessage {
	t.Helper()
	switch {
	case strings.Contains(idea, "decoración infantil"):
		return specDe(t, "torta", "10 o 12 porciones", evidenciaTortaChocolate,
			[]string{"decoración infantil"}, []string{"sin lactosa"})
	case strings.Contains(idea, "vainilla"):
		return specDe(t, "torta", "25 o 30 porciones", evidenciaTortaVainilla,
			[]string{"lluvia de colores"}, nil)
	default:
		return specDe(t, "tequeños congelados", "paquete de 30", evidenciaTequenos, nil, nil)
	}
}

// etapaP3 arma la etapa con sus tres dobles y el log capturado.
func etapaP3(t *testing.T, responde func(idea string, intento int) respuestaP3, buf *bytes.Buffer) (*stages.P3, *provFakeP3, *selFake, *storeFake) {
	t.Helper()
	prov := &provFakeP3{responde: responde}
	sel := &selFake{prov: prov}
	store := &storeFake{}
	p3, err := stages.NewP3(logger.New(logger.WithWriter(buf)), sel, store)
	if err != nil {
		t.Fatalf("NewP3: %v", err)
	}
	return p3, prov, sel, store
}

// todoBien es el fake que contesta bien a la primera, siempre.
func todoBien(t *testing.T) func(string, int) respuestaP3 {
	t.Helper()
	return func(idea string, _ int) respuestaP3 {
		return respuestaP3{raw: specDeLaIdea(t, idea)}
	}
}

// leerArtefactoP3 decodifica lo que se persistió, CON el parser compartido cuando toca
// y con la forma del Cloud aquí (que es la que lleva la marca).
func leerArtefactoP3(t *testing.T, a intake.Artifact) stages.ArtefactoP3 {
	t.Helper()
	var out stages.ArtefactoP3
	if err := json.Unmarshal(a.Payload, &out); err != nil {
		t.Fatalf("el artefacto persistido no decodifica: %v", err)
	}
	return out
}

// degenerada es la salida que el modelo chico produce cuando se rompe: JSON válido con
// el relleno del esquema dentro. `llm.ParseItemSpecs` la rechaza como ErrLLMQuality.
//
// ⚠️ Se escoge ÉSTA y no un «}{» a lo bruto a propósito: una salida que ni siquiera es
// JSON la cazaría cualquier cosa, mientras que el esquema repetido es la degeneración
// REAL medida en campo (el `PlaceholderEsquema` existe por eso) y solo la caza el parser
// compartido. Un fixture más tonto probaría menos.
func degenerada(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"version": llm.ArtifactVersion,
		"items":   []any{map[string]any{"product": "...", "evidence": "..."}},
	})
	if err != nil {
		t.Fatalf("marshal del fixture: %v", err)
	}
	return raw
}

// assertUnaLlamadaPorIdea comprueba lo que hace significativo al contador: que cada
// llamada llevó SU idea (y no la misma tres veces), el hilo ENTERO como contexto —de ahí
// tiene que salir la evidencia— y la temperatura del primer intento.
func assertUnaLlamadaPorIdea(t *testing.T, llamadas []llamadaP3, ideas []llm.Want) {
	t.Helper()
	for i, ll := range llamadas {
		if ll.in.Idea != ideas[i].Idea {
			t.Fatalf("la llamada %d pidió la idea %q; se esperaba %q", i, ll.in.Idea, ideas[i].Idea)
		}
		if ll.in.SourceText != textoAmbar {
			t.Fatalf("la llamada %d no recibió el hilo entero como contexto", i)
		}
		if ll.temp != llm.TemperatureGreedy {
			t.Fatalf("la llamada %d fue a temperatura %v; el primer intento es greedy", i, ll.temp)
		}
	}
}

// assertContenidoDeLaTorta comprueba las dos exigencias de contenido de T2.3 sobre el
// primer ítem del caso: el rango TEXTUAL y los dos campos de D-044.14 separados.
func assertContenidoDeLaTorta(t *testing.T, it llm.ItemSpec) {
	t.Helper()
	// El rango se conserva TEXTUAL: partirlo es de P4 y elegir un número es de nadie.
	if it.Variant != "10 o 12 porciones" {
		t.Fatalf("variant = %q; el rango tiene que llegar textual a P4", it.Variant)
	}
	// Y el candidato NO se mezcla con la personalización, porque el matcher mira uno y
	// no el otro: un «sin lactosa» en addon_candidates acabaría siendo una línea con
	// precio.
	if len(it.AddonCandidates) != 1 || it.AddonCandidates[0] != "decoración infantil" {
		t.Fatalf("addon_candidates = %v", it.AddonCandidates)
	}
	if len(it.Customizations) != 1 || it.Customizations[0] != "sin lactosa" {
		t.Fatalf("customizations = %v", it.Customizations)
	}
}

// assertPersistidoUnaVez comprueba el sobre del artefacto: uno solo, bajo el job, en la
// etapa `p3` y con las cuentas que se devolvieron.
func assertPersistidoUnaVez(t *testing.T, store *storeFake, items, aislados int) intake.Artifact {
	t.Helper()
	if len(store.guardados) != 1 || store.jobs[0] != jobID {
		t.Fatalf("artefactos persistidos = %d bajo %v", len(store.guardados), store.jobs)
	}
	if store.guardados[0].Stage != intake.StageP3 {
		t.Fatalf("etapa persistida = %q, se esperaba %q", store.guardados[0].Stage, intake.StageP3)
	}
	got := leerArtefactoP3(t, store.guardados[0])
	if got.Version != llm.ArtifactVersion || len(got.Items) != items || len(got.Isolated) != aislados {
		t.Fatalf("lo persistido no es lo devuelto: version=%d items=%d aislados=%d",
			got.Version, len(got.Items), len(got.Isolated))
	}
	return store.guardados[0]
}

// contarReintentos cuenta las llamadas a TemperatureRetry y exige que todas sean de la
// idea que falló: reintentar una idea sana sería gastar la plaza única por gusto.
func contarReintentos(t *testing.T, llamadas []llamadaP3, esperada string) int {
	t.Helper()
	n := 0
	for _, ll := range llamadas {
		if ll.temp != llm.TemperatureRetry {
			continue
		}
		n++
		if ll.in.Idea != esperada {
			t.Fatalf("se reintentó una idea que no falló: %q", ll.in.Idea)
		}
	}
	return n
}

// assertNadaDeTextoDelCliente es ADR-0034 / INV-6 en dos sitios a la vez: ni el artefacto
// ni el log pueden llevar una frase de la conversación.
func assertNadaDeTextoDelCliente(t *testing.T, payload []byte, log, texto string) {
	t.Helper()
	if strings.Contains(string(payload), texto) {
		t.Fatal("🔴 la marca del ítem aislado copió el texto de la idea en el artefacto")
	}
	if strings.Contains(log, texto) {
		t.Fatalf("🔴 el log volcó texto de la conversación: %q", log)
	}
}

// ---------------------------------------------------------------------------
// CRITERIO 1 DEL PLAN: 3 ítems ⇒ 3 llamadas al provider
// ---------------------------------------------------------------------------

// TestP3_TresItems_TresLlamadas es el criterio literal de T2.3: el fan-out hace UNA
// llamada POR ÍTEM y no una por lote. El contador del fake es la aserción que manda —si
// el código mandara los tres ítems en una sola llamada daría 1—, y va acompañado de las
// dos que la hacen significar algo: cada llamada lleva SU idea (no la misma tres veces)
// y el hilo ENTERO como contexto (§7.2: la evidencia sale de ahí).
//
// Comprueba además lo que la tarea exige del contenido: los rangos se conservan
// TEXTUALES («10 o 12 porciones», sin colapsar) y `addon_candidates` y `customizations`
// llegan al artefacto SEPARADOS (D-044.14: P3 propone, el match decide; y una
// customization jamás se vuelve línea).
//
// 💥 MUTACIONES EJECUTADAS, las cuatro rojas:
//   - en `fanOut`, `if i > 0 { break }` al entrar al bucle —una sola llamada para todo
//     el lote— ⇒ llamadas = 1 e items = 1;
//   - en `unaLlamada`, `Idea: ""` ⇒ cae la aserción de la idea por llamada;
//   - en `unaLlamada`, `SourceText: ""` ⇒ cae la del literal;
//   - en `fanOut`, colapsar el rango (`spec.Variant = "10 porciones"`) ⇒ cae la del rango.
func TestP3_TresItems_TresLlamadas(t *testing.T) {
	var buf bytes.Buffer
	p3, prov, _, store := etapaP3(t, todoBien(t), &buf)
	ideas := ideasDeAmbar()

	art, err := p3.Run(context.Background(), jobDeAmbar(), textoAmbar, ideas)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(prov.llamadas) != 3 {
		t.Fatalf("llamadas al modelo = %d, se esperaban 3 (UNA por ítem)", len(prov.llamadas))
	}
	assertUnaLlamadaPorIdea(t, prov.llamadas, ideas)

	if len(art.Items) != 3 {
		t.Fatalf("items = %d, se esperaban 3", len(art.Items))
	}
	if len(art.Isolated) != 0 {
		t.Fatalf("se aisló algo sin motivo: %+v", art.Isolated)
	}
	assertContenidoDeLaTorta(t, art.Items[0])
	assertPersistidoUnaVez(t, store, 3, 0)
}

// ---------------------------------------------------------------------------
// CRITERIO 2 DEL PLAN: degenerada ⇒ retry 1× a 0.3 ⇒ aislar y seguir
// ---------------------------------------------------------------------------

// TestP3_SalidaDegenerada_ReintentaUnaVezYAislaElItem es la otra mitad del criterio de
// T2.3 (REQ-03/REQ-14), y lleva CUATRO aserciones que no son la misma:
//
//  1. hubo reintento: 4 llamadas para 3 ítems;
//  2. el reintento fue a `llm.TemperatureRetry` (0.3) y solo él;
//  3. el ítem envenenado quedó AISLADO CON MARCA —posición y motivo—, no borrado;
//  4. los otros DOS siguieron y el job no se tumbó (Run devuelve nil).
//
// 💥 MUTACIONES EJECUTADAS, las tres rojas:
//   - en `especificar`, BORRAR el bloque del reintento entero ⇒ cae (1) y (2). ⚠️ La
//     versión ingenua de esta mutación —colar un `return nil, MotivoCalidad, nil` justo
//     antes del reintento— NO COMPILA: `go vet` la caza como «unreachable code», así que
//     no probaría nada. Hubo que quitar el bloque;
//   - en `especificar`, reintentar a `TemperatureGreedy` ⇒ cae (2);
//   - en `fanOut`, `return fmt.Errorf(...)` en vez de aislar ⇒ caen (3) y (4).
func TestP3_SalidaDegenerada_ReintentaUnaVezYAislaElItem(t *testing.T) {
	ideas := ideasDeAmbar()
	envenenada := ideas[1].Idea

	var buf bytes.Buffer
	p3, prov, _, store := etapaP3(t, func(idea string, _ int) respuestaP3 {
		if idea == envenenada {
			return respuestaP3{raw: degenerada(t)}
		}
		return respuestaP3{raw: specDeLaIdea(t, idea)}
	}, &buf)

	art, err := p3.Run(context.Background(), jobDeAmbar(), textoAmbar, ideas)
	if err != nil {
		t.Fatalf("un ítem envenenado NO puede tumbar el job, y Run devolvió: %v", err)
	}

	// (1) 3 ítems + 1 reintento del ítem 1.
	if len(prov.llamadas) != 4 {
		t.Fatalf("llamadas = %d; se esperaban 4 (3 ítems + 1 reintento)", len(prov.llamadas))
	}
	// (2) y el reintento —y SOLO él— fue a 0.3.
	if n := contarReintentos(t, prov.llamadas, envenenada); n != 1 {
		t.Fatalf("llamadas a temperatura %v = %d; se esperaba exactamente 1", llm.TemperatureRetry, n)
	}

	// (3) el ítem quedó AISLADO con su posición y su motivo.
	if len(art.Isolated) != 1 {
		t.Fatalf("aislados = %+v; se esperaba exactamente 1", art.Isolated)
	}
	if art.Isolated[0].IdeaPos != 1 || art.Isolated[0].Reason != stages.MotivoCalidad {
		t.Fatalf("marca = %+v; se esperaba {IdeaPos:1 Reason:%q}", art.Isolated[0], stages.MotivoCalidad)
	}
	// (4) y los otros dos siguieron, y se persistieron.
	if len(art.Items) != 2 {
		t.Fatalf("items = %d; los otros dos ítems tenían que seguir", len(art.Items))
	}
	guardado := assertPersistidoUnaVez(t, store, 2, 1)

	// Y la marca NO lleva texto del cliente: el log tampoco (ADR-0034, INV-6).
	log := buf.String()
	if !strings.Contains(log, "idea_pos=1") || !strings.Contains(log, "queda aislado") {
		t.Fatalf("el aislamiento no dejó constancia con su posición: %q", log)
	}
	assertNadaDeTextoDelCliente(t, guardado.Payload, log, envenenada)
}

// TestP3_ElReintentoEsExactamenteUno_NiDosNiN cuenta las llamadas de un job de UN solo
// ítem que falla SIEMPRE. Es una aserción aparte de la anterior y no un duplicado: allí
// se comprueba que el reintento EXISTE, y aquí que se PARA. REQ-03 dice «exactamente una
// vez», y un bucle `for intentos < N` con N mal puesto pasaría el test de arriba.
//
// El coste es la razón: cada intento son 22–32 s de la plaza única del Edge, así que un
// segundo reintento por ítem convierte un pedido de 5 en minutos de cola ajena.
//
// 💥 MUTACIÓN EJECUTADA (roja): en `especificar`, un tercer intento copiando el bloque
// del reintento ⇒ llamadas = 3.
func TestP3_ElReintentoEsExactamenteUno_NiDosNiN(t *testing.T) {
	var buf bytes.Buffer
	p3, prov, _, store := etapaP3(t, func(string, int) respuestaP3 {
		return respuestaP3{raw: degenerada(t)}
	}, &buf)

	ideas := ideasDeAmbar()[:1]
	art, err := p3.Run(context.Background(), jobDeAmbar(), textoAmbar, ideas)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(prov.llamadas) != 2 {
		t.Fatalf("llamadas = %d para UN ítem; se esperaban exactamente 2 (intento + UN reintento)", len(prov.llamadas))
	}
	if prov.llamadas[0].temp != llm.TemperatureGreedy || prov.llamadas[1].temp != llm.TemperatureRetry {
		t.Fatalf("temperaturas = %v, %v; se esperaba greedy y luego %v",
			prov.llamadas[0].temp, prov.llamadas[1].temp, llm.TemperatureRetry)
	}

	// Cero ítems válidos tampoco es fatal (design §3.2): el artefacto se persiste
	// vacío, con la marca dentro, y el dueño lo ve en la bandeja.
	if len(art.Items) != 0 || len(art.Isolated) != 1 {
		t.Fatalf("artefacto = %+v; se esperaba 0 items y 1 aislado", art)
	}
	if len(store.guardados) != 1 {
		t.Fatalf("artefactos persistidos = %d; un artefacto vacío también se persiste", len(store.guardados))
	}
}

// ---------------------------------------------------------------------------
// LO QUE SE SIGUE DEL DISEÑO Y EL CRITERIO NO DICE
// ---------------------------------------------------------------------------

// TestP3_ErrorDeInfraestructura_NiSeReintentaNiSeAisla: el retry es SOLO por calidad.
//
// El criterio del plan habla de «salida degenerada», y de ahí no se sigue qué hacer con
// un timeout o un Edge sin capacidad. Se sigue del diseño (REQ-02, y el docstring de
// `llm.ErrLLMQuality`): la calidad se reintenta a 0.3 y la infraestructura NO —es
// transitoria y la reintenta el job entero más tarde, con el backoff de T2.5—.
//
// Las dos aserciones son distintas y las dos importan:
//
//  1. NO se reintenta: una sola llamada. Reintentar una caída de red dos segundos
//     después es gastar la plaza única en volver a fallar;
//  2. NO se aísla: el error SALE, con su familia intacta (`errors.Is`), y nada se
//     persiste. Aislar el ítem sería peor que fallar: dejaría al cliente sin un ítem que
//     el sistema nunca llegó a preguntar, y con marca de «el modelo no supo».
//
// 💥 MUTACIONES EJECUTADAS, las dos rojas:
//   - en `especificar`, reintentar ante CUALQUIER error (quitar el `errors.Is`) ⇒ cae (1);
//   - en `especificar`, devolver `MotivoCalidad` también para el error de infra ⇒ cae (2).
func TestP3_ErrorDeInfraestructura_NiSeReintentaNiSeAisla(t *testing.T) {
	errInfra := errors.New("edge sin capacidad")

	var buf bytes.Buffer
	p3, prov, _, store := etapaP3(t, func(string, int) respuestaP3 {
		return respuestaP3{err: errInfra}
	}, &buf)

	art, err := p3.Run(context.Background(), jobDeAmbar(), textoAmbar, ideasDeAmbar())
	if !errors.Is(err, errInfra) {
		t.Fatalf("error = %v; el fallo de infraestructura tiene que salir con su familia intacta", err)
	}
	if errors.Is(err, llm.ErrLLMQuality) {
		t.Fatalf("un fallo de infraestructura se está contando como de calidad: %v", err)
	}
	if art != nil {
		t.Fatalf("Run devolvió artefacto con un fallo de infraestructura: %+v", art)
	}
	if len(prov.llamadas) != 1 {
		t.Fatalf("llamadas = %d; un fallo de infraestructura NO se reintenta aquí", len(prov.llamadas))
	}
	if len(store.guardados) != 0 {
		t.Fatalf("se persistió un artefacto pese al fallo de infraestructura: %d", len(store.guardados))
	}
}

// TestP3_UnItemAisladoNoImpideQueSePersistaElRestoConDosMotivosDistintos junta en un
// mismo job los DOS motivos de aislamiento —la salida ilegible y la evidencia
// inventada— para afirmar lo que el criterio pide de refilón: que el artefacto de los
// demás LLEGA A LA BASE.
//
// El anclaje no lo pide el enunciado de T2.3 y por eso se dice aquí: es decisión de la
// tarea, con la MISMA regla que P2 (`internal/evidence`) y una respuesta distinta —P2
// descarta la idea, P3 aísla el ítem—, porque P2 ya demostró que el cliente pidió esto y
// hacerlo desaparecer sería perder una petición real.
//
// 💥 MUTACIONES EJECUTADAS, las dos rojas:
//   - en `fanOut`, aceptar la evidencia sin comprobarla (`if false &&`) ⇒ el ítem
//     inventado entra en `items` y el aislado por evidencia desaparece;
//   - en `fanOut`, `return fmt.Errorf(...)` en cuanto hay un aislado ⇒ no se persiste
//     nada y caen las dos últimas aserciones.
func TestP3_UnItemAisladoNoImpideQueSePersistaElRestoConDosMotivosDistintos(t *testing.T) {
	ideas := ideasDeAmbar()
	porCalidad, porEvidencia := ideas[0].Idea, ideas[1].Idea

	var buf bytes.Buffer
	p3, _, _, store := etapaP3(t, func(idea string, _ int) respuestaP3 {
		switch idea {
		case porCalidad:
			return respuestaP3{raw: degenerada(t)}
		case porEvidencia:
			// Bien formada, y con una frase que NO está en el literal.
			return respuestaP3{raw: specDe(t, "bandeja de pasapalos", "", evidenciaInventada, nil, nil)}
		default:
			return respuestaP3{raw: specDeLaIdea(t, idea)}
		}
	}, &buf)

	art, err := p3.Run(context.Background(), jobDeAmbar(), textoAmbar, ideas)
	if err != nil {
		t.Fatalf("dos ítems aislados NO pueden tumbar el job, y Run devolvió: %v", err)
	}

	if len(art.Items) != 1 || art.Items[0].Product != "tequeños congelados" {
		t.Fatalf("items = %+v; solo el tercero tenía que sobrevivir", art.Items)
	}
	motivos := map[int]string{}
	for _, a := range art.Isolated {
		motivos[a.IdeaPos] = a.Reason
	}
	if motivos[0] != stages.MotivoCalidad || motivos[1] != stages.MotivoEvidencia {
		t.Fatalf("marcas = %+v; se esperaba {0:%s, 1:%s}", art.Isolated, stages.MotivoCalidad, stages.MotivoEvidencia)
	}

	// Y lo de los demás llegó a la base, que es lo que el criterio pide.
	if len(store.guardados) != 1 {
		t.Fatalf("artefactos persistidos = %d", len(store.guardados))
	}
	guardado := leerArtefactoP3(t, store.guardados[0])
	if len(guardado.Items) != 1 || len(guardado.Isolated) != 2 {
		t.Fatalf("lo persistido no coincide con lo devuelto: %+v", guardado)
	}
	if strings.Contains(string(store.guardados[0].Payload), "bandeja de pasapalos") {
		t.Fatal("la spec sin respaldo en el literal se persistió igual: se guarda la salida cruda del modelo")
	}
}

// TestP3_VariasEspecificacionesEnUnaLlamada_SoloEntraLaPrimera cubre la degeneración que
// el criterio no nombra y que es la más cara de todas: el modelo tiene el HILO ENTERO en
// el prompt, así que puede ignorar el «especifica UN SOLO ítem» y devolver los tres. En
// N llamadas eso son N² specs y el mismo producto cobrado N veces.
//
// Se conserva la PRIMERA. El lado seguro de romper la 1:1 entre idea y spec es perder
// una repetición, nunca duplicar una línea con precio.
//
// 💥 MUTACIÓN EJECUTADA (roja): en `unaLlamada`, guardar `specs.Items[1:]` en un campo
// nuevo de P3 y volcarlo en `fanOut` detrás de la primera ⇒ items = 2 para una sola idea.
func TestP3_VariasEspecificacionesEnUnaLlamada_SoloEntraLaPrimera(t *testing.T) {
	var buf bytes.Buffer
	dos := json.RawMessage(fmt.Sprintf(
		`{"version":%d,"items":[{"product":"torta","evidence":%q},{"product":"tequeños","evidence":%q}]}`,
		llm.ArtifactVersion, evidenciaTortaChocolate, evidenciaTequenos))
	p3, prov, _, _ := etapaP3(t, func(string, int) respuestaP3 {
		return respuestaP3{raw: dos}
	}, &buf)

	art, err := p3.Run(context.Background(), jobDeAmbar(), textoAmbar, ideasDeAmbar()[:1])
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(art.Items) != 1 || art.Items[0].Product != "torta" {
		t.Fatalf("items = %+v; se esperaba solo el primero", art.Items)
	}
	if len(prov.llamadas) != 1 {
		t.Fatalf("llamadas = %d; una spec de más no es un fallo de calidad y no se reintenta", len(prov.llamadas))
	}
	if !strings.Contains(buf.String(), "descartadas=1") {
		t.Fatalf("descartar una spec de más no dejó constancia: %q", buf.String())
	}
}

// TestP3_CeroEspecificaciones_EsFalloDeCalidad: se pidió UN ítem y el modelo devolvió un
// artefacto bien formado con la lista vacía. `llm.ParseItemSpecs` lo acepta —su bucle
// recorre cero elementos—, así que sin esta comprobación el ítem desaparecería sin marca
// y sin reintento: un pedido del cliente perdido en silencio.
//
// Es decisión de esta tarea, y es la conservadora: se trata como calidad ⇒ reintento ⇒
// aislamiento con marca.
//
// 💥 MUTACIÓN EJECUTADA (roja): quitar el `if len(specs.Items) == 0` de `unaLlamada` ⇒
// panic por índice fuera de rango... que NO vale como rojo del test (prueba otra cosa),
// así que la mutación buena es devolver `&llm.ItemSpec{}` cuando la lista viene vacía ⇒
// llamadas = 1, aislados = 0 y un ítem fantasma en el artefacto.
func TestP3_CeroEspecificaciones_EsFalloDeCalidad(t *testing.T) {
	var buf bytes.Buffer
	vacio := json.RawMessage(fmt.Sprintf(`{"version":%d,"items":[]}`, llm.ArtifactVersion))
	p3, prov, _, _ := etapaP3(t, func(string, int) respuestaP3 {
		return respuestaP3{raw: vacio}
	}, &buf)

	art, err := p3.Run(context.Background(), jobDeAmbar(), textoAmbar, ideasDeAmbar()[:1])
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(prov.llamadas) != 2 {
		t.Fatalf("llamadas = %d; cero specs es un fallo de calidad y se reintenta UNA vez", len(prov.llamadas))
	}
	if len(art.Items) != 0 || len(art.Isolated) != 1 || art.Isolated[0].Reason != stages.MotivoCalidad {
		t.Fatalf("artefacto = %+v; se esperaba el ítem aislado por calidad", art)
	}
}

// TestP3_ElArtefactoPersistidoLoSigueLeyendoElParserCompartido custodia el precio de
// haber extendido el contrato de §7.2 con la clave `isolated`: que el lector compartido
// —que es el que usará T2.4— siga leyendo el artefacto sin enterarse.
//
// No es una obviedad: si `llm.ParseItemSpecs` usara `DisallowUnknownFields`, la clave de
// más convertiría TODOS los artefactos de P3 en errores de calidad. Hoy no lo usa (su
// `decodeArtifact` se declara «tolerante a campos futuros»), y este test es lo que hace
// que esa dependencia se vea el día que cambie, en vez de leerla una vez y confiar.
//
// 💥 MUTACIÓN EJECUTADA (roja): cambiar el tag json de `ArtefactoP3.Items` a
// `"item_specs"` ⇒ el parser compartido devuelve 0 ítems.
func TestP3_ElArtefactoPersistidoLoSigueLeyendoElParserCompartido(t *testing.T) {
	ideas := ideasDeAmbar()
	var buf bytes.Buffer
	p3, _, _, store := etapaP3(t, func(idea string, _ int) respuestaP3 {
		if idea == ideas[1].Idea {
			return respuestaP3{raw: degenerada(t)}
		}
		return respuestaP3{raw: specDeLaIdea(t, idea)}
	}, &buf)

	if _, err := p3.Run(context.Background(), jobDeAmbar(), textoAmbar, ideas); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(store.guardados) != 1 {
		t.Fatalf("artefactos persistidos = %d", len(store.guardados))
	}

	leido, err := llm.ParseItemSpecs(store.guardados[0].Payload)
	if err != nil {
		t.Fatalf("el parser compartido no lee el artefacto de P3: %v", err)
	}
	if leido.Version != llm.ArtifactVersion || len(leido.Items) != 2 {
		t.Fatalf("el parser compartido leyó version=%d items=%d", leido.Version, len(leido.Items))
	}
}

// TestP3_SinIdeas_NoSeLlamaAlModeloYSePersistaVacio: un P2 que se quedó sin ideas vivas
// no es un fallo (design §3.2, «cero resultados válidos tampoco es fatal»). P3 no gasta
// la plaza única en un prompt donde lo único concreto sería lo que listamos nosotros
// (D-044.24) y deja el artefacto vacío escrito, que es lo que hace que la reanudación no
// repita la etapa.
//
// 💥 MUTACIÓN EJECUTADA (roja): quitar el `if len(ideas) > 0` de Run **no basta** —el
// bucle sobre cero elementos tampoco llama al modelo—, así que la mutación que sí prueba
// es entrar al fan-out con `append(ideas, llm.Want{})` ⇒ llamadas = 1 y `sel.pide` = 1.
func TestP3_SinIdeas_NoSeLlamaAlModeloYSePersistaVacio(t *testing.T) {
	var buf bytes.Buffer
	p3, prov, sel, store := etapaP3(t, todoBien(t), &buf)

	art, err := p3.Run(context.Background(), jobDeAmbar(), textoAmbar, nil)
	if err != nil {
		t.Fatalf("cero ideas NO es un fallo, y Run devolvió: %v", err)
	}
	if len(prov.llamadas) != 0 {
		t.Fatalf("se llamó al modelo %d veces sin una sola idea que especificar", len(prov.llamadas))
	}
	if len(sel.pide) != 0 {
		t.Fatalf("se pidió el provider %d veces sin nada que preguntarle", len(sel.pide))
	}
	if len(art.Items) != 0 || art.Version != llm.ArtifactVersion {
		t.Fatalf("artefacto = %+v", art)
	}
	if len(store.guardados) != 1 {
		t.Fatalf("artefactos persistidos = %d; el artefacto vacío TAMBIÉN se persiste", len(store.guardados))
	}
	// Y la clave `isolated` no aparece cuando no hay nada aislado: `omitempty`.
	if strings.Contains(string(store.guardados[0].Payload), "isolated") {
		t.Fatalf("el artefacto vacío trae la clave de las marcas: %s", store.guardados[0].Payload)
	}
}

// TestP3_SinLiteral_NoSeLlamaAlModelo: la misma guarda que P2, por el mismo motivo, y
// con más razón todavía —P3 gastaría N plazas, no una—.
//
// 💥 MUTACIÓN EJECUTADA (roja): quitar la guarda de `literal == ""` ⇒ el modelo recibe
// tres llamadas.
func TestP3_SinLiteral_NoSeLlamaAlModelo(t *testing.T) {
	var buf bytes.Buffer
	p3, prov, _, store := etapaP3(t, todoBien(t), &buf)

	_, err := p3.Run(context.Background(), jobDeAmbar(), "", ideasDeAmbar())
	if !errors.Is(err, stages.ErrSinLiteral) {
		t.Fatalf("error = %v; se esperaba ErrSinLiteral", err)
	}
	if len(prov.llamadas) != 0 {
		t.Fatalf("se llamó al modelo %d veces con un job sin literal", len(prov.llamadas))
	}
	if len(store.guardados) != 0 {
		t.Fatal("se persistió un artefacto para un job sin literal")
	}
}

// TestP3_LaViaSePideUnaSolaVezParaTodoElFanOut: el provider es del tenant y de la sesión
// de origen, no de la idea. Pedirlo dentro del bucle serían N lecturas de la
// configuración del tenant para obtener N veces lo mismo — y N oportunidades de que la
// vía cambie a mitad de un pedido, que es peor que la lectura de más.
//
// La sesión de origen es la que enruta la inferencia al Edge que recibió el mensaje, y
// ya se perdió una vez (T1.7-8), así que aquí se afirma igual que en P2.
//
// 💥 MUTACIÓN EJECUTADA (roja): mover el `sel.For` dentro del bucle de `fanOut` ⇒
// `sel.pide` pasa a tener 3 entradas.
func TestP3_LaViaSePideUnaSolaVezParaTodoElFanOut(t *testing.T) {
	var buf bytes.Buffer
	p3, _, sel, _ := etapaP3(t, todoBien(t), &buf)

	if _, err := p3.Run(context.Background(), jobDeAmbar(), textoAmbar, ideasDeAmbar()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(sel.pide) != 1 || sel.pide[0] != tenant+"/"+sesion {
		t.Fatalf("el provider se pidió como %v; se esperaba una sola vez, [%q]", sel.pide, tenant+"/"+sesion)
	}
}

// TestP3_JobQueDejoDeEstarEnProcessing: las dos formas de «no se guardó» siguen siendo
// distinguibles desde P3, porque el worker de T2.5 responde distinto a cada una.
//
// 💥 MUTACIÓN EJECUTADA (roja): en `persistir`, ignorar el bool de SaveStage ⇒ Run
// devuelve nil.
func TestP3_JobQueDejoDeEstarEnProcessing(t *testing.T) {
	var buf bytes.Buffer
	prov := &provFakeP3{responde: todoBien(t)}
	store := &storeFake{perdido: true}
	p3, err := stages.NewP3(logger.New(logger.WithWriter(&buf)), &selFake{prov: prov}, store)
	if err != nil {
		t.Fatalf("NewP3: %v", err)
	}
	if _, err := p3.Run(context.Background(), jobDeAmbar(), textoAmbar, ideasDeAmbar()); !errors.Is(err, stages.ErrJobFueraDeProcessing) {
		t.Fatalf("error = %v; se esperaba ErrJobFueraDeProcessing", err)
	}
}

// TestNewP3_SinCablear: una etapa a medio construir no nace.
func TestNewP3_SinCablear(t *testing.T) {
	log := logger.New(logger.WithWriter(&bytes.Buffer{}))
	casos := map[string]struct {
		log   logger.Logger
		sel   stages.ProviderSelector
		store stages.StageStore
	}{
		"sin log":   {nil, &selFake{}, &storeFake{}},
		"sin vía":   {log, nil, &storeFake{}},
		"sin store": {log, &selFake{}, nil},
	}
	for nombre, c := range casos {
		t.Run(nombre, func(t *testing.T) {
			if _, err := stages.NewP3(c.log, c.sel, c.store); !errors.Is(err, stages.ErrSinCablear) {
				t.Fatalf("error = %v; se esperaba ErrSinCablear", err)
			}
		})
	}
}
