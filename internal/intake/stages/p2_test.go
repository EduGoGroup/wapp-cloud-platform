package stages_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
)

// ---------------------------------------------------------------------------
// Banco de pruebas
// ---------------------------------------------------------------------------

const (
	jobID   = "11111111-1111-1111-1111-111111111111"
	tenant  = "t-p2"
	sesion  = "s-p2"
	cliente = "c-p2"
	evento  = "e-p2"
)

// errNoLlamar es lo que devuelven las cuatro etapas que P2 no debe tocar. Un error
// explícito hace ruido el día que alguien llame a P3 desde aquí; un stub que devolviera
// `nil, nil` dejaría pasar ese error en silencio.
var errNoLlamar = errors.New("fake: esta etapa no la llama P2")

// provFake es un llm.LLMProvider de mentira. Solo ExtractMainIdeas hace algo.
type provFake struct {
	respuesta json.RawMessage
	err       error
	// entradas guarda lo que recibió cada llamada: es como se comprueba que el
	// literal llega ENTERO al prompt y cuántas veces se llamó al modelo.
	entradas []llm.ExtractMainIdeasInput
}

func (p *provFake) ExtractMainIdeas(_ context.Context, in llm.ExtractMainIdeasInput, _ llm.Options) (json.RawMessage, error) {
	p.entradas = append(p.entradas, in)
	return p.respuesta, p.err
}

func (p *provFake) ClassifyRequest(context.Context, llm.ClassifyRequestInput, llm.Options) (json.RawMessage, error) {
	return nil, errNoLlamar
}

func (p *provFake) ExtractItemSpecs(context.Context, llm.ExtractItemSpecsInput, llm.Options) (json.RawMessage, error) {
	return nil, errNoLlamar
}

func (p *provFake) NormalizeQuantities(context.Context, llm.NormalizeQuantitiesInput, llm.Options) (json.RawMessage, error) {
	return nil, errNoLlamar
}

func (p *provFake) GenerateQuoteText(context.Context, llm.GenerateQuoteTextInput, llm.Options) (json.RawMessage, error) {
	return nil, errNoLlamar
}

// selFake es el selector de vía. Anota con qué tenant y con qué sesión de origen se le
// pidió el provider: ese segundo dato es el que enruta la inferencia al Edge que
// recibió el mensaje, y ya se perdió una vez (T1.7-8).
type selFake struct {
	prov llm.LLMProvider
	err  error
	pide []string
}

func (s *selFake) For(_ context.Context, tenantID, originSessionID string) (llm.LLMProvider, error) {
	s.pide = append(s.pide, tenantID+"/"+originSessionID)
	if s.err != nil {
		return nil, s.err
	}
	return s.prov, nil
}

// storeFake es el doble de la máquina de estados. Valida con la MISMA puerta que
// Postgres —intake.Artifact.Validate, llamada ANTES de «escribir»— porque si el doble
// validara con su propia regla, el test de «un artefacto sin version no se persiste»
// probaría al doble y no al sistema.
type storeFake struct {
	guardados []intake.Artifact
	jobs      []string
	// perdido hace que SaveStage devuelva (false, nil): el job dejó de estar en
	// `processing` mientras corría la etapa.
	perdido bool
	err     error
}

func (s *storeFake) SaveStage(_ context.Context, jobID string, a intake.Artifact) (bool, error) {
	if err := a.Validate(); err != nil {
		return false, err
	}
	if s.err != nil {
		return false, s.err
	}
	s.jobs = append(s.jobs, jobID)
	s.guardados = append(s.guardados, a)
	return !s.perdido, nil
}

func jobDeAmbar() intake.ClaimedJob {
	return intake.ClaimedJob{
		ID:  jobID,
		Key: intake.WindowKey{TenantID: tenant, SessionID: sesion, ContactID: cliente, EventID: evento},
	}
}

