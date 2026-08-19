package runtime_test

// Plan 053 · Ola 6 · T6.1 — tests de aceptación de la HERENCIA DE PUNTEROS del
// reinicio (startLocked, runtime/start.go), que es el cierre de DEUDA-053.1.
//
// La invariante, en una frase: un Start que en vez de dar 409 REINICIA una
// conversación viva escribe un estado fresco, y ese estado tiene que seguir
// perteneciendo a quien pertenecía la fila que pisa — los dos punteros a evento
// sobreviven, las Vars no.
//
// ── LA PATOLOGÍA QUE FIJAN, dicha entera ────────────────────────────────────────
//
// Hasta esta tarea, startLocked construía `model.Conversation{TenantID, SessionID,
// ContactID}` y lo persistía. El Save es un upsert que escribe event_id y
// owner_event_id como cualquier otra columna (store/repository_postgres.go, con su
// propio test de que NO se las deja fuera del DO UPDATE), así que el reinicio APAGABA
// los dos punteros. Y por esta puerta nadie los vuelve a estampar: pointStateAtEvent
// solo corre desde enterEventFlow (runtime/events.go).
//
// La consecuencia no es que una columna quede a NULL: es que el evento dueño se queda
// `open` PARA SIEMPRE. closeIfFinished cierra al DUEÑO desde T1.6, y sin puntero no
// tiene por dónde alcanzarlo — el evento se queda vivo y `stale` en
// GET /api/v1/conversation-events, que es exactamente la patología que este plan
// existe para cerrar, por la única puerta que el plan no miraba. Por eso el segundo
// test de aquí abajo no se conforma con leer la columna y va a por la consecuencia.
//
// ── POR QUÉ SE HEREDA Y NO SE ABANDONA ──────────────────────────────────────────
//
// Un reinicio no es un salto de conversación. La guarda de posesión de T3.1 ya
// rechazó con 409 todo Start cuyo dueño corra OTRO flujo, así que el dueño que llega
// hasta el reinicio es el del MISMO flujo que se arranca: reiniciar el carrito del
// pedido #7 no abandona el #7. Darle el trato de summarizeAbandoned —resumir y
// cerrar, que es lo que reciben el salto por tipo, el event_stop y el escape global—
// mataría un evento vivo y vaciaría su intake sin que el cliente lo pidiera.
//
// ── DOS NOTAS DE MONTAJE, heredadas de owner_flow_guard_test.go ─────────────────
//
//  1. El flujo que se arranca es sampleFlow (menú), no el del carrito: desde D-054.5
//     startLocked rechaza con ErrDurableFlowNeedsEvent —ANTES de rt.store.Exists—
//     todo flujo con contenido durable, así que montar la escena con cart daría un
//     test verde que nunca ejecuta la línea bajo prueba.
//  2. La ResumePolicy se registra sobre model.NodeTypeMenu (el tipo del nodo inicial
//     de sampleFlow). En producción la única registrada es la del carrito, y el
//     carrito es durable ⇒ esta rama HOY no tiene camino vivo que la recorra: la
//     herencia, igual que la guarda de T3.1, blinda la pieza para el día que lo
//     tenga. Lo que NO es preventivo es el diagnóstico — la pérdida de punteros era
//     real y estaba escrita en el código.
//
// Todo en memoria: ni Postgres, ni relojes de pared, ni time.Sleep.

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// t61MarcaVarsViejas es una clave de Vars que SOLO existe en la fila que se pisa. Si
// aparece en el estado de después, el reinicio dejó de ser un reinicio.
const t61MarcaVarsViejas = "t61_vars_de_antes"

