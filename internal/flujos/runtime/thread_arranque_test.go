// thread_arranque_test.go — Plan 044 · T1.4. EL LITERAL DEL MENSAJE QUE ABRE EL EVENTO.
//
// ⏳ NINGUNO DE ESTOS TESTS SE HA EJECUTADO. Se escribieron en un entorno sin Go, sin
// red y sin Postgres, así que no hay ninguno declarado como pasado. Lo que sí está
// escrito es CÓMO ponerlo rojo: cada test lleva su mutación, y todas están elegidas
// para que el paquete SIGA COMPILANDO (una mutación que no compila no prueba nada,
// porque el compilador la caza antes que el test).
//
// # EL DEFECTO QUE CUBREN, DICHO ENTERO
//
// `persistTurnMessages` (thread.go) tenía UN SOLO llamante: `advanceLiveStep`, el
// turno de una conversación YA VIVA. El camino del disparador —handleTrigger →
// startFromDecision → beginEvent → birthEvent → enterEventFlow → startLocked— no
// escribía NI UNA fila `message`, así que el «quiero presupuesto de 20 hamburguesas»
// que ABRE el pedido no llegaba nunca a `conversation_event_messages`.
//
// Y eso importa desde el 2026-08-22, no antes: ese mismo mensaje SÍ entra ya en
// `intake_jobs.source_refs` y ancla el `message_ts` (observeForAggregation en
// startFromDecision, D-044.9). O sea que la REFERENCIA existía y el LITERAL no. El
// compositor de T1.4 leía el hilo, no encontraba el primer mensaje de la ráfaga, y
// componía un `source_text` que EMPEZABA POR EL SEGUNDO — sin error, sin log y sin
// que nadie lo notara, que es la forma peor de fallar.
//
// # POR QUÉ ESTE FICHERO Y NO thread_test.go
//
// thread_test.go fija el turno NORMAL, y su
// TestHilo_ConLaFeatureGuardaClienteYNegocioEnOrden decía, en su docstring, que «el
// turno de ARRANQUE no pasa por advanceLive y —a propósito, mínimo de la ola— no se
// persiste». Eso ERA cierto y dejó de serlo: lo que aquel test llamaba mínimo aceptado
// es justo el defecto que T1.4 no puede tolerar, porque T1.4 es el LECTOR. Aquí se fija
// la conducta nueva; allí los dos asertos que la contradecían quedaron reconciliados el
// 2026-08-22 (ver el bloque final de este fichero).
package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
)

// literalDelArranque es el texto con el que el cliente abre el pedido. Se escribe
// como constante y no en línea porque los cuatro tests lo comparan y porque es EL
// dato: si este literal no está en el hilo, el presupuesto se compone sin él.
const literalDelArranque = "carrito"

// rolesYCuerpos aplana las filas `message` de un evento a algo legible en un t.Fatalf.
// El doble guarda el CLARO (el cifrado lo fija el store real, no este test), así que
// esto no expone nada que el doble no tuviera ya en memoria.
func rolesYCuerpos(rows []mensajeHilo) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, string(r.role)+": "+r.body)
	}
	return out
}

