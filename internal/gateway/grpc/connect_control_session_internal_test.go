package gatewaygrpc

import (
	"context"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cltransport "github.com/EduGoGroup/wapp-cloudlink/transport"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// connect_control_session_internal_test.go — EL CANAL DE CONTROL NO ES FLOTA (MP-11).
//
// 🔴 QUÉ SE CUSTODIA AQUÍ. El Edge estampa `cltransport.ControlSessionID` en los frames de auth
// porque el gateway exige un session_id no vacío y el operador puede loguearse ANTES de emparejar
// ningún teléfono. Ese id tiene que REGISTRARSE (sin eso la respuesta del login no tiene por dónde
// volver) y NO tiene que PERSISTIRSE: no hay ningún teléfono detrás.
//
// Los dos tests van EN PAREJA a propósito, y ninguno vale solo:
//   - el primero exige que la fila NO nazca para el id de control;
//   - el segundo exige que SÍ nazca para una sesión normal. Sin él, borrar el MarkOnline entero
//     dejaría el primero en verde — que es el modo clásico de que una guarda «pase» por demolición.
//
// El tercero cubre lo que NO se puede romper al arreglar esto: el registro en el Registry.

// TestRegisterSession_ElCanalDeControl_NO_ProduceFilaDeFlota es el test del MP-11.
//
// Mutación que lo pone en rojo: quitar `&& cc.sessionID != cltransport.ControlSessionID` de la
// guarda de registerSession.
func TestRegisterSession_ElCanalDeControl_NO_ProduceFilaDeFlota(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	srv := New(session.NewRegistry(), laneLog(), WithFleet(repo))
	cc := ccDePrueba(cltransport.ControlSessionID)

	srv.onSessionRegistered(context.Background(), cc)

	if _, found, err := repo.Get(context.Background(), cc.tenantID, cc.edgeID, cc.sessionID); err != nil || found {
		t.Fatalf("el canal de control dejó fila en fleet_sessions (found=%v err=%v). "+
			"Esa fila la ve el CLIENTE en su dashboard como si fuera un teléfono: con `self_pn` vacío la "+
			"plantilla cae al session_id, lleva selector de perfil —y marcarlo pasivo movería la version "+
			"del mapa de filters del tenant entero— y es seleccionable como destino de envío", found, err)
	}
	if lista, err := repo.List(context.Background(), cc.tenantID); err != nil || len(lista) != 0 {
		t.Fatalf("fleet.List devuelve %d sesiones para un tenant cuyo único id es el de control, "+
			"quiero 0 (err=%v)", len(lista), err)
	}
}

// TestRegisterSession_UnaSesionNormal_SI_ProduceFilaDeFlota es el CONTROL del anterior, y no es
// redundante: es lo único que distingue «la guarda funciona» de «alguien borró el MarkOnline».
//
// Mutación que lo pone en rojo: borrar la llamada a fleet.MarkOnline de registerSession.
func TestRegisterSession_UnaSesionNormal_SI_ProduceFilaDeFlota(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	srv := New(session.NewRegistry(), laneLog(), WithFleet(repo))
	cc := ccDePrueba("11111111-2222-3333-4444-555555555555")

	srv.onSessionRegistered(context.Background(), cc)

	s, found, err := repo.Get(context.Background(), cc.tenantID, cc.edgeID, cc.sessionID)
	if err != nil || !found {
		t.Fatalf("una sesión NORMAL no dejó fila en fleet_sessions: found=%v err=%v. "+
			"La guarda del canal de control se llevó por delante el caso bueno", found, err)
	}
	if s.State != fleet.StateOnline {
		t.Fatalf("estado = %q, quiero online", s.State)
	}
}

// TestRegisterSession_ElCanalDeControl_SIGUE_RegistradoEnElRegistry custodia lo que NO se puede
// romper al arreglar lo de arriba. El registro en el Registry es lo que enruta el UserAuthResponse
// (`registry.Push(session_id)`): si alguien «limpiara» el canal de control saltándose también el
// registro, el login del operador dejaría de tener por dónde volver — y el síntoma no sería un test
// rojo, sería un operador que no puede entrar en su propio Edge.
func TestRegisterSession_ElCanalDeControl_SIGUE_RegistradoEnElRegistry(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	srv := New(reg, laneLog(), WithFleet(fleet.NewMemoryRepository()))
	cc := ccDePrueba(cltransport.ControlSessionID)

	entregados := 0
	release := reg.Register(cc.sessionID, senderFunc(func(*cloudlinkv1.CloudToEdge) error {
		entregados++
		return nil
	}))
	defer release()
	srv.onSessionRegistered(context.Background(), cc)

	if err := reg.Push(context.Background(), cc.sessionID, &cloudlinkv1.CloudToEdge{}); err != nil {
		t.Fatalf("el canal de control dejó de ser enrutable (%v): el login del operador no tendría "+
			"por dónde volver", err)
	}
	if entregados == 0 {
		t.Fatal("el Push no llegó al sender del canal de control")
	}
}
