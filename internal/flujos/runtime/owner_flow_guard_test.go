package runtime_test

// Plan 053 · Ola 3 · T3.1 (REQ-053.3) — tests de aceptación de la GUARDA DE POSESIÓN
// de restartableOnStart (runtime/resume.go, con su helper ownerFlowMismatch).
//
// La invariante que estos tres tests congelan, dicha en una frase: si el flujo que hay
// guardado en la fila pertenece a un evento DUEÑO cuyo `FlowID` NO es el que se está
// arrancando, ese Start no reinicia nada —ni siquiera llega a consultar la
// ResumePolicy del módulo— y acaba en el 409 determinista (ErrConversationExists). El
// daño que evita no es teórico: sin la guarda, un Start de otro flujo entraría por la
// puerta del reinicio con las Vars AJENAS de un carrito a medias en la mano y las
// pasaría a la política del módulo que arranca, mezclando dos conversaciones en una
// sola fila.
//
// Y su otra mitad, que ocupa DOS de los tres tests: la guarda es FAIL-OPEN. Solo muerde
// cuando la respuesta es determinista; ante cualquier duda deja pasar y el
// comportamiento queda byte a byte como antes del Plan 053. Un test que solo probara el
// rechazo dejaría sin vigilar justo la parte que puede romperle el arranque a un
// cliente por una lectura NUESTRA que falló.
//
// ── Cuatro decisiones de montaje que NO son casuales ────────────────────────────
//
//  1. El flujo que se ARRANCA es el de menú (sampleFlow, FlowID=testFlow), no el de
//     encuesta ni el de carrito, y no por comodidad: desde el Plan 054 · T2.3
//     (D-054.5) startLocked rechaza con ErrDurableFlowNeedsEvent —ANTES de
//     rt.store.Exists y por tanto ANTES de restartableOnStart— todo flujo con
//     contenido durable, y survey y cart lo son SIEMPRE. Montar la escena con
//     cualquiera de los dos daría un test verde que nunca ejecuta la línea bajo
//     prueba. El filo no se pierde: la guarda compara `FlowID` y NADA MÁS (ver el
//     comentario de ownerFlowMismatch, que declara esa elección como desviación
//     deliberada del enunciado de T3.1), así que qué módulo hay detrás de cada flujo
//     le es indiferente por diseño. De hecho el caso legítimo (a) usa un dueño del
//     MISMO `kind` que el del rechazo —los dos `cart`— y pasa: es la demostración de
//     que el comparando es el flujo, no el tipo de evento.
//
//  2. La ResumePolicy se REGISTRA (WithResumePolicy sobre model.NodeTypeMenu, el tipo
//     del nodo inicial de sampleFlow). Sin registrarla, restartableOnStart sale en su
//     segunda línea —`rt.resumePolicies[node.Type]` no encuentra nada— y la guarda ni
//     se ejercita: el test pasaría sin mirar nada. En producción la ÚNICA política
//     registrada es la del carrito (bootstrap.go) y el carrito es durable, así que
//     hoy este tramo no tiene camino vivo que lo recorra (lo dice startLocked en su
//     comentario del bloque `exists`): estos tests protegen la pieza para el día que
//     lo tenga, que es exactamente para lo que T3.1 la escribió.
//
//  3. El evento dueño se fabrica con CreateEvent y no con memEventStore.seedAlive:
//     seedAlive clava FlowID=testFlow Y SessionID=testSession (event_switch_test.go:640-644),
//     y aquí hace falta poder mover los dos — el FlowID del carrito para el desajuste,
//     "" para el fail-open del evento que nació sin flujo, y una SESIÓN ajena para el
//     fail-open del dueño de otra conversación. Por eso no se tocó el doble compartido.
//
//  4. Todo evento que estos tests siembran nace con EXACTAMENTE la sesión y el contacto
//     de la store.Key bajo prueba (testSession + el contact_id resuelto, que es la clave
//     que arma Start en start.go:53), salvo el ÚNICO subtest que sale de esa
//     conversación a propósito. No es cosmética: tras la revisión adversarial la guarda
//     compara SessionID/ContactID ANTES de morder (resume.go:254), así que un dueño
//     sembrado «de otra conversación» por descuido haría que la guarda dejara pasar —y
//     el test del rechazo se pondría rojo, o peor, verde por la razón equivocada—. Por
//     eso la fábrica compartida (crearEventoDueno) clava testSession y solo su variante
//     explícita (crearEventoEn) abre la sesión como parámetro.
//
// Todo en memoria: ni Postgres, ni relojes de pared, ni time.Sleep.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// t31Ahora es el reloj fijo del doble de eventos: da igual cuál sea, pero fijarlo hace
// el HistoryID determinista y deja el test sin reloj de pared.
var t31Ahora = time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)

