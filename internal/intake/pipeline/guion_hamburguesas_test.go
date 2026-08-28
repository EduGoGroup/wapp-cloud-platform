package pipeline

// guion_hamburguesas_test.go — EL GUION CORTO DE REQ-31 (Plan 044 · Ola 6 · T6.1).
//
// El hilo es de una línea: «1 hamburguesa con queso y cebolla». El pipeline lo lee
// MAL y pare la revisión 1 con dos líneas —«2 con queso» y «1 sin cebolla»—, que es
// un pedido que el cliente nunca hizo. Herminia abre el detalle, compara la
// interpretación con el original que tiene al lado (design §7.6) y pide REGENERAR
// por la vía `api`. La revisión 2 sale correcta y la 1 SIGUE AHÍ.
//
// ════════════════════════════════════════════════════════════════════════════
// LO QUE ESTE FICHERO DEMUESTRA, Y CÓMO
// ════════════════════════════════════════════════════════════════════════════
//
//  1. **La rev 1 se conserva.** No se afirma con un «no cambió» a ciegas: se toma
//     una FOTO de la revisión 1 (número, clase, autor, fecha y payload byte a byte)
//     ANTES de regenerar, y se compara contra la misma revisión DESPUÉS. La rama por
//     la que podría cambiar se recorre de verdad en medio — la segunda pasada SÍ
//     escribe una revisión, y el test lo exige antes de mirar la primera: si la
//     escritura no ocurriera, la conservación sería la de un sistema en reposo y no
//     probaría nada.
//  2. **La rev 2 es la correcta**, y es OTRA revisión de la MISMA solicitud: una
//     cabecera, dos revisiones, `revision_no` 1 y 2.
//  3. **La regeneración va por la vía `api`**, y eso se lee en los DOS sitios donde
//     queda escrito: el `payload.analysis.provider` de la revisión y la clave `via`
//     del evento `intake_reanalyzed` (que es `via` y NUNCA `provider`, draft.go).
//
// ⚠️ CORRE CON DOBLES. Las cuatro etapas LLM son de guion; REALES son el `match`, el
// `draft`, los dos almacenes en memoria del dominio y el worker de producción. Nada
// de lo que hay aquí está verificado en campo.
//
// 🔴 POR QUÉ NO PASA POR EL ENDPOINT. `POST /api/v1/intakes/{id}/reanalyze` abre un
// job y devuelve; quien escribe la revisión es este worker, después y asíncrono. El
// atajo es sembrar el SEGUNDO job sobre el MISMO `event_id` con el contexto de la
// migración 0080 puesto, que es exactamente la fila que `AbrirReanalisis` deja en
// `intake_jobs`. Lo que queda fuera del alcance de este test —y se declara— es la
// puerta HTTP: sus 422/404 y su `ViaInvalidaError` viven en internal/reanalisis.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// ---------------------------------------------------------------------------
// EL DECORADO QUE LE DEVUELVE AL CLAIM LO QUE EL DOBLE SE DEJA
// ---------------------------------------------------------------------------

// hamStoreConReanalisis le añade al doble en memoria lo ÚNICO que le falta para
// poder representar un re-análisis: el contexto de la migración 0080.
//
// 🔴 ES UNA DIVERGENCIA REAL DEL DOBLE, NO UN CAPRICHO DE ESTE TEST. Los dos claims
// de producción devuelven las cuatro columnas —`COALESCE(j.requested_by, ”)`,
// `reanalysis_via`, `reanalysis_source`, `reanalyzed_from` (machine_postgres.go)— y
// las montan en `ClaimedJob.Reanalisis`. `StoreEnMemoria.reclamar` NO las devuelve:
// su `ClaimedJob` sale siempre con el `Reanalisis` en cero, así que contra ese doble
// TODO job es del pipeline normal y ningún test de nivel worker puede ejercitar el
// re-análisis. Este decorado repone exactamente eso y nada más.
//
// Se hace aquí y no editando `memoria.go` porque el doble es código exportado que
// consumen dos paquetes: cambiar su `Fila` es una decisión de la casa, no de un test.
// Queda ANOTADO como hallazgo para quien cierre la ola.
type hamStoreConReanalisis struct {
	*StoreEnMemoria
	candado  sync.Mutex
	contexto map[string]intake.Reanalisis
}

