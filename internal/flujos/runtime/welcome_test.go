package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
)

// welcome_test.go — LA BIENVENIDA ÚNICA (Plan 044 · Ola 1.8 · T1.8-2, D6).
//
// Lo que esta tanda fija NO es una función: es una promesa de producto con cuatro
// mitades —«una por conversación», «vuelve tras el silencio», «nunca por
// interacción» y «fuera del análisis»— y todas ellas se rompen sin que nada deje de
// compilar. El diseño entero está en welcome.go; aquí solo se comprueba.
//
// 🔴 UN DETALLE QUE PARECE DE MONTAJE Y NO LO ES: el reloj falso es EL MISMO para el
// runtime (WithClock) y para el doble de `intake_jobs` (intake.NewMemoryStore). Con
// dos relojes, el `updated_at` del job y el `last_incoming_at` de la bienvenida
// avanzarían por separado y la aserción de (b) —«el job no se movió en 30 horas»—
// no significaría nada. Es la misma razón por la que la migración 0076 le prohíbe a
// `conversation_welcomes.last_incoming_at` tener un `DEFAULT now()`.

// entornoBienvenida es el montaje mínimo en el que la bienvenida puede ocurrir:
// repositorio en memoria (que es TAMBIÉN el WelcomeStore), reloj falso compartido,
// resolver de features y —cuando el test lo necesita— el plano de eventos y el
// agregador de captación.
type entornoBienvenida struct {
	rt       *flowruntime.Runtime
	repo     *store.MemoryRepository
	sender   *fakeSender
	reloj    *aggReloj
	contacts *contact.MemoryResolver
	jobs     *intake.MemoryStore
	evs      *memEventStore
}

// nuevoEntornoBienvenida arma el runtime. `conFeature` decide si el tenant tiene
// `llm_intake`: es el gate de la bienvenida y el sujeto del criterio (d), así que se
// pasa como parámetro en vez de encenderse siempre.
//
// El agregador y el plano de eventos van SIEMPRE cableados —también en los tests que
// no los miran— a propósito: así los tests de (a), (c) y (d) corren sobre el MISMO
// montaje que el de (b), y un cambio que rompiera la separación entre la bienvenida y
// la ventana de captación no podría esconderse en un entorno más pobre.
func nuevoEntornoBienvenida(t *testing.T, conFeature bool, reglas ...trigger.Rule) *entornoBienvenida {
	t.Helper()
	ctx := context.Background()

	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(ctx, testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	for _, r := range reglas {
		if _, err := ts.Insert(ctx, r); err != nil {
			t.Fatalf("insert regla: %v", err)
		}
	}
	reloj := nuevoAggReloj()
	ents := entitlements.NewFake()
	if conFeature {
		ents.Enable(testTenant, entitlements.FeatureLLMIntake)
	}
	contacts := contact.NewMemoryResolver(repo)
	evs := newMemEventStore(reloj.now())
	jobs := intake.NewMemoryStore(reloj.now)
	agg := flowruntime.NewIntakeAggregator(aggLogger(), jobs, repo, ents,
		flowruntime.WithAggregatorClock(reloj.now))
	sender := &fakeSender{}

	rt := flowruntime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant},
		contacts, discardLogger(),
		flowruntime.WithClock(reloj.now),
		flowruntime.WithWelcomeStore(repo),
		flowruntime.WithEntitlements(ents),
		flowruntime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		flowruntime.WithEventStore(evs),
		flowruntime.WithAggregator(agg),
	)
	return &entornoBienvenida{rt: rt, repo: repo, sender: sender, reloj: reloj,
		contacts: contacts, jobs: jobs, evs: evs}
}

// escribe simula un mensaje del cliente y falla el test si el motor devuelve error.
func (e *entornoBienvenida) escribe(t *testing.T, texto, waID string) {
	t.Helper()
	if err := e.rt.HandleIncoming(context.Background(), testSession, incoming(testContact, texto, waID)); err != nil {
		t.Fatalf("HandleIncoming(%q, %s): %v", texto, waID, err)
	}
}

