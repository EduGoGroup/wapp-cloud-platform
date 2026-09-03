package gatewaygrpc

import (
	"context"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	cltransport "github.com/EduGoGroup/wapp-cloudlink/transport"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// connect_control_session_internal_test.go — EL CANAL DE CONTROL NO ES FLOTA (MP-11),
// Y DESDE EL PLAN 057 TAMPOCO ES UNA SESIÓN.
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
// Los dos últimos cubren lo que NO se puede romper al arreglar nada de esto: que el operador
// pueda entrar. Hasta el Plan 057 eso se custodiaba exigiendo que el canal de control SIGUIERA
// registrado en el Registry; hoy se custodia exigiendo lo contrario —que la respuesta salga por
// el stream que preguntó y NO por el Registry—, que es lo que hace innecesario el registro. El
// caso protegido es el mismo; lo que cambió es el mecanismo. Ver el comentario de cada uno.

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

// TestPushAuthResponse_SalePorElStreamQuePregunto SUSTITUYE, desde el Plan 057, al
// antiguo TestRegisterSession_ElCanalDeControl_SIGUE_RegistradoEnElRegistry.
//
// 🔴 POR QUÉ SE SUSTITUYE Y NO SE BORRA. Aquel test custodiaba una premisa que era
// CIERTA cuando se escribió —«sin el registro en el Registry, el login del operador no
// tiene por dónde volver»— y que la Ola 1 volvió FALSA: la respuesta de auth sale ahora
// por el stream que hizo la petición, así que el canal de control ya no necesita estar
// registrado en ningún sitio (y no debe estarlo: es una constante global compartida por
// todos los Edge, ver el `case` de Connect).
//
// Lo que NO cambió es el caso que hay que proteger, y por eso el test se rehace en vez
// de desaparecer: **el operador puede loguearse antes de emparejar ningún teléfono**
// (REQ-057.6). Si alguien «limpiara» el canal de control llevándose también la
// respuesta, el síntoma no sería un test rojo, sería un operador que no puede entrar en
// su propio Edge.
//
// El montaje es el del incidente en miniatura: el Registry tiene registrado bajo la
// clave de control a un Edge AJENO —eso es exactamente lo que pasaba con dos Edge
// conectados— y se comprueba que la respuesta sale por el sender del connCtx y que el
// ajeno no recibe nada.
//
// 🔬 MUTACIÓN: devolver pushAuthResponse a `s.registry.Push(ctx, cc.sessionID, msg)`.
func TestPushAuthResponse_SalePorElStreamQuePregunto(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	srv := New(reg, laneLog())

	ajeno := 0
	release := reg.Register(cltransport.ControlSessionID, senderFunc(func(*cloudlinkv1.CloudToEdge) error {
		ajeno++
		return nil
	}))
	defer release()

	var recibido []*cloudlinkv1.CloudToEdge
	cc := ccDePrueba(cltransport.ControlSessionID)
	cc.sender = senderFunc(func(msg *cloudlinkv1.CloudToEdge) error {
		recibido = append(recibido, msg)
		return nil
	})

	srv.pushAuthResponse(context.Background(), cc, &cloudlinkv1.UserAuthResponse{
		CommandId: "cmd-1",
		SessionId: cc.sessionID,
		Result: &cloudlinkv1.UserAuthResponse_Tokens{Tokens: &cloudlinkv1.UserTokens{
			AccessToken: "tok",
		}},
	})

	if len(recibido) != 1 {
		t.Fatalf("el operador se quedó sin respuesta: el stream que preguntó recibió %d frames, quiero 1",
			len(recibido))
	}
	if got := recibido[0].GetUserAuthResponse().GetCommandId(); got != "cmd-1" {
		t.Fatalf("command_id entregado = %q, quiero cmd-1", got)
	}
	if ajeno != 0 {
		t.Fatalf("la respuesta salió TAMBIÉN por el Edge registrado bajo la clave de control "+
			"(%d entregas): eso es la fuga del 2026-09-03, con tokens de un operador viajando "+
			"al cable de otro cliente", ajeno)
	}
}

// TestPushAuthResponse_SinSenderNoCaeAlRegistry custodia la decisión D-057.2: un
// connCtx sin stream emisor es un ERROR DE PROGRAMA, no un caso degradado que se
// resuelva tirando del Registry.
//
// 🔴 El análisis que originó el plan proponía dejar un «fallback defensivo a registry
// (solo para tests que no inyectan sender)». Eso es precisamente el camino que produjo
// el incidente: conservarlo lo deja armado para el día en que alguien construya un
// connCtx incompleto en producción, y entonces la fuga vuelve sin que nada chille.
//
// 🔬 MUTACIÓN: añadir el fallback `_ = s.registry.Push(ctx, cc.sessionID, msg)`.
func TestPushAuthResponse_SinSenderNoCaeAlRegistry(t *testing.T) {
	t.Parallel()
	reg := session.NewRegistry()
	srv := New(reg, laneLog())

	entregas := 0
	release := reg.Register(cltransport.ControlSessionID, senderFunc(func(*cloudlinkv1.CloudToEdge) error {
		entregas++
		return nil
	}))
	defer release()

	cc := ccDePrueba(cltransport.ControlSessionID) // sin sender, a propósito
	srv.pushAuthResponse(context.Background(), cc, &cloudlinkv1.UserAuthResponse{CommandId: "cmd-1"})

	if entregas != 0 {
		t.Fatalf("un connCtx sin sender cayó al Registry (%d entregas): el fallback es el camino "+
			"exacto del incidente del 2026-09-03", entregas)
	}
}