// t61PoliticaUnSoloReinicio contesta `restart=true` la PRIMERA vez y `false` después.
//
// No es un capricho de montaje, y el motivo se descubrió por ejecución: la
// ResumePolicy que estos tests registran la consultan DOS caminos distintos —
// restartableOnStart (el Start) y prepareResume (cada entrante que cae sobre un nodo
// con política)—. Con la testigo de T3.1, que contesta `true` siempre, el turno
// siguiente al reinicio vuelve a reiniciar en vez de avanzar y el flujo NUNCA alcanza
// su nodo final: el test de la consecuencia se quedaba con el evento en `open` por el
// montaje, no por el código, que es la peor forma de estar en rojo.
//
// Con `false` a partir de la segunda, prepareResume cae en su rama de navegación
// normal (siembra Vars y devuelve handled=false) y el entrante llega a engine.Step,
// que es lo que la escena necesita. Es además lo que hace la política REAL del
// carrito: reiniciar es la excepción (estado terminal), no la respuesta a todo.
type t61PoliticaUnSoloReinicio struct{ consultas int }

func (p *t61PoliticaUnSoloReinicio) Restart(_ context.Context, _, _ string, _ map[string]any) (bool, string, []modules.Effect, error) {
	p.consultas++
	return p.consultas == 1, "", nil, nil
}

func (p *t61PoliticaUnSoloReinicio) Seed(_ context.Context, _ string, _ map[string]any) error {
	return nil
}

var _ modules.ResumePolicy = (*t61PoliticaUnSoloReinicio)(nil)

// ---------------------------------------------------------------------------
// 1) La columna: los dos punteros sobreviven al reinicio, las Vars no
// ---------------------------------------------------------------------------

// TestReinicioPorStart_ConservaLosDosPunterosDeLaFilaQuePisa es la mitad barata de
// T6.1: mira lo que quedó escrito. La cara es el test siguiente, que mira lo que eso
// PROVOCA.
//
// Las Vars se comprueban en el mismo test a propósito, y no por ahorrar: herencia y
// limpieza son las dos mitades de la misma decisión (inheritedPointers lleva los
// punteros y NADA MÁS). Un test que solo exigiera la herencia lo aprobaría todo,
// incluido un `st := previo` que se trajera también las Vars ajenas — que es
// justamente la mezcla que la guarda de T3.1 existe para impedir.
func TestReinicioPorStart_ConservaLosDosPunterosDeLaFilaQuePisa(t *testing.T) {
	ctx := context.Background()
	evs := newMemEventStore(t31Ahora)
	pol := &t31PoliticaTestigo{}
	rt, repo, contacts, _ := nuevoRuntimeConGuarda(t, evs, pol, nil)
	cid := resolveID(t, contacts, testContact)

	// El dueño corre EL MISMO flujo que se va a arrancar: es el único caso que la
	// guarda de posesión deja pasar hasta el reinicio (con otro flujo sería 409 y no
	// habría nada que heredar).
	dueno := crearEventoDueno(t, evs, cid, "cart", testFlow)
	sembrarFilaConDueno(t, repo, cid, dueno.ID, dueno.ID, map[string]any{t61MarcaVarsViejas: true})

	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("el dueño corre el mismo flujo: el Start debe reiniciar; dio: %v", err)
	}
	if pol.reinicios != 1 {
		t.Fatalf("el montaje no ejercita nada si la política no se consulta: Restart=%d (¿la escena no llegó al reinicio?)", pol.reinicios)
	}

	st := loadState(t, repo, cid)
	if st.OwnerEventID != dueno.ID {
		t.Fatalf("DEUDA-053.1: el reinicio debe CONSERVAR el puntero al dueño (%q); quedó %q — un dueño apagado aquí deja el evento open para siempre, porque por esta puerta nadie lo vuelve a estampar",
			dueno.ID, st.OwnerEventID)
	}
	if st.EventID != dueno.ID {
		t.Fatalf("el reinicio debe conservar también el puntero al evento ACTIVO (%q); quedó %q", dueno.ID, st.EventID)
	}
	// La otra mitad: reiniciar ES arrancar limpio. Las Vars de antes no pueden viajar.
	if _, sigue := st.Vars[t61MarcaVarsViejas]; sigue {
		t.Fatalf("el reinicio arranca LIMPIO: las Vars de la fila que pisa no se heredan, y %q sigue ahí (%+v)", t61MarcaVarsViejas, st.Vars)
	}
	if st.CurrentNode != "root" {
		t.Fatalf("el reinicio debe dejar la conversación en el nodo inicial; quedó en %q", st.CurrentNode)
	}
}