// bienvenidas cuenta cuántos salientes son EXACTAMENTE el texto de la bienvenida.
// Se cuenta por el LITERAL y no por la posición ni por el total de salientes: en el
// entorno con flujo, el motor manda además el menú y sus respuestas, y un test que
// contara «cuántos mensajes salieron» mediría otra cosa.
func (e *entornoBienvenida) bienvenidas() int {
	n := 0
	for _, txt := range e.sender.texts() {
		if txt == store.DefaultWelcomeText {
			n++
		}
	}
	return n
}

// claveDe devuelve la clave conversacional del contacto de prueba, para mirar la
// fila de `conversation_welcomes` (el gemelo en memoria) directamente.
func (e *entornoBienvenida) claveDe(t *testing.T) store.Key {
	t.Helper()
	return store.Key{TenantID: testTenant, SessionID: testSession, ContactID: resolveID(t, e.contacts, testContact)}
}

// ---------------------------------------------------------------------------
// (a) DOS MENSAJES SEGUIDOS ⇒ UNA SOLA BIENVENIDA
// ---------------------------------------------------------------------------

// TestBienvenida_DosMensajesSeguidos_UnaSolaBienvenida es el criterio (a) y el más
// importante de los cuatro: es el que separa un acuse de recibo de un autorespondedor.
//
// El montaje NO tiene reglas de disparo a propósito, así que los dos entrantes caen en
// el LIMBO y el resolver contesta Ignore: el único saliente posible es la bienvenida, y
// el contador de salientes puede leerse sin ambigüedad. Es la forma más pura del caso
// que el enunciado describe —«dos mensajes seguidos del mismo contacto»—.
//
// 🔴 MUTACIÓN QUE COMPILA (criterio (e)) — quitar la guarda de «una por conversación».
// En welcome.go, en `func (t welcomeTurn) debe(...)`, sustituir las cinco líneas
// finales:
//
//	if t.previo.WelcomedAt.IsZero() {
//		return true
//	}
//	return t.ahora.Sub(t.previo.LastIncomingAt) >= silencio
//
// por estas dos:
//
//	_ = silencio
//	return true
//
// El `_ = silencio` NO es adorno: sin él el parámetro queda sin usar y el paquete no
// compila, y una mutación que no compila no dice nada de este test. Con ella, cada
// entrante que no avance conversación viva vuelve a saludar y este test se pone rojo
// («2 bienvenidas, quiero 1»), mientras (d) —el tenant sin la feature— sigue verde,
// que es lo que hace a este par un par: la guarda que se quita es la de la
// idempotencia, no la del gate.
func TestBienvenida_DosMensajesSeguidos_UnaSolaBienvenida(t *testing.T) {
	e := nuevoEntornoBienvenida(t, true)

	e.escribe(t, "hola, quiero cotizar", "wa-a1")
	e.reloj.avanza(30 * time.Second)
	e.escribe(t, "20 hamburguesas", "wa-a2")

	if got := e.bienvenidas(); got != 1 {
		t.Fatalf("dos mensajes seguidos produjeron %d bienvenidas, quiero exactamente 1: %v", got, e.sender.texts())
	}
	if got := e.sender.count(); got != 1 {
		t.Fatalf("sin reglas de disparo el ÚNICO saliente posible es la bienvenida; salieron %d: %v",
			got, e.sender.texts())
	}
	// Y la marca quedó sellada con el instante del PRIMER mensaje, no con el del
	// segundo: si `welcomed_at` se reescribiera en cada turno, «una por conversación»
	// seguiría cumpliéndose por casualidad y el umbral de silencio dejaría de tener
	// un ancla estable.
	marca := e.repo.Welcome(e.claveDe(t))
	if marca.WelcomedAt.IsZero() {
		t.Fatalf("la bienvenida salió pero no se selló: welcomed_at sigue vacío (el próximo mensaje la repetiría)")
	}
	if !marca.WelcomedAt.Equal(e.reloj.now().Add(-30 * time.Second)) {
		t.Fatalf("welcomed_at = %v, quiero el instante del PRIMER mensaje (%v)",
			marca.WelcomedAt, e.reloj.now().Add(-30*time.Second))
	}
}

// ---------------------------------------------------------------------------
// (c) TRAS N HORAS DE SILENCIO VUELVE; POR DEBAJO DE N, NO
// ---------------------------------------------------------------------------

