package gatewaygrpc

// inference_afinidad_internal_test.go — QUIÉN ATIENDE LA INFERENCIA CUANDO NO HAY
// ORIGEN (Plan 057 · Ola 3 · T3.2, T3.3, T3.4 y T3.5).
//
// 🔴 EL DEFECTO QUE ESTOS TESTS CONGELAN (D4). El desempate de `inferenceSession` era
// «la primera alfabética de las sesiones vivas del tenant», sin mirar una sola vez el
// `inference_readiness` que ese Edge acababa de declarar. Un Edge que dijo DOWN —sin
// Ollama, o con el cajero caído— se llevaba el prompt por ganar el orden, mientras el
// de al lado, con GPU y READY, miraba. El síntoma no era un error de enrutado: era un
// `ollama_down` devuelto por el Edge equivocado.
//
// 🔴 Y LA TRAMPA DE LA CORRECCIÓN, que es lo que hace que T3.2 y T3.3 vayan EN PAREJA:
// el arreglo obvio —«filtra por READY»— apaga en silencio a toda la flota que no
// reporta el campo (contrato anterior a v0.17.0, o un Edge recién arrancado).
// `INFERENCE_READINESS_UNSPECIFIED` significa «este Edge no lo dice», NUNCA «no
// puede»; está razonado en readiness.go. Por eso el filtro es en NEGATIVO —se descarta
// DOWN— y READY es solo una preferencia. T3.3 es el que impide que T3.2 se cumpla por
// demolición.

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// declara anota lo que un Edge dice de su Ollama por el MISMO camino que el latido
// real (anotaReadiness), y no escribiendo el mapa a mano: si mañana la escritura
// cambiara de regla —hoy el cero no se anota—, estos tests seguirían midiendo el
// camino verdadero en vez de un estado fabricado que quizá ya no es alcanzable.
func declara(srv *Server, tenantID, edgeID string, r cloudlinkv1.InferenceReadiness) {
	srv.anotaReadiness(connCtx{tenantID: tenantID, edgeID: edgeID}, r)
}

// TestInferenceSession_ElEdgeQueDijoDOWN_NoRecibeElPrompt (T3.2).
//
// El montaje es deliberadamente adverso: el Edge que dijo DOWN sostiene la sesión que
// GANA el orden alfabético. Al revés, el test pasaría con el código viejo por
// coincidencia y no probaría nada.
//
// 🔬 MUTACIÓN: volver a `vivas := s.sessionsForTenant(tenantID); slices.Sort(vivas);
// return vivas[0], true`.
func TestInferenceSession_ElEdgeQueDijoDOWN_NoRecibeElPrompt(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	srv := New(reg, logger.New(logger.WithWriter(io.Discard)))

	// s-aaa gana alfabéticamente y está en el Edge que NO puede servir.
	srv.trackSession(connCtx{tenantID: "t", edgeID: "e-down", sessionID: "s-aaa"})
	srv.trackSession(connCtx{tenantID: "t", edgeID: "e-ready", sessionID: "s-zzz"})
	declara(srv, "t", "e-down", cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_DOWN)
	declara(srv, "t", "e-ready", cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY)

	// Diez veces, por lo mismo que el test de determinismo: el recorrido de un map de
	// Go está aleatorizado y una sola llamada podría acertar por casualidad.
	for range 10 {
		got, ok := srv.inferenceSession("t", "")
		if !ok || got != "s-zzz" {
			t.Fatalf("sin origen quiero s-zzz (el Edge READY), salió %q (ok=%v)", got, ok)
		}
	}

	// 🔴 Y EL CONTRAPESO, que es doctrina y no un detalle (REQ-057.9): el ORIGEN VIVO
	// manda SIN CONDICIONES, aunque su Edge haya dicho DOWN. La inferencia jamás cruza
	// entre nodos: la conversación la atiende quien la sostiene, y si su Ollama no
	// puede, lo que vuelve es un ollama_down honesto del Edge correcto — no una
	// respuesta fabricada por la máquina de otra instalación.
	defer reg.Register("s-aaa", senderFunc(func(*cloudlinkv1.CloudToEdge) error { return nil }))()
	if got, ok := srv.inferenceSession("t", "s-aaa"); !ok || got != "s-aaa" {
		t.Fatalf("el origen vivo manda aunque su Edge esté DOWN: quiero s-aaa, salió %q (ok=%v)", got, ok)
	}
}

