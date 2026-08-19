package gatewaygrpc

import (
	"context"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// DEUDA-050.1 — la carrera de la RECONEXIÓN RÁPIDA (Plan 050 · Ola 3).
//
// Los dos tests de abajo son PAREJA y hay que leerlos juntos: uno dice cuándo el
// MarkOffline diferido NO debe escribir, el otro dice que en el caso normal SIGUE
// escribiendo. Por separado cualquiera de los dos aprueba un arreglo equivocado —el
// primero lo aprobaría un "arreglo" que se limitara a borrar el MarkOffline, y el
// segundo no notaría nunca la carrera—.
//
// 🔴 Antes de esta ola NINGÚN test del repo abría un segundo stream tras cerrar el
// primero: TestConnectReconexionIdempotente (server_test.go) corre SIN fleet y no
// reconecta —manda tres latidos por el MISMO stream—, y TestMTLSFleetOnlineThenOffline
// (mtls_test.go) cierra el único stream que abre. La carrera existía sin red.
//
// El instrumento es el mismo del barrido del bucle Recv (carrilTapado): con el worker
// de la sesión ocupado, el jobOffline se queda EN LA COLA y la reconexión ocurre —de
// forma determinista, sin relojes ni esperas— en la ventana exacta que la deuda
// describe. Con el carril libre la carrera sería un flake, no una prueba.

// TestReconexionRapidaNoDejaLaSesionOffline reproduce la carrera: cae el stream A y su
// MarkOffline queda encolado; el Edge reconecta y el stream B marca online YA (inline);
// el job de A aterriza DESPUÉS. La fila debe quedar ONLINE, porque la sesión está viva.
//
// Sin la mitigación, este test falla con la fila en offline: es la flota mintiendo
// sobre una sesión sana, que es exactamente el daño de DEUDA-050.1.
func TestReconexionRapidaNoDejaLaSesionOffline(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	reg := session.NewRegistry()
	srv := New(reg, laneLog(), WithFleet(repo))
	cc := ccDePrueba("s-1")

	// Stream A: sesión registrada y online.
	releaseA := reg.Register(cc.sessionID, senderFunc(func(*cloudlinkv1.CloudToEdge) error { return nil }))
	srv.onSessionRegistered(context.Background(), cc)
	exigirEstadoFleet(t, repo, cc, fleet.StateOnline, "tras el registro del stream A")

	lane, soltar := carrilTapado(t, cc.sessionID)
	defer cerrarCarril(lane, soltar)

	// Cae el stream A, en el MISMO orden que closeStream: release primero (el registry
	// es última-gana y el release compara identidad), MarkOffline encolado después.
	releaseA()
	srv.onStreamClosed(lane, cc)

	// El Edge RECONECTA dentro de la ventana: stream B registra el mismo session_id y
	// escribe online inline, mientras el jobOffline de A sigue atrapado en la cola.
	reg.Register(cc.sessionID, senderFunc(func(*cloudlinkv1.CloudToEdge) error { return nil }))
	srv.onSessionRegistered(context.Background(), cc)

	// Ahora sí: el jobOffline de A corre.
	cerrarCarril(lane, soltar)

	exigirEstadoFleet(t, repo, cc, fleet.StateOnline,
		"tras aterrizar el MarkOffline del stream A ya reconectado (DEUDA-050.1): "+
			"una escritura vieja NO puede pisar la reconexión")
}

// TestCaidaSinReconexionSigueMarcandoOffline es la otra mitad, y la que impide que la
// mitigación se convierta en «dejar de marcar offline». Mismo montaje, misma cola
// tapada, PERO sin stream B: al aterrizar el job la sesión no está registrada, así que
// la fila DEBE quedar offline.
func TestCaidaSinReconexionSigueMarcandoOffline(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	reg := session.NewRegistry()
	srv := New(reg, laneLog(), WithFleet(repo))
	cc := ccDePrueba("s-1")

	releaseA := reg.Register(cc.sessionID, senderFunc(func(*cloudlinkv1.CloudToEdge) error { return nil }))
	srv.onSessionRegistered(context.Background(), cc)

	lane, soltar := carrilTapado(t, cc.sessionID)
	defer cerrarCarril(lane, soltar)

	releaseA()
	srv.onStreamClosed(lane, cc)
	cerrarCarril(lane, soltar)

	exigirEstadoFleet(t, repo, cc, fleet.StateOffline,
		"tras caer el stream sin reconexión: el MarkOffline diferido sigue siendo obligatorio")
}

// exigirEstadoFleet afirma el estado persistido de la sesión, nombrando el momento del
// relato en el que se afirma (los dos tests de arriba miran la MISMA fila en momentos
// distintos, y sin el contexto el fallo no se sabe leer).
func exigirEstadoFleet(t *testing.T, repo fleet.Repository, cc connCtx, quiero fleet.State, cuando string) {
	t.Helper()
	s, ok, err := repo.Get(context.Background(), cc.tenantID, cc.edgeID, cc.sessionID)
	if err != nil {
		t.Fatalf("fleet Get (%s): %v", cuando, err)
	}
	if !ok {
		t.Fatalf("la sesión %q no está en fleet (%s)", cc.sessionID, cuando)
	}
	if s.State != quiero {
		t.Fatalf("fleet dice %q y quiero %q — %s", s.State, quiero, cuando)
	}
}