// TestBienvenida_VuelveTrasElSilencio_YNoAntes es el criterio (c), y va en UN solo
// test con los dos lados porque son la misma pregunta: la mitad «no antes» sin la
// mitad «vuelve» pasaría en verde con la bienvenida rota del todo.
//
// Los plazos están elegidos para clavar el BORDE, que es donde vive la ambigüedad de
// esta regla:
//
//   - segundo mensaje a `N - 1s` de silencio ⇒ NO saluda;
//   - tercer mensaje a EXACTAMENTE `N` de silencio ⇒ SÍ saluda.
//
// Es decir: el predicado es `>=` y no `>`. Con `>` el segundo caso también sería
// silencio y este test lo cazaría.
//
// 🔴 Y EL SILENCIO SE MIDE DESDE EL ÚLTIMO MENSAJE DEL CONTACTO, NO DESDE LA ÚLTIMA
// BIENVENIDA. El tercer mensaje llega a N+... del primero pero a EXACTAMENTE N del
// segundo, así que un `debe` anclado en `welcomed_at` daría el mismo verde aquí. Lo
// que separa las dos implementaciones es el mensaje 2: anclado en la bienvenida, su
// silencio sería N-1s igual... y por eso el caso que de verdad las separa lo cubre
// (b) —donde el contacto habla dentro de la ventana— y la propia forma del struct
// (welcomeTurn.previo trae las DOS marcas). Se dice aquí para que quien lea este test
// no crea que cubre más de lo que cubre.
func TestBienvenida_VuelveTrasElSilencio_YNoAntes(t *testing.T) {
	e := nuevoEntornoBienvenida(t, true)
	const n = store.DefaultWelcomeSilence // 24 h, el default de plataforma.

	e.escribe(t, "hola", "wa-c1")
	if got := e.bienvenidas(); got != 1 {
		t.Fatalf("el primer mensaje de la conversación debe traer bienvenida; hubo %d", got)
	}

	// Por DEBAJO del umbral: un segundo menos de silencio y no vuelve.
	e.reloj.avanza(n - time.Second)
	e.escribe(t, "sigo por aquí", "wa-c2")
	if got := e.bienvenidas(); got != 1 {
		t.Fatalf("a %v de silencio (por debajo de %v) la bienvenida NO debe volver; van %d", n-time.Second, n, got)
	}

	// EXACTAMENTE en el umbral: vuelve.
	e.reloj.avanza(n)
	e.escribe(t, "vuelvo al día siguiente", "wa-c3")
	if got := e.bienvenidas(); got != 2 {
		t.Fatalf("a %v EXACTOS de silencio la bienvenida debe volver (el predicado es >=); van %d bienvenidas: %v",
			n, got, e.sender.texts())
	}
}

// TestBienvenida_ElUmbralLoManda_ElTenant comprueba que el umbral es CONFIGURABLE por
// tenant y no una constante escondida (`tenant_settings.welcome_silence_seconds`).
//
// Sin este test, la implementación podría estar comparando contra
// store.DefaultWelcomeSilence directamente y el test (c) saldría verde igual: es
// exactamente la clase de test tautológico que esta casa persigue —comparar contra la
// constante que quieres proteger no prueba nada—. Aquí la fila del tenant dice 2 h y
// el comportamiento tiene que seguirla, no seguir a las 24 h del default.
func TestBienvenida_ElUmbralLoManda_ElTenant(t *testing.T) {
	e := nuevoEntornoBienvenida(t, true)
	cfg := store.DefaultTenantSettings(testTenant)
	cfg.WelcomeSilence = 2 * time.Hour
	cfg.WelcomeText = "" // '' = el texto de plataforma; lo traduce el runtime, no el repo.
	e.repo.SetTenantSettings(cfg)

	e.escribe(t, "hola", "wa-u1")
	e.reloj.avanza(3 * time.Hour) // por encima de las 2 h del tenant, MUY por debajo de las 24 h del default
	e.escribe(t, "vuelvo", "wa-u2")

	if got := e.bienvenidas(); got != 2 {
		t.Fatalf("con welcome_silence_seconds = 2 h, tres horas de silencio deben traer la SEGUNDA bienvenida; van %d. "+
			"Si va 1, el umbral que se está aplicando es el default de plataforma y no el del tenant", got)
	}
}

// ---------------------------------------------------------------------------
// (d) UN TENANT SIN `llm_intake` NO LA RECIBE NUNCA
// ---------------------------------------------------------------------------