const (
	// t31ClaveVars/t31MarcaVars marcan las Vars REALES del flow_state sembrado. Lo que
	// vigilan no es un detalle: la política tiene que recibir las Vars de la fila y no
	// un mapa vacío, porque el módulo decide con ellas si reinicia.
	t31ClaveVars = "t31_marca"
	t31MarcaVars = "vars-del-flow-state"
	// t31EfectoReinicio es el nombre del efecto que sintetiza la política testigo. No
	// lo materializa nadie: existe para que dispatch corra y el sink pueda leer el
	// EffectContext, que es lo que estos tests afirman.
	t31EfectoReinicio = "t31_reinicio_sintetico"
	// t31SesionAjena es OTRA sesión del MISMO tenant: el único identificador de estos
	// tests que se aparta a propósito de la store.Key bajo prueba (ver la nota 4 de la
	// cabecera). Solo lo usa el fail-open del dueño que vive en otra conversación.
	t31SesionAjena = "sess-de-otra-conversacion"
)

// ---------------------------------------------------------------------------
// Dobles
// ---------------------------------------------------------------------------

// t31PoliticaMortal es EL espía del primer test, con el patrón del Plan 043 · T5.1
// (carril_rapido_test.go, t51FatalResolver): no cuenta llamadas, MUERE si le llaman.
// Es la forma de afirmar «la guarda corta ANTES» sin depender de ningún efecto
// colateral observable — porque no lo hay: cortar antes o cortar después dan el MISMO
// 409, y la diferencia (las Vars ajenas viajando al módulo que arranca) solo se ve
// desde dentro de la política.
//
// Matan los DOS métodos del puerto. Seed no lo llama restartableOnStart por ningún
// camino, pero si algún día lo llamara sería igual de grave: sembraría en las Vars de
// OTRO evento las claves de navegación del módulo que arranca.
type t31PoliticaMortal struct{ t *testing.T }

func (p *t31PoliticaMortal) Restart(_ context.Context, tenantID, contactID string, vars map[string]any) (bool, string, []modules.Effect, error) {
	p.t.Fatalf("T3.1 (REQ-053.3): la guarda de posesión dejó llegar a ResumePolicy.Restart con las Vars de OTRO evento dueño — tenant=%q contacto=%q vars=%+v; el flujo guardado en esa fila NO es el que se arranca, así que el Start debe acabar en 409 sin consultar política alguna (resume.go, ownerFlowMismatch)",
		tenantID, contactID, vars)
	return false, "", nil, nil
}

func (p *t31PoliticaMortal) Seed(_ context.Context, tenantID string, vars map[string]any) error {
	p.t.Fatalf("T3.1 (REQ-053.3): la guarda de posesión dejó llegar a ResumePolicy.Seed — tenant=%q vars=%+v; sembrar las claves de navegación del módulo que arranca en las Vars de otro evento es justo la mezcla que esta guarda existe para impedir",
		tenantID, vars)
	return nil
}

var _ modules.ResumePolicy = (*t31PoliticaMortal)(nil)

// t31PoliticaTestigo es el doble de los caminos que SÍ deben pasar: registra la llamada
// —y las Vars con las que llegó— y contesta lo que contestaría una política que decide
// reiniciar, incluido UN efecto sintetizado. El efecto no es decoración: es lo que hace
// que restartableOnStart llame a dispatch, único punto donde se puede observar el
// EffectContext y comprobar que el EventID del estado que se reinicia sobrevive
// (retención del Plan 043 · T4.5.1, D-043.21).
type t31PoliticaTestigo struct {
	reinicios  int
	siembras   int
	varsVistas []map[string]any
}

func (p *t31PoliticaTestigo) Restart(_ context.Context, _, _ string, vars map[string]any) (bool, string, []modules.Effect, error) {
	p.reinicios++
	p.varsVistas = append(p.varsVistas, vars)
	return true, "", []modules.Effect{{
		Kind:    "persist",
		Name:    t31EfectoReinicio,
		Payload: map[string]any{"origen": "resume-policy"},
	}}, nil
}

func (p *t31PoliticaTestigo) Seed(_ context.Context, _ string, _ map[string]any) error {
	p.siembras++
	return nil
}

// ultimasVars devuelve las Vars de la ÚLTIMA llamada a Restart (nil si no hubo
// ninguna, que es distinto de «llegó con nil» y por eso los tests comprueban antes el
// contador).
func (p *t31PoliticaTestigo) ultimasVars() map[string]any {
	if len(p.varsVistas) == 0 {
		return nil
	}
	return p.varsVistas[len(p.varsVistas)-1]
}

var _ modules.ResumePolicy = (*t31PoliticaTestigo)(nil)