// artefactoP2 arma la salida del modelo. `wants` son pares idea/evidencia y `hint`
// puede ir vacío para omitir la pista.
func artefactoP2(t *testing.T, version int, wants [][2]string, hint [2]string) json.RawMessage {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, `{"version":%d,"wants":[`, version)
	for i, w := range wants {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"idea":%q,"evidence":%q}`, w[0], w[1])
	}
	b.WriteString("]")
	if hint[0] != "" {
		fmt.Fprintf(&b, `,"delivery_hint":{"text":%q,"evidence":%q}`, hint[0], hint[1])
	}
	b.WriteString("}")
	return json.RawMessage(b.String())
}

// artefactoSinVersion arma la MISMA salida pero sin el campo `version`. Se escribe a
// mano en vez de con artefactoP2 porque el campo hay que quitarlo, no ponerlo a cero:
// un `"version":0` prueba otra cosa (una versión inválida, no una ausente).
func artefactoSinVersion(t *testing.T, wants [][2]string) json.RawMessage {
	t.Helper()
	raw := artefactoP2(t, llm.ArtifactVersion, wants, [2]string{})
	sin := strings.Replace(string(raw), fmt.Sprintf(`{"version":%d,`, llm.ArtifactVersion), `{`, 1)
	if sin == string(raw) {
		t.Fatalf("el fixture no perdió el campo version: %q", sin)
	}
	return json.RawMessage(sin)
}

// wantsDeAmbar son las tres ideas del caso, tal como design §7.1 las escribe.
func wantsDeAmbar() [][2]string {
	return [][2]string{
		{"torta con decoración infantil, chocolate húmedo, 10 o 12 porciones", evidenciaTortaChocolate},
		{"torta de vainilla con lluvia de colores, dulce de leche y merengue, 25 o 30 porciones", evidenciaTortaVainilla},
		{"paquete de tequeños congelados de 30", evidenciaTequenos},
	}
}

func hintDeAmbar() [2]string {
	return [2]string{"el miércoles de la semana que viene", evidenciaEntrega}
}

// etapa arma la etapa con sus tres dobles y el log capturado.
func etapa(t *testing.T, resp json.RawMessage, buf *bytes.Buffer) (*stages.P2, *provFake, *storeFake) {
	t.Helper()
	prov := &provFake{respuesta: resp}
	sel := &selFake{prov: prov}
	store := &storeFake{}
	p2, err := stages.NewP2(logger.New(logger.WithWriter(buf)), sel, store)
	if err != nil {
		t.Fatalf("NewP2: %v", err)
	}
	return p2, prov, store
}

// leerArtefacto decodifica lo que se persistió.
func leerArtefacto(t *testing.T, a intake.Artifact) llm.MainIdeas {
	t.Helper()
	var out llm.MainIdeas
	if err := json.Unmarshal(a.Payload, &out); err != nil {
		t.Fatalf("el artefacto persistido no decodifica: %v", err)
	}
	return out
}

// ---------------------------------------------------------------------------
// EL CRITERIO DEL PLAN: 3 wants + delivery_hint, artefacto persistido
// ---------------------------------------------------------------------------

// TestP2_TextoDeAmbar_TresIdeasYPistaDeEntrega es el criterio literal de T2.2 con el
// fixture del caso (⚠️ calidad C, ver ambar_fixture_test.go): las tres ideas se
// sostienen sobre el literal, la pista también, y el artefacto queda persistido con
// `version` 1 bajo la etapa `p2`.
//
// 💥 MUTACIONES que lo ponen rojo, las dos ejecutadas:
//   - pasarle al modelo `SourceText: ""` en vez del literal ⇒ cae la aserción del prompt;
//   - anclar con un `strings.Contains(literal, evidence)` CRUDO, sin normalizar ⇒ caen a 2
//     las ideas vivas, porque `evidenciaTortaChocolate` empieza en minúscula donde el
//     fixture dice «Una torta». Esa es exactamente la razón de que el fixture esté escrito
//     así.
func TestP2_TextoDeAmbar_TresIdeasYPistaDeEntrega(t *testing.T) {
	var buf bytes.Buffer
	p2, prov, store := etapa(t, artefactoP2(t, llm.ArtifactVersion, wantsDeAmbar(), hintDeAmbar()), &buf)

	ideas, err := p2.Run(context.Background(), jobDeAmbar(), textoAmbar)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(ideas.Wants) != 3 {
		t.Fatalf("ideas devueltas = %d, se esperaban 3", len(ideas.Wants))
	}
	if ideas.DeliveryHint == nil {
		t.Fatal("la pista de entrega se cayó y su evidencia SÍ está en el literal")
	}
	if ideas.DeliveryHint.Text != "el miércoles de la semana que viene" {
		t.Fatalf("pista de entrega = %q", ideas.DeliveryHint.Text)
	}

	// El literal llega ENTERO al prompt: es lo único de lo que el modelo puede copiar.
	if len(prov.entradas) != 1 {
		t.Fatalf("llamadas al modelo = %d, se esperaba 1 (el reintento es de T2.5)", len(prov.entradas))
	}
	if prov.entradas[0].SourceText != textoAmbar {
		t.Fatalf("el prompt no recibió el literal del job: %q", prov.entradas[0].SourceText)
	}

	// Y quedó persistido, con la forma que la máquina de estados exige.
	if len(store.guardados) != 1 {
		t.Fatalf("artefactos persistidos = %d, se esperaba 1", len(store.guardados))
	}
	if store.jobs[0] != jobID {
		t.Fatalf("el artefacto se guardó bajo el job %q", store.jobs[0])
	}
	if store.guardados[0].Stage != intake.StageP2 {
		t.Fatalf("etapa persistida = %q, se esperaba %q", store.guardados[0].Stage, intake.StageP2)
	}
	guardado := leerArtefacto(t, store.guardados[0])
	if guardado.Version != llm.ArtifactVersion {
		t.Fatalf("version del artefacto = %d, se esperaba %d", guardado.Version, llm.ArtifactVersion)
	}
	if len(guardado.Wants) != 3 || guardado.DeliveryHint == nil {
		t.Fatalf("lo persistido no es lo devuelto: %d wants, hint=%v", len(guardado.Wants), guardado.DeliveryHint != nil)
	}
}

// TestP2_EvidenciaInventada_SeDescartaLaIdeaYElJobSigueVivo es la otra mitad del
// criterio, y lleva DOS aserciones que no son la misma:
//
//  1. la idea inventada NO está en el artefacto —ni en lo devuelto ni en lo persistido—;
//  2. el job SIGUE VIVO: Run devuelve nil y el artefacto se guarda igual. Ésta es la
//     forma que «no tumba el job» tiene EN ESTA ETAPA: P2 no puede fallar un job
//     —StageStore no tiene `Fail`, ver TestStageStore_NoPuedeTumbarUnJob— así que lo
//     único que podría tumbarlo es devolver error, y el worker de T2.5 fallaría el job
//     al verlo.
//
// 💥 MUTACIONES que lo ponen rojo, las dos ejecutadas:
//   - en `anclar`, aceptar la evidencia sin comprobarla (`if true {`) ⇒ cae (1);
//   - en `Run`, `if descartadas > 0 { return nil, fmt.Errorf(...) }` ⇒ cae (2).
func TestP2_EvidenciaInventada_SeDescartaLaIdeaYElJobSigueVivo(t *testing.T) {
	wants := wantsDeAmbar()
	const ideaInventada = "dos bandejas de pasapalos surtidos"
	wants[1] = [2]string{ideaInventada, evidenciaInventada}

	var buf bytes.Buffer
	p2, _, store := etapa(t, artefactoP2(t, llm.ArtifactVersion, wants, hintDeAmbar()), &buf)

	ideas, err := p2.Run(context.Background(), jobDeAmbar(), textoAmbar)
	if err != nil {
		t.Fatalf("descartar una idea NO puede tumbar el job, y Run devolvió: %v", err)
	}
	if len(ideas.Wants) != 2 {
		t.Fatalf("ideas vivas = %d, se esperaban 2 (la inventada se descarta)", len(ideas.Wants))
	}
	for _, w := range ideas.Wants {
		if w.Idea == ideaInventada {
			t.Fatal("la idea sin respaldo en el literal sobrevivió al anclaje")
		}
	}
	if len(store.guardados) != 1 {
		t.Fatalf("artefactos persistidos = %d: el job tenía que seguir su curso", len(store.guardados))
	}
	if strings.Contains(string(store.guardados[0].Payload), ideaInventada) {
		t.Fatal("la idea descartada se persistió igual: se está guardando la salida cruda del modelo")
	}
	if len(leerArtefacto(t, store.guardados[0]).Wants) != 2 {
		t.Fatal("lo persistido no coincide con lo devuelto")
	}

	// Y quedó DICHO en el log, con la posición y sin una palabra del cliente.
	log := buf.String()
	if !strings.Contains(log, "la idea se descarta") {
		t.Fatalf("el descarte no dejó log: %q", log)
	}
	if !strings.Contains(log, "idea_pos=1") {
		t.Fatalf("el log no dice CUÁL idea se descartó: %q", log)
	}
	if strings.Contains(log, evidenciaInventada) || strings.Contains(log, ideaInventada) {
		t.Fatalf("🔴 el log volcó texto de la conversación (ADR-0034/INV-6): %q", log)
	}
}

// TestP2_PistaDeEntregaInventada_SeCaeSoloLaPista extiende el anclaje al segundo campo
// con `evidence` del contrato §7.1. No lo pide el enunciado de T2.2 —habla de las
// «ideas»— y por eso se dice aquí: es una decisión de la tarea. Una fecha inventada es
// peor que ninguna, porque P4 la convertiría en una fecha absoluta con toda la
// apariencia de ser cierta.
//
// 💥 MUTACIÓN: en `anclar`, borrar el bloque del DeliveryHint ⇒ la pista sobrevive y el
// test cae.
func TestP2_PistaDeEntregaInventada_SeCaeSoloLaPista(t *testing.T) {
	var buf bytes.Buffer
	hint := [2]string{"el sábado", "lo necesito para el sábado por la mañana"}
	p2, _, store := etapa(t, artefactoP2(t, llm.ArtifactVersion, wantsDeAmbar(), hint), &buf)

	ideas, err := p2.Run(context.Background(), jobDeAmbar(), textoAmbar)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ideas.DeliveryHint != nil {
		t.Fatalf("la pista inventada sobrevivió: %+v", *ideas.DeliveryHint)
	}
	if len(ideas.Wants) != 3 {
		t.Fatalf("ideas vivas = %d: la pista se lleva por delante a las ideas", len(ideas.Wants))
	}
	if len(store.guardados) != 1 {
		t.Fatalf("artefactos persistidos = %d", len(store.guardados))
	}
	if leerArtefacto(t, store.guardados[0]).DeliveryHint != nil {
		t.Fatal("la pista descartada se persistió igual")
	}
	if !strings.Contains(buf.String(), "la pista se descarta") {
		t.Fatalf("el descarte de la pista no dejó log: %q", buf.String())
	}
}

// TestP2_ArtefactoSinVersion_NoSePersiste custodia el invariante de T2.1 desde el lado
// del llamante: un artefacto sin `version` no llega a la base.
//
// El camino real es éste y no otro: `llm.ParseMainIdeas` rechaza la salida sin
// `version` como fallo de CALIDAD, así que la etapa ni siquiera llega a construir un
// artefacto — y por eso la aserción que manda es «SaveStage no se llamó NI UNA VEZ».
//
// 💥 MUTACIÓN: en Run, saltarse ParseMainIdeas y persistir `raw` tal cual ⇒ el artefacto
// llega al store, `Validate` lo rechaza por falta de `version` y el error cambia de
// familia (deja de ser ErrLLMQuality) ⇒ el test cae por las dos aserciones.
func TestP2_ArtefactoSinVersion_NoSePersiste(t *testing.T) {
	var buf bytes.Buffer
	p2, _, store := etapa(t, artefactoSinVersion(t, wantsDeAmbar()), &buf)

	_, err := p2.Run(context.Background(), jobDeAmbar(), textoAmbar)
	if !errors.Is(err, llm.ErrLLMQuality) {
		t.Fatalf("error = %v; se esperaba un fallo de CALIDAD (el proveedor respondió, mal)", err)
	}
	if len(store.guardados) != 0 {
		t.Fatalf("se persistieron %d artefactos sin version", len(store.guardados))
	}
}

// TestP2_SinLiteral_NoSeLlamaAlModelo: un job cuyo sobre no se llegó a escribir no gasta
// la plaza única de inferencia (22–32 s por llamada de lote) ni manda al modelo un
// prompt donde lo único que hay son productos nuestros (D-044.24).
//
// 💥 MUTACIÓN: quitar la guarda de `literal == ""` ⇒ el modelo recibe una llamada y el
// contador de entradas deja de ser 0.
func TestP2_SinLiteral_NoSeLlamaAlModelo(t *testing.T) {
	var buf bytes.Buffer
	p2, prov, store := etapa(t, artefactoP2(t, llm.ArtifactVersion, wantsDeAmbar(), hintDeAmbar()), &buf)

	_, err := p2.Run(context.Background(), jobDeAmbar(), "")
	if !errors.Is(err, stages.ErrSinLiteral) {
		t.Fatalf("error = %v; se esperaba ErrSinLiteral", err)
	}
	if len(prov.entradas) != 0 {
		t.Fatalf("se llamó al modelo %d veces con un job sin literal", len(prov.entradas))
	}
	if len(store.guardados) != 0 {
		t.Fatal("se persistió un artefacto para un job sin literal")
	}
}

// TestP2_JobQueDejoDeEstarEnProcessing distingue las dos formas de «no se guardó»: la
// base caída (error) y la transición que no aplicó (`false, nil`). El worker de T2.5
// responde distinto a cada una, así que tiene que poder distinguirlas con errors.Is.
//
// 💥 MUTACIÓN: en Run, ignorar el bool de SaveStage ⇒ Run devuelve nil y el test cae.
func TestP2_JobQueDejoDeEstarEnProcessing(t *testing.T) {
	var buf bytes.Buffer
	prov := &provFake{respuesta: artefactoP2(t, llm.ArtifactVersion, wantsDeAmbar(), hintDeAmbar())}
	store := &storeFake{perdido: true}
	p2, err := stages.NewP2(logger.New(logger.WithWriter(&buf)), &selFake{prov: prov}, store)
	if err != nil {
		t.Fatalf("NewP2: %v", err)
	}

	if _, err := p2.Run(context.Background(), jobDeAmbar(), textoAmbar); !errors.Is(err, stages.ErrJobFueraDeProcessing) {
		t.Fatalf("error = %v; se esperaba ErrJobFueraDeProcessing", err)
	}
}

// TestP2_LaViaSePideAlSelectorConLaSesionDeOrigen: la etapa no sabe qué vía le tocó al
// tenant (C2 del ADR-0044) y pide el provider con la sesión de la conversación, que es
// lo que enruta la inferencia al Edge que recibió el mensaje. Ese dato ya se perdió una
// vez (T1.7-8: se pedía y se tiraba), así que aquí se afirma.
//
// 💥 MUTACIÓN: llamar a `For(ctx, job.Key.TenantID, "")` ⇒ el test cae.
func TestP2_LaViaSePideAlSelectorConLaSesionDeOrigen(t *testing.T) {
	var buf bytes.Buffer
	prov := &provFake{respuesta: artefactoP2(t, llm.ArtifactVersion, wantsDeAmbar(), hintDeAmbar())}
	sel := &selFake{prov: prov}
	p2, err := stages.NewP2(logger.New(logger.WithWriter(&buf)), sel, &storeFake{})
	if err != nil {
		t.Fatalf("NewP2: %v", err)
	}
	if _, err := p2.Run(context.Background(), jobDeAmbar(), textoAmbar); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(sel.pide) != 1 || sel.pide[0] != tenant+"/"+sesion {
		t.Fatalf("el provider se pidió como %v; se esperaba [%q]", sel.pide, tenant+"/"+sesion)
	}
}

// TestStageStore_NoPuedeTumbarUnJob es un test ESTRUCTURAL, y es la mitad que sostiene
// de verdad el «descartar una idea no tumba el job»: la conducta se puede probar con un
// caso, pero lo que garantiza que ninguna etapa —ni ésta ni P3 ni P4— pueda matar un
// job es que el puerto no tenga con qué. Si alguien añade `Fail` a StageStore, este
// test se pone rojo y la decisión pasa por la revisión en vez de colarse.
func TestStageStore_NoPuedeTumbarUnJob(t *testing.T) {
	tipo := reflect.TypeOf((*stages.StageStore)(nil)).Elem()
	if tipo.NumMethod() != 1 {
		nombres := make([]string, 0, tipo.NumMethod())
		for i := range tipo.NumMethod() {
			nombres = append(nombres, tipo.Method(i).Name)
		}
		t.Fatalf("StageStore tiene %d métodos (%v): una etapa solo puede GUARDAR su artefacto", tipo.NumMethod(), nombres)
	}
	if tipo.Method(0).Name != "SaveStage" {
		t.Fatalf("el único método de StageStore es %q", tipo.Method(0).Name)
	}
}

// TestNewP2_SinCablear: una etapa a medio construir no nace.
func TestNewP2_SinCablear(t *testing.T) {
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
			if _, err := stages.NewP2(c.log, c.sel, c.store); !errors.Is(err, stages.ErrSinCablear) {
				t.Fatalf("error = %v; se esperaba ErrSinCablear", err)
			}
		})
	}
}