// TestBienvenida_SinLaFeature_NoLaRecibeNunca es el criterio (d). El gate es el MISMO
// que el del hilo literal y el de la ventana de captación (`llm_intake`), y es
// fail-closed: ver `featureWelcome` en welcome.go para por qué se comparte en vez de
// inventar una feature propia.
//
// Comprueba DOS cosas y las dos hacen falta:
//
//   - no sale ni un saliente (lo obvio);
//   - y NO SE ESCRIBE NI UNA FILA en `conversation_welcomes`. Esto es lo que fija que
//     el gate va ANTES del toque y no después: con el orden invertido el tenant
//     seguiría sin recibir nada, este test pasaría a medias, y la tabla acumularía una
//     fila por contacto de todo el parque —incluidos los tenants que jamás recibirán
//     una bienvenida—, dejando de significar lo que su COMMENT dice que significa.
//
// MUTACIÓN (compila): en welcome.go, en observeWelcome, sustituir
//
//	if !has {
//		return welcomeTurn{}
//	}
//
// por
//
//	_ = has
//
// Este test se pone rojo y (a) sigue verde.
func TestBienvenida_SinLaFeature_NoLaRecibeNunca(t *testing.T) {
	e := nuevoEntornoBienvenida(t, false)

	e.escribe(t, "hola", "wa-d1")
	e.reloj.avanza(48 * time.Hour) // el doble del umbral: tampoco por el camino del silencio
	e.escribe(t, "sigo aquí", "wa-d2")

	if got := e.bienvenidas(); got != 0 {
		t.Fatalf("un tenant SIN llm_intake no recibe bienvenida NUNCA; recibió %d: %v", got, e.sender.texts())
	}
	if marca := e.repo.Welcome(e.claveDe(t)); !marca.LastIncomingAt.IsZero() || !marca.WelcomedAt.IsZero() {
		t.Fatalf("sin la feature no se escribe una sola fila de conversation_welcomes; hay %+v "+
			"(el gate tiene que ir ANTES del toque, no después)", marca)
	}
}

// ---------------------------------------------------------------------------
// (b) FUERA DEL ANÁLISIS: NI `source_refs`, NI `source_text`, NI `updated_at`
// ---------------------------------------------------------------------------

