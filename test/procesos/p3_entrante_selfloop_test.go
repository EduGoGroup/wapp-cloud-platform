//go:build integracion

package procesos

import (
	"strings"
	"testing"
)

// P3 · La guarda anti-bucle con la base real (hallazgo 51 de F8, punto (a)). Un entrante cuyo
// remitente es un número PROPIO de la empresa —el que declaró OTRA sesión de su flota— no se
// contesta: dos sesiones del mismo negocio que se respondieran la una a la otra no pararían nunca.
//
// Lo que ningún otro proceso ve: la guarda pregunta a Postgres por ÍNDICE CIEGO (self_pn_bidx), y
// el índice que calcula el lector solo casa con el que guardó el escritor si los dos usan la MISMA
// clave. Con dos claves distintas no hay error, ni log, ni métrica: la guarda deja de bloquear y ya
// está. Por eso el número propio llega aquí a la base por la puerta real —el latido de la otra
// sesión— y no sembrado por SQL: un índice escrito por el test casaría con lo que el test quisiera.

const (
	// p3SelfLoopSpelled es p3SelfPn como lo escribiría una persona: el mismo número con «+»,
	// espacios y guion. La guarda normaliza el remitente antes de calcular el índice.
	p3SelfLoopSpelled = "+57 300 999-0000"
	// p3SelfLoopStrangerPn es el remitente del control: un número que no es de ninguna sesión.
	p3SelfLoopStrangerPn = "573001110095"

	// p3ReasonSelfLoop es el valor de la etiqueta reason de p3MetricBlocked para este corte.
	p3ReasonSelfLoop = "self_loop"

	// p3MsgSelfLoop es la línea WARN con la que el servidor dice que cortó un entrante por venir de
	// un número propio, y p3MsgSelfLoopSkipped la WARN de cuando no pudo preguntarlo y dejó pasar.
	p3MsgSelfLoop        = "runtime: entrante de un número propio del tenant; auto-respuesta evitada (anti-self-loop)"
	p3MsgSelfLoopSkipped = "runtime: no se pudo comprobar si el remitente es un número propio del tenant; guarda anti-self-loop omitida"
)

// p3SelfLoopRun es el estado que comparten los pasos: la sesión que atiende (activa, con el menú) y
// la OTRA sesión de la misma empresa, en otro Edge, que es la que declara el número propio.
type p3SelfLoopRun struct {
	// sc es la sesión que recibe los entrantes; sibling, la que declara p3SelfPn en su latido.
	sc, sibling p3Scene
}

// TestP3_SelfLoopGuard recorre la guarda anti-bucle contra el servidor real, con dos Edge de la
// misma empresa. Los pasos van en orden, cada uno sobre lo que dejó el anterior:
//
//   - La otra sesión declara su número con un latido y sigue PASIVA: su número NO bloquea (una
//     pasiva nunca auto-responde, así que no puede cerrar un bucle) y el entrante se contesta.
//   - La otra sesión pasa a ACTIVA: el mismo entrante, del mismo número, ya no se contesta y sube
//     wapp_flow_reactive_blocked_total{reason="self_loop"}. Otra grafía del número, igual.
//   - El control: el mismo texto desde un número que no es de nadie SÍ se contesta. Sin él, «no
//     llegó respuesta» no distinguiría una guarda que corta de un servidor que no contesta a nadie.
//
// Necesita Docker.
func TestP3_SelfLoopGuard(t *testing.T) {
	t.Parallel()
	sc := p3ActiveMenuScene(t, "p3_selfloop", "p3-autobucle")
	p := &p3SelfLoopRun{sc: sc, sibling: p3SelfLoopSibling(t, sc)}

	p.passiveNumberDoesNotBlock(t)
	p.sibling.setProfile(t, "active")
	p.ownNumberIsBlocked(t)
	p.strangerIsAnswered(t)
	p.closing(t)
}