// hamNuevoStore envuelve un doble recién construido.
func hamNuevoStore(ahora func() time.Time) *hamStoreConReanalisis {
	return &hamStoreConReanalisis{
		StoreEnMemoria: NuevoStoreEnMemoria(ahora),
		contexto:       map[string]intake.Reanalisis{},
	}
}

// SembrarConReanalisis mete la fila y le cuelga el contexto que el claim devolverá.
func (s *hamStoreConReanalisis) SembrarConReanalisis(f Fila, r intake.Reanalisis) string {
	id := s.Sembrar(f)
	s.candado.Lock()
	defer s.candado.Unlock()
	s.contexto[id] = r
	return id
}

// ClaimNext implementa intake.PipelineStore: el claim del doble MÁS las cuatro
// columnas de la 0080, igual que hace `escanearClaim` con el RETURNING real.
func (s *hamStoreConReanalisis) ClaimNext(ctx context.Context) (intake.ClaimedJob, bool, error) {
	job, ok, err := s.StoreEnMemoria.ClaimNext(ctx)
	if !ok || err != nil {
		return job, ok, err
	}
	s.candado.Lock()
	defer s.candado.Unlock()
	if r, hay := s.contexto[job.ID]; hay {
		job.Reanalisis = r
	}
	return job, ok, nil
}

// El decorado sigue satisfaciendo el puerto entero (lo demás se promueve).
var _ intake.PipelineStore = (*hamStoreConReanalisis)(nil)

// ---------------------------------------------------------------------------
// LAS ETAPAS DE GUION, INDEXADAS POR JOB
// ---------------------------------------------------------------------------
//
// El guion va por `job.ID` y no por número de llamada a propósito: lo que cambia
// entre la rev 1 y la rev 2 es QUÉ ENTENDIÓ el modelo, y eso es una propiedad del
// job, no del orden en que el worker lo atienda. Con un contador, un reintento
// silencioso movería el guion entero y el test empezaría a probar otra cosa.

// hamP2 implementa EtapaIdeas. La idea es la misma en las dos pasadas: el error de
// la rev 1 no está en QUÉ quiere el cliente, está en cuántas y con qué.
type hamP2 struct {
	almacen stages.StageStore
}

func (e *hamP2) Run(ctx context.Context, job intake.ClaimedJob, _ string) (*llm.MainIdeas, error) {
	art := &llm.MainIdeas{
		Version: llm.ArtifactVersion,
		Wants: []llm.Want{
			{Idea: "hamburguesa", Evidence: hamLiteral},
		},
	}
	return art, guardar(ctx, e.almacen, job.ID, intake.StageP2, art)
}

// hamP3 implementa EtapaEspecificaciones.
type hamP3 struct {
	almacen stages.StageStore
	porJob  map[string][]llm.ItemSpec
}

func (e *hamP3) Run(ctx context.Context, job intake.ClaimedJob, _ string, _ []llm.Want) (*stages.ArtefactoP3, error) {
	items, hay := e.porJob[job.ID]
	if !hay {
		return nil, fmt.Errorf("guion de las hamburguesas: P3 no tiene entrada para el job %s", job.ID)
	}
	art := &stages.ArtefactoP3{Version: llm.ArtifactVersion, Items: items}
	return art, guardar(ctx, e.almacen, job.ID, intake.StageP3, art)
}

// hamP4 implementa EtapaNormalizacion. Es la etapa que DECIDE el desenlace del
// guion: la rev 1 sale mal porque P4 parte el pedido en dos, y la rev 2 sale bien
// porque lo deja en uno con sus dos indicaciones.
type hamP4 struct {
	almacen stages.StageStore
	porJob  map[string][]llm.NormalizedItem
}

func (e *hamP4) Run(ctx context.Context, job intake.ClaimedJob, _ string,
	_ []llm.ItemSpec, _ *llm.Hint) (*llm.Quantities, error) {
	items, hay := e.porJob[job.ID]
	if !hay {
		return nil, fmt.Errorf("guion de las hamburguesas: P4 no tiene entrada para el job %s", job.ID)
	}
	art := &llm.Quantities{Version: llm.ArtifactVersion, Items: items}
	return art, guardar(ctx, e.almacen, job.ID, intake.StageP4, art)
}