// cuerposDelCliente devuelve SOLO lo que habló el cliente, en orden. Es la lista
// sobre la que se cuentan duplicados: las filas del NEGOCIO sí pueden repetir cuerpo
// legítimamente —el menú re-imprime su misma pantalla cuando el texto no casa
// ninguna opción—, así que contar duplicados sobre todas las filas daría un falso
// rojo. La duplicación que este fichero vigila es la del LITERAL DEL CLIENTE, que es
// la que convertiría «20 hamburguesas» en 40.
func cuerposDelCliente(rows []mensajeHilo) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.role == events.RoleClient {
			out = append(out, r.body)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// (a) CON LA FEATURE: la fila del entrante existe, con su literal y su rol
// ---------------------------------------------------------------------------

// TestHiloArranque_ElLiteralQueAbreElEventoEntraEnElHilo es el test del defecto. UN
// SOLO entrante —el que PARE el evento— tiene que dejar el hilo del evento recién
// nacido con dos filas y en este orden:
//
//	[0] client   "carrito"                       ← el literal que abre el pedido
//	[1] business "Hola 👋\n1) Ventas\n2) Soporte"  ← lo que el flujo contestó al arrancar
//
// Las dos mitades importan y no son la misma. La [0] es la que faltaba y la que el
// compositor necesita (es la única de la que puede salir una `evidence`, REQ-13); la
// [1] es lo que hace que el hilo cuente el turno completo y no medio turno.
//
// El ORDEN también es contrato: `listThreadSQL` devuelve por `seq` ascendente y el
// compositor concatena en ese orden, así que unas salidas escritas antes del entrante
// pondrían al negocio hablando primero en el prompt del LLM.
//
// MUTACIÓN (compila): en start.go, en startLocked, borrar la línea
//
//	rt.persistOpeningTurn(ctx, tenantID, sessionID, eventID, opening, outs)
//
// El parámetro `opening` queda sin usar dentro de la función, cosa que Go permite en
// parámetros, así que el paquete compila y este test se pone rojo con CERO filas —que
// es exactamente el estado del que se viene—.
func TestHiloArranque_ElLiteralQueAbreElEventoEntraEnElHilo(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(testTenant, entitlements.FeatureLLMIntake)
	rt, evs, _ := newThreadRuntime(t, feats, eventStartRule(literalDelArranque, "cart"))
	ctx := context.Background()

	// UN SOLO entrante: sin conversación viva, así que va por el camino del disparador.
	if err := rt.HandleIncoming(ctx, testSession,
		incoming(testContact, literalDelArranque, "wamid.hilo-arranque-1")); err != nil {
		t.Fatalf("HandleIncoming del arranque: %v", err)
	}

	alive := evs.alive()
	if len(alive) != 1 {
		t.Fatalf("montaje: el arranque debe parir UN evento vivo; hay %d", len(alive))
	}
	ev := alive[0]

	rows := evs.mensajesDe(ev.ID)
	if len(rows) < 2 {
		t.Fatalf("el turno de ARRANQUE debe dejar el literal del cliente Y la respuesta del flujo; "+
			"esperaba al menos 2 filas y hay %d: %v", len(rows), rolesYCuerpos(rows))
	}
	if rows[0].role != events.RoleClient {
		t.Fatalf("rows[0].role = %q; esperado %q — la primera voz del hilo es la del CLIENTE, "+
			"que es quien abrió el evento", rows[0].role, events.RoleClient)
	}
	if rows[0].body != literalDelArranque {
		t.Fatalf("rows[0].body = %q; esperado %q — es EL literal del que sale el source_text "+
			"y la base de fechas del presupuesto (D-044.9)", rows[0].body, literalDelArranque)
	}
	for i, r := range rows[1:] {
		if r.role != events.RoleBusiness {
			t.Fatalf("rows[%d].role = %q; esperado %q — tras el entrante solo vienen las salidas "+
				"del arranque, y esas son la voz del negocio", i+1, r.role, events.RoleBusiness)
		}
		if r.body == "" {
			t.Fatalf("rows[%d] llegó con el cuerpo vacío; una salida sin texto no debe dejar fila "+
				"(la poda es la de persistTurnMessages)", i+1)
		}
	}

	// Y NADA fue a parar a otro evento: el hilo del arranque cuelga del evento que
	// acaba de nacer y de ninguno más.
	if evs.totalMensajes() != len(rows) {
		t.Fatalf("hay filas de hilo fuera del evento del arranque: total=%d, del evento=%d — "+
			"esperado que fueran el MISMO número", evs.totalMensajes(), len(rows))
	}
}

// ---------------------------------------------------------------------------
// (b) SIN LA FEATURE: cero filas — el fail-closed sigue intacto
// ---------------------------------------------------------------------------

// TestHiloArranque_SinLaFeatureCeroFilas cierra la puerta trasera. El punto de
// escritura es NUEVO, y un punto de escritura nuevo que se salte el gate escribiría
// texto literal de tenants que no tienen el pipeline contratado —y lo haría solo en
// el PRIMER mensaje de cada pedido, que es el sitio donde nadie miraría—.
//
// El montaje es idéntico al de (a) salvo por la feature: el evento nace igual, el
// flujo arranca igual y al cliente se le contesta igual. Lo único que no ocurre son
// las FILAS.
//
// Es la misma pareja que ya forman TestHilo_SinLaFeatureNoSeEscribeNiUnMensaje y su
// gemelo en thread_test.go, y por el mismo motivo: `persistOpeningTurn` no tiene gate
// propio, delega en `persistTurnMessages` → `threadAllowed`. Este test es lo que
// demuestra que esa delegación existe de verdad y no es solo una intención escrita.
//
// MUTACIÓN (compila): en thread.go, en persistTurnMessages, borrar entero el bloque
//
//	if !rt.threadAllowed(ctx, tenantID, sessionID, eventID) {
//		return
//	}
//
// El paquete compila (los cuatro parámetros se siguen usando más abajo) y este test se
// pone rojo con filas de un tenant sin `llm_intake`, mientras (a) sigue verde — que es
// lo que separa «falta el gate» de «falta el productor».
func TestHiloArranque_SinLaFeatureCeroFilas(t *testing.T) {
	// entitlements.NewFake() sin habilitar nada: el tenant NO tiene llm_intake.
	rt, evs, _ := newThreadRuntime(t, entitlements.NewFake(), eventStartRule(literalDelArranque, "cart"))
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, testSession,
		incoming(testContact, literalDelArranque, "wamid.hilo-arranque-2")); err != nil {
		t.Fatalf("HandleIncoming del arranque: %v", err)
	}

	// Precondición: el evento SÍ nació. Sin esto, un cero podría venir de que no hubo
	// evento en absoluto, y el test pasaría en verde sin probar el gate.
	if n := len(evs.alive()); n != 1 {
		t.Fatalf("montaje: el evento debe nacer igual sin la feature; eventos vivos = %d, esperado 1", n)
	}
	if got := evs.totalMensajes(); got != 0 {
		t.Fatalf("totalMensajes = %d; esperado 0 — sin llm_intake el arranque no puede dejar "+
			"NI UNA fila de texto literal (fail-closed, D-043.23)", got)
	}
	if got := evs.totalFueraDeTurno(); got != 0 {
		t.Fatalf("totalFueraDeTurno = %d; esperado 0 — el gate es el MISMO para las dos clases "+
			"de fila y el arranque no puede colar texto por la otra puerta", got)
	}
}

