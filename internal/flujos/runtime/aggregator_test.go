// aggregator_test.go — Plan 044 · Ola 1 · T1.1, T1.2 y T1.7.
//
// ⏳ NINGUNO DE ESTOS TESTS SE HA EJECUTADO. Se escribieron en un entorno sin Go,
// sin red y sin Postgres, así que ninguno está declarado como pasado. Lo que sí
// está escrito es CÓMO ponerlos rojos: cada test lleva su MUTACIÓN concreta, y la
// mutación está elegida para que COMPILE — una mutación que no compila no prueba
// nada, solo dice que el compilador funciona.
//
// Todo corre contra el doble en memoria de internal/intake, que replica las tres
// cosas que muerden de la migración 0072: una sola ventana viva por tupla (el
// índice único PARCIAL), message_ts fijado SOLO al abrir, y el cierre idempotente
// por guard de estado.
package runtime_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
)

const (
	aggTenant  = "t-agg"
	aggSession = "s-agg"
	aggContact = "c-opaco"
	aggEvent   = "00000000-0000-0000-0000-0000000000aa"
)

func aggKey() intake.WindowKey {
	return intake.WindowKey{
		TenantID: aggTenant, SessionID: aggSession, ContactID: aggContact, EventID: aggEvent,
	}
}

func aggLogger() logger.Logger { return logger.New(logger.WithWriter(io.Discard)) }

// aggReloj es el reloj falso: el patrón del repo (rt.now / WithClock) llevado al
// agregador. Sin él, «silencio ⇒ flush a los 45 s» solo se podría probar durmiendo
// 45 segundos de verdad, que es tanto como no probarlo.
//
// 🔒 LLEVA CANDADO, y no es adorno: el test del barrido periódico (TestRun_…, al final
// de este fichero) avanza el reloj desde la goroutine del test MIENTRAS el ticker de
// Run lo lee desde la suya. Sin el mutex eso es una carrera de datos que `-race`
// convierte en fallo, y sin `-race` en un test que pasa por accidente. Los tests que
// no arrancan Run no pagan nada apreciable por esto.
type aggReloj struct {
	mu sync.Mutex
	t  time.Time
}

func nuevoAggReloj() *aggReloj {
	return &aggReloj{t: time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)}
}
func (r *aggReloj) now() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.t
}
func (r *aggReloj) avanza(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.t = r.t.Add(d)
}

// ---------------------------------------------------------------------------
// LOS DOS CONTADORES QUE LE FALTABAN AL PRESUPUESTO (D-044.26)
// ---------------------------------------------------------------------------
//
// `intake.MemoryStore` ya contaba sus sentencias, así que el test del presupuesto
// medía SOLO `intake_jobs`. Pero D-044.26 no habla solo de esa tabla: dice CERO
// SELECT «ni para encontrar el job abierto, NI PARA LEER `tenant_settings`», y
// además fija que el gate va por el resolver CACHEADO y no por una consulta por
// mensaje. Sin contar esas dos, esta mutación COMPILA y deja la suite entera verde:
//
//	// en Observe, justo antes de OpenOrAppend
//	_ = s.windowFor(ctx, ref.Key.TenantID)
//
// que es un incumplimiento LITERAL del criterio, con la config del tenant leída en
// línea con el mensaje del cliente.
//
// 🔧 SE ENVUELVEN LOS DOBLES, NO SE TOCAN. `store.MemoryRepository` y
// `entitlements.Fake` los usa medio repo, y el `Fake` promete POR ESCRITO ser
// «seguro para lectura concurrente si no se muta tras construirlo»: meterle un
// contador dentro convertiría cada lectura en una escritura y sembraría una carrera
// de datos en todos los tests que hoy lo comparten entre goroutines (`-race` la
// destaparía, y el fallo aparecería en tests que no tienen nada que ver con esta
// ola). Los envoltorios viven AQUÍ, llevan su propio candado y no cambian el
// comportamiento de nadie.

// settingsContadas cuenta las lecturas de `tenant_settings`. Satisface
// flowruntime.AggregationSettings por delegación pura.
type settingsContadas struct {
	dentro *store.MemoryRepository
	mu     sync.Mutex
	n      int
}

func contarSettings(r *store.MemoryRepository) *settingsContadas {
	return &settingsContadas{dentro: r}
}

func (s *settingsContadas) GetTenantSettings(ctx context.Context, tenantID string) (store.TenantSettings, error) {
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
	return s.dentro.GetTenantSettings(ctx, tenantID)
}

// lecturas es cuántas veces se ha leído la config del tenant desde el montaje (o
// desde el último olvida).
func (s *settingsContadas) lecturas() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// olvida pone el contador a cero, para medir UN entrante con el escenario ya
// montado (mismo criterio que intake.MemoryStore.ResetCounters).
func (s *settingsContadas) olvida() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n = 0
}

// entsContados cuenta las consultas al resolver de entitlements. Implementa las
// TRES funciones de entitlements.Resolver porque el agregador lo recibe por esa
// interfaz completa, no por una local.
type entsContados struct {
	dentro *entitlements.Fake
	mu     sync.Mutex
	n      int
}

func contarEnts(f *entitlements.Fake) *entsContados { return &entsContados{dentro: f} }

func (e *entsContados) Has(ctx context.Context, tenantID, feature string) (bool, error) {
	e.mu.Lock()
	e.n++
	e.mu.Unlock()
	return e.dentro.Has(ctx, tenantID, feature)
}

func (e *entsContados) ListEffective(ctx context.Context, tenantID string) (string, []string, error) {
	return e.dentro.ListEffective(ctx, tenantID)
}

func (e *entsContados) CacheTTL() time.Duration { return e.dentro.CacheTTL() }

// consultas es cuántas veces se le ha preguntado al resolver desde el montaje (o
// desde el último olvida).
func (e *entsContados) consultas() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.n
}

func (e *entsContados) olvida() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.n = 0
}

// Que los dos envoltorios sigan encajando en las interfaces que el agregador pide
// se comprueba EN COMPILACIÓN: si alguien cambia una firma, esto se rompe aquí y no
// en una línea de construcción perdida a mitad del fichero.
var (
	_ flowruntime.AggregationSettings = (*settingsContadas)(nil)
	_ entitlements.Resolver           = (*entsContados)(nil)
)

// aggEntorno monta el agregador con sus tres dependencias dobles y la feature
// llm_intake ENCENDIDA para el tenant de estos tests.
type aggEntorno struct {
	sink *flowruntime.AggregatorSink
	jobs *intake.MemoryStore
	// cfg y ents son los dobles DE VERDAD (los que un test siembra o apaga).
	cfg  *store.MemoryRepository
	ents *entitlements.Fake
	// cfgCnt y entsCnt son los envoltorios que ve el sink: lo mismo, contado.
	cfgCnt  *settingsContadas
	entsCnt *entsContados
	clock   *aggReloj
}

func nuevoAggEntorno(t *testing.T, ventana time.Duration) *aggEntorno {
	t.Helper()
	clock := nuevoAggReloj()
	jobs := intake.NewMemoryStore(clock.now)
	cfg := store.NewMemoryRepository()
	// 🔴 Se parte de DefaultTenantSettings y se cambia SOLO la ventana: sembrar el
	// struct a mano dejaría AggregationWindow en el cero de Go, que NO significa
	// «45 por defecto» sino FLUSH INMEDIATO — y el test estaría probando el
	// agregador con la agregación apagada. Es la trampa que documenta
	// SetTenantSettings.
	s := store.DefaultTenantSettings(aggTenant)
	s.AggregationWindow = ventana
	cfg.SetTenantSettings(s)
	ents := entitlements.NewFake()
	ents.Enable(aggTenant, entitlements.FeatureLLMIntake)
	cfgCnt := contarSettings(cfg)
	entsCnt := contarEnts(ents)
	return &aggEntorno{
		// 🔴 EL SINK RECIBE LOS ENVOLTORIOS, no los dobles pelados: si recibiera los
		// dobles, el presupuesto de `tenant_settings` y el de entitlements volverían a
		// no medirse y las mutaciones de arriba volverían a quedar mudas.
		sink:    flowruntime.NewAggregatorSink(aggLogger(), jobs, cfgCnt, entsCnt, flowruntime.WithAggregatorClock(clock.now)),
		jobs:    jobs,
		cfg:     cfg,
		ents:    ents,
		cfgCnt:  cfgCnt,
		entsCnt: entsCnt,
		clock:   clock,
	}
}

