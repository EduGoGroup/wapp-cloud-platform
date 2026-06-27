// Package runtime define las interfaces de frontera del motor de flujos hacia
// el resto de la plataforma. El motor depende del Gateway SOLO por interfaces
// estrechas (no del struct concreto), para mantener la frontera y testear con
// dobles (design.md §2).
//
// En T0 solo están las interfaces; la orquestación viva (OnIncoming → resolver
// clave → single-flight → cargar/persistir → empujar, y Start por API) llega
// en T4.
package runtime

import (
	"context"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// Sender es la salida del motor: enviar texto al contacto vía el Gateway. Su
// firma encaja exactamente con (*gatewaygrpc.Server).SendText, de modo que el
// Gateway la implementa sin adaptador.
type Sender interface {
	SendText(ctx context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error)
}

// TenantResolver resuelve el tenant_id a partir del session_id, porque el hook
// OnIncoming solo entrega session_id (design.md §10.A, a confirmar). Lo
// implementa el fleet repo o equivalente.
type TenantResolver interface {
	ResolveTenant(ctx context.Context, sessionID string) (tenantID string, err error)
}
