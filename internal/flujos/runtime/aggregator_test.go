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
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
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
	sink *flowruntime.IntakeAggregator
	jobs *intake.MemoryStore
	// cfg y ents son los dobles DE VERDAD (los que un test siembra o apaga).
	cfg  *store.MemoryRepository
	ents *entitlements.Fake
	// cfgCnt y entsCnt son los envoltorios que ve el sink: lo mismo, contado.
	cfgCnt  *settingsContadas
	entsCnt *entsContados
	clock   *aggReloj
}

// nuevoAggEntorno monta el entorno con el TECHO en su default de plataforma (120 s,
// T1.8-1). Los tests que no hablan del techo siguen escribiéndose igual que antes.
func nuevoAggEntorno(t *testing.T, ventana time.Duration) *aggEntorno {
	t.Helper()
	return nuevoAggEntornoConTecho(t, ventana, store.DefaultAggregationMax)
}

// nuevoAggEntornoConTecho monta el entorno fijando LOS DOS plazos de la ventana
// híbrida (T1.8-1). Existe porque los dos números son UNA regla —cierra el que venza
// antes— y hay casos que solo se pueden montar moviendo el segundo.
func nuevoAggEntornoConTecho(t *testing.T, ventana, techo time.Duration) *aggEntorno {
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
	// 🔴 EL TECHO TAMBIÉN SE NOMBRA, Y LA TRAMPA ES LA MISMA QUE LA DE LA VENTANA: un
	// AggregationMax en el cero de Go NO significa «120 por defecto» sino VENCIDO
	// SIEMPRE, o sea que el barrido cerraría toda ventana en su primera pasada y los
	// tests del silencio medirían el vacío.
	s.AggregationMax = techo
	cfg.SetTenantSettings(s)
	ents := entitlements.NewFake()
	ents.Enable(aggTenant, entitlements.FeatureLLMIntake)
	cfgCnt := contarSettings(cfg)
	entsCnt := contarEnts(ents)
	return &aggEntorno{
		// 🔴 EL SINK RECIBE LOS ENVOLTORIOS, no los dobles pelados: si recibiera los
		// dobles, el presupuesto de `tenant_settings` y el de entitlements volverían a
		// no medirse y las mutaciones de arriba volverían a quedar mudas.
		sink:    flowruntime.NewIntakeAggregator(aggLogger(), jobs, cfgCnt, entsCnt, flowruntime.WithAggregatorClock(clock.now)),
		jobs:    jobs,
		cfg:     cfg,
		ents:    ents,
		cfgCnt:  cfgCnt,
		entsCnt: entsCnt,
		clock:   clock,
	}
}