// p3SelfLoopSibling enrola y conecta OTRO Edge en la empresa de sc y devuelve su escena: el mismo
// servidor, la misma empresa y la misma administradora, con su propia sesión (que nace pasiva y sin
// número propio). Vuelve cuando ese Edge puede operar y su sesión está en la flota.
func p3SelfLoopSibling(t *testing.T, sc p3Scene) p3Scene {
	t.Helper()
	e := enrolar(t, sc.S, edgeEmitirCodigo(t, sc.S, sc.TokenStaff, sc.Tenant))
	e.conectar(t)
	e.esperarLeases(t, 2, edgeTopeFila)
	if !e.puedeOperar() {
		t.Fatalf("el segundo Edge de la empresa, recién conectado, no puede operar")
	}
	e.esperarConfig(t, "filters", edgeTopeFila)
	edgeEsperarValor(t, sc.DB, "online", "la segunda sesión de la empresa en la flota", edgeEstadoSesion, sc.Tenant, e.EdgeID, e.SessionID)
	if e.EdgeID == sc.Edge.EdgeID || e.SessionID == sc.Edge.SessionID {
		t.Fatalf("el segundo Edge repite identidad con el primero: edge %q, sesión %q", e.EdgeID, e.SessionID)
	}
	return p3Scene{edgeEscenario: sc.edgeEscenario, Edge: e, Pub: sc.Pub}
}

// blocked lee el contador de entrantes cortados por venir de un número propio.
func (p *p3SelfLoopRun) blocked(t *testing.T) float64 {
	t.Helper()
	return p3Counter(t, p.sc.S, p3MetricBlocked, "reason", p3ReasonSelfLoop)
}

// passiveNumberDoesNotBlock es el antes. La otra sesión declara p3SelfPn en su latido: el número
// queda en fleet_sessions cifrado y con su índice ciego (lo escribe el servidor, no el test), y como
// la sesión es pasiva recibe el aviso de sesión pasiva. Con esa sesión todavía PASIVA, un entrante
// desde ese número a la sesión activa se contesta como el de cualquier contacto nuevo —bienvenida y
// menú— y el contador de bloqueos no se mueve.
func (p *p3SelfLoopRun) passiveNumberDoesNotBlock(t *testing.T) {
	t.Helper()
	sc, sib := p.sc, p.sibling
	sib.beatWithSelfPn(t, 5)
	sib.expectText(t, p3SelfPn, p3PassiveNotice(t))
	// Los trabajos de una sesión van en serie: con el lease del SEGUNDO latido, el del primero
	// terminó entero y la fila ya trae el número.
	sib.beatWithSelfPn(t, 7)
	edgeEsperarValor(t, sc.DB, "4/true|passive", "el número propio de la otra sesión, sellado, y su perfil",
		`SELECT num_nonnulls(self_pn_enc, self_pn_dek, self_pn_kek_id, self_pn_bidx)::text || '/' ||
				(length(self_pn_enc) > 0 AND length(self_pn_dek) > 0 AND self_pn_kek_id <> '' AND self_pn_bidx <> '')::text || '|' || profile
			FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`,
		sc.Tenant, sib.Edge.EdgeID, sib.Edge.SessionID)

	before := p.blocked(t)
	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: p3SelfPn + "@s.whatsapp.net", WaID: "P3-SELFLOOP-1", Text: p3Keyword, FromPn: p3SelfPn})
	sc.expectText(t, p3SelfPn, p3Welcome)
	sc.expectText(t, p3SelfPn, p3MenuPrompt)
	if got := p.blocked(t); got != before {
		t.Errorf("con la otra sesión PASIVA, el contador de self_loop pasó de %v a %v: su número no debía bloquear", before, got)
	}
}

