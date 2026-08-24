// via_local_sin_api_llm_test.go — Plan 044 · Ola 1.5 · T1.5-1 (D-044.28, ADR-0044).
//
// QUÉ AFIRMA ESTE FICHERO, EN UNA FRASE: un tenant con `llm_intake` y SIN `api_llm`
// es un tenant VÁLIDO, no uno «a medias» — la ventana abre, el hilo del evento se
// archiva y el job nace `pending`, sin que nadie le pregunte por la vía.
//
// 🔴 POR QUÉ EXISTE SI EL CÓDIGO YA ESTABA BIEN. El barrido de T1.5-1 encontró que
// NINGÚN gate de capacidad consultaba `api_llm`: el agregador (aggregator.go) y el
// productor del hilo (thread.go) miran `llm_intake` y solo `llm_intake`. Lo que
// fallaba era la TAXONOMÍA COMERCIAL —no existe ningún plan que venda `llm_intake`
// sin `api_llm`, así que la combinación jamás se había ejercitado— y una doctrina
// que vivía en los comentarios al revés (D-044.6 exigía las dos). Estos tests
// convierten «hoy no consulta la vía» en «si alguien la consulta, esto se pone
// rojo», que es la única forma de que un invariante sobreviva a la siguiente ola.
//
// ⏳ NINGUNO DE ESTOS TESTS SE HA EJECUTADO. Se escribieron en un entorno sin Go,
// sin red y sin Postgres. Cada uno lleva su MUTACIÓN, y la mutación está elegida
// para que COMPILE: una mutación que no compila no prueba nada.
package runtime_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// ---------------------------------------------------------------------------
// El resolver ESPÍA: no cuenta consultas, las NOMBRA
// ---------------------------------------------------------------------------

// resolverEspia envuelve un Fake y apunta CADA feature por la que se pregunta.
// Es la diferencia con `entsContados` (aggregator_test.go), que cuenta cuántas
// veces se consulta pero no QUÉ se consulta: el criterio de T1.5-1 —«ninguna ruta
// de capacidad consulta `api_llm`»— es una afirmación sobre el NOMBRE, y un
// contador no la puede hacer.
//
// Es el grep del criterio convertido en test. Un grep se ejecuta una vez, el día
// que alguien se acuerda; esto corre en cada CI.
type resolverEspia struct {
	dentro *entitlements.Fake

	mu          sync.Mutex
	preguntadas []string
	porFeature  map[string]int
}

func espiar(f *entitlements.Fake) *resolverEspia {
	return &resolverEspia{dentro: f, porFeature: map[string]int{}}
}

func (e *resolverEspia) Has(ctx context.Context, tenantID, feature string) (bool, error) {
	e.mu.Lock()
	e.preguntadas = append(e.preguntadas, feature)
	e.porFeature[feature]++
	e.mu.Unlock()
	return e.dentro.Has(ctx, tenantID, feature)
}

func (e *resolverEspia) ListEffective(ctx context.Context, tenantID string) (string, []string, error) {
	return e.dentro.ListEffective(ctx, tenantID)
}

func (e *resolverEspia) CacheTTL() time.Duration { return e.dentro.CacheTTL() }

// veces devuelve cuántas veces se preguntó por una feature concreta.
func (e *resolverEspia) veces(feature string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.porFeature[feature]
}

// censo devuelve la lista de features preguntadas, en orden, para que el mensaje
// de fallo diga QUÉ se preguntó y no solo «alguien preguntó de más».
func (e *resolverEspia) censo() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.preguntadas...)
}

// Que el espía siga encajando en el puerto se comprueba EN COMPILACIÓN: si alguien
// añade un método a Resolver, esto se rompe aquí y no a mitad de un montaje.
var _ entitlements.Resolver = (*resolverEspia)(nil)

// ---------------------------------------------------------------------------
// El entorno del tenant de VÍA LOCAL
// ---------------------------------------------------------------------------