// TestBienvenida_NoEntraEnElAnalisis es el criterio (b), y es el que protege la
// promesa más delicada de la tarea: una `evidence` del borrador no puede apuntar
// JAMÁS a un texto que escribimos nosotros.
//
// ⚠️ HONESTIDAD SOBRE ESTE TEST, dicha antes que sus aserciones: dos de sus tres
// mitades SE CUMPLEN HOY POR CONSTRUCCIÓN y este test no puede ponerse rojo por un
// descuido, solo por un cambio deliberado.
//
//   - `source_refs` se construye SOLO dentro de IntakeAggregator.Observe, y `Observe`
//     recibe un ENTRANTE (`IncomingRef`, con su `wa_message_id`). La bienvenida es un
//     SALIENTE: no tiene `wa_message_id` que ofrecerle. Para que entrara habría que
//     inventarle uno y llamar a `Observe` a mano.
//   - `updated_at` del job lo mueve esa MISMA `Observe`, así que la referencia y la
//     marca de tiempo caen o se salvan juntas.
//
// ⇒ El valor de este test no es cazar un descuido: es SELLAR la decisión, para que el
// día que alguien quiera «dejar traza de la bienvenida en el hilo por trazabilidad»
// (que es una idea razonable y que D-044.24 concede a OTROS salientes fuera de turno)
// tenga que borrar aserciones con nombre y leer por qué existen. La tercera mitad —el
// hilo— sí es un rojo real: escribir la bienvenida en `conversation_event_messages`
// son dos líneas en welcome.go y el compositor de T1.4 la metería en `source_text`.
//
// MUTACIÓN (compila, y es la que este test caza de verdad): en welcome.go, en
// welcomeIfDue, justo después del `if !ack.GetOk()`, añadir
//
//	rt.persistOutOfTurnMessage(ctx, tenantID, sessionID, key.SessionID, textoDeBienvenida(cfg))
//
// —o, mejor dicho, con el eventID del turno—; el literal aparece en el hilo y la
// aserción del hilo se pone roja.
func TestBienvenida_NoEntraEnElAnalisis(t *testing.T) {
	e := nuevoEntornoBienvenida(t, true, eventStartRule("carrito", "cart"))
	clave := e.claveDe(t)

	// Turno 1: la palabra que ABRE el pedido. Nace el evento, se abre la ventana de
	// captación con este mensaje, y —antes de todo eso— sale la bienvenida.
	e.escribe(t, "carrito", "wa-b1")
	// Turno 2: el cliente sigue dentro de la conversación viva. Este entrante SÍ entra
	// en la ventana; la bienvenida no vuelve (es un avance, no un inicio).
	e.reloj.avanza(5 * time.Second)
	e.escribe(t, "1", "wa-b2")

	e.exigeBienvenidas(t, 1, "la del inicio; el turno de dentro del evento no repite")
	job := e.unicaVentana(t)
	// (b.1) source_refs lleva los DOS entrantes y NADA MÁS. La bienvenida no tiene
	// wa_message_id que colar aquí, y no debe tenerlo.
	exigeSourceRefs(t, job.SourceRefs, "wa-b1", "wa-b2")
	// (b.2) el literal de la bienvenida NO está en el hilo del evento — ni como turno
	// (`message`) ni rotulado como saliente fuera de turno (`message_out_of_turn`). El
	// hilo es la ÚNICA fuente de `source_text` (ComposeAtFlush), así que esto es lo que
	// garantiza que el literal no llegue al prompt del LLM.
	e.exigeHiloSinBienvenida(t)

	// (b.3) `updated_at` del job NO se mueve por una bienvenida. Para poder afirmarlo
	// de verdad hace falta un turno en el que la bienvenida SALGA y la ventana NO se
	// toque, y eso pide tres cosas:
	//   1. soltar el flow_state (lo que en producción hace el TTL / la recolección),
	//      para que el siguiente entrante caiga en el LIMBO en vez de avanzar el flujo;
	//   2. pasar el umbral de silencio, para que la bienvenida vuelva;
	//   3. un texto que NO case ninguna regla, para que handleTrigger conteste Ignore y
	//      no abra ni alimente ninguna ventana.
	updatedAntes := job.UpdatedAt
	escriturasAntes := e.jobs.Counters().OpenOrAppend
	if err := e.repo.Delete(context.Background(), clave); err != nil {
		t.Fatalf("soltar el flow_state (simula el TTL): %v", err)
	}
	e.reloj.avanza(30 * time.Hour)
	e.escribe(t, "gracias", "wa-b3")

	e.exigeBienvenidas(t, 2, "tras 30 h de silencio la bienvenida vuelve")
	job = e.unicaVentana(t)
	if !job.UpdatedAt.Equal(updatedAntes) {
		t.Fatalf("updated_at del job se movió de %v a %v POR UNA BIENVENIDA: la ventana no puede "+
			"reabrirse ni refrescarse por un saliente del sistema", updatedAntes, job.UpdatedAt)
	}
	exigeSourceRefs(t, job.SourceRefs, "wa-b1", "wa-b2")
	if got := e.jobs.Counters().OpenOrAppend; got != escriturasAntes {
		t.Fatalf("hubo %d escrituras de ventana y antes había %d: la bienvenida escribió en intake_jobs",
			got, escriturasAntes)
	}
}

// exigeBienvenidas afirma cuántas bienvenidas han salido. `porQue` es la razón, y va en
// el mensaje del fallo: un «quiero 1» sin explicación no le dice nada a quien lo lea
// dentro de tres meses.
func (e *entornoBienvenida) exigeBienvenidas(t *testing.T, quiero int, porQue string) {
	t.Helper()
	if got := e.bienvenidas(); got != quiero {
		t.Fatalf("bienvenidas = %d, quiero %d (%s): %v", got, quiero, porQue, e.sender.texts())
	}
}

// unicaVentana devuelve la única ventana de captación y falla si hay otra cantidad.
func (e *entornoBienvenida) unicaVentana(t *testing.T) intake.Job {
	t.Helper()
	trabajos := e.jobs.Jobs()
	if len(trabajos) != 1 {
		t.Fatalf("se esperaba UNA ventana de captación, hay %d", len(trabajos))
	}
	return trabajos[0]
}