// t31ExistsSinFila es un FlowStore que dice SIEMPRE que la clave existe y delega todo
// lo demás en el repositorio real. Sirve para una sola cosa: alcanzar la rama
// `found == false` de restartableOnStart, que de otro modo es inalcanzable desde
// Start —startLocked solo llama a restartableOnStart cuando rt.store.Exists dio true,
// así que con el repositorio de verdad las dos lecturas siempre coinciden—.
//
// No es una escena inventada: Exists y Load son DOS lecturas separadas y el keyedMutex
// que las envuelve es EN MEMORIA y por proceso (keyedMutex, runtime_engine.go), así que
// con dos instancias de la plataforma contra la misma base la fila puede irse entre
// una y otra (un escape global, un Delete de enterEventFlow). Lo que se fija aquí es que
// esa ventana se comporta como el día antes del Plan 053: sin dueño que consultar, la
// guarda no se pronuncia y el reinicio sigue su curso con Vars nil.
type t31ExistsSinFila struct{ runtime.FlowStore }

func (t31ExistsSinFila) Exists(context.Context, store.Key) (bool, error) { return true, nil }

// ---------------------------------------------------------------------------
// Montaje
// ---------------------------------------------------------------------------

// nuevoRuntimeConGuarda arma el runtime MÍNIMO que ejercita la guarda: definición de
// sampleFlow publicada, la ResumePolicy registrada bajo el tipo del nodo inicial y,
// opcionalmente, el plano de eventos y un sink. `evs == nil` deja rt.events nil a
// propósito (el tercer fail-open); `sink == nil` deja el LogSink por defecto.
func nuevoRuntimeConGuarda(t *testing.T, evs runtime.EventStore, pol modules.ResumePolicy, sink runtime.EventSink) (*runtime.Runtime, *store.MemoryRepository, *contact.MemoryResolver, *fakeSender) {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar la definición del flujo que se arranca: %v", err)
	}
	contacts := contact.NewMemoryResolver(repo)
	sender := &fakeSender{}
	opts := []runtime.Option{runtime.WithResumePolicy(model.NodeTypeMenu, pol)}
	if evs != nil {
		opts = append(opts, runtime.WithEventStore(evs))
	}
	if sink != nil {
		opts = append(opts, runtime.WithEventSink(sink))
	}
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant}, contacts, discardLogger(), opts...)
	return rt, repo, contacts, sender
}

// sembrarFilaConDueno deja en el repositorio un flow_state VIVO del flujo de menú, con
// su puntero al evento activo (eventID), su puntero al evento DUEÑO (ownerEventID) y
// las Vars dadas. Es el punto de partida de todos los casos: la fila que ya está ahí
// cuando llega el Start. Su clave (testTenant + testSession + contactID) es la MISMA que
// arma Start (start.go:53) con los argumentos que le pasan estos tests — si dejara de
// serlo, el Start abriría una conversación nueva y ninguna de las escenas ocurriría.
func sembrarFilaConDueno(t *testing.T, repo *store.MemoryRepository, contactID, eventID, ownerEventID string, vars map[string]any) {
	t.Helper()
	if err := repo.Save(context.Background(), model.Conversation{
		TenantID: testTenant, SessionID: testSession, ContactID: contactID,
		FlowID: testFlow, FlowVersion: 1, CurrentNode: "root",
		Vars: vars, EventID: eventID, OwnerEventID: ownerEventID,
	}); err != nil {
		t.Fatalf("sembrar el flow_state con dueño: %v", err)
	}
}

// crearEventoDueno pare un evento vivo EN LA MISMA CONVERSACIÓN que la fila bajo prueba
// —testSession y el contactID que se le pasa, byte a byte los de la store.Key— con el
// kind y el FlowID que se le pidan. Esa coincidencia es un REQUISITO, no una comodidad:
// la guarda comprueba SessionID/ContactID antes de pronunciarse (resume.go:254), así que
// un dueño sembrado en otra sesión desactivaría el mordisco sin que ninguna otra
// aserción se enterara (nota 4 de la cabecera). Se usa CreateEvent (y no seedAlive)
// porque el FlowID es justo la variable independiente de estos tests — nota 3.
func crearEventoDueno(t *testing.T, evs *memEventStore, contactID, kind, flowID string) events.Event {
	t.Helper()
	return crearEventoEn(t, evs, testSession, contactID, kind, flowID)
}

