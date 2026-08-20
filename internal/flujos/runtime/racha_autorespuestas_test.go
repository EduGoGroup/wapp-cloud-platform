package runtime_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// Rachas de auto-respuestas consecutivas por conversación (Plan 049 · Opción A:
// OBSERVAR). Aquí se fija el contrato de lo que el motor CUENTA y de CUÁNDO lo
// publica; la mecánica del contador en sí vive en streak_test.go.
//
// Lo que estos tests protegen es una decisión de diseño, no una función: que el
// EPISODIO se mida entre destrucciones del estado conversacional y NO entre
// entrantes. Es la clase de propiedad que un refactor bienintencionado rompe sin
// que nada se ponga rojo — la métrica sigue existiendo, sigue exportándose, y
// simplemente deja de decir la verdad.

const testRachaFlow = "racha-bucle"

// observadorRachas recoge las longitudes que el runtime publica por el hook. El
// runtime procesa entrantes concurrentes, así que el doble se sincroniza (mismo
// patrón que contadorCortes en reactive_blocked_visibility_test.go).
type observadorRachas struct {
	mu       sync.Mutex
	cerradas []int
}

func nuevoObservadorRachas() *observadorRachas { return &observadorRachas{} }

func (o *observadorRachas) registrar(racha int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cerradas = append(o.cerradas, racha)
}

func (o *observadorRachas) observadas() []int {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]int, len(o.cerradas))
	copy(out, o.cerradas)
	return out
}

// flujoEnBucle es un menú de UNA sola opción que vuelve a sí mismo: cada «1» del
// contacto produce una auto-respuesta (el prompt otra vez) y deja la conversación
// viva en el mismo nodo. Sirve para encadenar tantos turnos como haga falta sin que
// el flujo termine, sin agotar el reprompt acotado (la opción es válida, así que el
// contador de inválidos ni se toca) y sin depender de ningún módulo durable.
//
// Es, a escala mínima, el catálogo paginado del §5 del plan: el cliente pide «ver
// más» una y otra vez y el motor contesta una y otra vez. Legítimo, largo, y
// exactamente igual —turno a turno— que un bucle contra un autorespondedor.
func flujoEnBucle() model.Flow {
	return model.Flow{
		FlowID:  testRachaFlow,
		Initial: "root",
		Nodes: map[string]model.Node{
			"root": {
				Type:    model.NodeTypeMenu,
				Prompt:  "¿Qué quieres?\n1) Ver más",
				Options: map[string]string{"1": "root"},
			},
		},
	}
}

// newRachaRuntime arma un runtime con el flujo dado ya publicado y el observador de
// rachas cableado. Sin trigger.Resolver a propósito (queda el Noop): así un entrante
// sobre una conversación ya soltada NO arranca nada y no abre una racha nueva que
// enturbie la cuenta.
//
// Devuelve SOLO lo que los tests usan —el runtime y el sender—: el repo y el resolver
// de contactos se quedan dentro porque ningún llamante los tocaba y devolverlos solo
// obligaba a escribir `_, _` en cada línea.
func newRachaRuntime(t *testing.T, flujo model.Flow, obs *observadorRachas) (*runtime.Runtime, *fakeSender) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, flujo); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	sender := &fakeSender{}
	contacts := contact.NewMemoryResolver(repo)
	opts := []runtime.Option{}
	if obs != nil {
		opts = append(opts, runtime.WithAutoreplyStreakHook(obs.registrar))
	}
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(), opts...)
	return rt, sender
}