func (e *aggEntorno) observa(ctx context.Context, waID string, hint *flowruntime.IntentHint) {
	e.sink.Observe(ctx, flowruntime.IncomingRef{
		Key:         aggKey(),
		WaMessageID: waID,
		MessageTS:   e.clock.now(),
		Intent:      hint,
	})
}

// ---------------------------------------------------------------------------
// T1.1 — la ráfaga se convierte en UNA ventana
// ---------------------------------------------------------------------------

// TestRafagaDeTresMensajesYUnaFoto_UnSoloJob es el criterio literal de T1.1: tres
// mensajes y una foto seguidos producen UN job, con las CUATRO referencias en
// ORDEN y el message_ts del PRIMERO.
//
// El ts es la mitad que suele romperse en silencio: es la BASE DE FECHAS del
// presupuesto (D-044.9), así que fijarlo al último mensaje resolvería «para el
// jueves» contra el instante equivocado sin que nadie viera un error.
//
// MUTACIÓN (compila, y pone rojo el orden y el número de refs): en aggregator.go,
// dentro de Observe, sustituir
//
//	refs := append([]string{ref.WaMessageID}, ref.MediaRefs...)
//
// por
//
//	refs := ref.MediaRefs
//
// MUTACIÓN 2 (compila, y pone rojo SOLO el ts): en internal/intake/memory.go,
// dentro de OpenOrAppend, en la rama "ya existía" (la del if live := ...), añadir
// la línea
//
//	live.MessageTS = a.MessageTS
//
// Eso es exactamente lo que pasaría si alguien metiera message_ts en el DO UPDATE
// del ON CONFLICT, que es lo que D-044.26 prohíbe.
func TestRafagaDeTresMensajesYUnaFoto_UnSoloJob(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)
	primerTS := e.clock.now()

	e.observa(ctx, "wa-1", nil)
	e.clock.avanza(3 * time.Second)
	e.observa(ctx, "wa-2", nil)
	e.clock.avanza(2 * time.Second)
	e.observa(ctx, "wa-3", nil)
	e.clock.avanza(1 * time.Second)
	e.observa(ctx, "wa-foto", nil) // la foto entra por su propio wa_message_id, SIN descargarla

	jobs := e.jobs.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("la ráfaga tenía que producir UN job, produjo %d", len(jobs))
	}
	j := jobs[0]
	if j.Status != intake.StatusAggregating {
		t.Fatalf("la ventana sigue abierta hasta que le toque: status=%q", j.Status)
	}
	quiero := []string{"wa-1", "wa-2", "wa-3", "wa-foto"}
	if len(j.SourceRefs) != len(quiero) {
		t.Fatalf("source_refs = %v; se esperaban las 4 referencias", j.SourceRefs)
	}
	for i, ref := range quiero {
		if j.SourceRefs[i] != ref {
			t.Fatalf("source_refs[%d] = %q; se esperaba %q (el ORDEN es parte del contrato)", i, j.SourceRefs[i], ref)
		}
	}
	if !j.MessageTS.Equal(primerTS) {
		t.Fatalf("message_ts = %v; tenía que ser el del PRIMER mensaje (%v)", j.MessageTS, primerTS)
	}
}

// TestTenantSinLaFeature_CeroJobs: el gate es llm_intake y es fail-closed. Un
// tenant sin la feature no abre ninguna ventana y no escribe una sola fila.
//
// MUTACIÓN (compila): en aggregator.go, dentro de Observe, sustituir
//
//	if !has {
//
// por
//
//	if !has && false {
//
// (así "has" sigue estando usada y el paquete compila, pero el gate deja de cerrar).
func TestTenantSinLaFeature_CeroJobs(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)
	e.ents.Disable(aggTenant, entitlements.FeatureLLMIntake)

	e.observa(ctx, "wa-1", nil)
	e.observa(ctx, "wa-2", nil)

	if n := len(e.jobs.Jobs()); n != 0 {
		t.Fatalf("un tenant sin llm_intake tiene que producir CERO jobs, produjo %d", n)
	}
	if c := e.jobs.Counters(); c.OpenOrAppend != 0 {
		t.Fatalf("el gate cierra ANTES de escribir: OpenOrAppend=%d, se esperaba 0", c.OpenOrAppend)
	}
}

// ---------------------------------------------------------------------------
// T1.2 / T1.7 — la política de disparo
// ---------------------------------------------------------------------------

// TestSilencio_FlusheaALosNSegundos_YEsIdempotente es el CAMINO PRINCIPAL (T1.7):
// sin ningún intent, la ventana se cierra sola al cumplirse el plazo. Y un segundo
// barrido no produce un segundo job.
//
// MUTACIÓN (compila): en aggregator.go, en la función due, sustituir la última
// línea
//
//	return !now.Before(job.Anchor.Add(win))
//
// por
//
//	return false
//
// ("now" es un parámetro y puede quedar sin usar; "win" sigue usada en la guarda
// del flush inmediato de la línea anterior, así que el paquete compila).
func TestSilencio_FlusheaALosNSegundos_YEsIdempotente(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)

	e.observa(ctx, "wa-1", nil)

	e.clock.avanza(44 * time.Second)
	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("a los 44 s la ventana NO ha vencido todavía; se cerraron %d", n)
	}
	e.clock.avanza(1 * time.Second) // exactamente 45 s: el plazo se cumple, no se supera
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("a los 45 s la ventana tenía que cerrarse; se cerraron %d", n)
	}
	if got := e.jobs.Jobs()[0].Status; got != intake.StatusPending {
		t.Fatalf("status tras el flush = %q; se esperaba %q", got, intake.StatusPending)
	}
	// 🔧 QUÉ AFIRMA ESTE SEGUNDO BARRIDO Y QUÉ NO (revisión 2026-08-22). Afirma que
	// el BARRIDO no vuelve sobre una ventana cerrada: `ListAggregating` filtra por
	// `status='aggregating'` y ya no la lista. Eso es real y vale la pena fijarlo —es
	// lo que impide que el ticker rehaga trabajo cada 5 s— pero NO es el guard de
	// `CloseWindow`, que aquí ni se llama. El guard tiene su propio test y se llama
	// TestCloseWindow_DosLlamadasSeguidas_LaSegundaNoTocaLaFila. El comentario que
	// había aquí atribuía este verde al `UPDATE … WHERE status='aggregating'`, que es
	// la afirmación equivocada: con el guard roto, esto seguía pasando.
	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("el segundo barrido cerró %d ventanas; tenía que cerrar 0", n)
	}
	if n := len(e.jobs.Jobs()); n != 1 {
		t.Fatalf("sigue teniendo que haber UN job, hay %d", n)
	}
}