// viaLocalEnv es el agregador CON compositor cableado —como corre en producción
// desde T1.4— para un tenant que tiene `llm_intake` y NO tiene `api_llm`.
//
// 🔴 EL `Disable(api_llm)` ES EXPLÍCITO Y NO ES REDUNDANTE aunque el Fake ya
// responda false a lo ausente: escrito así, la premisa del test se LEE en el
// montaje. Quien pase por aquí dentro de tres olas ve que la ausencia de la vía es
// el escenario, no un olvido de sembrar la feature.
type viaLocalEnv struct {
	sink *flowruntime.IntakeAggregator
	jobs *intake.MemoryStore
	// 🔧 NO hay campo `hilo`: el doble se le pasa al composer dentro del
	// constructor y NINGÚN test de este fichero lo vuelve a mirar. Guardarlo era
	// un campo escrito y jamás leído — lo marca `unused` y, peor, sugiere una
	// afirmación sobre el hilo que aquí no se hace (la del hilo archivado se hace
	// contra el store de eventos, no contra el doble).
	cipher *crypto.FieldCipher
	clock  *aggReloj
	espia  *resolverEspia
}

func nuevoViaLocalEnv(t *testing.T, ventana time.Duration, entradas []events.ThreadEntry) *viaLocalEnv {
	t.Helper()
	clock := nuevoAggReloj()
	jobs := intake.NewMemoryStore(clock.now)

	cfg := store.NewMemoryRepository()
	// Se parte de DefaultTenantSettings y se cambia SOLO la ventana: el cero de Go
	// en AggregationWindow NO significa «45 por defecto» sino FLUSH INMEDIATO. Es la
	// trampa que documenta SetTenantSettings, y aquí dejaría el test probando el
	// agregador con la agregación apagada.
	s := store.DefaultTenantSettings(aggTenant)
	s.AggregationWindow = ventana
	cfg.SetTenantSettings(s)

	feats := entitlements.NewFake()
	feats.Enable(aggTenant, entitlements.FeatureLLMIntake)
	feats.Disable(aggTenant, entitlements.FeatureAPILLM)
	espia := espiar(feats)

	hilo := &hiloFalso{entradas: entradas}
	cipher := crypto.NewFieldCipher(kpDePrueba(t))
	comp := flowruntime.NewSourceTextComposer(aggLogger(), hilo, jobs, cipher)

	return &viaLocalEnv{
		sink: flowruntime.NewIntakeAggregator(aggLogger(), jobs, cfg, espia,
			flowruntime.WithAggregatorClock(clock.now),
			flowruntime.WithSourceComposer(comp)),
		jobs:   jobs,
		cipher: cipher,
		clock:  clock,
		espia:  espia,
	}
}

func (e *viaLocalEnv) observa(ctx context.Context, waID string) {
	e.sink.Observe(ctx, flowruntime.IncomingRef{
		Key:         aggKey(),
		WaMessageID: waID,
		MessageTS:   e.clock.now(),
	})
}

// ---------------------------------------------------------------------------
// T1.5-1 · criterio (a): la ventana abre, el sobre se cifra, el job nace pending
// ---------------------------------------------------------------------------