// observa mete un entrante en su ventana y, si el caso trae una clasificación,
// la entrega DESPUÉS, por el mismo camino que producción.
//
// 🔧 REESCRITO EN T1.6-4, y el cambio de forma ES el cambio del sistema: hasta la
// Ola 1.6 la intención viajaba DENTRO del entrante (`IncomingRef.Intent`) porque el
// Edge la adjuntaba al mensaje. D-044.31 mató ese push. Hoy la señal llega por una
// llamada APARTE y POSTERIOR —`OnClassified`—, que es exactamente lo que hace el pool
// cuando la inferencia termina, segundos después del turno. Los tests de política
// (umbral, nombre, idempotencia) siguen diciendo lo mismo con el mismo `hint`; lo que
// cambió es por qué puerta entra.
func (e *aggEntorno) observa(ctx context.Context, waID string, hint *flowruntime.IntentHint) {
	e.sink.Observe(ctx, flowruntime.IncomingRef{
		Key:         aggKey(),
		WaMessageID: waID,
		MessageTS:   e.clock.now(),
	})
	if hint != nil {
		e.sink.OnClassified(aggKey(), hint.Name, hint.Confidence)
	}
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
// 🔧 ES TAMBIÉN EL CRITERIO (a) DE T1.8-1 —«un mensaje y silencio ⇒ cierre a los 45 s»—
// y no hizo falta tocarlo: con UN solo mensaje, `updated_at` y `created_at` valen lo
// mismo, así que el ancla nueva del silencio da el mismo número que la vieja. El techo
// de 120 s ni se acerca a disparar. Que este test siguiera verde sin cambios es, de
// hecho, parte de lo que se quería: la ventana híbrida no cambia el caso simple.
//
// 🔧 SU MUTACIÓN SE MUDÓ DE FUNCIÓN CON T1.8-1 (la de antes citaba `job.Anchor`, que
// ya no existe). MUTACIÓN (compila): en aggregator.go, en venceElSilencio, sustituir
// la última línea
//
//	return !now.Before(job.LastActivity.Add(p.silencio))
//
// por
//
//	return false
//
// («now» y «job» son PARÁMETROS y pueden quedar sin usar; «p.silencio» sigue usada en
// la guarda del flush inmediato de la línea anterior, así que el paquete compila).
// Aquí el techo de 120 s no rescata el verde: el test barre a los 45 s.
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
			Key: aggKey(), WaMessageID: "wa-2", MessageTS: e.clock.now(),
		})
		if hint != nil {
			// Por la puerta de T1.6-4: la clasificación llega DESPUÉS del entrante.
			e.sink.OnClassified(aggKey(), hint.Name, hint.Confidence)
		}
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
// 🔧 REESCRITO POR T1.8-1 (2026-08-25), Y EL CAMBIO DE NÚMEROS ES EL CAMBIO DEL
// SISTEMA. Este test EXIGÍA que la ráfaga cerrara «a los 45 s del PRIMER mensaje», y
// eso era exactamente el defecto que T1.8-1 vino a cerrar: con cinco mensajes a
// intervalos de 4 s, el cliente todavía estaba tecleando cuando el barrido decidía que
// ya tenía bastante. Con la ventana híbrida el plazo cuenta desde el ÚLTIMO mensaje
// (t=16 s), así que cierra a t=61 s — y el techo de 120 s ni se acerca a disparar. El
// criterio de T1.7 no se cae, cambia de ancla: «flush por ventana» sigue siendo el
// camino garantizado, y sigue sin necesitar ningún intent.
//
// MUTACIÓN (compila): en aggregator.go, en venceElSilencio, sustituir la última línea
// por
//
//	return false
//
// («now» y «job» quedan sin usar dentro de la función, pero son PARÁMETROS y en Go eso
// compila; «p.silencio» sigue usada en la guarda de la línea anterior). Es la MISMA
// mutación que rompe el test del silencio, y eso es el punto: si el plazo deja de
// cerrar ventanas, un tenant cuyo Edge nunca clasifica se queda sin un solo
// presupuesto y NADIE ve un error. ⚠️ Con esa mutación este test NO se queda en 0
// para siempre: el TECHO de 120 s acabaría cerrando la ventana igual — lo que se pone
// rojo es el aserto de t=61.
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
	// Han pasado 20 s desde el primero y 4 s desde el último: todavía no.
	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("a los 20 s no toca; se cerraron %d", n)
	}
	// 🔴 EL PLAZO CUENTA DESDE EL ÚLTIMO MENSAJE (t=16 s), NO DESDE EL PRIMERO. A los
	// 45 s del primero —t=45— todavía faltan 16 para el silencio. Este aserto es el que
	// se pone rojo si alguien vuelve a anclar en el primer mensaje.
	e.clock.avanza(25 * time.Second) // t = 45 s
	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("a los 45 s del PRIMER mensaje NO toca: el silencio se mide desde el ÚLTIMO (t=16 s) "+
			"y vence en t=61. Se cerraron %d — si esto cierra, el ancla volvió a message_ts y la ráfaga "+
			"tecleada despacio vuelve a partirse en dos jobs", n)
	}
	e.clock.avanza(16 * time.Second) // t = 61 s: 45 s desde el ÚLTIMO mensaje
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("a los 45 s del ÚLTIMO mensaje tenía que cerrarse; se cerraron %d", n)
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
	})
	e.sink.OnClassified(aggKey(), flowruntime.IntentIntakeRequest, 0.95)
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
// El reinicio se simula construyendo un IntakeAggregator NUEVO sobre el MISMO store:
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
	renacido := flowruntime.NewIntakeAggregator(aggLogger(), e.jobs, e.cfg, e.ents,
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
// MUTACIÓN (compila — ver el aviso de abajo): en aggregator.go, en plazosFor,
// sustituir la última línea
//
//	return plazosDeVentana{silencio: cfg.AggregationWindow, techo: cfg.AggregationMax}
//
// por ESTAS DOS:
//
//	_ = cfg
//	return porDefecto
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
// ⚠️ Y una que NO sirve, anotada para que nadie la reintente: mutar el
// `if p.silencio <= 0` de venceElSilencio. Con silencio=0 la comparación de plazo
// también cierra, así que el test seguiría verde. Lo que hay que romper es DE DÓNDE
// SALE EL NÚMERO.
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
	sink := flowruntime.NewIntakeAggregator(aggLogger(), e.jobs, e.cfg, e.ents,
		flowruntime.WithAggregatorClock(e.clock.now),
		flowruntime.WithIntentConfidence(0.5))

	// 0.4 < 0.5: por debajo del umbral INYECTADO, no adelanta nada.
	sink.Observe(ctx, flowruntime.IncomingRef{
		Key: aggKey(), WaMessageID: "wa-1", MessageTS: e.clock.now(),
	})
	sink.OnClassified(aggKey(), flowruntime.IntentIntakeRequest, 0.4)
	if n := sink.Sweep(ctx); n != 0 {
		t.Fatalf("0.4 está por debajo del umbral inyectado (0.5) y no puede adelantar; se cerraron %d", n)
	}

	// 0.6: por encima del inyectado y por DEBAJO del default de plataforma (0.7).
	e.clock.avanza(time.Second)
	sink.Observe(ctx, flowruntime.IncomingRef{
		Key: aggKey(), WaMessageID: "wa-2", MessageTS: e.clock.now(),
	})
	sink.OnClassified(aggKey(), flowruntime.IntentIntakeRequest, 0.6)
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
	sink := flowruntime.NewIntakeAggregator(aggLogger(), e.jobs, e.cfg, e.ents,
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
	sink := flowruntime.NewIntakeAggregator(aggLogger(), e.jobs, e.cfg, e.ents,
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

// ---------------------------------------------------------------------------
// T1.6-4 — el adelanto pasa a PULL (D-044.31, REQ-09, REQ-35, INV-10)
// ---------------------------------------------------------------------------

// aheadSpy es un flowruntime.AheadRequester de mentira: apunta lo que se le pide.
type aheadSpy struct {
	mu    sync.Mutex
	pedid []pedidoAhead
}

type pedidoAhead struct {
	key   intake.WindowKey
	texto string
}

func (a *aheadSpy) Request(key intake.WindowKey, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pedid = append(a.pedid, pedidoAhead{key: key, texto: text})
}

func (a *aheadSpy) todo() []pedidoAhead {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]pedidoAhead(nil), a.pedid...)
}

// conAhead arma un agregador sobre el entorno con el espía cableado.
func (e *aggEntorno) conAhead(t *testing.T, spy *aheadSpy) *flowruntime.IntakeAggregator {
	t.Helper()
	return flowruntime.NewIntakeAggregator(aggLogger(), e.jobs, e.cfgCnt, e.entsCnt,
		flowruntime.WithAggregatorClock(e.clock.now),
		flowruntime.WithAheadRequester(spy))
}

// TestT164_ObserveEncolaLaPeticionConElTextoDelEntrante: con el push muerto, admitir un
// texto en ventana es lo que DISPARA la petición de clasificación.
//
// 💥 MUTACIÓN (compila): en aggregator.go, retirar la llamada a `s.requestAhead(ref)`
// del final de `Observe` ⇒ rojo aquí, y NINGÚN otro test del paquete se entera. Es el
// único sitio desde el que sale la petición.
func TestT164_ObserveEncolaLaPeticionConElTextoDelEntrante(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)
	spy := &aheadSpy{}
	sink := e.conAhead(t, spy)

	sink.Observe(ctx, flowruntime.IncomingRef{
		Key: aggKey(), WaMessageID: "wa-1", MessageTS: e.clock.now(),
		Text: "quiero 200 sillas para el sábado",
	})

	got := spy.todo()
	if len(got) != 1 {
		t.Fatalf("admitir un texto en ventana debe pedir UNA clasificación; se pidieron %d", len(got))
	}
	if got[0].key != aggKey() {
		t.Fatalf("la petición debe ir atada a SU ventana: %+v", got[0].key)
	}
	if got[0].texto != "quiero 200 sillas para el sábado" {
		t.Fatalf("la petición debe llevar el texto del entrante tal cual: %q", got[0].texto)
	}
}