// ---------------------------------------------------------------------------
// (c) SIN DUPLICADO: el arranque escribe una vez, y el turno siguiente no lo repite
// ---------------------------------------------------------------------------

// TestHiloArranque_NoSeDuplicaConElTurnoNormal es la guarda contra el fallo MUDO.
//
// 🔴 POR QUÉ HACE FALTA UN TEST PARA ESTO, y no basta con leer el código: el `seq` de
// una entrada se calcula con `MAX(seq)+1` DENTRO de la propia sentencia, contra un
// `UNIQUE(event_id, seq)` y con 5 reintentos por colisión (events/store.go). Ese
// diseño protege de perder una fila, NO de escribir la misma dos veces: no hay
// unicidad por CUERPO. Un literal duplicado en el hilo no da error, no da log y no da
// warning — se cuela, y llega al `source_text` como una cantidad pedida dos veces.
//
// Lo que se ejerce son los DOS caminos seguidos, que es como ocurre de verdad:
//
//  1. «carrito»  → camino del DISPARADOR (handleTrigger → startFromDecision)
//  2. «ni idea»  → camino del TURNO NORMAL (advanceLiveStep), ya con evento vivo
//
// La afirmación es que el cliente aparece EXACTAMENTE DOS VECES —una por mensaje— y
// no tres. Por cada entrante corre un camino O el otro (HandleIncoming enruta a
// handleTrigger o a advanceLive, jamás a los dos), y este test es lo que impide que
// alguien rompa esa exclusividad sin enterarse.
//
// ⚠️ Se cuentan SOLO las filas del cliente. Las del negocio pueden repetir cuerpo con
// toda legitimidad —el menú vuelve a imprimir su pantalla cuando el texto no casa
// ninguna opción—, así que contar duplicados sobre todas las filas daría un rojo
// falso. Ver cuerposDelCliente.
//
// MUTACIÓN (compila): en incoming.go, en startFromDecision, añadir dentro del
// `if consumed {`, justo ANTES de la llamada a observeForAggregation, la línea
//
//	rt.persistTurnMessages(ctx, tenantID, sessionID, eventID, m.GetText(), nil)
//
// Es exactamente el error que un lector apurado cometería al «cablear también el
// hilo» sin darse cuenta de que startLocked ya lo hizo. El paquete compila, (a) sigue
// verde —la fila [0] está y es la correcta— y este test se pone rojo con TRES voces
// del cliente en vez de dos.
func TestHiloArranque_NoSeDuplicaConElTurnoNormal(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(testTenant, entitlements.FeatureLLMIntake)
	rt, evs, _ := newThreadRuntime(t, feats, eventStartRule(literalDelArranque, "cart"))
	ctx := context.Background()

	const segundoLiteral = "ni idea"

	if err := rt.HandleIncoming(ctx, testSession,
		incoming(testContact, literalDelArranque, "wamid.hilo-arranque-3a")); err != nil {
		t.Fatalf("HandleIncoming del arranque: %v", err)
	}
	alive := evs.alive()
	if len(alive) != 1 {
		t.Fatalf("montaje: el arranque debe parir UN evento vivo; hay %d", len(alive))
	}
	ev := alive[0]

	if err := rt.HandleIncoming(ctx, testSession,
		incoming(testContact, segundoLiteral, "wamid.hilo-arranque-3b")); err != nil {
		t.Fatalf("HandleIncoming del segundo mensaje: %v", err)
	}

	rows := evs.mensajesDe(ev.ID)
	voces := cuerposDelCliente(rows)
	quiero := []string{literalDelArranque, segundoLiteral}

	if len(voces) != len(quiero) {
		t.Fatalf("voces del cliente = %v (%d filas); esperado exactamente %v (2 filas), una por "+
			"mensaje. Tres filas significa que el arranque se escribió DOS veces, y un literal "+
			"repetido no da error: se cuela hasta el presupuesto. Hilo completo: %v",
			voces, len(voces), quiero, rolesYCuerpos(rows))
	}
	for i, q := range quiero {
		if voces[i] != q {
			t.Fatalf("voces[%d] = %q; esperado %q — el ORDEN de la conversación es parte del "+
				"contrato del hilo", i, voces[i], q)
		}
	}

	// Y el primer mensaje del hilo sigue siendo el del arranque: si alguien invirtiera
	// el orden dentro de startLocked, las voces de arriba seguirían siendo dos.
	if rows[0].role != events.RoleClient || rows[0].body != literalDelArranque {
		t.Fatalf("rows[0] = %+v; esperado {role:%q body:%q} — el hilo empieza por el literal "+
			"que abrió el evento", rows[0], events.RoleClient, literalDelArranque)
	}
}