// TestIntent_AdelantaElFlushSinEsperarElSilencio_YEsIdempotente: el intent
// intake_request con confianza suficiente cierra la ventana en el barrido
// siguiente, sin esperar los 45 s. Es un ADELANTO, no una condición.
//
// MUTACIÓN (compila): en aggregator.go, en la función due, borrar las dos líneas
// del adelanto:
//
//	if _, adelantada := hints[job.Key]; adelantada {
//	    return true
//	}
//
// ("hints" queda como parámetro sin usar, que en Go compila).
func TestIntent_AdelantaElFlushSinEsperarElSilencio_YEsIdempotente(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)

	e.observa(ctx, "wa-1", nil)
	e.clock.avanza(2 * time.Second)
	e.observa(ctx, "wa-2", &flowruntime.IntentHint{Name: flowruntime.IntentIntakeRequest, Confidence: 0.91})

	// Sin haber avanzado nada parecido a los 45 s.
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("el intent tenía que adelantar el cierre; se cerraron %d", n)
	}
	// Mismo matiz que en el test del silencio: lo que esto fija es que el BARRIDO no
	// vuelve sobre lo ya cerrado (el filtro de ListAggregating) y que la pista del
	// intent se CONSUME —takeHints vacía el mapa, así que no queda un adelanto
	// pegado que dispare barrido tras barrido—. El guard de CloseWindow tiene su
	// propio test.
	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("doble flush: el segundo barrido cerró %d, tenía que cerrar 0", n)
	}
	if n := len(e.jobs.Jobs()); n != 1 {
		t.Fatalf("el doble flush NO puede duplicar jobs: hay %d", n)
	}
}

// TestIntentDebilNoAdelanta: por debajo del umbral el intent no adelanta nada, y
// la ventana sigue viva hasta su plazo. Se comprueba que la ventana sigue ABIERTA,
// no solo que el barrido devolvió 0.
//
// MUTACIÓN (compila): en aggregator.go, en intentTriggers, sustituir
//
//	return hint.Name == IntentIntakeRequest && hint.Confidence >= s.intentThreshold
//
// por
//
//	return hint.Name == IntentIntakeRequest
func TestIntentDebilNoAdelanta(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)

	e.observa(ctx, "wa-1", &flowruntime.IntentHint{Name: flowruntime.IntentIntakeRequest, Confidence: 0.1})
	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("un intent por debajo del umbral no adelanta; se cerraron %d", n)
	}
	if got := e.jobs.Jobs()[0].Status; got != intake.StatusAggregating {
		t.Fatalf("la ventana tenía que seguir abierta; status=%q", got)
	}
}

// TestOtroIntentNoAdelanta: solo intake_request dispara. Cualquier otra intención
// del tenant (saludo, consulta de estado…) deja la ventana en paz.
//
// 🔧 El aserto del ESTADO se añadió en la revisión del 2026-08-22: con solo
// `Sweep()==0` este test pasaba también si la ventana no se hubiera abierto nunca
// —o si se hubiera cerrado y el barrido devolviera 0 por no encontrar nada—, que es
// lo contrario de lo que dice probar. Es el mismo aserto que ya llevaba su hermano
// TestIntentDebilNoAdelanta, y por eso los dos lo llevan ahora.
//
// MUTACIÓN (compila): en aggregator.go, en intentTriggers, sustituir
//
//	return hint.Name == IntentIntakeRequest && hint.Confidence >= s.intentThreshold
//
// por
//
//	return hint.Confidence >= s.intentThreshold
func TestOtroIntentNoAdelanta(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)

	e.observa(ctx, "wa-1", &flowruntime.IntentHint{Name: "saludo", Confidence: 0.99})
	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("un intent que no es intake_request no adelanta; se cerraron %d", n)
	}
	jobs := e.jobs.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("el entrante tenía que haber abierto UNA ventana; hay %d jobs", len(jobs))
	}
	if got := jobs[0].Status; got != intake.StatusAggregating {
		t.Fatalf("la ventana tenía que seguir ABIERTA esperando su plazo; status=%q", got)
	}
}

// TestIntentSinParams_ProduceUnJobINDISTINGUIBLE es D-044.20 hecho un test.
//
// La política mira NOMBRE y CONFIANZA y NADA MÁS del intent. La garantía más fuerte
// no la da este test sino el TIPO: flowruntime.IntentHint NO TIENE campo params,
// así que la política no puede leerlos ni aunque alguien quisiera. Este test cierra
// la otra mitad: que el job resultante del camino con intent sea indistinguible del
// que produce el camino sin intent — mismas refs, mismo message_ts, mismo estado
// final, y ninguna marca de por qué se disparó (T1.7 (d)).
//
// MUTACIÓN (compila): añadir a flowruntime.IntentHint un campo
//
//	Params map[string]string
//
// y en aggregator.go, en intentTriggers, exigirlo:
//
//	return hint.Name == IntentIntakeRequest && hint.Confidence >= s.intentThreshold && len(hint.Params) > 0
//
// 🔧 DÓNDE SE PONE ROJO, dicho bien (el comentario anterior hablaba de un sub-test
// `t.Run` que este test NO tiene: aquí no hay sub-tests, hay una closure `corre`
// invocada dos veces): con esa mutación, la llamada `corre(t, true)` —el camino CON
// intent, cuyo `IntentHint` no lleva params porque el tipo no los tiene— deja de
// adelantar; como ese camino NO avanza el reloj, la ventana sigue viva y el Sweep
// devuelve 0, así que muere en el `t.Fatalf("conIntent=%v: se esperaba UN cierre…")`
// de dentro de la closure. Que ESA sea la mutación necesaria es justo la prueba de
// que hoy los params no se leen.
func TestIntentSinParams_ProduceUnJobINDISTINGUIBLE(t *testing.T) {
	ctx := context.Background()

	corre := func(t *testing.T, conIntent bool) intake.Job {
		t.Helper()
		e := nuevoAggEntorno(t, 45*time.Second)
		var hint *flowruntime.IntentHint
		if conIntent {
			// La forma FIJADA del intent (T1.3): nombre y confianza, cero campos de
			// producto. Aquí no hay params que pasar porque el tipo no los tiene.
			hint = &flowruntime.IntentHint{Name: flowruntime.IntentIntakeRequest, Confidence: 0.88}
		}
		e.observa(ctx, "wa-1", nil)
		e.clock.avanza(1 * time.Second)
		e.sink.Observe(ctx, flowruntime.IncomingRef{
			Key: aggKey(), WaMessageID: "wa-2", MessageTS: e.clock.now(), Intent: hint,
		})
		if !conIntent {
			e.clock.avanza(45 * time.Second) // el camino garantizado: el plazo
		}
		if n := e.sink.Sweep(ctx); n != 1 {
			t.Fatalf("conIntent=%v: se esperaba UN cierre, hubo %d", conIntent, n)
		}
		return e.jobs.Jobs()[0]
	}

	conIntent := corre(t, true)
	sinIntent := corre(t, false)

	if conIntent.Status != sinIntent.Status {
		t.Fatalf("los dos caminos dejan estados distintos: %q vs %q", conIntent.Status, sinIntent.Status)
	}
	if len(conIntent.SourceRefs) != len(sinIntent.SourceRefs) {
		t.Fatalf("refs distintas: %v vs %v", conIntent.SourceRefs, sinIntent.SourceRefs)
	}
	for i := range conIntent.SourceRefs {
		if conIntent.SourceRefs[i] != sinIntent.SourceRefs[i] {
			t.Fatalf("ref %d distinta: %q vs %q", i, conIntent.SourceRefs[i], sinIntent.SourceRefs[i])
		}
	}
	if !conIntent.MessageTS.Equal(sinIntent.MessageTS) {
		t.Fatalf("message_ts distinto: %v vs %v", conIntent.MessageTS, sinIntent.MessageTS)
	}
}