// hamCifra devuelve el literal del guion. No modela el envelope: eso tiene sus
// propios tests contra Postgres.
type hamCifra struct{}

func (hamCifra) Decrypt(_, _ []byte, _ string) (string, error) { return hamLiteral, nil }

// hamPuenteCRM retiene lo que el draft le empuje. Se cablea aunque el criterio no
// lo pida porque sin él la etapa emite un `Error` («no hay puente CRM cableado») en
// cuanto el job es del dueño, y un guion que deja un error rojo en el log enseña un
// sistema mal montado que no es el que se quiere retratar.
type hamPuenteCRM struct {
	candado sync.Mutex
	empujes []int
}

func (p *hamPuenteCRM) PushRevisionByID(_ context.Context, _, _ string, revisionNo int) error {
	p.candado.Lock()
	defer p.candado.Unlock()
	p.empujes = append(p.empujes, revisionNo)
	return nil
}

func (p *hamPuenteCRM) recibidos() []int {
	p.candado.Lock()
	defer p.candado.Unlock()
	return append([]int(nil), p.empujes...)
}

// ---------------------------------------------------------------------------
// EL DECORADO DEL GUION
// ---------------------------------------------------------------------------

const (
	// hamLiteral es lo que el cliente escribió. UNA hamburguesa, con las DOS cosas.
	hamLiteral = "1 hamburguesa con queso y cebolla"
	// hamEvento es el evento del que cuelgan los dos jobs. Que sea el MISMO es lo
	// que hace que la segunda pasada aterrice en la solicitud que ya existía
	// (idDeLaSolicitud deriva el id del evento, draft.go).
	hamEvento  = "7c4d1f80-2a63-4e15-9b88-6ef0a3d51c24"
	hamTenant  = "tenant-ham"
	hamSesion  = "sess-ham"
	hamContact = "contacto-ham"
	// hamJobPrimero es la pasada del pipeline normal; hamJobSegundo, la que abre el
	// dueño con `/reanalyze`.
	hamJobPrimero = "job-ham-1"
	hamJobSegundo = "job-ham-2"
	// hamArticulo es la etiqueta del catálogo del tenant: el nombre con el que
	// Herminia conoce su producto, y el que las líneas `matched` copian.
	hamArticulo = "Hamburguesa"
)

// hamBanco es el worker del guion, con todo lo que hay que interrogar después.
type hamBanco struct {
	w           *Worker
	almacen     *hamStoreConReanalisis
	log         *captor
	rel         *reloj
	flujos      *store.MemoryRepository
	solicitudes *intakes.MemoryStore
	crm         *hamPuenteCRM
}

// hamMontar cablea el worker de producción contra las etapas de guion y las dos
// etapas REALES de la Ola 3.
func hamMontar(t *testing.T) *hamBanco {
	t.Helper()
	rel := nuevoReloj()
	almacen := hamNuevoStore(rel.ahora)
	log := &captor{}
	flujos := store.NewMemoryRepository()
	solicitudes := intakes.NewMemoryStore()
	crm := &hamPuenteCRM{}

	etapaMatch, err := stages.NewMatch(log, almacen.StoreEnMemoria)
	if err != nil {
		t.Fatalf("construir el match real: %v", err)
	}
	// El reloj de la etapa es el MISMO que el del worker: sin eso, el `elapsed_ms`
	// del borrador restaría `time.Now` real menos un `message_ts` de laboratorio y
	// saldría en días. No lo mira ninguna aserción, pero un número absurdo en el log
	// de un guion es una pista falsa esperando a que alguien la siga.
	etapaDraft, err := stages.NewDraft(log, almacen.StoreEnMemoria, flujos, solicitudes, flujos,
		stages.ConEmpujeCRM(crm), stages.ConReloj(rel.ahora))
	if err != nil {
		t.Fatalf("construir el draft real: %v", err)
	}
	catalogos, err := NuevoCatalogoEnMemoria(hamArticulo)
	if err != nil {
		t.Fatalf("construir el catálogo del tenant: %v", err)
	}

	p2 := &hamP2{almacen: almacen.StoreEnMemoria}
	p3 := &hamP3{almacen: almacen.StoreEnMemoria, porJob: map[string][]llm.ItemSpec{
		hamJobPrimero: {
			{Product: "hamburguesa", Evidence: hamLiteral},
			{Product: "hamburguesa", Evidence: hamLiteral},
		},
		hamJobSegundo: {
			{Product: "hamburguesa", Evidence: hamLiteral},
		},
	}}
	// 🔴 AQUÍ ESTÁ EL ERROR DE REQ-31, ESCRITO A MANO. La rev 1 parte una hamburguesa
	// en dos líneas y se inventa una cantidad: «2 con queso» + «1 sin cebolla». El
	// cliente pidió UNA con queso Y cebolla. La rev 2 es esa.
	p4 := &hamP4{almacen: almacen.StoreEnMemoria, porJob: map[string][]llm.NormalizedItem{
		hamJobPrimero: {
			{Product: "hamburguesa", Qty: 2, Customizations: []string{"con queso"}, Evidence: hamLiteral},
			{Product: "hamburguesa", Qty: 1, Customizations: []string{"sin cebolla"}, Evidence: hamLiteral},
		},
		hamJobSegundo: {
			{Product: "hamburguesa", Qty: 1, Customizations: []string{"con queso", "con cebolla"}, Evidence: hamLiteral},
		},
	}}

	w, err := NewWorker(log, almacen, p2, p3, p4, etapaMatch, etapaDraft, catalogos,
		hamCifra{}, Config{}, ConZonasDeEnvio(solicitudes))
	if err != nil {
		t.Fatalf("cablear el worker: %v", err)
	}
	w.ahora = rel.ahora

	return &hamBanco{w: w, almacen: almacen, log: log, rel: rel,
		flujos: flujos, solicitudes: solicitudes, crm: crm}
}