// TestT164_SinTextoNoSePide: un mensaje de solo media no tiene nada que clasificar. Es
// un motivo SANO (REQ-38) y por eso no se pide nada ni se avisa a nadie.
func TestT164_SinTextoNoSePide(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)
	spy := &aheadSpy{}
	sink := e.conAhead(t, spy)

	sink.Observe(ctx, flowruntime.IncomingRef{
		Key: aggKey(), WaMessageID: "wa-1", MessageTS: e.clock.now(),
	})
	if got := len(spy.todo()); got != 0 {
		t.Fatalf("sin texto no hay nada que clasificar; se pidieron %d clasificaciones", got)
	}
}

// TestT164_SinLaFeatureNoSePide: el gate `llm_intake` corta ANTES de la petición. Un
// tenant sin el pipeline contratado no puede generar ni una inferencia — que además de
// ser lo correcto es lo que impide que la vía LLM de un tenant la gaste otro.
func TestT164_SinLaFeatureNoSePide(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)
	e.ents.Disable(aggTenant, entitlements.FeatureLLMIntake)
	spy := &aheadSpy{}
	sink := e.conAhead(t, spy)

	sink.Observe(ctx, flowruntime.IncomingRef{
		Key: aggKey(), WaMessageID: "wa-1", MessageTS: e.clock.now(),
		Text: "quiero 200 sillas",
	})
	if got := len(spy.todo()); got != 0 {
		t.Fatalf("sin la feature llm_intake no se abre ventana NI se pide clasificación; se pidieron %d", got)
	}
}

// TestT164_RespuestaTardia_TrasElCierre_EsINOCUA es la pregunta que el diseño del pull
// tiene que responder: la inferencia tarda segundos (p50 de campo: 8,1 s) y puede
// contestar DESPUÉS de que la ventana haya cerrado por su reloj. ¿Qué pasa entonces?
//
// NADA, y esta es la demostración: la pista se anota igual —no se comprueba nada, y no
// comprobarlo es la decisión: mirar si la ventana vive costaría un SELECT para no hacer
// nada—, pero el barrido solo mira las pistas de las ventanas que siguen `aggregating`.
// La del job ya cerrado no casa con ninguna y se tira en la misma pasada.
//
// El assert que de verdad muerde no es el «cerró 0 ventanas», sino que la FILA no se
// tocó: si un día alguien hiciera que la pista reabriera o re-cerrara algo, el estado o
// el UpdatedAt del job cambiarían.
func TestT164_RespuestaTardia_TrasElCierre_EsINOCUA(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)

	e.observa(ctx, "wa-1", nil)
	e.clock.avanza(45 * time.Second)
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("la ventana tenía que cerrarse por plazo; se cerraron %d", n)
	}
	antes := e.jobs.Jobs()[0]

	// La inferencia contesta ahora, 10 s tarde y con una confianza altísima.
	e.clock.avanza(10 * time.Second)
	e.sink.OnClassified(aggKey(), flowruntime.IntentIntakeRequest, 0.99)

	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("una respuesta posterior al cierre no puede cerrar nada; cerró %d ventanas", n)
	}
	jobs := e.jobs.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("y tampoco puede abrir un job nuevo; hay %d", len(jobs))
	}
	if jobs[0].Status != antes.Status || !jobs[0].UpdatedAt.Equal(antes.UpdatedAt) {
		t.Fatalf("la fila no se puede haber tocado: antes {status=%q updated=%v}, después {status=%q updated=%v}",
			antes.Status, antes.UpdatedAt, jobs[0].Status, jobs[0].UpdatedAt)
	}
}