// ownNumberIsBlocked es el caso: con la otra sesión ya ACTIVA, el entrante desde su número —el mismo
// texto que hace un momento se contestó— se corta, y también el que trae el número con otra grafía.
// Cada corte sube el contador en uno y deja su línea WARN con la sesión que lo recibió; el entrante sí
// pasa por el dedupe de ingesta (el corte va después), pero no toca ni contacts ni flow_state.
//
// «No hubo respuesta» se afirma dos veces: aquí, sin esperar, con el contador ya subido (el corte
// vuelve antes de llegar al motor), y en strangerIsAnswered, que exige que el SIGUIENTE texto del
// Edge sea ya el del control.
func (p *p3SelfLoopRun) ownNumberIsBlocked(t *testing.T) {
	t.Helper()
	sc := p.sc
	const contacts = `SELECT count(*) FROM public.contacts WHERE tenant_id = $1::uuid`
	const state = `SELECT coalesce(string_agg(current_node || '/' || coalesce(last_wa_message_id, ''), ',' ORDER BY updated_at), '')
		FROM public.flow_state WHERE tenant_id = $1::uuid AND session_id = $2`
	contactsBefore := consultaEntero(t, sc.DB, contacts, sc.Tenant)
	stateBefore := p9Scalar(t, sc.DB, state, sc.Tenant, sc.Edge.SessionID)
	before := p.blocked(t)

	for i, in := range []sealedIncoming{
		{From: p3SelfPn + "@s.whatsapp.net", WaID: "P3-SELFLOOP-2", Text: p3Keyword, FromPn: p3SelfPn},
		{From: p3SelfPn + "@s.whatsapp.net", WaID: "P3-SELFLOOP-3", Text: p3Keyword, FromPn: p3SelfLoopSpelled},
	} {
		sc.Edge.sendSealedIncoming(t, in)
		p3WaitCounter(t, sc.S, p3MetricBlocked, "reason", p3ReasonSelfLoop, before+float64(i+1))
		sc.expectNoPendingText(t, "entrante "+in.WaID+" desde un número propio de la empresa")
		if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.ingest_dedupe WHERE session_id = $1 AND wa_message_id = $2`, sc.Edge.SessionID, in.WaID); n != 1 {
			t.Errorf("ingest_dedupe tiene %d filas de %s, quería 1: el corte va después del dedupe", n, in.WaID)
		}
	}
	if n := len(p9LogLines(sc.S, p3MsgSelfLoop, map[string]string{"tenant_id": sc.Tenant, "session_id": sc.Edge.SessionID})); n != 2 {
		t.Errorf("el log trae %d líneas %q de la sesión que recibió los entrantes, quería 2", n, p3MsgSelfLoop)
	}
	if n := consultaEntero(t, sc.DB, contacts, sc.Tenant); n != contactsBefore {
		t.Errorf("los entrantes cortados movieron contacts de %d a %d filas", contactsBefore, n)
	}
	if got := p9Scalar(t, sc.DB, state, sc.Tenant, sc.Edge.SessionID); got != stateBefore {
		t.Errorf("los entrantes cortados movieron flow_state: de %q a %q", stateBefore, got)
	}
}

// strangerIsAnswered es el control: la misma palabra clave, por la misma sesión, desde un número
// que no es de ninguna sesión de la empresa, recibe la bienvenida y el menú, y el contador de
// bloqueos no se mueve. El texto que llega es el SUYO: si alguno de los entrantes cortados hubiera
// provocado una respuesta, estaría delante en la cola del Edge y expectText fallaría.
func (p *p3SelfLoopRun) strangerIsAnswered(t *testing.T) {
	t.Helper()
	sc := p.sc
	before := p.blocked(t)
	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: p3SelfLoopStrangerPn + "@s.whatsapp.net", WaID: "P3-SELFLOOP-4", Text: p3Keyword, FromPn: p3SelfLoopStrangerPn})
	sc.expectText(t, p3SelfLoopStrangerPn, p3Welcome)
	sc.expectText(t, p3SelfLoopStrangerPn, p3MenuPrompt)
	if got := p.blocked(t); got != before {
		t.Errorf("el entrante de un número ajeno movió el contador de self_loop de %v a %v", before, got)
	}
}

// closing es el cierre: ningún texto de más en ninguno de los dos Edge, la guarda nunca se saltó por
// no poder preguntar, el número propio no aparece en el log en ninguna de sus dos grafías, y no hay
// líneas ERROR ni errores del núcleo de los Edge.
func (p *p3SelfLoopRun) closing(t *testing.T) {
	t.Helper()
	p.sc.expectNoPendingText(t, "al cerrar, la sesión que atiende")
	p.sibling.expectNoPendingText(t, "al cerrar, la sesión que declaró el número")
	if n := len(p9LogLines(p.sc.S, p3MsgSelfLoopSkipped, nil)); n != 0 {
		t.Errorf("el log trae %d líneas %q: la guarda no pudo preguntar por el número", n, p3MsgSelfLoopSkipped)
	}
	log := p.sc.S.Log()
	for _, literal := range []string{p3SelfPn, p3SelfLoopSpelled} {
		if strings.Contains(log, literal) {
			t.Errorf("el número propio (%q) aparece en el log del servidor", literal)
		}
	}
	for _, e := range []*edge{p.sc.Edge, p.sibling.Edge} {
		if errs := e.Errores(); len(errs) != 0 {
			t.Errorf("errores del núcleo del Edge %s: %v", e.EdgeID, errs)
		}
	}
	edgeSinErrores(t, p.sc.S, nil)
}