// hamFila es el job del guion. `id` decide qué entrada del guion le toca.
func (b *hamBanco) hamFila(id string) Fila {
	return Fila{
		ID: id,
		Key: intake.WindowKey{TenantID: hamTenant, SessionID: hamSesion,
			ContactID: hamContact, EventID: hamEvento},
		SourceText: intake.SourceText{Enc: []byte("cifrado"), DEK: []byte("dek"), KEKID: "kek-ham"},
		MessageTS:  b.rel.ahora().Add(-3 * time.Minute),
		CreatedAt:  b.rel.ahora(),
	}
}

// hamExigeTerminado comprueba que el job salió por la puerta buena.
func (b *hamBanco) hamExigeTerminado(t *testing.T, id string) Fila {
	t.Helper()
	f, ok := b.almacen.Ver(id)
	if !ok {
		t.Fatalf("el job %s no existe", id)
	}
	if f.Status != intake.StatusDone {
		t.Fatalf("el job %s tenía que terminar bien y quedó %q (%s): %s",
			id, f.Status, f.Error, b.log.volcado())
	}
	if f.IntakeID == "" {
		t.Fatalf("el job %s terminó SIN intake_id: %s", id, b.log.volcado())
	}
	return f
}

// hamPayload decodifica el contrato §7.4 de una revisión.
func hamPayload(t *testing.T, rev intakes.Revision) stages.PayloadRevision {
	t.Helper()
	var p stages.PayloadRevision
	if err := json.Unmarshal(rev.Payload, &p); err != nil {
		t.Fatalf("decodificar el payload de la revisión %d: %v", rev.RevisionNo, err)
	}
	return p
}

// hamRetrato es lo que una línea del presupuesto dice, en lo que a este guion le
// importa. Se compara así y no campo a campo suelto para que un fallo enseñe la
// línea entera y no un booleano.
type hamRetrato struct {
	kind          string
	label         string
	qty           int
	customization string
}

// hamLineas retrata las líneas del payload EN ORDEN.
func hamLineas(p stages.PayloadRevision) []hamRetrato {
	out := make([]hamRetrato, 0, len(p.Lines))
	for _, l := range p.Lines {
		out = append(out, hamRetrato{kind: l.Kind, label: l.Label,
			qty: l.Qty, customization: l.Customization})
	}
	return out
}

// hamEventos son las filas de telemetría con ese nombre.
func hamEventos(evs []store.FlowEvent, nombre string) []store.FlowEvent {
	out := make([]store.FlowEvent, 0, 1)
	for _, e := range evs {
		if e.Name == nombre {
			out = append(out, e)
		}
	}
	return out
}