// TestT164_RespuestaTardia_NoContaminaLaVentanaSIGUIENTE es el corner que el test de
// arriba no cubre y que el pull hace MUCHO más probable que el push: entre que se pide
// la clasificación y llega la respuesta pueden pasar 30 s, tiempo de sobra para que la
// ventana cierre y el cliente abra OTRA sobre el mismo evento (el índice único de la
// 0072 es PARCIAL a propósito).
//
// 🔴 LO QUE ESTE TEST FIJA ES QUE SÍ LA ADELANTA, y no es un descuido: la clave de la
// pista es la tupla (tenant, sesión, contacto, evento), no el id del job, así que la
// pista tardía cierra la ventana nueva. Está ACEPTADO y el porqué está escrito en
// `defaultIntentConfidence`: «errar por lo BAJO no rompe nada — un intent que adelanta
// de más cierra una ventana antes de tiempo y el cliente puede abrir otra sobre el
// mismo evento». Lo que sí sería un defecto es que perdiera mensajes o duplicara jobs,
// y eso es lo que se comprueba aquí.
//
// Se deja escrito para que quien lo descubra en campo sepa que se vio y se decidió, en
// vez de tratarlo como una sorpresa.
func TestT164_RespuestaTardia_NoContaminaLaVentanaSIGUIENTE(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)

	e.observa(ctx, "wa-1", nil)
	e.clock.avanza(45 * time.Second)
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("la primera ventana tenía que cerrarse por plazo; se cerraron %d", n)
	}

	// El cliente vuelve a escribir: se abre una ventana NUEVA sobre el mismo evento.
	e.clock.avanza(2 * time.Second)
	e.observa(ctx, "wa-2", nil)

	// Y ahora contesta la inferencia de la ventana ANTERIOR.
	e.sink.OnClassified(aggKey(), flowruntime.IntentIntakeRequest, 0.99)
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("DECISIÓN VIVA: la pista es por tupla, no por job, así que adelanta la ventana nueva; cerró %d", n)
	}

	jobs := e.jobs.Jobs()
	if len(jobs) != 2 {
		t.Fatalf("dos ventanas, dos jobs y ni uno más: hay %d", len(jobs))
	}
	// Lo que NO puede pasar: perder un mensaje por el camino.
	var refs []string
	for _, j := range jobs {
		refs = append(refs, j.SourceRefs...)
	}
	if len(refs) != 2 {
		t.Fatalf("los dos mensajes tienen que estar, cada uno en su ventana: %v", refs)
	}
}

// ---------------------------------------------------------------------------
// T1.8-1 — LA VENTANA HÍBRIDA (D-044.43): silencio 45 s / techo 120 s
// ---------------------------------------------------------------------------
//
// LOS CINCO CRITERIOS Y DÓNDE VIVE CADA UNO, para que nadie los busque:
//
//   (a) un mensaje y silencio ⇒ 45 s .......... TestSilencio_FlusheaALosNSegundos_…
//                                               (ya existía; sigue valiendo palabra
//                                               por palabra y el techo ni se acerca)
//   (b) ráfaga lenta de 70 s ⇒ UN job a 105 s .. TestT181b_…
//   (c) goteo cada 40 s ⇒ techo a 120 s ....... TestT181c_…
//   (d) el contexto no mueve updated_at ....... TestT181d_… (ver su docstring: hoy
//                                               pasa POR CONSTRUCCIÓN)
//   (f) message_ts sigue siendo el del 1.º .... asertado DENTRO de (b) y (c)
//   (h) el hint no espera al tick ............. TestT181h_…
//
// Y una pieza que los sostiene a todos: TestT181_ElTechoSaleDeLaConfigDelTenant, que
// impide que el 120 sea una constante escondida en el barrido.
//
// ✅ ESTOS SÍ SE HAN EJECUTADO (2026-08-25), al contrario que el aviso de la cabecera
// del fichero: `GOWORK=off go test -race ./internal/flujos/runtime/`. Y sus dos
// mutaciones de (e) y la de (h) se escribieron, se COMPILARON, se ejecutaron y se
// vieron en rojo antes de revertirlas — el rojo exacto está anotado en cada una.