// TestT17a_RafagaCompletaSinNingunIntent_FlusheaPorVentana es T1.7 (a) literal: la
// ráfaga entera SIN un solo ClassifiedIntent —el caso que el despachador del Edge
// produce cuando se le acaba el presupuesto de espera— abre la ventana, la acumula
// y la cierra a los N segundos. No hay respaldo que probar: éste ES el camino.
//
// MUTACIÓN (compila): en aggregator.go, en la función due, sustituir la última
// línea por
//
//	return false
//
// Es la MISMA mutación que rompe el test del silencio, y eso es el punto: si el
// plazo deja de cerrar ventanas, un tenant cuyo Edge nunca clasifica se queda sin
// un solo presupuesto y NADIE ve un error.
func TestT17a_RafagaCompletaSinNingunIntent_FlusheaPorVentana(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)

	for _, wa := range []string{"wa-1", "wa-2", "wa-3", "wa-4", "wa-5"} {
		e.observa(ctx, wa, nil) // nil = el Edge despachó SIN intent. Es normal, no una avería.
		e.clock.avanza(4 * time.Second)
	}
	if n := len(e.jobs.Jobs()); n != 1 {
		t.Fatalf("cinco mensajes seguidos son UNA ventana, hay %d jobs", n)
	}
	// Han pasado 20 s desde el primero: todavía no.
	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("a los 20 s no toca; se cerraron %d", n)
	}
	e.clock.avanza(25 * time.Second) // 45 s desde el PRIMER mensaje
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("a los 45 s del primer mensaje tenía que cerrarse; se cerraron %d", n)
	}
	j := e.jobs.Jobs()[0]
	if j.Status != intake.StatusPending {
		t.Fatalf("status = %q; se esperaba %q", j.Status, intake.StatusPending)
	}
	if len(j.SourceRefs) != 5 {
		t.Fatalf("el job tenía que llevar las 5 referencias, lleva %v", j.SourceRefs)
	}
}

// TestT17b_IntentTardio_NoAbreSegundoJobNiDuplica es T1.7 (b): el intent llega
// DESPUÉS de que la ventana ya se cerró por su plazo. No puede reabrirla, no puede
// duplicar el job y no puede volver a cerrarla.
//
// ⚠️ Lo que SÍ pasa —y es correcto— es que el mensaje que trae el intent tardío
// abre una ventana NUEVA sobre el mismo evento: el índice de la 0072 es PARCIAL a
// propósito para que un cliente pueda volver a pedir sobre la misma conversación.
// Lo que este test fija es que el job VIEJO no se toca.
//
// MUTACIÓN (compila, y es el defecto REAL que este test encontró mientras se
// escribía): en aggregator.go, en closeWindow, añadir justo después de la comprobación
// de "cerrada" la línea
//
//	s.mu.Lock(); delete(s.seen, job.Key); s.mu.Unlock()
//
// Al olvidar el último id visto, la re-entrega del MISMO wa_message_id vuelve a
// pasar la guarda y ABRE UNA VENTANA NUEVA con un mensaje ya procesado.
//
// 🔧 DÓNDE MUERE, corregido el 2026-08-22: en el PRIMER bloque de asserts, no en el
// segundo. La ventana nueva que abre el mensaje re-entregado hereda la pista del
// intent tardío (0.95 dispara), así que el barrido siguiente la cierra y el test se
// para ya en `el intent tardío cerró %d ventanas`. Si alguien quitara ese aserto,
// caería en el de `duplicó el job`. Los dos son del primer bloque.
//
// MUTACIÓN 2 (compila, y muerde SOLO el segundo bloque): en aggregator.go, en
// alreadySeen, sustituir
//
//	if s.seen[ref.Key] == ref.WaMessageID {
//
// por
//
//	if _, vista := s.seen[ref.Key]; vista {
//
// («ref.WaMessageID» se sigue usando en la línea siguiente, así que compila). La
// guarda pasa de descartar EL MISMO mensaje a bloquear CUALQUIER mensaje posterior
// de la tupla: el primer bloque sigue verde —el reenvío se sigue descartando— y el
// segundo se pone rojo, porque «wa-2», que es un mensaje NUEVO, ya no abre la
// ventana siguiente. Es la guarda que bloquea DE MÁS, y por eso hace falta la mitad
// positiva de este test.
//
// ⚠️ LA QUE ESTABA AQUÍ ANTES NO MORDÍA Y SE MUDÓ. Era «en memory.go, en
// CloseWindow, sustituir `live := m.liveLocked(k)` por `live := m.jobs[0]`»:
// compila, pero este test nunca la ve, porque `Sweep` solo llama a `CloseWindow`
// con lo que `ListAggregating` ya filtró por `status='aggregating'` — cuando el
// intent tardío barre, no hay ninguna ventana viva y `CloseWindow` no se llama.
// Esa mutación vive ahora donde sí muerde: en
// TestCloseWindow_DosLlamadasSeguidas_LaSegundaNoTocaLaFila, que llama al guard de
// idempotencia DIRECTAMENTE.
func TestT17b_IntentTardio_NoAbreSegundoJobNiDuplica(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)

	e.observa(ctx, "wa-1", nil)
	e.clock.avanza(45 * time.Second)
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("la ventana tenía que cerrarse por plazo; se cerraron %d", n)
	}
	cerradoEn := e.jobs.Jobs()[0].UpdatedAt

	// El intent llega tarde, montado sobre el MISMO wa_message_id que ya se procesó
	// (el caso del reenvío) — no puede reabrir ni duplicar nada.
	e.clock.avanza(1 * time.Second)
	e.sink.Observe(ctx, flowruntime.IncomingRef{
		Key: aggKey(), WaMessageID: "wa-1", MessageTS: e.clock.now(),
		Intent: &flowruntime.IntentHint{Name: flowruntime.IntentIntakeRequest, Confidence: 0.95},
	})
	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("el intent tardío cerró %d ventanas; no tenía que cerrar ninguna", n)
	}
	jobs := e.jobs.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("el intent tardío duplicó el job: hay %d", len(jobs))
	}
	if !jobs[0].UpdatedAt.Equal(cerradoEn) {
		t.Fatalf("el job cerrado se volvió a tocar (updated_at %v -> %v)", cerradoEn, jobs[0].UpdatedAt)
	}
	if len(jobs[0].SourceRefs) != 1 {
		t.Fatalf("source_refs del job cerrado creció: %v", jobs[0].SourceRefs)
	}

	// LA OTRA MITAD, y hay que probarla o el test de arriba pasaría también con una
	// guarda que bloquease DE MÁS: un mensaje NUEVO sobre el mismo evento SÍ tiene
	// que abrir la ventana siguiente. El índice de la 0072 es PARCIAL justo para
	// esto — un cliente puede volver a pedir sobre la misma conversación.
	e.clock.avanza(1 * time.Second)
	e.observa(ctx, "wa-2", nil)
	jobs = e.jobs.Jobs()
	if len(jobs) != 2 {
		t.Fatalf("un mensaje NUEVO tras el flush tiene que abrir otra ventana; hay %d jobs", len(jobs))
	}
}