// hamEntero saca un entero del payload de un evento diciendo qué había si no lo es.
func hamEntero(t *testing.T, ev store.FlowEvent, clave string) int {
	t.Helper()
	v, hay := ev.Payload[clave]
	if !hay {
		t.Fatalf("el evento %q no trae la clave %q; trae %v", ev.Name, clave, ev.Payload)
	}
	n, ok := v.(int)
	if !ok {
		t.Fatalf("la clave %q del evento %q no es un entero: %T(%v)", clave, ev.Name, v, v)
	}
	return n
}

// ---------------------------------------------------------------------------
// EL GUION
// ---------------------------------------------------------------------------

// hamViaAPI es el eje VÍA con el que Herminia manda regenerar. El vocabulario es de
// `tenantllm` y esta cola no lo interpreta (intake/machine.go): viaja como cadena.
const hamViaAPI = "api"

// hamErroneas es lo que el pipeline entendió MAL en la pasada 1: una hamburguesa
// partida en dos líneas con una cantidad inventada. Es una función y no una variable
// de paquete para que ningún test pueda mutarle el slice a otro.
func hamErroneas() []hamRetrato {
	return []hamRetrato{
		{kind: stages.KindMatched, label: hamArticulo, qty: 2, customization: "con queso"},
		{kind: stages.KindMatched, label: hamArticulo, qty: 1, customization: "sin cebolla"},
		{kind: stages.KindShipping, label: "Envío", qty: 1},
	}
}

// hamCorrectas es lo que el cliente pidió de verdad, y lo que la rev 2 tiene que
// decir: UNA hamburguesa con sus DOS indicaciones.
func hamCorrectas() []hamRetrato {
	return []hamRetrato{
		{kind: stages.KindMatched, label: hamArticulo, qty: 1, customization: "con queso, con cebolla"},
		{kind: stages.KindShipping, label: "Envío", qty: 1},
	}
}

// TestGuionHamburguesas_ElReanalisisPorLaViaAPI_DejaLaRev1Intacta es REQ-31 entero.
//
// El cuerpo es la NARRACIÓN —pasada 1, foto, pasada 2, veredictos— y cada veredicto
// es un helper con nombre. La densidad de comprobaciones no baja ni un assert: lo que
// baja es la complejidad de UNA función, que gocyclo mide por función.
func TestGuionHamburguesas_ElReanalisisPorLaViaAPI_DejaLaRev1Intacta(t *testing.T) {
	ctx := context.Background()
	b := hamMontar(t)

	// ── PASADA 1: el pipeline normal se equivoca ────────────────────────────
	intakeID, rev1 := hamPasadaUno(ctx, t, b)

	// LA FOTO. Es lo que hace que la conservación se pueda afirmar en vez de
	// suponerse: se guarda el payload EXACTO, no un resumen. El slice se COPIA —
	// `Revisions` ya devuelve copias, pero una foto que compartiera el array con lo
	// que va a cambiar no sería una foto.
	foto := rev1
	foto.Payload = append(json.RawMessage(nil), rev1.Payload...)

	// ── PASADA 2: Herminia regenera por la vía `api` ────────────────────────
	// El contexto es el que `AbrirReanalisis` deja en `intake_jobs` (migración 0080):
	// lo pidió el dueño, por la vía `api`, con el material del hilo, sucediendo a la
	// revisión 1.
	b.almacen.SembrarConReanalisis(b.hamFila(hamJobSegundo), intake.Reanalisis{
		RequestedBy: intake.RequestedByOwner,
		Via:         hamViaAPI,
		Source:      stages.OrigenHiloDelEvento,
		From:        1,
	})
	b.w.Drenar(ctx)

	segundo := b.hamExigeTerminado(t, hamJobSegundo)
	if segundo.IntakeID != intakeID {
		t.Fatalf("el re-análisis tenía que aterrizar en la MISMA solicitud (%s) y aterrizó en %s",
			intakeID, segundo.IntakeID)
	}
	if cab := b.flujos.Intakes(); len(cab) != 1 {
		t.Fatalf("el re-análisis NO pare una segunda solicitud: hay %d", len(cab))
	}

	// 🔴 QUE HAYA DOS REVISIONES SE EXIGE ANTES QUE NADA, y ése es el orden que hace
	// que el veredicto sobre la rev 1 no sea vacuo: la rama por la que la rev 1
	// podría cambiar es la escritura de la rev 2, y aquí se comprueba que ocurrió.
	revs := b.solicitudes.Revisions(intakeID)
	if len(revs) != 2 {
		t.Fatalf("tras regenerar tenía que haber DOS revisiones, hay %d: %s", len(revs), b.log.volcado())
	}
	rev2 := revs[1]

	hamExigeLaRev2(t, rev2)
	hamExigeLaRev1Conservada(t, revs[0], foto)
	hamExigeElEventoDeReanalisis(t, b, rev2.RevisionNo)
	hamExigeElEmpujeAlCRM(t, b, rev2.RevisionNo)
}