// ---------------------------------------------------------------------------
// 2) La consecuencia: el dueño heredado SIGUE alcanzable por el cierre natural
// ---------------------------------------------------------------------------

// TestReinicioPorStart_ElDuenoHeredadoSeCierraAlTerminarElFlujo es el test que vale, y
// la razón de que exista es la lección del Plan 053 entero: una columna bien escrita
// no prueba nada si nadie la lee después. Aquí se lleva el flujo reiniciado hasta su
// final y se exige que el cierre natural de T1.6 alcance al dueño.
//
// Es la MEDIDA DIRECTA de la deuda: con el código de antes de T6.1 este test falla en
// el último assert con el evento en `open` —no por un error, sino porque ya no hay
// puntero por el que closeIfFinished pueda encontrarlo—, que es palabra por palabra lo
// que DEUDA-053.1 describía.
//
// El assert intermedio (tras el reinicio el evento sigue open) es el GUARD, mismo
// patrón que TestCierreNatural_CompletarElFlujoCierraElEvento: sin él, un cierre que
// disparara en el propio Start pasaría el resto del test igual y estaríamos midiendo
// otra cosa.
func TestReinicioPorStart_ElDuenoHeredadoSeCierraAlTerminarElFlujo(t *testing.T) {
	ctx := context.Background()
	evs := newMemEventStore(t31Ahora)
	rt, repo, contacts, _ := nuevoRuntimeConGuarda(t, evs, &t61PoliticaUnSoloReinicio{}, nil)
	cid := resolveID(t, contacts, testContact)

	dueno := crearEventoDueno(t, evs, cid, "cart", testFlow)
	sembrarFilaConDueno(t, repo, cid, dueno.ID, dueno.ID, map[string]any{t61MarcaVarsViejas: true})

	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start que reinicia: %v", err)
	}
	// GUARD: reiniciar no cierra a nadie. El evento sigue vivo, solo que su flujo
	// volvió al principio.
	if got := evs.statuses()[dueno.ID]; got != events.StatusOpen {
		t.Fatalf("reiniciar el flujo NO cierra el evento (nadie lo pidió); quedó %q", got)
	}

	// «1» lleva el menú a un nodo message sin next ⇒ el flujo TERMINA, y con él debe
	// morir su dueño (T1.6: el cierre natural transiciona el DUEÑO, no el activo).
	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "1", "t61-w1")); err != nil {
		t.Fatalf("completar el flujo reiniciado: %v", err)
	}
	// GUARD del guard: si el turno NO llegó a terminar el flujo, el assert de abajo
	// estaría midiendo el montaje y no la herencia. Se comprueba aquí y no se deduce.
	if st := loadState(t, repo, cid); !st.Finished() {
		t.Fatalf("el turno debía llevar el flujo a su nodo final y quedó en %q: la escena no está midiendo el cierre natural", st.CurrentNode)
	}
	if got := evs.statuses()[dueno.ID]; got != events.StatusClosed {
		t.Fatalf("DEUDA-053.1: al terminar el flujo, su evento DUEÑO debe cerrarse y quedó %q. Con el puntero apagado por el reinicio, closeIfFinished no tiene por dónde alcanzarlo y el evento se queda open para siempre y stale en la bandeja",
			got)
	}
	if st := loadState(t, repo, cid); st.OwnerEventID != "" || st.EventID != "" {
		t.Fatalf("el cierre natural apaga los DOS punteros en el mismo Save; quedaron activo=%q dueño=%q", st.EventID, st.OwnerEventID)
	}
}

// ---------------------------------------------------------------------------
// 3) Cada puntero va a SU columna cuando divergen
// ---------------------------------------------------------------------------