// ---------------------------------------------------------------------------
// LA CONMUTA: escribe el literal del cliente y NO repite las salidas
// ---------------------------------------------------------------------------

// TestHiloArranque_LaConmutaEscribeElLiteralYNoRepiteLasSalidas cubre la cuarta
// combinación, que no estaba en el enunciado y sí en el código.
//
// `startFromDecision` no siempre PARE un evento: si ya había uno vivo de ese tipo y la
// conversación no lo tenía activo (no hay flow_state), beginEvent CONMUTA hacia él
// (switchToEvent) en vez de crear otro. Y ese camino observa igual para la ventana
// —`observeForAggregation` recibe el `ev.ID` del evento vivo—, así que la referencia
// del entrante entra en `source_refs` y ancla el `message_ts`.
//
// De ahí sale la invariante que este test fija, y que es la que gobierna las dos
// escrituras del 044:
//
//	TODO MENSAJE QUE ENTRA EN source_refs TIENE SU LITERAL EN EL HILO.
//
// Por eso la conmuta SÍ escribe el literal del cliente. Y por eso NO escribe las
// salidas: switchToEvent re-entra a un evento que ya existía y cuyo hilo ya tiene
// escrito su nodo inicial de cuando nació. Meterlo otra vez sería el duplicado MUDO
// del test anterior, solo que por la otra puerta.
//
// Lo que la conmuta sí deja además es su resumen de rescate, y va por la puerta de los
// MARCADOS (sendResumeSummary → persistOutOfTurnMessage, `message_out_of_turn`): otra
// clase de fila, imposible de confundir con el pedido del cliente (D-044.24).
//
// MUTACIÓN (compila): en events.go, en beginEvent, borrar entero el bloque
//
//	if opening.FromClient {
//		rt.persistTurnMessages(ctx, key.TenantID, sessionID, ev.ID, opening.Text, nil)
//	}
//
// El parámetro `opening` se sigue usando más abajo (viaja a birthEvent), así que el
// paquete compila. Este test se pone rojo en la primera aserción y (a) sigue verde:
// son dos ramas distintas de la misma función.
func TestHiloArranque_LaConmutaEscribeElLiteralYNoRepiteLasSalidas(t *testing.T) {
	feats := entitlements.NewFake()
	feats.Enable(testTenant, entitlements.FeatureLLMIntake)
	rt, evs, contacts := newThreadRuntime(t, feats, eventStartRule(literalDelArranque, "cart"))
	ctx := context.Background()

	// El doble necesita el contact_id OPACO ya resuelto para que la fila sembrada case
	// con la conversación (es lo que hace GetAliveByKind).
	evs.contactID = resolveID(t, contacts, testContact)
	// Un evento `cart` vivo, SIN flow_state que lo apunte: es la situación real tras
	// soltar la conversación (el reloj venció, o el flujo terminó y se liberó la fila).
	// El reloj del doble está en 2026-08-10 15:00 UTC; se siembra cinco minutos antes.
	ev := evs.seedAlive("cart", time.Date(2026, 8, 10, 14, 55, 0, 0, time.UTC))
	if n := len(evs.mensajesDe(ev.ID)); n != 0 {
		t.Fatalf("montaje: el evento sembrado debe empezar con el hilo vacío; hay %d filas", n)
	}

	if err := rt.HandleIncoming(ctx, testSession,
		incoming(testContact, literalDelArranque, "wamid.hilo-arranque-4")); err != nil {
		t.Fatalf("HandleIncoming de la conmuta: %v", err)
	}

	// No nació un segundo evento: esto es la CONMUTA, no un nacimiento.
	if n := len(evs.alive()); n != 1 {
		t.Fatalf("montaje: debe seguir habiendo UN solo evento vivo (se conmuta, no se pare otro); hay %d", n)
	}

	rows := evs.mensajesDe(ev.ID)
	if len(rows) != 1 {
		t.Fatalf("filas `message` del evento conmutado = %d (%v); esperado EXACTAMENTE 1 — "+
			"el literal del cliente entra (su referencia ya está en source_refs) y las salidas "+
			"de la re-entrada NO, porque el hilo ya tiene el nodo inicial de cuando el evento nació",
			len(rows), rolesYCuerpos(rows))
	}
	if rows[0].role != events.RoleClient || rows[0].body != literalDelArranque {
		t.Fatalf("rows[0] = %+v; esperado {role:%q body:%q}", rows[0], events.RoleClient, literalDelArranque)
	}
}