// TestCloseWindow_DosLlamadasSeguidas_LaSegundaNoTocaLaFila es EL TEST DEL GUARD DE
// IDEMPOTENCIA, y hasta el 2026-08-22 no existía.
//
// # POR QUÉ HACÍA FALTA, aunque tres tests dijeran probarlo
//
// TestSilencio_…, TestIntent_… y TestT17b llevan un «doble flush no duplica jobs» y
// los tres afirman esa idempotencia POR LA RAZÓN EQUIVOCADA. El segundo `Sweep`
// devuelve 0 porque `ListAggregating` FILTRA por `status='aggregating'` y ya no
// encuentra nada que mirar — no porque el guard de `CloseWindow` haga su trabajo. La
// prueba de que no lo estaban probando: romper el guard del doble
// (`live := m.jobs[0]` en vez de `live := m.liveLocked(k)`) COMPILA y no ponía rojo
// nada, porque por el camino del barrido `CloseWindow` nunca llega a ver una fila ya
// cerrada.
//
// Ese guard NO es decorativo: en producción es el `UPDATE … WHERE
// status='aggregating'` de Postgres, y es lo ÚNICO que separa a DOS PROCESOS
// barriendo a la vez —dos réplicas del cloud, o un barrido y un reinicio— de dos
// jobs sobre la misma ventana. La carrera real es justo la que el filtro del listado
// no puede evitar: dos barridos listan a la vez, los dos ven la ventana viva y los
// dos llaman a cerrar. Por eso este test llama a `CloseWindow` DIRECTAMENTE dos
// veces sobre la MISMA WindowKey, que es lo que hace la carrera, y no a `Sweep`.
//
// MUTACIÓN (compila, y es la que quedó huérfana al reescribir TestT17b): en
// internal/intake/memory.go, en CloseWindow, sustituir
//
//	live := m.liveLocked(k)
//
// por
//
//	live := m.jobs[0]
//
// («liveLocked» se sigue usando desde OpenOrAppend, así que el paquete compila). El
// doble pierde el guard de estado: la segunda llamada vuelve a marcar `pending` la
// fila ya cerrada, devuelve true y le mueve el `updated_at`. Los tres asertos de
// abajo se ponen rojos.
//
// MUTACIÓN 2 (compila, y es la misma pérdida vista por el otro lado): en el mismo
// CloseWindow, sustituir
//
//	if live == nil {
//		return false, nil // idempotente: ya estaba cerrada (o nunca existió).
//	}
//
// por
//
//	if live == nil {
//		return true, nil
//	}
//
// Ahora el cierre MIENTE: dice que cerró una ventana que no existía. En producción
// eso es un `ComposeAtFlush` de más por cada barrido que llegue tarde —el compositor
// se llama SOLO si `cerrada` es true (aggregator.go, closeWindow)—, y aquí se ve en
// el aserto del segundo `escrito`.
func TestCloseWindow_DosLlamadasSeguidas_LaSegundaNoTocaLaFila(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)

	e.observa(ctx, "wa-1", nil)
	k := aggKey()

	// PRIMERA llamada: es la que cierra de verdad.
	escrito, err := e.jobs.CloseWindow(ctx, k)
	if err != nil {
		t.Fatalf("el primer cierre no puede fallar: %v", err)
	}
	if !escrito {
		t.Fatal("el primer CloseWindow sobre una ventana viva tiene que cerrarla y decir que sí")
	}
	primero := e.jobs.Jobs()
	if len(primero) != 1 || primero[0].Status != intake.StatusPending {
		t.Fatalf("tras el primer cierre tiene que haber UNA fila en pending: %+v", primero)
	}
	cerradoEn := primero[0].UpdatedAt

	// SEGUNDA llamada, sobre la MISMA clave y sin pasar por el barrido: es la
	// carrera de dos procesos. El reloj se mueve ANTES para que, si el guard no
	// estuviera, el `updated_at` cambiara de valor y el aserto lo notara — con el
	// reloj quieto, una segunda escritura sería indistinguible de no escribir.
	e.clock.avanza(3 * time.Second)
	escrito, err = e.jobs.CloseWindow(ctx, k)
	if err != nil {
		t.Fatalf("un segundo cierre no es un error, es un no-op: %v", err)
	}
	if escrito {
		t.Fatal("el segundo CloseWindow NO cerró nada y tiene que decirlo: si devuelve true, " +
			"el agregador compondría el literal DOS veces sobre la misma ventana")
	}

	// Y LA FILA NO SE TOCÓ. Esto es lo que de verdad afirma «no duplica»: no basta
	// con que no aparezca un job nuevo —el store no tiene por dónde crearlo—, hace
	// falta que la fila que ya estaba siga byte a byte donde estaba.
	segundo := e.jobs.Jobs()
	if len(segundo) != 1 {
		t.Fatalf("el segundo cierre no puede materializar una fila: hay %d", len(segundo))
	}
	if !segundo[0].UpdatedAt.Equal(cerradoEn) {
		t.Fatalf("el segundo cierre tocó la fila (updated_at %v -> %v)", cerradoEn, segundo[0].UpdatedAt)
	}
	if segundo[0].Status != intake.StatusPending {
		t.Fatalf("status = %q; la ventana ya estaba cerrada y ahí se queda", segundo[0].Status)
	}
	// Las DOS llamadas llegaron al store: si el contador dijera 1, este test estaría
	// probando que alguien filtró antes y no que el guard existe.
	if c := e.jobs.Counters(); c.Close != 2 {
		t.Fatalf("las dos llamadas tienen que haber llegado al guard; Close=%d", c.Close)
	}
}

// ---------------------------------------------------------------------------
// T1.1 — la recuperación del reinicio
// ---------------------------------------------------------------------------

// TestReinicioSimulado_ElJobNoSePierdeYElRecoveryLoCierra es el criterio de T1.1
// «reinicio simulado ⇒ el job no se pierde».
//
// El reinicio se simula construyendo un AggregatorSink NUEVO sobre el MISMO store:
// eso tira exactamente lo que un despliegue tira —las pistas de adelanto y la
// memoria de deduplicación, que viven en el proceso— y conserva exactamente lo que
// sobrevive: la fila en intake_jobs. Es la razón de que esta tabla exista en vez de
// un mapa en memoria, y la razón de que aquí no haya un time.AfterFunc por ventana.
//
// MUTACIÓN (compila): en aggregator.go, en RecoverAtBoot, sustituir
//
//	n := s.Sweep(ctx)
//
// por
//
//	n := 0
//
// El agregador arranca sin mirar la tabla y las ventanas que vencieron mientras el
// proceso no estaba se quedan en aggregating hasta el primer tick.
func TestReinicioSimulado_ElJobNoSePierdeYElRecoveryLoCierra(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)

	e.observa(ctx, "wa-1", nil)
	e.clock.avanza(2 * time.Second)
	e.observa(ctx, "wa-2", nil)

	// 💥 REINICIO. El proceso muere con la ventana abierta.
	e.clock.avanza(10 * time.Minute)
	renacido := flowruntime.NewAggregatorSink(aggLogger(), e.jobs, e.cfg, e.ents,
		flowruntime.WithAggregatorClock(e.clock.now))

	if n := renacido.RecoverAtBoot(ctx); n != 1 {
		t.Fatalf("el recovery tenía que cerrar la ventana vencida; cerró %d", n)
	}
	jobs := e.jobs.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("el job no puede perderse ni duplicarse en un reinicio: hay %d", len(jobs))
	}
	if jobs[0].Status != intake.StatusPending {
		t.Fatalf("status tras el recovery = %q; se esperaba %q", jobs[0].Status, intake.StatusPending)
	}
	if len(jobs[0].SourceRefs) != 2 {
		t.Fatalf("las referencias acumuladas antes del reinicio tenían que sobrevivir: %v", jobs[0].SourceRefs)
	}
}

// ---------------------------------------------------------------------------
// D-044.26 — EL PRESUPUESTO. Este es el corazón de la ola.
// ---------------------------------------------------------------------------