// EL TEST QUE DA SENTIDO A TODO ESTO: un entrante del contacto NO reinicia la racha.
//
// El modo de fallo que fija es el del MP-09 llevado a esta métrica: en un motor
// REACTIVO toda auto-respuesta es la respuesta a un entrante, así que reiniciar la
// cuenta cuando llega un mensaje del contacto dejaría la racha clavada en 1 para
// SIEMPRE. La métrica seguiría publicándose, el histograma seguiría teniendo datos,
// y el p99 sería 1 tanto en una conversación de dos frases como en un bucle de
// cuatrocientas vueltas contra un autorespondedor — es decir, sería CIEGA justo a lo
// único que existe para ver.
//
// Se descartó por eso: lo que separa el recorrido legítimo del bucle no es ningún
// mensaje concreto (los dos son entrante→saliente→entrante→saliente) sino cuánto
// dura el episodio. Por eso se mide el episodio, y el episodio solo termina cuando
// muere el estado de la conversación. Ver la cabecera de streak.go.
func TestRacha_EntranteNoReiniciaLaRacha(t *testing.T) {
	obs := nuevoObservadorRachas()
	rt, sender := newRachaRuntime(t, flujoEnBucle(), obs)
	ctx := context.Background()

	// Turno 1: el arranque por API ya es la primera auto-respuesta del episodio.
	if _, err := rt.Start(ctx, testTenant, testRachaFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Turnos 2..6: conversación NORMAL — el contacto escribe, el motor contesta.
	for _, waID := range []string{"wamid.r1", "wamid.r2", "wamid.r3", "wamid.r4", "wamid.r5"} {
		if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", waID)); err != nil {
			t.Fatalf("HandleIncoming %s: %v", waID, err)
		}
	}

	const turnos = 6
	if sender.count() != turnos {
		t.Fatalf("el motor debió emitir %d auto-respuestas (1 arranque + 5 avances), emitió %d: %v",
			turnos, sender.count(), sender.texts())
	}
	viva := rt.MaxAutoreplyStreak()
	if viva == 1 {
		t.Fatalf("la racha viva quedó en 1 tras %d turnos: el entrante está reiniciando el episodio "+
			"y la métrica es CIEGA (es el modo de fallo que este test existe para impedir)", turnos)
	}
	if viva != turnos {
		t.Fatalf("la racha viva = %d, quiero %d (una por turno emitido, no una por mensaje)", viva, turnos)
	}
	// El episodio SIGUE ABIERTO: nada se ha destruido, así que no hay nada que observar
	// todavía. Publicar aquí convertiría el histograma en la distribución de los
	// prefijos de la racha (1, 2, 3…) y hundiría el p99 hacia 1.
	if got := obs.observadas(); len(got) != 0 {
		t.Fatalf("con el episodio vivo el hook no debe recibir nada, recibió %v", got)
	}
}

// 🔴 EL TEST QUE EL PLAN PIDE (§5, «una racha larga legítima no falsea el dato»),
// AQUÍ Y NO SOLO EN streak_test.go.
//
// Su gemelo interno —TestRacha_RachaLargaLegitimaNoSeFalsea— hace 30 Inc a mano sobre
// el streakCounter aislado: prueba la ARITMÉTICA del contador, que también hace falta,
// pero NO la propiedad de diseño. Si mañana alguien deja de llamar a Inc desde `send`,
// o mete un Close en el camino del entrante «para limpiar», aquel test sigue verde: el
// contador seguiría sumando bien, solo que ya nadie lo llamaría. Y el plan lo pide
// precisamente como defensa contra ese refactor.
//
// Este recorre el MOTOR ENTERO: 30 turnos reales entrante→auto-respuesta sobre el menú
// que vuelve a sí mismo, que es el catálogo paginado del §5 a escala mínima (el cliente
// pide «ver más» treinta veces y el motor contesta treinta veces). Todas legítimas,
// todas pedidas por una persona, ninguna cerrando el episodio.
func TestRacha_RachaLargaLegitimaNoSeFalseaEnElMotor(t *testing.T) {
	obs := nuevoObservadorRachas()
	rt, sender := newRachaRuntime(t, flujoEnBucle(), obs)
	ctx := context.Background()

	const turnos = 30

	// Turno 1: el arranque por API ya es la primera auto-respuesta del episodio.
	if _, err := rt.Start(ctx, testTenant, testRachaFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Turnos 2..30: el cliente pide «ver más» y el menú se vuelve a pintar. waID
	// distinto en cada uno: dos entrantes con el mismo id serían un duplicado y el
	// deduper se comería el turno, dejando la racha corta por un motivo que no tiene
	// nada que ver con lo que este test mide.
	for i := 2; i <= turnos; i++ {
		waID := fmt.Sprintf("wamid.largo-%02d", i)
		if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", waID)); err != nil {
			t.Fatalf("HandleIncoming %s: %v", waID, err)
		}
		// La comprobación que da sentido al test: NADA se ha observado todavía. Un
		// recorrido legítimo no debe fragmentarse en trozos por el camino — si el
		// entrante cerrara el episodio, aquí habría 29 rachas de 1 y el p99 de la
		// métrica sería 1 tanto para este pedido como para un bucle de 400 vueltas.
		if vistas := obs.observadas(); len(vistas) != 0 {
			t.Fatalf("turno %d: el episodio sigue vivo y el hook no debe haber recibido nada, recibió %v "+
				"(alguien está cerrando el episodio en el camino del entrante)", i, vistas)
		}
	}

	if sender.count() != turnos {
		t.Fatalf("el motor debió emitir %d auto-respuestas (1 arranque + %d avances), emitió %d",
			turnos, turnos-1, sender.count())
	}
	// Y el motor tiene que poder VER el 30: es el número que el §9 necesita para elegir
	// un umbral con datos. Si sale 1, el entrante está reiniciando; si sale 0, `send`
	// dejó de contar.
	if viva := rt.MaxAutoreplyStreak(); viva != turnos {
		t.Fatalf("la racha viva debe llegar a %d, es %d (si es 1 el entrante reinicia el episodio; "+
			"si es 0, `send` ya no cuenta y la métrica está muerta sin que nada más se ponga rojo)",
			turnos, viva)
	}
	if vistas := obs.observadas(); len(vistas) != 0 {
		t.Fatalf("con el episodio todavía vivo el histograma no debe tener nada, tiene %v", vistas)
	}
}