// TestT181b_RafagaLentaDe70s_UnSoloJobQueCierraALos105 es el criterio (b) literal, y
// es EL defecto que esta tarea vino a cerrar dicho con el caso real: el cliente
// escribe tres mensajes separados 30 s —una sola petición, tecleada despacio— y hasta
// hoy el sistema se la partía en DOS pedidos.
//
// # POR QUÉ EL BARRIDO DE t=50 ES LA MITAD DEL TEST
//
// Sin él, el test no distinguiría «la ventana se extendió» de «nadie miró a tiempo».
// Con el ancla vieja (el PRIMER mensaje, `message_ts`), ese barrido cerraba la ventana
// —han pasado 50 s de los 45— y el tercer mensaje, a t=60, abría una ventana NUEVA:
// dos jobs, cada uno con parte del pedido, y ni un error en ningún log. El aserto
// final de «UN job» es el que cuenta esa historia.
//
// 💥 MUTACIÓN de (e) — «volver a anclar en message_ts» (compila): en
// internal/intake/memory.go, en ListAggregating, sustituir
//
//	out = append(out, OpenJob{ID: j.ID, Key: j.Key, LastActivity: j.UpdatedAt, CreatedAt: j.CreatedAt})
//
// por
//
//	out = append(out, OpenJob{ID: j.ID, Key: j.Key, LastActivity: j.MessageTS, CreatedAt: j.CreatedAt})
//
// Es EXACTAMENTE el ancla vieja (`message_ts`, el ts del primer mensaje) puesta donde
// ahora va la actividad, y es además lo que pasaría si alguien devolviera esa columna
// desde `listAggregatingSQL`. Pone rojos DOS asertos: el barrido de t=50 cierra, y al
// final hay dos jobs en vez de uno.
func TestT181b_RafagaLentaDe70s_UnSoloJobQueCierraALos105(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second) // techo = 120 s de plataforma
	primerTS := e.clock.now()

	e.observa(ctx, "wa-1", nil) // t = 0
	e.clock.avanza(30 * time.Second)
	e.observa(ctx, "wa-2", nil) // t = 30

	// t = 50: han pasado 50 s DEL PRIMER MENSAJE y solo 20 del último. Con el ancla
	// vieja esto cerraba y partía la ráfaga.
	e.clock.avanza(20 * time.Second)
	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("a los 50 s del primer mensaje —y 20 del último— la ventana NO ha vencido; se cerraron %d. "+
			"Si esto cierra, el silencio volvió a anclarse en el PRIMER mensaje y la ráfaga se parte en dos jobs", n)
	}

	e.clock.avanza(10 * time.Second)
	e.observa(ctx, "wa-3", nil) // t = 60, el último de la ráfaga

	// t = 104: falta un segundo para el silencio (60 + 45 = 105) y faltan 16 para el
	// techo (0 + 120). No toca.
	e.clock.avanza(44 * time.Second)
	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("a t=104 s no toca (silencio vence en 105, techo en 120); se cerraron %d", n)
	}
	// t = 105: 45 s exactos desde el ÚLTIMO mensaje. Cierra el SILENCIO, no el techo.
	e.clock.avanza(1 * time.Second)
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("a t=105 s (45 s tras el último mensaje) tenía que cerrarse; se cerraron %d", n)
	}

	jobs := e.jobs.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("la ráfaga lenta de 70 s tenía que ser UN solo job, hay %d: la petición del cliente "+
			"se partió en dos pedidos y ninguno lleva el texto entero", len(jobs))
	}
	j := jobs[0]
	if j.Status != intake.StatusPending {
		t.Fatalf("status tras el flush = %q; se esperaba %q", j.Status, intake.StatusPending)
	}
	if len(j.SourceRefs) != 3 {
		t.Fatalf("el job tenía que llevar las 3 referencias de la ráfaga, lleva %v", j.SourceRefs)
	}
	// CRITERIO (f): message_ts NO cambia de significado. Sigue siendo el del PRIMER
	// mensaje —la base de fechas del presupuesto, D-044.9— aunque el plazo ahora se
	// mida contra el último. Son dos cosas distintas y esta tarea solo tocó una.
	if !j.MessageTS.Equal(primerTS) {
		t.Fatalf("message_ts = %v; tenía que seguir siendo el del PRIMER mensaje (%v). La ventana híbrida "+
			"cambia CONTRA QUÉ se mide el plazo, no qué fecha ancla el presupuesto", j.MessageTS, primerTS)
	}
}