// TestT151_ViaLocal_LaVentanaAbreElSobreSeCifraYElJobNacePending es el criterio
// literal de T1.5-1 sobre el carril de capacidad entero, con la vía API AUSENTE.
//
// Afirma las TRES mitades de una sola pasada, porque el defecto que vigila las
// rompería a las tres a la vez (un gate que exigiera la vía cierra el carril
// completo):
//
//  1. LA VENTANA ABRE — tres entrantes ⇒ UN job en `aggregating`.
//  2. EL SOBRE SE CIFRA Y SE PUEDE ABRIR — al flush, las tres piezas del sobre
//     están y el texto se descifra con el keyring de verdad. No se compara contra
//     el literal completo del formato: eso es lo que afirma
//     TestT14_AlFlush_ElSobreEsDeTresPiezasYSeAbre, y repetirlo aquí sería un
//     segundo dueño del mismo contrato. Aquí basta con que el literal del cliente
//     ESTÉ: es lo que demuestra que no se perdió texto por no tener vía API.
//  3. EL JOB NACE `pending` — el estado que espera al worker de la Ola 2.
//
// MUTACIÓN (compila, y es EXACTAMENTE la regresión que este fichero existe para
// impedir): en aggregator.go, sustituir
//
//	const featureIntakeAggregation = entitlements.FeatureLLMIntake
//
// por
//
//	const featureIntakeAggregation = entitlements.FeatureAPILLM
//
// Las dos son constantes string, así que compila. Con eso el tenant de vía local
// deja de abrir ventana y este test se pone rojo en la primera aserción —mientras
// los tests del Ola 1, que encienden las dos features, seguirían verdes—.
func TestT151_ViaLocal_LaVentanaAbreElSobreSeCifraYElJobNacePending(t *testing.T) {
	ctx := context.Background()
	e := nuevoViaLocalEnv(t, 45*time.Second, hiloConContexto(events.ThreadEntry{
		Seq: 1, Role: events.RoleSystem, Kind: events.KindSummary, Text: compContexto,
	}))

	e.observa(ctx, "wa-local-1")
	e.clock.avanza(2 * time.Second)
	e.observa(ctx, "wa-local-2")
	e.clock.avanza(2 * time.Second)
	e.observa(ctx, "wa-local-3")

	// (1) La ventana ABRE sin vía API.
	jobs := e.jobs.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("un tenant con llm_intake y SIN api_llm tenía que abrir UNA ventana, abrió %d "+
			"(ADR-0044: la vía no gatea la capacidad)", len(jobs))
	}
	if jobs[0].Status != intake.StatusAggregating {
		t.Fatalf("status = %q; la ventana recién abierta está en %q", jobs[0].Status, intake.StatusAggregating)
	}

	// (2) y (3) Al flush: sobre entero, descifrable, y el job en pending.
	e.clock.avanza(46 * time.Second)
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("el barrido tenía que cerrar UNA ventana, cerró %d", n)
	}
	jobs = e.jobs.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("se esperaba UN job, hay %d", len(jobs))
	}
	j := jobs[0]
	if j.Status != intake.StatusPending {
		t.Fatalf("status = %q; sin api_llm el job tiene que nacer %q igual", j.Status, intake.StatusPending)
	}
	if !j.SourceText.Complete() {
		t.Fatalf("el sobre no está entero (enc=%d dek=%d kek=%q): son las tres o ninguna",
			len(j.SourceText.Enc), len(j.SourceText.DEK), j.SourceText.KEKID)
	}
	plano, err := e.cipher.Decrypt(j.SourceText.Enc, j.SourceText.DEK, j.SourceText.KEKID)
	if err != nil {
		t.Fatalf("el sobre guardado no se puede abrir: %v", err)
	}
	// ⚠️ LO QUE ESTE ASERTO PRUEBA, Y LO QUE NO (code review 2026-08-23). Prueba
	// que el sobre lleva TEXTO DEL CLIENTE y que se abre con la KEK de la casa —
	// que es lo que aquí se afirma: sin `api_llm`, el carril compone y cifra
	// igual. NO prueba que los tres entrantes de este test hayan entrado en él:
	// `compContexto` incluye `compMsg1` (source_composer_test.go:70), así que el
	// bloque de contexto solo ya satisface el Contains. La composición mensaje a
	// mensaje —quién habla, en qué orden y con qué voz— la custodian los tests del
	// composer (source_composer_test.go:527 y :752, que comparan el plano ENTERO),
	// y repetir aquí ese aserto sería duplicar su cobertura sin añadir la de esta
	// tarea. Lo que sí ata los tres entrantes a este job es el `source_refs` de
	// abajo.
	if !strings.Contains(plano, compMsg1) {
		t.Fatalf("el sobre no lleva texto del cliente; guardado=%q", plano)
	}
	if len(j.SourceRefs) != 3 {
		t.Fatalf("source_refs = %v; las tres referencias del cliente tienen que estar", j.SourceRefs)
	}
}

// ---------------------------------------------------------------------------
// T1.5-1 · criterio (c): el grep, hecho test
// ---------------------------------------------------------------------------