// crearEventoEn es la MISMA fábrica con la sesión abierta como parámetro. Existe para un
// solo caso —el fail-open del dueño que vive en otra conversación del mismo tenant— y se
// deja explícita justamente para que salirse de testSession tenga que escribirse a mano.
func crearEventoEn(t *testing.T, evs *memEventStore, sessionID, contactID, kind, flowID string) events.Event {
	t.Helper()
	ev, err := evs.CreateEvent(context.Background(), events.NewEvent{
		TenantID: testTenant, SessionID: sessionID, ContactID: contactID,
		Kind: kind, FlowID: flowID, FlowVersion: 1,
	})
	if err != nil {
		t.Fatalf("crear el evento dueño (sesión=%q kind=%q flow=%q): %v", sessionID, kind, flowID, err)
	}
	return ev
}

// varsConMarca devuelve unas Vars reconocibles: lo que la política DEBE recibir.
func varsConMarca() map[string]any {
	return map[string]any{t31ClaveVars: t31MarcaVars}
}

// ---------------------------------------------------------------------------
// 1) El rechazo: el dueño es de otro flujo ⇒ 409 y la política ni se consulta
// ---------------------------------------------------------------------------

// TestRestartableOnStart_DuenoDeOtroModulo_NoInvocaPolicy es el corazón de T3.1: la
// fila guarda un flujo cuyo dueño es un evento `cart` (FlowID = el del carrito) y llega
// un Start del flujo de menú. Deben pasar DOS cosas, y las dos importan por separado:
//
//   - hacia fuera, ErrConversationExists ⇒ el 409 determinista de startLocked;
//   - hacia dentro, la ResumePolicy NO se consulta. Esto es lo que el espía mortal
//     vigila y lo que ninguna aserción sobre el error podría distinguir: sin la guarda
//     el 409 podría salir IGUAL (basta que la política conteste restart=false), pero
//     por el camino la política ya habría recibido las Vars de otro evento y podría
//     haber sintetizado efectos contra la conversación equivocada.
//
// Y una tercera, que se comprueba al final: la fila queda INTACTA. Un rechazo no puede
// dejar rastro en el estado del dueño legítimo.
func TestRestartableOnStart_DuenoDeOtroModulo_NoInvocaPolicy(t *testing.T) {
	ctx := context.Background()
	evs := newMemEventStore(t31Ahora)
	rt, repo, contacts, sender := nuevoRuntimeConGuarda(t, evs, &t31PoliticaMortal{t: t}, nil)
	cid := resolveID(t, contacts, testContact)

	// El dueño: un carrito a medias, con SU flujo congelado (events.Event.FlowID lo
	// fija el nacimiento y ya no se mueve).
	dueno := crearEventoDueno(t, evs, cid, "cart", testCartFlow)
	sembrarFilaConDueno(t, repo, cid, dueno.ID, dueno.ID, varsConMarca())

	_, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact))
	if !errors.Is(err, runtime.ErrConversationExists) {
		t.Fatalf("arrancar %q sobre una fila cuyo dueño corre %q debe dar ErrConversationExists (el 409 determinista); dio: %v",
			testFlow, testCartFlow, err)
	}

	st := loadState(t, repo, cid)
	if st.OwnerEventID != dueno.ID {
		t.Fatalf("el rechazo no puede tocar el puntero al dueño: owner_event_id = %q, seguía siendo %q", st.OwnerEventID, dueno.ID)
	}
	if st.FlowID != testFlow || st.CurrentNode != "root" {
		t.Fatalf("el rechazo no puede mover el flujo guardado: flow=%q nodo=%q", st.FlowID, st.CurrentNode)
	}
	if got, ok := st.Vars[t31ClaveVars].(string); !ok || got != t31MarcaVars {
		t.Fatalf("las Vars del dueño deben quedar intactas tras el 409; %s = %v", t31ClaveVars, st.Vars[t31ClaveVars])
	}
	if sender.count() != 0 {
		t.Fatalf("un Start rechazado con 409 no le habla al cliente; envió %d mensajes: %q", sender.count(), sender.texts())
	}
}

// ---------------------------------------------------------------------------
// 2) Los caminos legítimos: la guarda no puede cambiar NADA
// ---------------------------------------------------------------------------

