package gatewaygrpc

import (
	"io"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// readiness_edge_ready_internal_test.go — EL SEGUNDO CONSUMIDOR DEL FLANCO
// (Plan 044 · Ola 2 · T2.7, D-044.43).
//
// El criterio (f) de T2.7 tiene DOS mitades y viven en dos paquetes:
//
//  1. **El gateway avisa en el flanco `DOWN → READY`**, con la dirección
//     `(tenant, Edge)` y una sola vez por flanco. Es lo que fija este fichero, y lo
//     fija ENTRANDO POR `route` —la puerta real del frame— por la misma lección que
//     dejó T1.8-6: un método perfecto sin consumidor deja los tests en verde y el
//     campo sin reanudar.
//  2. **El worker reanuda sin esperar al backoff** en cuanto le avisan. Eso se fija en
//     `internal/intake/pipeline/plaza_test.go`, con un gateway falso que reproduce
//     exactamente el flanco de aquí.
//
// 🔴 SE CUENTAN LLAMADAS, NO LÍNEAS DE LOG: lo que hay que demostrar es que el aviso
// OCURRE o NO OCURRE, no que se anuncie.

// srvConAvisosDeEdgeContados arma un Server con OnEdgeReady instrumentado. El contador
// es un `int` pelado a propósito, igual que el de OnWarmup: el hook se invoca INLINE en
// la goroutine que llama a route, y si algún día alguien lo mudara al carril, `-race`
// lo gritaría en vez de dejarlo pasar.
func srvConAvisosDeEdgeContados(t *testing.T) (*Server, *int) {
	t.Helper()
	srv := New(session.NewRegistry(), logger.New(logger.WithWriter(io.Discard)))
	n := 0
	srv.OnEdgeReady = func(tenantID, edgeID string) {
		if tenantID != "t-1" || edgeID != "e-1" {
			t.Errorf("el aviso trae (%q,%q); la plaza que se reanuda es POR EDGE", tenantID, edgeID)
		}
		n++
	}
	return srv, &n
}

// TestEdgeReady_SoloElFlancoAvisaAlPipeline es la mitad de (f) que vive en el gateway.
//
// La secuencia es la misma que la del calentamiento porque el hecho observado es el
// MISMO: `inference_readiness` viaja en TODOS los latidos (es estado, no evento), así
// que avisar «cuando llega READY» sería avisar en cada cadencia del Edge —decenas de
// veces por hora—, y cada aviso arrastra una pasada de claims sobre `intake_jobs`
// ignorando el backoff. Lo que se dispara es el FLANCO.
//
// 🔬 MUTACIÓN EJECUTADA (roja): quitar `anterior != READY` del return de
// anotaReadiness ⇒ rojo en el paso 3, la cadencia.
func TestEdgeReady_SoloElFlancoAvisaAlPipeline(t *testing.T) {
	t.Parallel()
	srv, n := srvConAvisosDeEdgeContados(t)
	lane := carrilDePrueba(t)
	defer cerrarCarril(lane, func() {})

	cc := ccDePrueba("s-1")
	pasos := []struct {
		nombre   string
		readi    cloudlinkv1.InferenceReadiness
		acumulan int
		porque   string
	}{
		{"1. el Edge arranca sin cajero", cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_DOWN, 0,
			"un Edge que DICE que no puede no desatasca nada: sus jobs seguirían muriendo por timeout"},
		{"2. el cajero levanta", cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY, 1,
			"DOWN→READY es EL evento: el motivo por el que esos jobs estaban castigados acaba de desaparecer"},
		{"3. cadencia", cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY, 1,
			"READY→READY no es un cambio; reanudar aquí sería un barrido de la cola por cada latido"},
		{"4. el cajero se cae", cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_DOWN, 1,
			"caerse no reanuda nada"},
		{"5. y vuelve", cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY, 2,
			"READY→DOWN→READY son DOS flancos, y el segundo hay que volver a atenderlo"},
	}
	for _, p := range pasos {
		srv.route(lane, cc, latidoConReadiness(cc.sessionID, p.readi))
		if *n != p.acumulan {
			t.Fatalf("%s: avisos acumulados = %d, quiero %d — %s", p.nombre, *n, p.acumulan, p.porque)
		}
	}
}

// TestEdgeReady_ElCeroNoEsDOWN_NoAvisa cubre desde este hook la misma regla que ya
// custodia el del calentamiento: INFERENCE_READINESS_UNSPECIFIED significa «este Edge
// no lo dice», jamás «no puede».
//
// Aquí la consecuencia de equivocarse es distinta y peor: leer el cero como DOWN
// convertiría el siguiente READY de CUALQUIER Edge viejo en un flanco, y cada flanco
// dispara una pasada de claims que se salta el backoff. Un latido mudo pasaría a ser
// un barrido de la cola.
//
// 🔬 MUTACIÓN EJECUTADA (roja): en anotaReadiness, tratar el cero como DOWN ⇒ el
// contador sube a 2.
func TestEdgeReady_ElCeroNoEsDOWN_NoAvisa(t *testing.T) {
	t.Parallel()
	srv, n := srvConAvisosDeEdgeContados(t)
	lane := carrilDePrueba(t)
	defer cerrarCarril(lane, func() {})

	cc := ccDePrueba("s-1")
	srv.route(lane, cc, latidoConReadiness(cc.sessionID, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY))
	srv.route(lane, cc, latidoConReadiness(cc.sessionID, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_UNSPECIFIED))
	srv.route(lane, cc, latidoConReadiness(cc.sessionID, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY))
	if *n != 1 {
		t.Fatalf("avisos = %d, quiero 1: el silencio de en medio no puede fabricar un flanco", *n)
	}
}

// TestEdgeReady_SinHookElGatewaySeComportaIgual fija que los dos consumidores del
// flanco son INDEPENDIENTES: un despliegue donde el worker del pipeline todavía no
// está cableado —o sea, HOY— deja `OnEdgeReady` en nil, y eso no puede afectar al
// calentamiento, que sí tiene consumidor desde T1.8-6.
//
// 🔬 MUTACIÓN EJECUTADA (roja): colgar el aviso de calentamiento del `OnEdgeReady !=
// nil` (o compartir un solo `return` para los dos hooks) ⇒ cero calentamientos.
func TestEdgeReady_SinHookElGatewaySeComportaIgual(t *testing.T) {
	t.Parallel()
	srv, calentamientos := srvConCalentamientosContados(t)
	srv.OnEdgeReady = nil
	lane := carrilDePrueba(t)
	defer cerrarCarril(lane, func() {})

	cc := ccDePrueba("s-1")
	srv.route(lane, cc, latidoConReadiness(cc.sessionID, cloudlinkv1.InferenceReadiness_INFERENCE_READINESS_READY))
	if *calentamientos != 1 {
		t.Fatalf("calentamientos = %d, quiero 1: sin consumidor del pipeline, el gateway hace lo de siempre",
			*calentamientos)
	}
}

// TestPlazaDe_EsElMismoEdgeQueAtiendeLaInferencia fija la dirección de la plaza contra
// el enrutado REAL, y esa pareja es todo el valor del test: si `PlazaDe` y
// `inferenceSession` dejaran de coincidir, el aforo del pipeline protegería un Edge y
// la petición saldría por otro — la avería clásica de los caminos gemelos, silenciosa
// por definición.
//
// 🔬 MUTACIÓN EJECUTADA (roja): que PlazaDe resuelva por su cuenta (devolver el primer
// Edge del tenant sin pasar por inferenceSession) ⇒ con dos Edges vivos, la sesión de
// origen deja de mandar y el Edge devuelto es el otro.
func TestPlazaDe_EsElMismoEdgeQueAtiendeLaInferencia(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	srv := New(reg, logger.New(logger.WithWriter(io.Discard)))

	// Dos Edges del mismo tenant, cada uno con su sesión. El alfabéticamente primero
	// es el de "s-a", así que si la sesión de origen NO mandara, saldría siempre ése.
	srv.trackSession(connCtx{sessionID: "s-a", tenantID: "t-1", edgeID: "e-alfa", hasIdentity: true})
	srv.trackSession(connCtx{sessionID: "s-z", tenantID: "t-1", edgeID: "e-omega", hasIdentity: true})

	// Sin ninguna sesión VIVA en el registry, el enrutado cae a la primera alfabética
	// de las conocidas del tenant.
	edge, ok := srv.PlazaDe("t-1", "s-z")
	if !ok {
		t.Fatal("PlazaDe dijo que no hay Edge con dos sesiones registradas")
	}
	if edge != "e-alfa" {
		t.Fatalf("plaza = %q; sin sesión viva el enrutado cae a la primera alfabética (s-a ⇒ e-alfa)", edge)
	}

	// Y un tenant sin nada registrado no ocupa plaza: no hay máquina que proteger.
	if _, ok := srv.PlazaDe("t-sin-nada", ""); ok {
		t.Fatal("un tenant sin Edges vivos no puede ocupar plaza")
	}
}