// TestPresupuestoDelEntrante_UnaEscrituraYCeroLecturas sustituye al criterio de
// benchmark que T1.5 (c) pedía y que no se podía ejecutar (en este repo no existe
// un solo func Benchmark, y el número del 050 mide el Ack, que no pasa por aquí).
//
// Cuenta lo que el cambio SÍ toca: cuántas veces el camino del entrante habla con
// la base. La respuesta tiene que ser UNA escritura y CERO lecturas, por entrante,
// siempre — con intent y sin intent, abriendo la ventana y ampliándola.
//
// # 🔧 EL PERÍMETRO SE AMPLIÓ EL 2026-08-22, Y ESA ERA LA MITAD QUE FALTABA
//
// Hasta esa revisión este test contaba SOLO `intake_jobs`, y D-044.26 dice tres
// cosas, no una: cero SELECT para encontrar el job abierto, cero SELECT PARA LEER
// `tenant_settings`, y el gate por el resolver CACHEADO. Con el perímetro viejo,
// esta mutación COMPILABA y dejaba la suite entera en verde:
//
//	// en Observe, justo antes de OpenOrAppend
//	_ = s.windowFor(ctx, ref.Key.TenantID)
//
// que es un incumplimiento LITERAL del criterio. Ahora se cuentan las tres
// superficies: `intake_jobs` (por intake.MemoryStore), `tenant_settings` (por
// settingsContadas) y el resolver (por entsContados).
//
// # POR QUÉ ENTITLEMENTS ES «COMO MUCHO UNA» Y NO «CERO»
//
// Porque el gate SÍ corre en línea, a propósito: es lo que hace fail-closed al
// agregador y por eso se eligió el resolver con caché de TTL
// (internal/entitlements/postgres.go) y no el `SELECT` sin caché del WebhookSink.
// Lo que este test fija es que sea UNA pregunta y no dos —nadie ha metido un
// segundo gate— y, sobre todo, que la respuesta no se pida ANTES de las guardas
// baratas (ver TestSinEventoVivo_NoAbreVentana, que exige CERO).
//
// MUTACIÓN (compila, y es la que de verdad importa): en aggregator.go, dentro de
// Observe, añadir justo antes de la llamada a s.jobs.OpenOrAppend la línea
//
//	_, _ = s.jobs.ListAggregating(ctx, 1)
//
// Eso es exactamente el SELECT «para encontrar el job abierto» que D-044.26
// prohíbe, y este test es lo único que lo delata.
//
// MUTACIÓN 2 (compila): añadir, en el mismo sitio,
//
//	_, _ = s.jobs.CloseWindow(ctx, ref.Key)
//
// que es el «flush inmediato ejecutado en línea» — la forma tentadora de
// implementar T1.2 y la que mete una segunda sentencia en el camino del mensaje.
//
// MUTACIÓN 3 (compila, y es la que el perímetro viejo no veía): en el mismo sitio,
//
//	_ = s.windowFor(ctx, ref.Key.TenantID)
//
// La ventana del tenant se lee en línea con el mensaje. Pone rojo el aserto de
// `tenant_settings` y NINGÚN otro test del árbol.
//
// MUTACIÓN 4 (compila): en aggregator.go, dentro de Observe, DUPLICAR la línea del
// gate justo debajo de la que ya está:
//
//	_, _ = s.ents.Has(ctx, ref.Key.TenantID, featureIntakeAggregation)
//
// Es el segundo gate añadido «por si acaso»: cada mensaje del cliente paga dos
// consultas en vez de una. Pone rojo el aserto del resolver.
func TestPresupuestoDelEntrante_UnaEscrituraYCeroLecturas(t *testing.T) {
	ctx := context.Background()

	casos := []struct {
		nombre string
		previo bool
		hint   *flowruntime.IntentHint
	}{
		{nombre: "abre la ventana, sin intent", previo: false, hint: nil},
		{nombre: "amplía la ventana, sin intent", previo: true, hint: nil},
		{nombre: "abre la ventana, con intent que dispara", previo: false,
			hint: &flowruntime.IntentHint{Name: flowruntime.IntentIntakeRequest, Confidence: 0.99}},
		{nombre: "amplía la ventana, con intent que dispara", previo: true,
			hint: &flowruntime.IntentHint{Name: flowruntime.IntentIntakeRequest, Confidence: 0.99}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			e := nuevoAggEntorno(t, 45*time.Second)
			if c.previo {
				e.observa(ctx, "wa-0", nil)
				e.clock.avanza(time.Second)
			}
			// El presupuesto se mide sobre UN entrante concreto, con el escenario ya
			// montado. Los TRES contadores se ponen a cero a la vez, o el entrante
			// previo contaminaría la medida.
			e.jobs.ResetCounters()
			e.cfgCnt.olvida()
			e.entsCnt.olvida()

			e.observa(ctx, "wa-medido", c.hint)

			got := e.jobs.Counters()
			if got.OpenOrAppend != 1 {
				t.Fatalf("el camino del entrante tiene que escribir EXACTAMENTE UNA vez; escribió %d", got.OpenOrAppend)
			}
			if got.Reads != 0 {
				t.Fatalf("el camino del entrante NO puede leer intake_jobs; leyó %d veces (D-044.26)", got.Reads)
			}
			if got.Close != 0 {
				t.Fatalf("el cierre NO ocurre en línea con el mensaje; hubo %d (el adelanto por intent es una PISTA, la ejecuta el barrido)", got.Close)
			}
			if got.PutSourceText != 0 {
				t.Fatalf("el sobre del literal se llena AL FLUSH; en línea con el mensaje hubo %d escrituras", got.PutSourceText)
			}
			// LA SEGUNDA TABLA. D-044.26 la nombra con estas palabras: «CERO SELECT …
			// ni para leer `tenant_settings` (la ventana se lee en el barrido, fuera de
			// línea)».
			if n := e.cfgCnt.lecturas(); n != 0 {
				t.Fatalf("el camino del entrante leyó %d veces tenant_settings; la ventana se resuelve "+
					"en el BARRIDO, fuera de línea (D-044.26)", n)
			}
			// EL GATE. Una consulta —la del resolver cacheado— y ni una más.
			if n := e.entsCnt.consultas(); n != 1 {
				t.Fatalf("el gate tiene que ser UNA consulta al resolver cacheado; hubo %d", n)
			}
		})
	}
}

// TestFalloDelStore_NoTumbaElTurno es INV-10 / la condición heredada de T0.4: un
// fallo del agregador se LOGUEA y el turno del cliente sigue. Observe no devuelve
// error, así que la garantía es estructural (no hay valor que propagar) y lo que
// este test fija es que además no entra en pánico y no deja el estado a medias.
//
// 🔧 EL ASERTO DE LA ESCRITURA INTENTADA se añadió el 2026-08-22, y es el que
// convierte esto en un test. Sin él, los otros dos asertos —cero jobs, cero
// cierres— pasaban tan ricamente con `Observe` VACÍA: un negativo puro no distingue
// «lo intentó y falló como debía» de «no llegó a intentarlo». `OpenOrAppend == 1`
// dice que el camino del entrante recorrió sus guardas, pasó el gate y llegó hasta
// la sentencia; lo que falló fue la base, que es el escenario que este test monta.
//
// MUTACIÓN (compila): en aggregator.go, dentro de Observe, sustituir el cuerpo del
// if de error de OpenOrAppend por
//
//	panic(err)
//
// Un fallo transitorio de la base tumbaría la goroutine del entrante.
//
// MUTACIÓN 2 (compila, y es la que solo ve el aserto nuevo): en aggregator.go, en
// acceptable, añadir como PRIMER caso del switch
//
//	case true:
//		return false
//
// Observe se vuelve un no-op integral. Con el test viejo esto quedaba VERDE.
func TestFalloDelStore_NoTumbaElTurno(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)
	e.jobs.FailOpenWith(errors.New("la base dijo que no"))

	e.observa(ctx, "wa-1", &flowruntime.IntentHint{Name: flowruntime.IntentIntakeRequest, Confidence: 0.99})

	// LO INTENTÓ. El fallo es de la base, no de una guarda que se comió el entrante.
	if c := e.jobs.Counters(); c.OpenOrAppend != 1 {
		t.Fatalf("el entrante tenía que llegar a la sentencia y que la base lo rechazara; "+
			"OpenOrAppend=%d (si es 0, este test no está probando el fallo del store sino que Observe no hace nada)", c.OpenOrAppend)
	}
	if n := len(e.jobs.Jobs()); n != 0 {
		t.Fatalf("con el store caído no se escribe nada; hay %d jobs", n)
	}
	// Y el barrido siguiente tampoco revienta ni inventa un cierre.
	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("no hay nada que cerrar; se cerraron %d", n)
	}
}