// TestT181c_GoteoCada40s_CierraPorElTechoALos120 es el criterio (c) literal, y es el
// defecto que aparecería si alguien «arreglara» (b) anclando SOLO en el silencio: una
// conversación que gotea cada 40 s no alcanza NUNCA los 45 s de silencio, así que su
// ventana no cerraría jamás. Un job en `aggregating` que nadie recoge es un pedido
// perdido sin una línea de error en ningún sitio.
//
// El último tramo —el mensaje de t=120 abriendo ventana NUEVA— no es decorado: dice
// que el techo CORTA el pedido, no la conversación. El cliente que sigue escribiendo
// obtiene un segundo job, que es lo correcto (el índice de la 0072 es PARCIAL a
// propósito).
//
// 💥 MUTACIÓN de (e) — «quitar el techo» (compila): en aggregator.go, en la función
// due, sustituir la última línea
//
//	return s.venceElSilencio(job, p, now) || s.venceElTecho(job, p, now)
//
// por
//
//	return s.venceElSilencio(job, p, now)
//
// (`venceElTecho` se queda sin llamantes, y un MÉTODO sin usar en Go compila — no es
// una variable local). El barrido de t=120 devuelve 0 y este test se para ahí. Ningún
// otro test del paquete se entera, que es exactamente por qué este hace falta.
func TestT181c_GoteoCada40s_CierraPorElTechoALos120(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second) // silencio 45, techo 120
	primerTS := e.clock.now()

	e.observa(ctx, "wa-1", nil) // t = 0

	e.clock.avanza(40 * time.Second)
	e.observa(ctx, "wa-2", nil) // t = 40
	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("a t=40 el silencio lleva 0 s y el techo 40; se cerraron %d", n)
	}

	e.clock.avanza(40 * time.Second)
	e.observa(ctx, "wa-3", nil) // t = 80
	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("a t=80 el silencio lleva 0 s y el techo 80; se cerraron %d", n)
	}

	// t = 119: el silencio lleva 39 s (vencería en t=125, y el goteo lo reiniciaría
	// otra vez antes). El techo vence en t=120. Un segundo antes, nada.
	e.clock.avanza(39 * time.Second)
	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("a t=119 no toca todavía (techo en 120); se cerraron %d", n)
	}
	// t = 120: 120 s exactos desde que la ventana NACIÓ. Cierra el TECHO —el silencio
	// nunca llegó a 45 y no habría llegado nunca.
	e.clock.avanza(1 * time.Second)
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("a t=120 tenía que cerrar EL TECHO (el silencio nunca alcanza 45 s con un goteo de 40); "+
			"se cerraron %d. Sin techo, este job se queda en `aggregating` para siempre", n)
	}

	jobs := e.jobs.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("hasta aquí es UN solo job, hay %d", len(jobs))
	}
	if len(jobs[0].SourceRefs) != 3 {
		t.Fatalf("el job tenía que llevar los 3 mensajes del goteo, lleva %v", jobs[0].SourceRefs)
	}
	// CRITERIO (f), también por el camino del techo.
	if !jobs[0].MessageTS.Equal(primerTS) {
		t.Fatalf("message_ts = %v; tenía que seguir siendo el del PRIMER mensaje (%v)", jobs[0].MessageTS, primerTS)
	}

	// Y el goteo SIGUE: el techo cortó el pedido, no la conversación. El mensaje de
	// t=120 abre la ventana siguiente.
	e.observa(ctx, "wa-4", nil)
	jobs = e.jobs.Jobs()
	if len(jobs) != 2 {
		t.Fatalf("tras cerrar por techo, el siguiente mensaje tiene que abrir OTRA ventana; hay %d jobs", len(jobs))
	}
}

// TestT181_ElTechoSaleDeLaConfigDelTenant impide el defecto que (c) por sí solo no ve:
// que el 120 esté escrito a mano en el barrido en vez de leerse de
// `tenant_settings.aggregation_max_seconds`. Es la misma familia que
// TestVentanaCero_EsFlushInmediato, que existe por la misma razón para la ventana.
//
// 🔴 EL NÚMERO ELEGIDO (90 s) NO ES NI EL DEFAULT DE PLATAFORMA NI LA VENTANA. Si
// fuera 120, el test pasaría con el número quemado; si fuera 45, pasaría con el techo
// leyendo la columna equivocada. Está entre los dos a propósito.
//
// MUTACIÓN (compila): en aggregator.go, en plazosFor, sustituir
//
//	return plazosDeVentana{silencio: cfg.AggregationWindow, techo: cfg.AggregationMax}
//
// por
//
//	return plazosDeVentana{silencio: cfg.AggregationWindow, techo: store.DefaultAggregationMax}
//
// El techo del tenant se ignora y el barrido de t=90 devuelve 0.
func TestT181_ElTechoSaleDeLaConfigDelTenant(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntornoConTecho(t, 45*time.Second, 90*time.Second)

	// El goteo va cada 30 s para que el SILENCIO no llegue nunca antes que el techo:
	// último mensaje en t=60 ⇒ el silencio vencería en t=105, y el techo en t=90. Si
	// los mensajes fueran más espaciados, cerraría el silencio y este test estaría
	// midiendo el otro plazo sin enterarse.
	e.observa(ctx, "wa-1", nil) // t = 0
	e.clock.avanza(30 * time.Second)
	e.observa(ctx, "wa-2", nil) // t = 30
	e.clock.avanza(30 * time.Second)
	e.observa(ctx, "wa-3", nil) // t = 60

	e.clock.avanza(29 * time.Second) // t = 89
	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("a t=89 no toca (techo del tenant = 90 s; el silencio vencería en 105); se cerraron %d", n)
	}
	e.clock.avanza(1 * time.Second) // t = 90
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("a t=90 tenía que cerrar el techo DEL TENANT (90 s, no los 120 de plataforma); se cerraron %d", n)
	}
	// Y la lectura fue UNA sola: los dos plazos salen del mismo GetTenantSettings.
	// Dos lecturas aquí significarían que alguien partió el memo en dos.
	if n := e.cfgCnt.lecturas(); n != 2 {
		t.Fatalf("dos barridos son DOS lecturas de tenant_settings (una por pasada, memo por tenant); hubo %d", n)
	}
}