// TestRestartableOnStart_CasoLegitimo_SinCambios recorre los tres caminos en los que la
// guarda debe ser invisible. En todos se exige lo mismo: la ResumePolicy SE INVOCA
// (con las Vars reales de la fila, no un mapa vacío) y el EffectContext que llega al
// sink conserva el `EventID` del estado que se reinicia.
//
// Esa última aserción es la que más protege: la retención del EventID es del Plan 043 ·
// T4.5.1 (D-043.21) —los efectos que sintetiza la política pertenecen al evento al que
// apuntaba la conversación que se reinicia, y el proyector los escribe declarando ese
// padre—. Se rompe sin ruido: un `eventID` que dejara de retenerse no rompería ninguna
// otra aserción de este paquete y solo se vería semanas después como filas huérfanas.
//
// Lo que estos tests NO afirman —ni pueden— es DÓNDE queda la guarda respecto de
// `vars = st.Vars`. Moverla por encima o por debajo de esa asignación da un programa
// indistinguible: al retornar, la local está muerta. La frontera OBSERVABLE, y la única
// que el espía mortal del primer test caza, es que la guarda corta ANTES de invocar
// policy.Restart. Colocarla además antes de retener `vars` es higiene de lectura, no una
// propiedad que ningún test pueda falsear.
func TestRestartableOnStart_CasoLegitimo_SinCambios(t *testing.T) {
	// (a) El dueño corre EL MISMO flujo que se arranca. Nótese que su `kind` es `cart`,
	// igual que el del test del rechazo: lo único que cambia entre pasar y no pasar es
	// el FlowID, que es exactamente lo que la guarda promete comparar.
	t.Run("el dueño corre el mismo flujo que se arranca", func(t *testing.T) {
		ctx := context.Background()
		evs := newMemEventStore(t31Ahora)
		pol := &t31PoliticaTestigo{}
		sink := &ecSink{}
		rt, repo, contacts, sender := nuevoRuntimeConGuarda(t, evs, pol, sink)
		cid := resolveID(t, contacts, testContact)

		dueno := crearEventoDueno(t, evs, cid, "cart", testFlow)
		sembrarFilaConDueno(t, repo, cid, dueno.ID, dueno.ID, varsConMarca())

		if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
			t.Fatalf("el dueño corre el MISMO flujo: la guarda no tiene nada que decir y el Start debe reiniciar como siempre; dio: %v", err)
		}
		if pol.reinicios != 1 {
			t.Fatalf("la ResumePolicy debe consultarse EXACTAMENTE una vez (así se comportaba antes del Plan 053); se consultó %d", pol.reinicios)
		}
		if got, ok := pol.ultimasVars()[t31ClaveVars].(string); !ok || got != t31MarcaVars {
			t.Fatalf("la política debe recibir las Vars REALES de la fila (con ellas decide si reinicia); recibió %+v", pol.ultimasVars())
		}
		ecs := sink.all()
		if len(ecs) != 1 {
			t.Fatalf("el efecto sintetizado por la política debe despacharse UNA vez por el fan-out; llegaron %d", len(ecs))
		}
		if ecs[0].EventID != dueno.ID {
			t.Fatalf("el efecto del reinicio debe declarar como padre el evento al que apuntaba la conversación (%q, T4.5.1/D-043.21); llegó con %q", dueno.ID, ecs[0].EventID)
		}
		if sender.count() != 1 {
			t.Fatalf("el reinicio re-renderiza el nodo inicial: debe salir UN mensaje, salieron %d", sender.count())
		}
	})

	// (b) La fila NO declara dueño. Es el caso normal y frecuente (menú puro de
	// D-043.3, o una fila legada anterior al backfill de T1.3). El evento ACTIVO de la
	// escena corre a propósito el flujo del CARRITO: si alguien "arreglara" la guarda
	// para mirar st.EventID en vez de st.OwnerEventID, este subtest se pondría rojo con
	// un 409 — que es justo el rechazo indebido que hay que impedir.
	t.Run("la fila no declara dueño", func(t *testing.T) {
		ctx := context.Background()
		evs := newMemEventStore(t31Ahora)
		pol := &t31PoliticaTestigo{}
		sink := &ecSink{}
		rt, repo, contacts, sender := nuevoRuntimeConGuarda(t, evs, pol, sink)
		cid := resolveID(t, contacts, testContact)

		activo := crearEventoDueno(t, evs, cid, "cart", testCartFlow)
		sembrarFilaConDueno(t, repo, cid, activo.ID, "", varsConMarca())

		if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
			t.Fatalf("sin owner_event_id no hay a quién preguntar: la guarda deja pasar y el Start reinicia como siempre; dio: %v", err)
		}
		if pol.reinicios != 1 {
			t.Fatalf("la ResumePolicy debe consultarse EXACTAMENTE una vez; se consultó %d (¿la guarda mordió mirando el evento ACTIVO en vez del DUEÑO?)", pol.reinicios)
		}
		if got, ok := pol.ultimasVars()[t31ClaveVars].(string); !ok || got != t31MarcaVars {
			t.Fatalf("la política debe recibir las Vars REALES de la fila; recibió %+v", pol.ultimasVars())
		}
		ecs := sink.all()
		if len(ecs) != 1 {
			t.Fatalf("el efecto sintetizado debe despacharse UNA vez; llegaron %d", len(ecs))
		}
		if ecs[0].EventID != activo.ID {
			t.Fatalf("el efecto debe declarar como padre el evento ACTIVO de la fila (%q); llegó con %q", activo.ID, ecs[0].EventID)
		}
		if sender.count() != 1 {
			t.Fatalf("el reinicio debe re-renderizar el nodo inicial (1 mensaje), salieron %d", sender.count())
		}
	})

	// (c) No hay fila de estado. Dos mitades, porque hay DOS verdades distintas que
	// fijar y ninguna cubre a la otra. Vive en una función CON NOMBRE y no en una
	// clausura aquí, y no por gusto: gocyclo (min-complexity 15, .golangci.yml) suma
	// la complejidad de las clausuras a la de la función que las contiene, y con las
	// tres dentro este test daba 26.
	t.Run("no hay fila de estado que reiniciar", t31SinFilaQueReiniciar)
}

