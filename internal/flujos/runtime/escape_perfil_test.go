package runtime_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// PIEZA 1 de T5.2 (Plan 046 · Ola 5, REQ-21): el escape global, en sus DOS
// direcciones y con el perfil de sesión delante.
//
// ── QUÉ DEUDA SALDA, Y POR QUÉ NO ESTABA SALDADA ──────────────────────────────────
// El escape del Plan 019 · T4 se probó cuando el perfil de sesión no existía: sus
// tests (escape_effect_test.go, orphan_menu_escape_test.go, event_escaped_reason_
// test.go) montan el runtime con `fakeResolver{tenantID: testTenant}` y perfil VACÍO,
// que la guarda interpreta como ACTIVA. Ninguno pregunta qué pasa en una pasiva.
//
// Y la respuesta no es obvia leyendo: el escape NO tiene guarda propia. Vive dentro
// de advanceLive (incoming.go), al que solo se llega si reactiveBlocked dejó pasar el
// entrante. O sea, que una pasiva no escape es una consecuencia del ORDEN de dos
// funciones que nadie ata — mover el escape unas líneas arriba, a la entrada de
// HandleIncoming, parecería una simplificación inocente y rompería esto sin que
// ningún test se quejara.
//
// 🔴 POR QUÉ IMPORTA QUE UNA PASIVA NO ESCAPE. El escape hace TRES cosas: borra el
// flow_state, emite event_escaped y AVISA al cliente. Las tres son actividad del
// motor sobre una sesión que el dueño configuró para no actuar (D-046.7). La tercera
// es literalmente una auto-respuesta —handleEscape pasa por replyAllowed y por send—,
// así que una pasiva que escapara estaría escribiendo por WhatsApp, que es justo lo
// único que el perfil pasivo promete que no ocurre.
//
// Las dos direcciones van en el MISMO fichero a propósito: un test que solo afirmara
// «la pasiva no escapa» pasaría también si el escape estuviera roto para todos.

// escapeRule arma la regla kind=escape que usan los dos casos. Sin Message: el aviso
// entonces es el texto por defecto del runtime, y a estos tests les basta CONTAR
// envíos — lo que se mide es si sale algo, no qué dice.
func escapeRule() trigger.Rule {
	return trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindEscape,
		Keyword: "salir", MatchType: trigger.MatchExact, Enabled: true,
	}
}

// TestHandleIncoming_EscapeEnSesionActiva_CortaYAvisa es la dirección de
// NO-REGRESIÓN: con la sesión activa, el escape del 019 sigue haciendo lo suyo sobre
// una conversación viva — borra el estado y manda el aviso.
//
// 💥 MUTACIÓN QUE LO PONE ROJO: deshabilitar la regla (Enabled: false) o mover la
// llamada a IsEscape fuera de advanceLive ⇒ el estado sobrevive.
func TestHandleIncoming_EscapeEnSesionActiva_CortaYAvisa(t *testing.T) {
	ctx := context.Background()
	rt, repo, sender, contacts := newProfileTriggerRuntime(t, "active", escapeRule())
	cid := resolveID(t, contacts, testContact)
	key := store.Key{TenantID: testTenant, SessionID: testSession, ContactID: cid}

	// Conversación viva. Start NO consulta el perfil (es la vía por API, no el motor
	// reactivo), así que sirve igual para los dos casos de este fichero.
	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	trasStart := sender.count()

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "salir", "wamid.esc.act")); err != nil {
		t.Fatalf("HandleIncoming con la palabra de escape (activa): %v", err)
	}

	if _, vivo, err := repo.Load(ctx, key); err != nil || vivo {
		t.Fatalf("la conversación SIGUE viva tras el escape en una sesión activa (vivo=%v err=%v): "+
			"el escape del Plan 019 · T4 dejó de cortar", vivo, err)
	}
	if sender.count() != trasStart+1 {
		t.Fatalf("el aviso de escape no salió: envíos %d → %d, quiero uno más. "+
			"El escape corta Y avisa; sin aviso el cliente no sabe que su conversación murió",
			trasStart, sender.count())
	}
}

// TestHandleIncoming_EscapeEnSesionPasiva_NoHaceNada es la dirección NUEVA, la que
// REQ-21 pedía y nadie había escrito: la MISMA regla, la MISMA palabra y la MISMA
// conversación viva, con la sesión en pasiva ⇒ no pasa NADA.
//
// Se afirman las dos consecuencias por separado porque fallan por vías distintas:
// que NO se envíe (la pasiva no habla) y que el estado SOBREVIVA (la pasiva no
// destruye). Un escape que borrara el estado sin avisar sería silencioso: la
// conversación del cliente desaparecería y nadie vería un mensaje de más.
//
// 🔴 EL ESTADO NO SE BORRA, Y ES DELIBERADO. Es el mismo criterio conservador que
// TestHandleIncoming_PassiveNoAvanzaConversacionViva ya fija para el avance normal:
// una conversación viva se CONGELA mientras la sesión sea pasiva y retoma si alguien
// la reactiva. Un escape que la borrara convertiría un cambio de configuración en
// pérdida de estado del cliente.
//
// 💥 MUTACIÓN QUE LO PONE ROJO: subir la llamada a IsEscape de advanceLive a la
// entrada de HandleIncoming, por delante de reactiveBlocked. Compila, parece una
// simplificación —«el escape es global, ¿por qué depende de tener conversación
// viva?»— y hace que una sesión pasiva empiece a mandar mensajes por WhatsApp.
func TestHandleIncoming_EscapeEnSesionPasiva_NoHaceNada(t *testing.T) {
	ctx := context.Background()
	rt, repo, sender, contacts := newProfileTriggerRuntime(t, "passive", escapeRule())
	cid := resolveID(t, contacts, testContact)
	key := store.Key{TenantID: testTenant, SessionID: testSession, ContactID: cid}

	if _, err := rt.Start(ctx, testTenant, testFlow, testSession, phoneRef(t, testContact)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	trasStart := sender.count()
	antes := loadState(t, repo, cid)

	if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "salir", "wamid.esc.pas")); err != nil {
		t.Fatalf("HandleIncoming con la palabra de escape (pasiva): %v", err)
	}

	if sender.count() != trasStart {
		t.Fatalf("una sesión PASIVA envió el aviso de escape (%d → %d envíos). El aviso es una "+
			"auto-respuesta —pasa por replyAllowed y por send— y una pasiva no auto-responde "+
			"(D-046.7): acaba de escribir por WhatsApp en nombre del dueño",
			trasStart, sender.count())
	}
	despues := loadState(t, repo, cid)
	if despues.CurrentNode != antes.CurrentNode {
		t.Fatalf("la conversación cambió de nodo en una sesión pasiva (%q → %q)",
			antes.CurrentNode, despues.CurrentNode)
	}
	if _, vivo, err := repo.Load(ctx, key); err != nil || !vivo {
		t.Fatalf("una sesión PASIVA BORRÓ la conversación por escape (vivo=%v err=%v). "+
			"El criterio es conservador: la conversación se congela mientras la sesión sea "+
			"pasiva y retoma si alguien la reactiva; borrarla convierte un cambio de "+
			"configuración en pérdida de estado del cliente", vivo, err)
	}
}