// TestT181d_ElContextoNoMueveLaVentanaDeSilencio es el criterio (d).
//
// 🔴 HOY PASA POR CONSTRUCCIÓN, Y HAY QUE DECIRLO O ESTE TEST MIENTE. Se verificó
// contra el código (2026-08-25): las entradas de contexto de T1.4 —`entry_kind`
// 'summary' y los salientes fuera de turno rotulados— y la bienvenida que traerá
// T1.8-2 se escriben en `public.conversation_event_messages` a través del EventStore
// (`thread.go`: persistTurnMessages / persistOpeningTurn / persistOutOfTurnMessage →
// events.AppendMessage / AppendOutOfTurnMessage / AppendSummary), que es OTRA TABLA.
// Las únicas tres sentencias que tocan `intake_jobs` son openOrAppendSQL,
// closeWindowSQL y putSourceTextSQL, y no hay un solo `CREATE TRIGGER` en el esquema.
// ⇒ es IMPOSIBLE que `updated_at` se mueva por ahí, y por tanto este test NO PUEDE
// FALLAR hoy. No cuenta como prueba de conducta viva; cuenta como CERROJO.
//
// Qué cerrojo, dicho para que se entienda por qué se escribe igual: la regresión
// realista es que alguien enrute la bienvenida de T1.8-2 —o cualquier saliente del
// sistema— por `Observe`, «ya que estamos, para que quede en source_refs». Ese día la
// ventana se REABRIRÍA cada vez que el negocio habla y una conversación con
// recordatorios automáticos no cerraría nunca. Este test se pone rojo ese día.
//
// El segundo aserto es el que tiene dientes de verdad: no basta con que la columna no
// se mueva, tiene que seguir venciendo el plazo ORIGINAL. Se comprueba cerrando en
// t=45 y no en t=85 (que es donde vencería si el contexto de t=40 hubiera contado como
// actividad).
func TestT181d_ElContextoNoMueveLaVentanaDeSilencio(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)

	e.observa(ctx, "wa-1", nil) // t = 0, el único mensaje DEL CLIENTE
	antes := e.jobs.Jobs()[0].UpdatedAt

	// t = 40: el NEGOCIO habla, por las tres puertas que existen. Se usan los mismos
	// métodos del EventStore que llaman persistTurnMessages / persistOutOfTurnMessage /
	// PersistSummary, no una imitación: si alguien cambiara esos escritores para tocar
	// también la ventana, lo tocarían desde aquí.
	e.clock.avanza(40 * time.Second)
	evs := newMemEventStore(e.clock.now())
	if _, err := evs.AppendSummary(ctx, aggEvent, json.RawMessage(`{"resumen":"rescate"}`)); err != nil {
		t.Fatalf("AppendSummary: %v", err)
	}
	if _, err := evs.AppendOutOfTurnMessage(ctx, aggEvent, "te recuerdo que falta la seña"); err != nil {
		t.Fatalf("AppendOutOfTurnMessage: %v", err)
	}
	if _, err := evs.AppendMessage(ctx, aggEvent, events.RoleBusiness, "estamos procesando tu pedido"); err != nil {
		t.Fatalf("AppendMessage (la bienvenida de T1.8-2): %v", err)
	}

	// (1) LA COLUMNA, LEÍDA. No el log: `UpdatedAt` del doble es `intake_jobs.updated_at`.
	if despues := e.jobs.Jobs()[0].UpdatedAt; !despues.Equal(antes) {
		t.Fatalf("updated_at de la ventana se movió con las filas de CONTEXTO: %v -> %v. "+
			"El silencio solo lo mueven los MENSAJES DEL CLIENTE; si el negocio lo mueve, una "+
			"conversación con recordatorios automáticos no cierra nunca", antes, despues)
	}

	// (2) Y EL PLAZO SIGUE SIENDO EL ORIGINAL: t=45, no t=85.
	e.clock.avanza(5 * time.Second) // t = 45
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("a t=45 la ventana tenía que cerrarse: el contexto de t=40 no es actividad del cliente. "+
			"Se cerraron %d — si es 0, el plazo se reinició con lo que dijimos NOSOTROS", n)
	}
}