// TestInferenceSession_ElQueNoLoDice_SigueSiendoElegible (T3.3).
//
// 🔴 ESTE TEST ES LA REFUTACIÓN EN CÓDIGO del §5.4 del análisis que originó el plan,
// que pedía filtrar «obligatoriamente por READY». Exigirlo dejaría sin inferencia a
// todo Edge que no publique el campo, y sin un solo error: el gateway diría «no hay
// nadie» de un tenant cuya única instalación está perfectamente viva.
//
// 🔬 MUTACIÓN: exigir READY en el filtro (devolver solo el grupo `listas`) ⇒ el primer
// bloque se pone rojo con `ok=false`.
func TestInferenceSession_ElQueNoLoDice_SigueSiendoElegible(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), logger.New(logger.WithWriter(io.Discard)))

	// Un tenant cuyo único Edge nunca reportó nada: es la flota vieja.
	srv.trackSession(connCtx{tenantID: "t-muda", edgeID: "e", sessionID: "s-1"})
	if got, ok := srv.inferenceSession("t-muda", ""); !ok || got != "s-1" {
		t.Fatalf("el Edge que no lo dice sigue siendo elegible: quiero s-1, salió %q (ok=%v)", got, ok)
	}

	// Y READY es PREFERENCIA, no requisito: cuando conviven los dos grupos, el que lo
	// dijo va primero aunque pierda el orden alfabético.
	srv.trackSession(connCtx{tenantID: "t-mixto", edgeID: "e-muda", sessionID: "s-aaa"})
	srv.trackSession(connCtx{tenantID: "t-mixto", edgeID: "e-ready", sessionID: "s-zzz"})
	declara(srv, "t-mixto", "e-ready", cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY)
	for range 10 {
		got, ok := srv.inferenceSession("t-mixto", "")
		if !ok || got != "s-zzz" {
			t.Fatalf("con un READY y un mudo quiero el READY (s-zzz), salió %q (ok=%v)", got, ok)
		}
	}
}

// TestInfer_SinNadieElegible_FallaHonestoYNoEnvia (T3.4, aserto 1).
//
// El único Edge del tenant dijo DOWN. Antes se le mandaba el prompt igual y el dueño
// recibía un ollama_down tras esperar el presupuesto entero; ahora la petición muere
// en el gateway, deprisa y diciendo la verdad.
//
// El contador de envíos es la mitad que importa: sin él, un `Infer` que devolviera el
// error DESPUÉS de haber escrito en el stream pasaría el test.
func TestInfer_SinNadieElegible_FallaHonestoYNoEnvia(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	srv := New(reg, logger.New(logger.WithWriter(io.Discard)), WithCloudEncPrivKey(testCloudPriv()))

	var envios atomic.Int64
	defer reg.Register("s-1", senderFunc(func(*cloudlinkv1.CloudToEdge) error {
		envios.Add(1)
		return nil
	}))()
	srv.trackSession(connCtx{tenantID: "t", edgeID: "e-down", sessionID: "s-1"})
	declara(srv, "t", "e-down", cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_DOWN)

	_, err := srv.Infer(context.Background(), "t", InferRequest{
		Prompt: "clasifica esto", Format: "json", Timeout: 2 * time.Second,
	})
	if err == nil {
		t.Fatal("con el único Edge en DOWN, Infer no puede devolver una salida")
	}
	var ie *InferError
	if !errors.As(err, &ie) || ie.Motivo() != MotivoEdgeOffline {
		t.Fatalf("quiero un InferError con motivo %q, llegó %v", MotivoEdgeOffline, err)
	}
	if !errors.Is(err, session.ErrSessionOffline) {
		t.Fatalf("el error debe envolver ErrSessionOffline, llegó %v", err)
	}
	if n := envios.Load(); n != 0 {
		t.Fatalf("no se envía nada cuando no hay nadie elegible: hubo %d envíos", n)
	}
}

// TestPlazaDe_HeredaLaAfinidadDeReadiness (T3.5).
//
// `PlazaDe` no cambia una línea en esta ola: el test existe para que no PUEDA cambiar
// sin avisar. El día que alguien reimplemente el criterio aquí «para optimizar», el
// aforo del ADR-0046 protegería un Edge y la petición saldría por otro — la avería de
// los caminos gemelos, silenciosa por definición.
//
// 🔬 MUTACIÓN: que PlazaDe resuelva por su cuenta (primer Edge del tenant, sin pasar
// por inferenceSession) ⇒ devuelve e-alfa, el que dijo DOWN.
func TestPlazaDe_HeredaLaAfinidadDeReadiness(t *testing.T) {
	t.Parallel()
	srv := New(session.NewRegistry(), logger.New(logger.WithWriter(io.Discard)))

	srv.trackSession(connCtx{tenantID: "t-1", edgeID: "e-alfa", sessionID: "s-a", hasIdentity: true})
	srv.trackSession(connCtx{tenantID: "t-1", edgeID: "e-omega", sessionID: "s-z", hasIdentity: true})
	declara(srv, "t-1", "e-alfa", cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_DOWN)
	declara(srv, "t-1", "e-omega", cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY)

	edge, ok := srv.PlazaDe("t-1", "")
	if !ok {
		t.Fatal("PlazaDe dijo que no hay Edge, y hay uno READY")
	}
	if edge != "e-omega" {
		t.Fatalf("plaza = %q; la plaza es del Edge que de verdad atenderá (e-omega, READY)", edge)
	}

	// Y si NADIE puede servir, no hay plaza que ocupar: reservarla sería proteger una
	// máquina a la que no va a llegar ningún trabajo.
	declara(srv, "t-1", "e-omega", cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_DOWN)
	if _, ok := srv.PlazaDe("t-1", ""); ok {
		t.Fatal("con toda la flota del tenant en DOWN no puede haber plaza")
	}
}