// hamPasadaUno corre el pipeline NORMAL y deja comprobado todo lo suyo: que nace una
// solicitud esperando al dueño, que su única revisión la firma la máquina, que dice
// lo EQUIVOCADO y que no deja ni un rastro de re-análisis.
//
// Devuelve el id de la solicitud y la revisión 1 tal como quedó, que es de lo que se
// toma la foto.
func hamPasadaUno(ctx context.Context, t *testing.T, b *hamBanco) (string, intakes.Revision) {
	t.Helper()
	b.almacen.Sembrar(b.hamFila(hamJobPrimero))
	b.w.Drenar(ctx)

	primero := b.hamExigeTerminado(t, hamJobPrimero)

	cabeceras := b.flujos.Intakes()
	if len(cabeceras) != 1 {
		t.Fatalf("la pasada 1 tenía que parir UNA solicitud, hay %d", len(cabeceras))
	}
	if cabeceras[0].Status != intakes.StatusPendingApproval {
		t.Fatalf("el borrador interpretado nace esperando al dueño; nació en %q", cabeceras[0].Status)
	}

	revs := b.solicitudes.Revisions(primero.IntakeID)
	if len(revs) != 1 {
		t.Fatalf("tras la pasada 1 tenía que haber UNA revisión, hay %d", len(revs))
	}

	hamExigeLaRev1Recien(t, revs[0])
	hamExigeSinRastroDeReanalisis(t, b)
	return primero.IntakeID, revs[0]
}

// hamExigeLaRev1Recien es el veredicto sobre lo que el pipeline normal acaba de
// escribir: quién la firma, que trae el original del cliente al lado y que su
// interpretación es la MALA de REQ-31.
func hamExigeLaRev1Recien(t *testing.T, rev intakes.Revision) {
	t.Helper()
	if rev.RevisionNo != 1 {
		t.Fatalf("la primera revisión es la número %d y tenía que ser la 1", rev.RevisionNo)
	}
	if rev.Kind != intakes.RevisionKindInterpreted {
		t.Fatalf("la rev 1 es %q y tenía que ser %q", rev.Kind, intakes.RevisionKindInterpreted)
	}
	if rev.CreatedBy != intakes.RevisionBySystem {
		t.Fatalf("la rev 1 la escribió el pipeline: su autor tenía que ser %q, es %q",
			intakes.RevisionBySystem, rev.CreatedBy)
	}

	p := hamPayload(t, rev)
	if p.SourceText != hamLiteral {
		t.Fatalf("la rev 1 tiene que llevar al lado el original del cliente; lleva %q", p.SourceText)
	}
	// 🔴 LA REV 1 ESTÁ MAL, Y SE AFIRMA CUÁL ES EL ERROR. Un test que solo dijera
	// «hay 3 líneas» pasaría igual con las líneas correctas partidas en tres.
	hamExigeLineas(t, "rev 1", p, hamErroneas())
	if p.Analysis.Provider != "" {
		t.Fatalf("la rev 1 la parió el pipeline normal, que no sabe por qué vía corrió; trae provider=%q",
			p.Analysis.Provider)
	}
	if p.Analysis.ReanalyzedFrom != nil {
		t.Fatalf("la rev 1 no sucede a ninguna: `reanalyzed_from` tenía que ser null, es %d",
			*p.Analysis.ReanalyzedFrom)
	}
}

