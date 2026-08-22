package runtime_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// PIEZA 3 de T5.2 (Plan 046 · Ola 5, REQ-21): los triggers ACOTADOS A UNA SESIÓN
// (Plan 020 · T4), con el perfil de sesión delante.
//
// ── QUÉ FALTABA ───────────────────────────────────────────────────────────────────
// Que una regla con SessionID no se filtre a otra sesión ya lo prueba
// TestConfigResolver_SessionSpecificDoesNotLeak — pero AL NIVEL DEL RESOLVER, con una
// llamada directa a Resolve y sin runtime, sin perfil y sin motor. Lo que nunca se
// probó es el cruce: las dos razones por las que un disparo puede NO ocurrir viven en
// capas distintas y producen el MISMO síntoma observable (cero envíos, cero estado).
//
//	· la regla no es de esta sesión  → lo decide el ConfigResolver, en trigger/
//	· la sesión es pasiva            → lo decide reactiveBlocked, en runtime/
//
// 🔴 POR QUÉ ESO HACE FALSIFICABLE UN TEST MAL ESCRITO. Un caso que solo afirmara «la
// sesión B no disparó» pasaría igual si el motor entero estuviera apagado, si la
// keyword estuviera mal escrita o si el flujo no existiera. La única forma de que el
// «no» signifique algo es tener en el MISMO test un «sí» que lo contraste: por eso
// van las tres vueltas juntas y la tercera es obligatoria.

// resolverPorSesion resuelve el perfil SEGÚN LA SESIÓN, que es lo que fakeResolver no
// sabe hacer: aquel devuelve un perfil fijo para cualquier session_id, y esta pieza
// necesita justamente dos sesiones del mismo tenant con perfiles OPUESTOS.
type resolverPorSesion struct {
	tenantID string
	perfiles map[string]string
}

func (r resolverPorSesion) ResolveTenant(_ context.Context, sessionID string) (string, string, error) {
	return r.tenantID, r.perfiles[sessionID], nil
}

// reglaDeSesion arma la keyword→testFlow ACOTADA a una sesión concreta. El SessionID
// no vacío es lo único que la distingue de keywordRule(); esa diferencia es la tarea.
func reglaDeSesion(sessionID string) trigger.Rule {
	return trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindKeyword,
		Keyword: "pedido", MatchType: trigger.MatchExact, FlowID: testFlow,
		Enabled: true, SessionID: sessionID,
	}
}

// TestHandleIncoming_TriggerDeSesion_NoSeFiltraNiDisparaEnPasiva recorre las tres
// vueltas sobre UNA regla acotada a `sess-dueña`:
//
//	(1) sess-ajena  ACTIVA  ⇒ NO dispara — la regla no es suya
//	(2) sess-dueña  PASIVA  ⇒ NO dispara — la guarda de perfil corta antes
//	(3) sess-dueña  ACTIVA  ⇒ SÍ dispara — y sin esta vuelta las otras dos no valen
//
// Cada vuelta usa su propio repositorio: el estado que crea la vuelta (3) haría que
// las anteriores dejaran de ser «no hay estado» si compartieran almacén.
//
// 💥 MUTACIÓN QUE ENROJECE LA VUELTA (1): quitar el filtro por sesión del
// ConfigResolver (config_resolver.go) o sembrar la regla como global (SessionID: "")
// ⇒ la sesión ajena empieza a arrancar el flujo del vecino.
//
// 💥 MUTACIÓN QUE ENROJECE LA VUELTA (2): hacer que reactiveBlocked devuelva false
// para el perfil pasivo ⇒ la sesión pasiva arranca el flujo y auto-responde.
func TestHandleIncoming_TriggerDeSesion_NoSeFiltraNiDisparaEnPasiva(t *testing.T) {
	const dueña, ajena = "sess-dueña", "sess-ajena"

	casos := []struct {
		nombre   string
		sesion   string
		perfil   string
		dispara  bool
		porque   string
		mensajes int
	}{
		{"la sesión AJENA no hereda la regla de la dueña", ajena, "active", false,
			"la regla lleva SessionID=sess-dueña: acotarla a una sesión es exactamente " +
				"impedir que las demás del tenant la disparen (Plan 020 · T4)", 0},
		{"la sesión DUEÑA en pasiva no dispara su propia regla", dueña, "passive", false,
			"el perfil pasivo corta en reactiveBlocked, ANTES de que el resolver llegue a " +
				"mirar la regla: la sesión es la dueña y aun así no actúa (D-046.7)", 0},
		{"la sesión DUEÑA en activa SÍ dispara", dueña, "active", true,
			"sin esta vuelta los dos «no» de arriba pasarían con el motor apagado", 1},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			ctx := context.Background()
			repo := store.NewMemoryRepository()
			if _, err := repo.InsertDefinition(ctx, testTenant, sampleFlow()); err != nil {
				t.Fatalf("sembrar definición: %v", err)
			}
			ts := trigger.NewMemoryStore()
			if _, err := ts.Insert(ctx, reglaDeSesion(dueña)); err != nil {
				t.Fatalf("insert regla de sesión: %v", err)
			}
			sender := &fakeSender{}
			contacts := contact.NewMemoryResolver(repo)
			rt := runtime.New(repo, newEngine(), sender,
				resolverPorSesion{tenantID: testTenant, perfiles: map[string]string{c.sesion: c.perfil}},
				contacts, discardLogger(),
				runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)))

			if err := rt.HandleIncoming(ctx, c.sesion, incoming(testContact, "pedido", "wamid."+c.sesion+c.perfil)); err != nil {
				t.Fatalf("HandleIncoming: %v", err)
			}

			if sender.count() != c.mensajes {
				t.Fatalf("envíos = %d, quiero %d — %s", sender.count(), c.mensajes, c.porque)
			}
			cid := resolveID(t, contacts, testContact)
			_, vivo, err := repo.Load(ctx, store.Key{TenantID: testTenant, SessionID: c.sesion, ContactID: cid})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if vivo != c.dispara {
				t.Fatalf("estado vivo = %v, quiero %v — %s.\n"+
					"Contar envíos no basta: un disparo que arrancara el flujo y fallara al enviar "+
					"dejaría estado sin mensajes, y un envío suelto no prueba que la conversación naciera",
					vivo, c.dispara, c.porque)
			}
		})
	}
}