// TestVentanaCero_EsFlushInmediato fija que el 0 es un OVERRIDE LEGÍTIMO y no un
// «sin configurar»: significa sin agregación, un pipeline por mensaje — que es lo
// que el sistema hacía antes de esta ola. Lo dice el CHECK >= 0 de la 0072 y el
// COMMENT de la columna.
//
// MUTACIÓN (compila — ver el aviso de abajo): en aggregator.go, en windowFor,
// sustituir la última línea
//
//	return cfg.AggregationWindow
//
// por ESTAS DOS:
//
//	_ = cfg
//	return store.DefaultAggregationWindow
//
// Es exactamente el defecto de «sustituir ceros por defaults» que
// repository_postgres.go prohíbe por escrito: el override explícito del tenant se
// convierte en 45 s sin que nadie se entere.
//
// 🔴 EL `_ = cfg` NO ES ADORNO Y POR ESO VA EN LA MUTACIÓN. Tal como estaba escrita
// —solo cambiando el `return`— la mutación NO COMPILABA: `cfg` quedaba declarada por
// el `cfg, err := s.settings.GetTenantSettings(...)` y sin usar, que en Go es un
// error de compilación, no un aviso. Una mutación que no compila no prueba nada
// sobre el test: solo dice que el compilador funciona. (La otra forma válida sería
// cambiar la asignación a `_, err := s.settings.GetTenantSettings(...)`, que también
// compila porque `err` sigue siendo una variable nueva; se elige la de arriba por
// tocar un solo sitio.)
//
// ⚠️ Y una que NO sirve, anotada para que nadie la reintente: mutar el `if win <= 0`
// de la función due. Con win=0 la comparación de plazo también cierra, así que el
// test seguiría verde. Lo que hay que romper es DE DÓNDE SALE EL NÚMERO.
func TestVentanaCero_EsFlushInmediato(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 0)

	e.observa(ctx, "wa-1", nil)
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("con la ventana en 0 el flush es inmediato; se cerraron %d", n)
	}
	// Y el número salió de la config del tenant, no de un default: el barrido SÍ
	// tiene que leer `tenant_settings` (es lo que D-044.26 le permite justo por
	// correr fuera del camino del entrante). Cero lecturas aquí significaría que el
	// 0 del tenant no se está consultando y el verde de arriba sería casualidad.
	if n := e.cfgCnt.lecturas(); n != 1 {
		t.Fatalf("el barrido tenía que leer la ventana del tenant UNA vez (memo por pasada); leyó %d", n)
	}
}

// TestSinEventoVivo_NoAbreVentana: intake_jobs.event_id es NOT NULL y la fuente del
// literal (el hilo) cuelga del evento. Un entrante sin evento —el LIMBO: un saludo,
// la cháchara— no abre nada, y la guarda es BARATA (no consulta entitlements).
//
// 🔧 «LA GUARDA ES BARATA» AHORA SE COMPRUEBA, no se promete. Hasta el 2026-08-22
// esa frase estaba en el comentario y en ningún aserto: el doble de entitlements no
// contaba nada, así que el ORDEN —guardas baratas primero, gate después— no lo
// fijaba nadie. Con `entsContados` sí: un entrante del LIMBO no le cuesta al sistema
// ni una consulta al resolver. Importa de verdad porque el LIMBO no es raro: los
// saludos y la cháchara sin evento son tráfico normal y constante, y pagarles un
// gate a cada uno es pagar el caso mayoritario.
//
// MUTACIÓN (compila): en aggregator.go, en acceptable, sustituir
//
//	case !ref.Key.Valid():
//
// por
//
//	case ref.Key.TenantID == "":
//
// El agregador intentaría escribir una fila con event_id vacío, que en Postgres es
// un error de NOT NULL en cada entrante sin evento. Pone rojos los DOS asertos: se
// escribe, y además se paga el gate.
//
// MUTACIÓN 2 (compila, y pone rojo SOLO el aserto del resolver): en aggregator.go,
// dentro de Observe, mover el gate ARRIBA del todo — añadir como primera línea del
// cuerpo, antes del `if !s.acceptable(ref) {`:
//
//	_, _ = s.ents.Has(ctx, ref.Key.TenantID, featureIntakeAggregation)
//
// El entrante sigue sin abrir ventana (la guarda barata sigue ahí, solo que ahora
// llega tarde) pero el LIMBO entero pasa a costar una consulta por mensaje. Es el
// orden invertido que el patrón de thread.go existe para evitar, y sin este aserto
// nadie lo notaría.
func TestSinEventoVivo_NoAbreVentana(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)

	k := aggKey()
	k.EventID = ""
	e.sink.Observe(ctx, flowruntime.IncomingRef{Key: k, WaMessageID: "wa-1", MessageTS: e.clock.now()})

	if c := e.jobs.Counters(); c.OpenOrAppend != 0 {
		t.Fatalf("sin evento no se escribe nada; hubo %d escrituras", c.OpenOrAppend)
	}
	if n := e.entsCnt.consultas(); n != 0 {
		t.Fatalf("la guarda es BARATA y va ANTES del gate: un entrante sin evento no puede costar "+
			"una consulta de entitlements; hubo %d", n)
	}
}

// TestMismoMensajeDosVeces_NoDuplicaLaReferencia: el DO UPDATE concatena a ciegas
// (no lee, no puede comprobar nada), así que la deduplicación tiene que estar aquí.
// Es la red SECUNDARIA: la primera es duplicateIngest (Plan 028 · T6).
//
// MUTACIÓN (compila): en aggregator.go, en alreadySeen, sustituir
//
//	if s.seen[ref.Key] == ref.WaMessageID {
//
// por
//
//	if false {
//
// (el mapa se sigue escribiendo en la línea siguiente, así que "s.seen" sigue usado
// y el paquete compila).
func TestMismoMensajeDosVeces_NoDuplicaLaReferencia(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)

	e.observa(ctx, "wa-1", nil)
	e.observa(ctx, "wa-1", nil)

	jobs := e.jobs.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("hay %d jobs, tenía que haber 1", len(jobs))
	}
	if len(jobs[0].SourceRefs) != 1 {
		t.Fatalf("source_refs = %v; la referencia repetida no puede entrar dos veces", jobs[0].SourceRefs)
	}
}

// ---------------------------------------------------------------------------
// LAS TRES OPCIONES DE CONSTRUCCIÓN QUE NO TENÍAN CONSUMIDOR (revisión 2026-08-22)
// ---------------------------------------------------------------------------
//
// WithSweepInterval, WithSweepBatch y WithIntentConfidence nacieron exportadas y sin
// que nadie las llamara: ni bootstrap.go ni un test. Eso es exactamente lo que D-044.23
// condena —superficie exportada sin consumidor—, y la salida elegida NO es retirarlas
// sino EJERCERLAS aquí, que es donde tienen sentido de verdad: las tres existen para
// que el despliegue pueda mover un número, y un test que fija ese número explícitamente
// es a la vez el consumidor que faltaba y la prueba de que el número se respeta.
//
// El criterio con el que se decidió, por si alguien las vuelve a mirar: una opción de
// construcción se queda si un test la NECESITA para ser determinista o para separar un
// default de plataforma de una decisión de despliegue. Las tres pasan ese filtro. Si
// alguna dejara de tener llamante, vuelve a ser deuda: retírala o dale uno.