// ---------------------------------------------------------------------------
// ✅ LOS DOS ASERTOS DE thread_test.go, RECONCILIADOS EL 2026-08-22
// ---------------------------------------------------------------------------
//
// Aquí había un AVISO: este fichero cambiaba una conducta que thread_test.go daba por
// buena —«el turno de ARRANQUE no se persiste»— y dejaba dos tests de allí mirando
// `rows[0]` en busca del SEGUNDO mensaje. No se editaban desde aquí porque otro frente
// corría en paralelo sobre la misma rama. Ese frente terminó y los dos ya están
// corregidos, así que el aviso se convierte en constancia de qué se hizo:
//
//   - TestHilo_ConLaFeatureGuardaClienteYNegocioEnOrden — «ni idea» pasó de rows[0] a
//     rows[2], y el test afirma ahora las CUATRO filas (los dos turnos completos).
//   - TestHilo_ElTurnoQueCierraElFlujoPerteneceAlEvento — «1» pasó de rows[0] a
//     rows[2], con el mismo refuerzo.
//
// Se movió el ÍNDICE y no la afirmación: los roles, el orden y la pertenencia del turno
// de cierre al evento que moría siguen diciendo exactamente lo mismo. Y los dos ganaron
// un aserto NUEVO —que las filas del arranque ESTÁN—, precisamente para que el
// desplazamiento no pueda «arreglarse» borrando el turno de apertura y devolviendo el
// hilo al defecto que este fichero cerró.