// exigeSourceRefs afirma la lista EXACTA de referencias de la ventana, en orden. La
// lista completa y no un `len()`: lo que este criterio protege no es cuántas hay sino
// que sean EXACTAMENTE los entrantes del cliente.
func exigeSourceRefs(t *testing.T, got []string, quiero ...string) {
	t.Helper()
	if len(got) != len(quiero) {
		t.Fatalf("source_refs = %v, quiero exactamente %v", got, quiero)
	}
	for i := range quiero {
		if got[i] != quiero[i] {
			t.Fatalf("source_refs = %v, quiero exactamente %v (difieren en la posición %d)", got, quiero, i)
		}
	}
}

// exigeHiloSinBienvenida barre TODO el hilo de TODOS los eventos vivos buscando el
// literal de la bienvenida, en sus dos clases de fila: el turno (`message`) y el
// saliente rotulado fuera de turno (`message_out_of_turn`).
//
// Las dos hacen falta y la segunda es la que de verdad se juega algo: D-044.24 CONCEDE
// a otros salientes del sistema —el resumen del rescate, el recordatorio de la seña—
// entrar al hilo rotulados, así que «meterla rotulada» es la idea razonable que este
// barrido tiene que rechazar. El enunciado dice «ni rotulada».
func (e *entornoBienvenida) exigeHiloSinBienvenida(t *testing.T) {
	t.Helper()
	for _, ev := range e.evs.alive() {
		for _, m := range e.evs.mensajesDe(ev.ID) {
			if m.body == store.DefaultWelcomeText {
				t.Fatalf("la bienvenida quedó en el hilo como turno (rol %v); de ahí sale source_text", m.role)
			}
		}
		for _, m := range e.evs.fueraDeTurnoDe(ev.ID) {
			if m.body == store.DefaultWelcomeText {
				t.Fatal("la bienvenida quedó en el hilo ROTULADA como saliente fuera de turno; " +
					"el enunciado dice «ni rotulada»")
			}
		}
	}
	if got := e.evs.totalFueraDeTurno(); got != 0 {
		t.Fatalf("este recorrido no tiene ningún saliente fuera de turno legítimo; hay %d "+
			"(si la bienvenida empezó a escribir en el hilo, es esta línea la que lo dice)", got)
	}
}

// ---------------------------------------------------------------------------
// LA RACHA DEL PLAN 049: LA BIENVENIDA QUEDA FUERA
// ---------------------------------------------------------------------------

// TestBienvenida_NoCuentaEnLaRachaDeAutorrespuestas fija la exclusión que documenta
// sendSystemText (send.go): la bienvenida no es una auto-respuesta conversacional y no
// puede entrar en el histograma con el que el Plan 049 · Opción B va a calibrar un
// umbral de CORTE.
//
// Sin esta exclusión, TODA conversación de un tenant con `llm_intake` empezaría con la
// racha en 1 sin que el motor haya contestado nada: la distribución entera se desplaza
// un escalón y el p99 sale inflado por un mensaje que no es conversación. Un umbral
// calibrado sobre eso silencia clientes de verdad.
//
// MUTACIÓN (compila): en send.go, en sendSystemText, sustituir
//
//	ack, _, err := rt.emit(ctx, sessionID, to, []engine.Output{{Text: text}})
//	return ack, err
//
// por
//
//	return rt.send(ctx, sessionID, to, store.Key{}, []engine.Output{{Text: text}})
//
// (compila: `store` ya está importado en send.go). La racha viva pasa de 0 a 1 con un
// solo mensaje del cliente y este test se pone rojo.
func TestBienvenida_NoCuentaEnLaRachaDeAutorrespuestas(t *testing.T) {
	e := nuevoEntornoBienvenida(t, true)

	e.escribe(t, "hola", "wa-r1")

	if got := e.bienvenidas(); got != 1 {
		t.Fatalf("montaje: se esperaba la bienvenida y salieron %d", got)
	}
	if got := e.rt.MaxAutoreplyStreak(); got != 0 {
		t.Fatalf("la racha viva máxima es %d tras UNA bienvenida y CERO auto-respuestas del motor; "+
			"la bienvenida no es una auto-respuesta conversacional (ver sendSystemText)", got)
	}
}