// t31SinFilaQueReiniciar es el subtest (c) de
// TestRestartableOnStart_CasoLegitimo_SinCambios, extraído a su propia función por el
// límite de gocyclo (ver la nota en el punto de uso).
func t31SinFilaQueReiniciar(t *testing.T) {
	ctx := context.Background()

	// (c.1) Por la puerta normal: sin fila, startLocked ni siquiera llega a
	// restartableOnStart —su bloque vive dentro del `if exists`—, así que la
	// política no se consulta y el arranque es el de toda la vida. Se afirma el
	// contador a CERO a propósito: si alguien sacara la guarda (o la llamada a la
	// política) fuera de ese `if`, todo arranque limpio empezaría a pagar una
	// consulta que hoy no paga, y este es el único sitio donde eso se vería.
	pol := &t31PoliticaTestigo{}
	rt, repo, contacts, sender := nuevoRuntimeConGuarda(t, nil, pol, nil)
	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("un Start sobre una clave sin conversación debe abrirla sin más; dio: %v", err)
	}
	if pol.reinicios != 0 || pol.siembras != 0 {
		t.Fatalf("sin fila previa, startLocked no consulta política alguna (el bloque vive dentro del `if exists`); Restart=%d Seed=%d", pol.reinicios, pol.siembras)
	}
	if sender.count() != 1 {
		t.Fatalf("el arranque limpio debe enviar el nodo inicial (1 mensaje), envió %d", sender.count())
	}
	if st := loadState(t, repo, resolveID(t, contacts, testContact)); st.CurrentNode != "root" {
		t.Fatalf("el arranque limpio debe dejar la conversación en el nodo inicial; quedó en %q", st.CurrentNode)
	}

	// (c.2) Por la costura Exists/Load (ver t31ExistsSinFila): aquí SÍ se entra en
	// restartableOnStart y se recorre la rama `found == false`. Sin estado que
	// cargar no hay dueño que consultar, así que la guarda no se pronuncia: la
	// política se invoca con Vars nil y el efecto viaja SIN padre.
	repoSuelto := store.NewMemoryRepository()
	if _, err := repoSuelto.InsertDefinition(ctx, testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar la definición: %v", err)
	}
	contactosSueltos := contact.NewMemoryResolver(repoSuelto)
	senderSuelto := &fakeSender{}
	sinkSuelto := &ecSink{}
	polSuelta := &t31PoliticaTestigo{}
	rtSuelto := runtime.New(t31ExistsSinFila{FlowStore: repoSuelto}, newEngine(), senderSuelto,
		fakeResolver{tenantID: testTenant}, contactosSueltos, discardLogger(),
		runtime.WithResumePolicy(model.NodeTypeMenu, polSuelta),
		runtime.WithEventSink(sinkSuelto))

	if _, err := rtSuelto.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("con la clave marcada como existente pero SIN fila, restartableOnStart debe reiniciar igual que antes del Plan 053; dio: %v", err)
	}
	if polSuelta.reinicios != 1 {
		t.Fatalf("la ResumePolicy debe consultarse una vez también sin fila que cargar; se consultó %d", polSuelta.reinicios)
	}
	if v := polSuelta.ultimasVars(); v != nil {
		t.Fatalf("sin fila que cargar, la política recibe Vars nil (no un mapa de otra conversación); recibió %+v", v)
	}
	ecs := sinkSuelto.all()
	if len(ecs) != 1 {
		t.Fatalf("el efecto sintetizado debe despacharse UNA vez; llegaron %d", len(ecs))
	}
	if ecs[0].EventID != "" {
		t.Fatalf("sin fila no hay evento al que atribuir el efecto: EventID debe ser \"\", llegó %q", ecs[0].EventID)
	}
}

// ---------------------------------------------------------------------------
// 3) Fail-open: ante la duda, se deja pasar
// ---------------------------------------------------------------------------