// Al morir el estado conversacional el episodio se cierra y su longitud se observa
// UNA sola vez. Aquí la muerte llega por el camino más común: el flujo alcanza su
// nodo terminal y el entrante SIGUIENTE suelta el flow_state (releaseFinishedState).
func TestRacha_CierreDeConversacionObservaLaRacha(t *testing.T) {
	obs := nuevoObservadorRachas()
	rt, sender := newRachaRuntime(t, sampleFlow(), obs)
	ctx := context.Background()

	// Turno 1: el menú.
	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Turno 2: «1» → nodo message «ventas» → el flujo llega a su fin (centinela).
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.c1")); err != nil {
		t.Fatalf("HandleIncoming avance: %v", err)
	}
	if sender.count() != 2 {
		t.Fatalf("el episodio debió emitir 2 auto-respuestas, emitió %d: %v", sender.count(), sender.texts())
	}
	if got := obs.observadas(); len(got) != 0 {
		t.Fatalf("el flujo terminó pero el flow_state sigue ahí: nada que observar aún, se observó %v", got)
	}

	// El entrante siguiente encuentra el estado TERMINAL, lo suelta (Delete) y con él
	// muere el episodio. Con el resolver Noop no se arranca nada detrás, así que no se
	// abre ninguna racha nueva.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "hola", "wamid.c2")); err != nil {
		t.Fatalf("HandleIncoming tras el fin: %v", err)
	}

	got := obs.observadas()
	if len(got) != 1 {
		t.Fatalf("el cierre del episodio debe observarse UNA sola vez, se observó %d veces: %v", len(got), got)
	}
	if got[0] != 2 {
		t.Fatalf("la racha cerrada = %d, quiero 2 (las dos auto-respuestas del episodio)", got[0])
	}
	// Cerrado es cerrado: la entrada se borró, así que ya no hay racha viva que publicar.
	if viva := rt.MaxAutoreplyStreak(); viva != 0 {
		t.Fatalf("tras cerrar el único episodio la racha viva debe ser 0, es %d", viva)
	}
	// Un cierre más sobre la misma clave no vuelve a contar (Close es idempotente): el
	// siguiente entrante ya no encuentra estado y no reabre nada.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "hola", "wamid.c3")); err != nil {
		t.Fatalf("HandleIncoming sobre conversación ya soltada: %v", err)
	}
	if got := obs.observadas(); len(got) != 1 {
		t.Fatalf("el episodio no debe observarse dos veces, se observó %v", got)
	}
}

// Sin hook inyectado el motor se comporta igual (contrato nil-safe): observar es
// opcional, decidir no. Hermano de TestReactiveBlocked_SinHookNoRompe, y cubre el
// arranque de cualquier consumidor que no cablee métricas — incluidos los cientos de
// tests del paquete que construyen el runtime sin Options.
func TestRacha_SinHookNoRompe(t *testing.T) {
	rt, sender := newRachaRuntime(t, sampleFlow(), nil)
	ctx := context.Background()

	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start sin hook: %v", err)
	}
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "wamid.nh1")); err != nil {
		t.Fatalf("HandleIncoming sin hook: %v", err)
	}
	if sender.count() != 2 {
		t.Fatalf("sin hook el flujo debe funcionar igual (2 auto-respuestas), emitió %d", sender.count())
	}
	// El contador existe aunque nadie lo escuche: el gauge de la racha viva sigue
	// sabiendo contestar (es del runtime, no del hook).
	if viva := rt.MaxAutoreplyStreak(); viva != 2 {
		t.Fatalf("sin hook la racha viva debe seguir contándose, es %d y quiero 2", viva)
	}
	// Y el cierre tampoco revienta al no encontrar a quién reportarle.
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "hola", "wamid.nh2")); err != nil {
		t.Fatalf("cierre de episodio sin hook: %v", err)
	}
	if viva := rt.MaxAutoreplyStreak(); viva != 0 {
		t.Fatalf("tras el cierre la racha viva debe ser 0, es %d", viva)
	}
}