// TestT151_ElCarrilDeCapacidadNoPreguntaPorLaVia pone bajo test UNA RUTA del
// criterio «grep de los gates: ninguna ruta de capacidad consulta `api_llm`».
//
// 🔧 NO ES EL GREP ENTERO, Y NO CONVIENE LEERLO ASÍ (code review 2026-08-23). El
// `resolverEspia` está cableado en el `IntakeAggregator` y en nadie más: el
// productor del hilo (`thread.go:81`, que resuelve `featureThreadMessages` por el
// resolver del Runtime) y los gates de `publicapi` usan sus propios dobles, así
// que sus consultas no pasan por este contador. Lo que este test cubre —y cubre
// de verdad— es la ruta de captación de punta a punta: entrada, ventana, barrido
// y cierre. El criterio COMPLETO sigue necesitando el grep sobre el árbol; esto
// es la parte que un test puede sostener sola y que ninguna refactorización puede
// desandar en silencio.
//
// Recorre esa ruta entera —tres entrantes y el barrido que cierra— con un resolver
// que APUNTA cada feature por la que se le pregunta, y afirma dos cosas:
//
//   - por `api_llm` no se preguntó NI UNA VEZ (la vía no se consulta para decidir
//     capacidad, ADR-0044);
//   - por `llm_intake` SÍ se preguntó (si no, el test pasaría también con el gate
//     borrado entero, y entonces no estaría probando nada — es la mitad que
//     convierte «no preguntó de más» en «preguntó lo que debe y nada más»).
//
// MUTACIÓN (compila): en aggregator.go, dentro de `Observe`, añadir antes del gate
//
//	if _, err := s.ents.Has(ctx, ref.Key.TenantID, entitlements.FeatureAPILLM); err != nil {
//		return
//	}
//
// Es la forma exacta que tendría el defecto que esto vigila —«ya que consulto una
// feature, consulto también la vía»— y pone rojo el primer aserto.
func TestT151_ElCarrilDeCapacidadNoPreguntaPorLaVia(t *testing.T) {
	ctx := context.Background()
	e := nuevoViaLocalEnv(t, 45*time.Second, hiloConContexto(events.ThreadEntry{
		Seq: 1, Role: events.RoleSystem, Kind: events.KindSummary, Text: compContexto,
	}))

	e.observa(ctx, "wa-espia-1")
	e.observa(ctx, "wa-espia-2")
	e.observa(ctx, "wa-espia-3")
	e.clock.avanza(46 * time.Second)
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("el barrido tenía que cerrar UNA ventana, cerró %d", n)
	}

	if n := e.espia.veces(entitlements.FeatureAPILLM); n != 0 {
		t.Fatalf("el carril de capacidad preguntó %d veces por %q; la vía NO gatea capacidad (ADR-0044). "+
			"Censo de features consultadas: %v", n, entitlements.FeatureAPILLM, e.espia.censo())
	}
	if n := e.espia.veces(entitlements.FeatureLLMIntake); n == 0 {
		t.Fatalf("nadie preguntó por %q: el gate de capacidad desapareció, y entonces el aserto de "+
			"arriba no prueba nada. Censo: %v", entitlements.FeatureLLMIntake, e.espia.censo())
	}
}

// ---------------------------------------------------------------------------
// T1.5-1 · criterio (b): el hilo del evento se archiva
// ---------------------------------------------------------------------------

// TestT151_ViaLocal_ElHiloDelEventoSeArchiva es la otra puerta del mismo carril: el
// productor de filas `message` (thread.go), que es quien DEJA LA MATERIA PRIMA que
// el agregador después lee. Un tenant sin vía API la deja igual.
//
// ⚠️ QUÉ NO AFIRMA ESTE TEST, dicho aquí para que nadie lo lea de más: que las filas
// estén CIFRADAS no se comprueba sobre el doble en memoria, porque el doble guarda
// un string. El cifrado es estructural en la puerta —`AppendMessage` es la única
// forma de meter texto libre en el hilo y cifra siempre (events/store.go:27)— y se
// verifica contra Postgres real en los tests de integración del hilo. Lo que este
// test añade es lo que faltaba: que con `llm_intake` y SIN `api_llm` la puerta se
// ABRA, que es lo que la doctrina vieja dejaba en duda.
//
// MUTACIÓN (compila): en thread.go, sustituir
//
//	const featureThreadMessages = entitlements.FeatureLLMIntake
//
// por
//
//	const featureThreadMessages = entitlements.FeatureAPILLM
//
// Este test se pone rojo (cero filas) y TestHilo_LaFeatureBASTA_… también, que es
// justo lo que debe pasar: sería la misma regresión vista dos veces.
func TestT151_ViaLocal_ElHiloDelEventoSeArchiva(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(testTenant, entitlements.FeatureLLMIntake)
	feats.Disable(testTenant, entitlements.FeatureAPILLM)
	rt, evs, _ := newThreadRuntime(t, feats, eventStartRule("carrito", "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "carrito", "t151-1")); err != nil {
		t.Fatalf("carrito: %v", err)
	}
	ev := evs.alive()[0]
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "quiero 2 tortas", "t151-2")); err != nil {
		t.Fatalf("turno dentro del evento: %v", err)
	}

	if got := evs.mensajesDe(ev.ID); len(got) == 0 {
		t.Fatal("con llm_intake y SIN api_llm el hilo del evento tiene que archivarse igual; hay 0 filas `message`")
	}
}