// TestRestartableOnStart_DuenoNoResoluble_DejaPasar cubre los CUATRO fail-open que NO
// son triviales: la fila SÍ declara dueño, pero de ese dueño no se puede afirmar nada —o
// lo que se afirmaría vendría de otra conversación—. Son cuatro de los CINCO que
// enumera el comentario de ownerFlowMismatch; el quinto (ownerEventID == "", el caso
// normal y frecuente) se prueba arriba, en el subtest (b) de
// TestRestartableOnStart_CasoLegitimo_SinCambios, porque allí lo que interesa afirmar es
// el camino feliz entero y no solo el «deja pasar».
//
// La decisión que congela es de producto, no de estilo (ver el comentario de
// ownerFlowMismatch, y su precedente D-054.8): negarle el arranque a un cliente porque
// una lectura NUESTRA falló —o porque el único dato del que disponemos es de otra
// conversación— sería castigarle por un problema nuestro. Por eso el error del lookup se
// LOGUEA y no se propaga jamás hacia arriba, y por eso estos cuatro casos tienen que
// comportarse EXACTAMENTE como el día antes del Plan 053. Si un día alguien "endurece"
// la guarda haciéndola fail-closed, estos cuatro subtests son lo único que se pondría
// rojo antes de que lo hiciera un cliente por WhatsApp.
func TestRestartableOnStart_DuenoNoResoluble_DejaPasar(t *testing.T) {
	// (a) El LOOKUP del dueño falla. Contra Postgres real lo que cabe aquí es un fallo
	// TRANSITORIO de la BD, un timeout o un `ctx` ya cancelado — y NO las causas que uno
	// escribiría de memoria: este subtest NO modela «un id borrado» ni «un id corrupto».
	// Ninguno de los dos estados existe en producción: owner_event_id es UUID y un
	// no-UUID revienta al ESCRIBIR con 22P02 (nunca llega a leerse), y la FK de
	// internal/platform/storage/postgres/migrations/structure/0062_flow_state_owner_event_id.sql:107
	// va SIN `ON DELETE` a propósito, así que el DELETE del evento falla antes que dejar
	// el puntero colgando. Donde SÍ se construyen esos estados es en el runtime EN
	// MEMORIA — y eso es justo lo que hay aquí: el almacén vacío devuelve
	// events.ErrEventNotFound, que para la guarda es un error del lookup como cualquier
	// otro. Ninguno puede convertirse en un 409.
	t.Run("el dueño no existe en el almacén", func(t *testing.T) {
		ctx := context.Background()
		evs := newMemEventStore(t31Ahora) // vacío: nada que encontrar
		pol := &t31PoliticaTestigo{}
		rt, repo, contacts, sender := nuevoRuntimeConGuarda(t, evs, pol, nil)
		cid := resolveID(t, contacts, testContact)
		sembrarFilaConDueno(t, repo, cid, "", "ev-que-ya-no-esta", varsConMarca())

		if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
			t.Fatalf("un dueño irresoluble (ErrEventNotFound) NO puede convertirse en un rechazo: la guarda es fail-open; dio: %v", err)
		}
		if pol.reinicios != 1 {
			t.Fatalf("la ResumePolicy debe consultarse igual que antes del Plan 053; se consultó %d veces", pol.reinicios)
		}
		if sender.count() != 1 {
			t.Fatalf("y el cliente debe recibir su reinicio (1 mensaje), recibió %d", sender.count())
		}
	})

	// (b) El dueño existe pero nació SIN flujo. ⚠️ NO es «el caso del evento menu»: un
	// evento sin flujo NUNCA llega a ser dueño hoy. enterEventFlow saca el `menu` por
	// presentMenu antes de tocar flujo alguno (runtime/events.go:543-546), y cualquier
	// otro kind con flowID=="" llega a pointStateAtEvent con arrancado=false
	// (runtime/events.go:553-567), que es exactamente lo que impide estampar el dueño
	// (runtime/events.go:651-653). Es, por tanto, una rama INALCANZABLE POR CONSTRUCCIÓN
	// hoy, mantenida como defensa en profundidad y alcanzable solo desde aquí, con el
	// store en memoria. Lo que el subtest congela es la política, no el camino: de un
	// dueño con FlowID vacío no se puede afirmar que el flujo sea de otro, así que no se
	// afirma. El kind "menu" del montaje es decorado legible, no la causa del fail-open.
	t.Run("el dueño existe pero nació sin flujo", func(t *testing.T) {
		ctx := context.Background()
		evs := newMemEventStore(t31Ahora)
		pol := &t31PoliticaTestigo{}
		rt, repo, contacts, sender := nuevoRuntimeConGuarda(t, evs, pol, nil)
		cid := resolveID(t, contacts, testContact)

		dueno := crearEventoDueno(t, evs, cid, "menu", "")
		sembrarFilaConDueno(t, repo, cid, "", dueno.ID, varsConMarca())

		if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
			t.Fatalf("un dueño con FlowID vacío no permite afirmar desajuste alguno: la guarda debe dejar pasar; dio: %v", err)
		}
		if pol.reinicios != 1 {
			t.Fatalf("la ResumePolicy debe consultarse igual que antes del Plan 053; se consultó %d veces", pol.reinicios)
		}
		if sender.count() != 1 {
			t.Fatalf("y el cliente debe recibir su reinicio (1 mensaje), recibió %d", sender.count())
		}
	})

	// (c) El runtime corre SIN plano de eventos (rt.events nil). Es un despliegue
	// legítimo —WithEventStore es opcional (INV-6)— y en él nadie estampa
	// owner_event_id: no hay a quién preguntar. La fila se siembra CON dueño a
	// propósito, para que el fail-open sea el del `rt.events == nil` y no el de un
	// puntero vacío.
	t.Run("el runtime no tiene plano de eventos", func(t *testing.T) {
		ctx := context.Background()
		pol := &t31PoliticaTestigo{}
		rt, repo, contacts, sender := nuevoRuntimeConGuarda(t, nil, pol, nil)
		cid := resolveID(t, contacts, testContact)
		sembrarFilaConDueno(t, repo, cid, "", "ev-de-un-despliegue-sin-eventos", varsConMarca())

		if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
			t.Fatalf("sin plano de eventos no hay a quién preguntar por el dueño: la guarda debe ser invisible (INV-6); dio: %v", err)
		}
		if pol.reinicios != 1 {
			t.Fatalf("la ResumePolicy debe consultarse igual que antes del Plan 053; se consultó %d veces", pol.reinicios)
		}
		if sender.count() != 1 {
			t.Fatalf("y el cliente debe recibir su reinicio (1 mensaje), recibió %d", sender.count())
		}
	})

	// (d) El dueño resuelve SIN error, pero es de OTRA conversación del MISMO tenant.
	// Es el único de los cinco fail-open que no nace de un fallo: acierta, con el dato
	// equivocado. El lookup acota por `id AND tenant_id` y nada más
	// (memEventStore.GetEventForTenant, event_switch_test.go:214-223, misma cláusula que
	// el repositorio real de flujos/events), y la FK de la 0062 tampoco liga sesión ni
	// contacto, así que un puntero cruzado resuelve limpiamente.
	//
	// El montaje CUMPLE deliberadamente el caso que muerde —dueño con el FlowID del
	// carrito frente a un Start del flujo de menú, idéntico al del primer test— y aun
	// así debe pasar: emitir un veredicto determinista sobre el flujo y las Vars de otra
	// conversación sería un 409 FALSO contra un cliente que no tiene nada que ver, y por
	// eso la guarda prefiere ENSANCHAR el fail-open (convierte un mordisco en un «deja
	// pasar», nunca al revés). Hoy ningún escritor produce este estado —pointStateAtEvent
	// estampa el evento de la MISMA conversación, runtime/events.go:651-653—: esto es
	// blindaje contra una corrupción o un backfill futuro.
	t.Run("el dueño es de otra conversación del mismo tenant", func(t *testing.T) {
		ctx := context.Background()
		evs := newMemEventStore(t31Ahora)
		pol := &t31PoliticaTestigo{}
		rt, repo, contacts, sender := nuevoRuntimeConGuarda(t, evs, pol, nil)
		cid := resolveID(t, contacts, testContact)

		// Mismo tenant y mismo contacto; otra SESIÓN. Es el único sitio del fichero que
		// se aparta de testSession, y por eso usa crearEventoEn y no crearEventoDueno.
		ajeno := crearEventoEn(t, evs, t31SesionAjena, cid, "cart", testCartFlow)
		sembrarFilaConDueno(t, repo, cid, "", ajeno.ID, varsConMarca())

		if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
			t.Fatalf("el dueño %q vive en la sesión %q y la fila en %q: la guarda NO puede pronunciarse sobre datos de otra conversación —sería un 409 falso— y debe dejar pasar; dio: %v",
				ajeno.ID, t31SesionAjena, testSession, err)
		}
		if pol.reinicios != 1 {
			t.Fatalf("la ResumePolicy debe consultarse EXACTAMENTE una vez: el desajuste de FlowID (%q ≠ %q) es REAL, pero se lee de una conversación AJENA, así que no autoriza a morder; se consultó %d veces",
				testCartFlow, testFlow, pol.reinicios)
		}
		if got, ok := pol.ultimasVars()[t31ClaveVars].(string); !ok || got != t31MarcaVars {
			t.Fatalf("la política debe recibir las Vars REALES de la fila, igual que en cualquier otro fail-open; recibió %+v", pol.ultimasVars())
		}
		if sender.count() != 1 {
			t.Fatalf("y el cliente debe recibir su reinicio (1 mensaje), recibió %d", sender.count())
		}
	})
}