// TestT181h_ElHintDespiertaElBarridoSinEsperarAlTick es el criterio (h): el adelanto
// por intent NO espera al tick.
//
// # QUÉ ESTABA MAL Y POR QUÉ IMPORTAN 5 SEGUNDOS
//
// `hintDueNow` anotaba la pista en memoria y se iba. El cierre lo hacía el siguiente
// tick del barrido, o sea hasta `defaultSweepInterval` (5 s) DESPUÉS de que el sistema
// ya supiera lo que tenía que saber. Sobre un presupuesto de «< 5 min» no es fatal,
// pero es tiempo regalado a cambio de nada — y sobre todo es un reloj haciendo el
// trabajo de un evento, que es el antipatrón que esta casa rechaza por escrito.
//
// # CÓMO ESTÁ MONTADO PARA QUE PRUEBE LO QUE DICE
//
// 🔴 EL TICKER SE PONE EN UNA HORA. Es la pieza entera del test: con el intervalo por
// defecto (5 s) —o con cualquiera pequeño— no se podría distinguir «lo cerró el
// despertador» de «lo cerró un tick que pasaba por ahí». Con una hora, lo único capaz
// de cerrar esa ventana dentro del plazo del assert es el canal.
//
// Y la ventana nace con su plazo SIN cumplir (silencio 45 s, techo 120 s, reloj fake
// parado), así que tampoco puede cerrarla el `RecoverAtBoot` del arranque ni el paso
// del tiempo: no pasa tiempo.
//
// El plazo del assert (100 ms) sale del criterio literal de la tarea. El margen real
// es de cuatro órdenes de magnitud contra el ticker de una hora.
//
// 💥 MUTACIÓN de (h) — «quitar el despertador» (compila): en aggregator.go, en
// hintDueNow, borrar el bloque
//
//	select {
//	case s.despertar <- struct{}{}:
//	default:
//	}
//
// El campo `despertar` sigue construido y sigue leído en `Run`, así que el paquete
// compila entero. La pista se anota igual y el cierre vuelve a esperar al tick: con el
// ticker en una hora, este test agota sus 3 s y falla. Ningún otro test del paquete se
// entera.
func TestT181h_ElHintDespiertaElBarridoSinEsperarAlTick(t *testing.T) {
	e := nuevoAggEntorno(t, 45*time.Second)
	sink := flowruntime.NewIntakeAggregator(aggLogger(), e.jobs, e.cfg, e.ents,
		flowruntime.WithAggregatorClock(e.clock.now),
		// UNA HORA: el tick no puede ser quien cierre. Ver el docstring.
		flowruntime.WithSweepInterval(time.Hour))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink.Observe(ctx, flowruntime.IncomingRef{Key: aggKey(), WaMessageID: "wa-1", MessageTS: e.clock.now()})

	parado := make(chan struct{})
	go func() {
		defer close(parado)
		sink.Run(ctx)
	}()

	// Que el RecoverAtBoot del arranque quede atrás, y comprobar que NO cerró nada: el
	// plazo de la ventana no se ha cumplido (el reloj fake no se mueve en todo el test).
	time.Sleep(20 * time.Millisecond)
	if got := e.jobs.Jobs()[0].Status; got != intake.StatusAggregating {
		t.Fatalf("el arranque de Run NO puede cerrar una ventana que no ha vencido; status=%q", got)
	}

	// EL EVENTO. A partir de aquí se mide.
	inicio := time.Now()
	sink.OnClassified(aggKey(), flowruntime.IntentIntakeRequest, 0.99)

	limite := time.Now().Add(3 * time.Second)
	for e.jobs.Jobs()[0].Status != intake.StatusPending {
		if time.Now().After(limite) {
			t.Fatalf("el hint no despertó el barrido: la ventana seguía `aggregating` 3 s después. " +
				"Con el ticker en una hora, lo único que puede cerrarla es el canal de despertar")
		}
		time.Sleep(time.Millisecond)
	}
	if tardo := time.Since(inicio); tardo > 100*time.Millisecond {
		t.Fatalf("el cierre tardó %v desde el hint; el criterio (h) pide < 100 ms. Si esto se acerca al "+
			"intervalo del ticker, el cierre lo hizo un tick y no el despertador", tardo)
	}

	cancel()
	select {
	case <-parado:
	case <-time.After(2 * time.Second):
		t.Fatal("Run no retornó al cancelar el contexto")
	}
}

// TestT181h_AvisosDeMasNoApilanBarridos fija la otra mitad del molde del despertador:
// el envío es NO BLOQUEANTE y el buffer es 1, así que una ráfaga de clasificaciones no
// puede dejar al agregador barriendo N veces seguidas ni —peor— BLOQUEAR a la
// goroutine del pool que avisa.
//
// 🔴 SIN `Run` CORRIENDO, EL CANAL NO TIENE LECTOR, que es justo el escenario límite:
// el primer aviso llena el buffer y los 999 siguientes se descartan. Si el envío fuera
// bloqueante (`s.despertar <- struct{}{}` a secas), esta llamada colgaría para siempre
// la goroutine que clasifica y el pool entero se pararía. El test acaba, y que acabe
// ES la aserción.
//
// Y las pistas NO se pierden por descartar avisos: se comprueba cerrando la ventana en
// el barrido siguiente. El aviso es un adelanto; la verdad vive en `dueNow` y en la
// tabla.
func TestT181h_AvisosDeMasNoApilanBarridos(t *testing.T) {
	ctx := context.Background()
	e := nuevoAggEntorno(t, 45*time.Second)

	e.observa(ctx, "wa-1", nil)
	for i := 0; i < 1000; i++ {
		e.sink.OnClassified(aggKey(), flowruntime.IntentIntakeRequest, 0.99)
	}

	// La pista está anotada aunque 999 avisos se hayan ido a la basura.
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("la pista tenía que cerrar la ventana en el barrido siguiente; se cerraron %d", n)
	}
}