// hamExigeSinRastroDeReanalisis es la mitad de NO-REGRESIÓN del guion: el pipeline
// normal no re-analiza nada y no empuja al CRM. Sin ella, los dos asserts de la
// pasada 2 pasarían igual con un sistema que emite y empuja SIEMPRE.
func hamExigeSinRastroDeReanalisis(t *testing.T, b *hamBanco) {
	t.Helper()
	if evs := hamEventos(b.flujos.FlowEvents(), stages.EventoReanalizado); len(evs) != 0 {
		t.Fatalf("el pipeline NORMAL no re-analiza nada y publicó %d filas de %q",
			len(evs), stages.EventoReanalizado)
	}
	if got := b.crm.recibidos(); len(got) != 0 {
		t.Fatalf("el borrador de la pasada 1 no está aprobado por nadie y NO se empuja al CRM; se empujaron %v", got)
	}
}

// hamExigeLaRev2 es el veredicto sobre la revisión que sale del re-análisis: es la
// número 2, la firma el DUEÑO, dice lo correcto y su rastro declara la vía `api`
// (§7.4 / D-044.15) — el PRIMERO de los dos sitios donde la vía queda escrita.
func hamExigeLaRev2(t *testing.T, rev intakes.Revision) {
	t.Helper()
	if rev.RevisionNo != 2 {
		t.Fatalf("la revisión nueva es la número %d y tenía que ser la 2", rev.RevisionNo)
	}
	if rev.CreatedBy != intakes.RevisionByOwner {
		t.Fatalf("el re-análisis lo pidió el dueño: su revisión la firma %q, no %q",
			intakes.RevisionByOwner, rev.CreatedBy)
	}
	if rev.Kind != intakes.RevisionKindInterpreted {
		t.Fatalf("un re-análisis sigue siendo una interpretación de la máquina: kind %q", rev.Kind)
	}

	p := hamPayload(t, rev)
	hamExigeLineas(t, "rev 2", p, hamCorrectas())
	if p.Analysis.Provider != hamViaAPI {
		t.Fatalf("la rev 2 se regeneró por la vía %q; su `analysis.provider` dice %q",
			hamViaAPI, p.Analysis.Provider)
	}
	if p.Analysis.Source != stages.OrigenHiloDelEvento {
		t.Fatalf("el material del re-análisis era el hilo del evento; el rastro dice %q", p.Analysis.Source)
	}
	if p.Analysis.ReanalyzedFrom == nil || *p.Analysis.ReanalyzedFrom != 1 {
		t.Fatalf("la rev 2 sucede a la rev 1: `reanalyzed_from` tenía que ser 1, es %v",
			p.Analysis.ReanalyzedFrom)
	}
}

// hamExigeLaRev1Conservada es EL CRITERIO DE T6.1: tras regenerar, la revisión 1
// sigue siendo la misma. No se afirma con un «no cambió» a ciegas — se compara con la
// FOTO tomada antes, y el payload va byte a byte.
//
// 🔴 EL ORDEN IMPORTA Y ESTÁ FUERA DE AQUÍ: el llamante ya exigió que la pasada 2
// escribiera su revisión, así que la rama por la que la rev 1 podría haber cambiado
// se recorrió de verdad. Sin ese requisito previo, este veredicto sería el de un
// sistema en reposo y no probaría nada.
func hamExigeLaRev1Conservada(t *testing.T, viva, foto intakes.Revision) {
	t.Helper()
	if viva.RevisionNo != foto.RevisionNo || viva.Kind != foto.Kind ||
		viva.CreatedBy != foto.CreatedBy || !viva.CreatedAt.Equal(foto.CreatedAt) {
		t.Fatalf("la rev 1 cambió de identidad al regenerar: no=%d kind=%q autor=%q creada=%s",
			viva.RevisionNo, viva.Kind, viva.CreatedBy, viva.CreatedAt)
	}
	if !bytes.Equal(viva.Payload, foto.Payload) {
		t.Fatalf("el payload de la rev 1 NO es el mismo tras regenerar.\nantes: %s\nahora: %s",
			foto.Payload, viva.Payload)
	}
	if !viva.LiteralPrunedAt.IsZero() {
		t.Fatalf("la rev 1 salió podada de una regeneración que no le tocaba: %s", viva.LiteralPrunedAt)
	}
	// Y sigue siendo la EQUIVOCADA: conservarla es conservar el error, que es el
	// punto — es contra ella contra la que Herminia comparó.
	hamExigeLineas(t, "rev 1 tras regenerar", hamPayload(t, viva), hamErroneas())
}