// TestUmbralDeConfianzaInyectado_MandaSobreElDefaultDePlataforma da consumidor a
// WithIntentConfidence y, de paso, prueba lo único que esa opción promete: que el
// umbral inyectado SUSTITUYE al default de plataforma (0.7, defaultIntentConfidence) en
// vez de sumarse a él.
//
// La confianza elegida para el segundo mensaje —0.6— está DELIBERADAMENTE entre los dos
// números: por encima del umbral inyectado (0.5) y por debajo del default. Con la opción
// respetada adelanta; con la opción ignorada, no. No hay forma de que este test pase por
// accidente.
//
// MUTACIÓN (compila): en aggregator.go, en WithIntentConfidence, sustituir el cuerpo
//
//	if min > 0 {
//	    s.intentThreshold = min
//	}
//
// por
//
//	_ = min
//
// El parámetro sigue usado, el paquete compila, y el agregador vuelve a decidir con el
// 0.7 de plataforma: el segundo mensaje deja de adelantar y el segundo Sweep devuelve 0.
func TestUmbralDeConfianzaInyectado_MandaSobreElDefaultDePlataforma(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)
	sink := flowruntime.NewAggregatorSink(aggLogger(), e.jobs, e.cfg, e.ents,
		flowruntime.WithAggregatorClock(e.clock.now),
		flowruntime.WithIntentConfidence(0.5))

	// 0.4 < 0.5: por debajo del umbral INYECTADO, no adelanta nada.
	sink.Observe(ctx, flowruntime.IncomingRef{
		Key: aggKey(), WaMessageID: "wa-1", MessageTS: e.clock.now(),
		Intent: &flowruntime.IntentHint{Name: flowruntime.IntentIntakeRequest, Confidence: 0.4},
	})
	if n := sink.Sweep(ctx); n != 0 {
		t.Fatalf("0.4 está por debajo del umbral inyectado (0.5) y no puede adelantar; se cerraron %d", n)
	}

	// 0.6: por encima del inyectado y por DEBAJO del default de plataforma (0.7).
	e.clock.avanza(time.Second)
	sink.Observe(ctx, flowruntime.IncomingRef{
		Key: aggKey(), WaMessageID: "wa-2", MessageTS: e.clock.now(),
		Intent: &flowruntime.IntentHint{Name: flowruntime.IntentIntakeRequest, Confidence: 0.6},
	})
	if n := sink.Sweep(ctx); n != 1 {
		t.Fatalf("0.6 supera el umbral inyectado (0.5) y tenía que adelantar el cierre; se cerraron %d "+
			"(si es 0, la opción se está ignorando y manda el 0.7 de plataforma)", n)
	}
}

// TestTechoDeTrabajoPorPasada_ElBarridoNoMiraMasDeLoQueSeLeDijo da consumidor a
// WithSweepBatch y fija lo que el docstring de defaultSweepBatch promete: el batch es un
// TECHO DE TRABAJO POR PASADA, no un límite de negocio. Lo que no entra sale en el tick
// siguiente y no se pierde nada.
//
// Tres ventanas de tres eventos distintos —el índice único de la 0072 es por tupla
// (tenant, sesión, contacto, evento), así que tres eventos son tres ventanas vivas— y un
// batch de 1: hacen falta tres pasadas, y a la cuarta ya no queda nada.
//
// MUTACIÓN (compila): en aggregator.go, en WithSweepBatch, sustituir el cuerpo
//
//	if n > 0 {
//	    s.sweepBatch = n
//	}
//
// por
//
//	_ = n
//
// El agregador vuelve al techo de plataforma (200), la primera pasada cierra las TRES y
// el primer assert se pone rojo.
func TestTechoDeTrabajoPorPasada_ElBarridoNoMiraMasDeLoQueSeLeDijo(t *testing.T) {
	ctx := context.Background()
	// Ventana 0 = flush inmediato: a las tres les toca ya, así que lo ÚNICO que puede
	// impedir que se cierren en la primera pasada es el techo.
	e := nuevoAggEntorno(t, 0)
	sink := flowruntime.NewAggregatorSink(aggLogger(), e.jobs, e.cfg, e.ents,
		flowruntime.WithAggregatorClock(e.clock.now),
		flowruntime.WithSweepBatch(1))

	for _, c := range []struct{ evento, wa string }{
		{"00000000-0000-0000-0000-0000000000aa", "wa-a"},
		{"00000000-0000-0000-0000-0000000000bb", "wa-b"},
		{"00000000-0000-0000-0000-0000000000cc", "wa-c"},
	} {
		k := aggKey()
		k.EventID = c.evento
		sink.Observe(ctx, flowruntime.IncomingRef{Key: k, WaMessageID: c.wa, MessageTS: e.clock.now()})
	}
	if n := len(e.jobs.Jobs()); n != 3 {
		t.Fatalf("tres eventos distintos son TRES ventanas vivas; hay %d", n)
	}

	for pasada := 1; pasada <= 3; pasada++ {
		if n := sink.Sweep(ctx); n != 1 {
			t.Fatalf("pasada %d: con el techo en 1 el barrido cierra UNA ventana; cerró %d", pasada, n)
		}
	}
	if n := sink.Sweep(ctx); n != 0 {
		t.Fatalf("a la cuarta pasada ya no queda ninguna ventana viva; se cerraron %d", n)
	}
	for _, j := range e.jobs.Jobs() {
		if j.Status != intake.StatusPending {
			t.Fatalf("lo que no entra en una pasada sale en la siguiente y NO se pierde: job %s en %q", j.ID, j.Status)
		}
	}
}

// TestRun_ElTickerCierraLaVentanaSinQueNadieLlameASweep da consumidor a
// WithSweepInterval y cubre `Run`, que hasta esta revisión no tenía un solo test: en
// producción NADIE llama a Sweep a mano —lo llama el ticker— así que el camino que de
// verdad corre era el único sin cubrir.
//
// ⚠️ CÓMO ESTÁ MONTADO PARA QUE PRUEBE LO QUE DICE, que aquí es la mitad del trabajo:
//
//   - la ventana se abre ANTES de arrancar Run y con su plazo SIN cumplir, de modo que
//     el RecoverAtBoot que Run hace al arrancar NO puede cerrarla;
//   - se duerme un instante DESPUÉS de arrancar Run y ANTES de mover el reloj, para que
//     ese RecoverAtBoot haya ocurrido ya con toda seguridad. Sin esa espera, quien
//     cerrara la ventana podría ser el arranque y no un tick, y el test estaría
//     probando otra cosa;
//   - solo entonces se cumple el plazo, así que quien cierra es NECESARIAMENTE un tick.
//
// El plazo de espera del assert (3 s) está elegido contra el default de plataforma:
// `defaultSweepInterval` son 5 s, así que si la opción se ignorase el primer tick
// llegaría DESPUÉS del plazo y el test fallaría. Con el intervalo inyectado (1 ms) el
// margen es de tres órdenes de magnitud.
//
// MUTACIÓN (compila): en aggregator.go, en WithSweepInterval, sustituir el cuerpo
//
//	if d > 0 {
//	    s.sweepEvery = d
//	}
//
// por
//
//	_ = d
//
// El ticker vuelve a los 5 s de plataforma y la espera de 3 s se agota con la ventana
// todavía en `aggregating`.
func TestRun_ElTickerCierraLaVentanaSinQueNadieLlameASweep(t *testing.T) {
	e := nuevoAggEntorno(t, 45*time.Second)
	sink := flowruntime.NewAggregatorSink(aggLogger(), e.jobs, e.cfg, e.ents,
		flowruntime.WithAggregatorClock(e.clock.now),
		flowruntime.WithSweepInterval(time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// La ventana nace con su plazo por cumplir: el RecoverAtBoot de Run no puede con ella.
	sink.Observe(ctx, flowruntime.IncomingRef{Key: aggKey(), WaMessageID: "wa-1", MessageTS: e.clock.now()})

	parado := make(chan struct{})
	go func() {
		defer close(parado)
		sink.Run(ctx)
	}()

	// Que el RecoverAtBoot del arranque quede atrás ANTES de cumplir el plazo.
	time.Sleep(20 * time.Millisecond)
	if got := e.jobs.Jobs()[0].Status; got != intake.StatusAggregating {
		t.Fatalf("el arranque de Run NO puede cerrar una ventana que no ha vencido; status=%q", got)
	}

	e.clock.avanza(45 * time.Second) // ahora sí: a partir de aquí, el primer tick la cierra.

	limite := time.Now().Add(3 * time.Second)
	for {
		jobs := e.jobs.Jobs()
		if len(jobs) == 1 && jobs[0].Status == intake.StatusPending {
			break
		}
		if time.Now().After(limite) {
			t.Fatalf("el barrido periódico no cerró la ventana en 3 s (jobs=%v); "+
				"si el intervalo inyectado se ignora, el primer tick llega a los 5 s", jobs)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Y Run tiene que soltar cuando se cancela el contexto: es un bucle de proceso, no
	// una goroutine fugada.
	cancel()
	select {
	case <-parado:
	case <-time.After(2 * time.Second):
		t.Fatal("Run no retornó al cancelar el contexto")
	}
}