// TestReinicioPorStart_ActivoYDuenoDivergentesNoSeMezclan monta el estado que da
// nombre al plan —activo y dueño APUNTANDO A EVENTOS DISTINTOS, que es lo que ocurre
// en cuanto un menú se abre sobre un carrito a medias— y exige que el reinicio los
// herede por separado.
//
// Sin este test, heredar `st.EventID` en las DOS columnas pasaría los dos tests de
// arriba sin despeinarse: en ellos activo y dueño valen lo mismo. Y esa mutación no es
// hipotética — es el error más natural de cometer aquí, porque durante todo el Plan
// 043 hubo un solo puntero y las dos preguntas se contestaban con el mismo dato.
func TestReinicioPorStart_ActivoYDuenoDivergentesNoSeMezclan(t *testing.T) {
	ctx := context.Background()
	evs := newMemEventStore(t31Ahora)
	rt, repo, contacts, _ := nuevoRuntimeConGuarda(t, evs, &t31PoliticaTestigo{}, nil)
	cid := resolveID(t, contacts, testContact)

	// El dueño del flujo guardado (mismo flujo que se arranca ⇒ la guarda deja pasar)
	// y, ENCIMA, otro evento como activo: el menú, que por D-043.3 no tiene flujo
	// propio y por eso nace con FlowID "".
	dueno := crearEventoDueno(t, evs, cid, "cart", testFlow)
	activo := crearEventoDueno(t, evs, cid, "menu", "")
	sembrarFilaConDueno(t, repo, cid, activo.ID, dueno.ID, nil)

	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start que reinicia con punteros divergentes: %v", err)
	}

	st := loadState(t, repo, cid)
	if st.OwnerEventID != dueno.ID {
		t.Fatalf("el DUEÑO heredado debe ser el del flujo guardado (%q); quedó %q", dueno.ID, st.OwnerEventID)
	}
	if st.EventID != activo.ID {
		t.Fatalf("el ACTIVO heredado debe ser el evento al que le hablaba el contacto (%q); quedó %q — heredar el dueño en las dos columnas vuelve a fundir las dos preguntas que este plan separó",
			activo.ID, st.EventID)
	}
}

// ---------------------------------------------------------------------------
// 4) No-regresión: sin fila previa no se hereda nada
// ---------------------------------------------------------------------------

// TestArranqueSinFilaPrevia_NoHeredaNada fija la mitad aburrida, que es la que cubre
// al 99 % del tráfico: un Start sobre una clave libre abre la conversación con los dos
// punteros vacíos, igual que antes de T6.1.
//
// No es redundante con el subtest (c.1) de owner_flow_guard_test.go: aquel afirma que
// la POLÍTICA no se consulta, y este que las dos COLUMNAS nacen vacías. Un cambio que
// heredara punteros de la nada —de otra clave, de un cero mal inicializado— dejaría
// aquel verde y este rojo.
//
// El mismo assert cubre, de propina, el camino de enterEventFlow: esa puerta borra el
// flow_state antes de llamar a startLocked, así que entra por aquí exactamente igual
// (exists==false) y sigue dependiendo de pointStateAtEvent para estampar sus punteros
// DESPUÉS. Si la herencia hubiera empezado a inventar valores, ese camino habría sido
// el primero en romperse.
func TestArranqueSinFilaPrevia_NoHeredaNada(t *testing.T) {
	ctx := context.Background()
	evs := newMemEventStore(t31Ahora)
	rt, repo, contacts, _ := nuevoRuntimeConGuarda(t, evs, &t31PoliticaTestigo{}, nil)

	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start sobre una clave libre: %v", err)
	}

	st := loadState(t, repo, resolveID(t, contacts, testContact))
	if st.EventID != "" || st.OwnerEventID != "" {
		t.Fatalf("un arranque limpio no pertenece a ningún evento: activo=%q dueño=%q deberían estar vacíos", st.EventID, st.OwnerEventID)
	}
	if st.FlowID != testFlow || st.CurrentNode != "root" {
		t.Fatalf("el arranque limpio debe dejar el flujo en su nodo inicial; flow=%q nodo=%q", st.FlowID, st.CurrentNode)
	}
}