// hamExigeElEventoDeReanalisis es el SEGUNDO sitio donde queda escrita la vía: la
// fila `intake_reanalyzed` de `flow_events` (design §10, D-044.15).
func hamExigeElEventoDeReanalisis(t *testing.T, b *hamBanco, revisionNueva int) {
	t.Helper()
	evs := hamEventos(b.flujos.FlowEvents(), stages.EventoReanalizado)
	if len(evs) != 1 {
		t.Fatalf("el re-análisis consumado publica UNA fila de %q, hay %d",
			stages.EventoReanalizado, len(evs))
	}
	ev := evs[0]
	// 🔴 LA CLAVE ES `via`, NUNCA `provider` (draft.go: se renombró el 2026-08-23).
	if v := ev.Payload["via"]; v != hamViaAPI {
		t.Fatalf("el evento tenía que decir via=%q; dice %v (payload %v)", hamViaAPI, v, ev.Payload)
	}
	if _, hay := ev.Payload["provider"]; hay {
		t.Fatalf("la clave `provider` está retirada de este evento y volvió: %v", ev.Payload)
	}
	if s := ev.Payload["source"]; s != stages.OrigenHiloDelEvento {
		t.Fatalf("el evento tenía que decir source=%q; dice %v", stages.OrigenHiloDelEvento, s)
	}
	hamExigeElSalto(t, ev, revisionNueva)
}

// hamExigeElSalto comprueba el par `from_rev`/`to_rev`.
//
// 🔴 LOS DOS NÚMEROS TIENEN QUE SER DISTINTOS, y se exige explícitamente: con 1 y 1
// un golden pasaría con los campos CRUZADOS y nadie lo vería (pasó en la Ola 5). Y
// `to_rev` se compara contra el número REAL de la revisión nueva, no contra un 2
// escrito a mano, que acertaría también si la etapa publicara el de partida.
func hamExigeElSalto(t *testing.T, ev store.FlowEvent, revisionNueva int) {
	t.Helper()
	desde, hasta := hamEntero(t, ev, "from_rev"), hamEntero(t, ev, "to_rev")
	if desde == hasta {
		t.Fatalf("from_rev y to_rev valen los dos %d: con el mismo número este test no puede distinguirlos", desde)
	}
	if desde != 1 {
		t.Fatalf("el re-análisis partía de la revisión 1; el evento dice from_rev=%d", desde)
	}
	if hasta != revisionNueva {
		t.Fatalf("to_rev tiene que ser el número REAL de la revisión nueva (%d); el evento dice %d",
			revisionNueva, hasta)
	}
}

// hamExigeElEmpujeAlCRM: el re-análisis SÍ sale al puente, y solo él. El CRM no puede
// quedarse con la versión mala del pedido (D-044.19 / T4.10).
func hamExigeElEmpujeAlCRM(t *testing.T, b *hamBanco, revisionNueva int) {
	t.Helper()
	got := b.crm.recibidos()
	if len(got) != 1 || got[0] != revisionNueva {
		t.Fatalf("el re-análisis empuja al CRM la revisión %d y solo ésa; se empujaron %v",
			revisionNueva, got)
	}
}

// hamExigeLineas compara el retrato de las líneas con el esperado, en orden.
//
// Compara la línea ENTERA y no un campo: «hay dos líneas» pasa con dos líneas
// equivocadas, y «la primera tiene qty 2» pasa con una etiqueta que no es del
// catálogo. La etiqueta del envío no se compara porque su texto lo fija la plataforma
// (D-041.11) y no es lo que este guion prueba.
func hamExigeLineas(t *testing.T, quien string, p stages.PayloadRevision, esperadas []hamRetrato) {
	t.Helper()
	got := hamLineas(p)
	if len(got) != len(esperadas) {
		t.Fatalf("%s: tenía que tener %d líneas y tiene %d: %+v", quien, len(esperadas), len(got), got)
	}
	for i, e := range esperadas {
		g := got[i]
		if g.kind != e.kind || g.qty != e.qty || g.customization != e.customization {
			t.Fatalf("%s: la línea %d es %+v y tenía que ser %+v", quien, i, g, e)
		}
		if e.kind == stages.KindShipping {
			continue
		}
		if g.label != e.label {
			t.Fatalf("%s: la línea %d se etiqueta %q y tenía que copiar el catálogo (%q)",
				quien, i, g.label, e.label)
		}
	}
}
