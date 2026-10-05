// Porta internal/gateway/grpc/connect.go @ cbc5736 (el bucle Recv del stream CloudLink, su
// cierre y la identidad mTLS del peer). Trozo de connect.go, partido por E-13.
//
// Aquí vive Connect y lo que es solo suyo: closeStream y peerIdentity, que no son exportados
// y nacen con el verde (T-17). Lo que el bucle LLAMA está en los otros trozos: el reparto de
// cada frame en connect_route.go, el registro y el cierre de una sesión en connect_session.go,
// y las partes del job del latido en connect_heartbeat.go.
//
// Nombres (E-11), viejo → nuevo: controlVisto → controlSeen; sesionNueva → newSession;
// calientaPorRegistro → warmOnRegister (readiness.go). El paquete google.golang.org/grpc se
// importa como googlegrpc. Los textos y las claves de log son los del viejo, literales.

package grpc

import (
	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	googlegrpc "google.golang.org/grpc"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Connect atiende el stream bidireccional CloudLink de UN Edge: lee sus frames hasta que
// el stream termina y devuelve nil si el Edge cerró con orden (io.EOF) o, si no, el error
// de Recv tal cual. Es el método del servicio gRPC; lo registra Register.
//
// Lo que promete, y prueban connect_test.go y sus hermanos:
//
//   - IDENTIDAD. El (tenant, Edge) del stream sale SOLO del certificado mTLS del peer
//     (CN = edge_id, Organization[0] = tenant_id). Sin TLS, o con un certificado al que
//     le falte cualquiera de los dos, el stream es anónimo y degrada: sus sesiones entran
//     en el Registry y sus frames se encaminan, pero no hay flota, lease, config,
//     seguimiento ni calentamiento a nombre de nadie.
//   - REGISTRO PEREZOSO (ADR-0008). Cada session_id no vacío se registra con su PRIMER
//     frame, una sola vez por stream: entra en el Registry con el cable de este stream
//     —el MISMO candado de escritura para todas sus sesiones (R-G12)— y, con identidad,
//     se rastrea, se audita, se marca online y recibe su lease inicial y después su
//     config, antes de encaminar ese frame. Un frame sin session_id no registra nada.
//   - EL CANAL DE CONTROL NO ES UNA SESIÓN (ADR-0048). `__wapp_control__` no entra en el
//     Registry ni en el seguimiento, ni produce flota, lease o calentamiento. La primera
//     vez que un stream lo usa recibe la config de su tenant por su propio cable; las
//     respuestas de auth vuelven por ese mismo cable.
//   - DESPACHO. Cada frame se encamina bajo SU session_id (route): lo que es memoria se
//     resuelve aquí, en la goroutine del bucle Recv, y lo que toca red o base va al
//     carril de su sesión, que no muere con el stream.
//   - CALENTAMIENTO. Tras encaminar el frame que registró una sesión NUEVA, y solo ese,
//     se avisa del calentamiento de compatibilidad (warmOnRegister): después, para que lo
//     que ese frame diga sobre la capacidad de inferencia del Edge ya esté aprendido.
//   - HOOKS. OnIncoming, OnHeartbeat, OnWarmup y OnEdgeReady corren INLINE en el bucle
//     Recv: mientras uno no vuelve, no se lee el siguiente frame.
//   - CIERRE. Al terminar el stream, cada una de sus sesiones sale del Registry; si quedó
//     sin stream, sus envíos en vuelo dejan de esperar; y su MarkOffline se encola como
//     ÚLTIMO trabajo de la sesión. El carril se sella y se drena antes de volver. Una
//     sesión que ya reconectó por otro stream no se toca (R-G4).
func (s *Server) Connect(stream googlegrpc.BidiStreamingServer[cloudlinkv1.EdgeToCloud, cloudlinkv1.CloudToEdge]) error {
	panic(pendiente.Implementar("grpc.Connect"))
}
