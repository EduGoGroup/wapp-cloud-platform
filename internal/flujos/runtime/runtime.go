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
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// Sender es la salida del motor hacia el Gateway: enviar texto o un adjunto al
// contacto. Sus firmas encajan exactamente con (*gatewaygrpc.Server).SendText y
// .SendMedia, de modo que el Gateway las implementa sin adaptador.
type Sender interface {
	SendText(ctx context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error)
	// SendMedia despacha un adjunto (Plan 017 §4.2/§6.1): el binario NO viaja por
	// gRPC, va la URL prefirmada (presignedURL) que el Edge descarga y sube a
	// WhatsApp. kind ("document"|"image") elige la rama DocumentMessage/ImageMessage.
	SendMedia(ctx context.Context, sessionID, to, presignedURL, filename, mime, caption, kind string) (*cloudlinkv1.Ack, error)
}

// Presigner genera la URL prefirmada de DESCARGA (GET) de un objeto del almacén.
// El runtime la consume al despachar un nodo media: presigna la MediaRef.Key y
// pasa la URL al Sender (design.md §4.2/§9.C). Interfaz ESTRECHA (solo el download
// que 017 usa) para testear con dobles y no acoplar el runtime al adapter S3/R2;
// la satisface objectstore.PresignClient (superset con GenerateUploadURL, Plan 018).
type Presigner interface {
	GenerateDownloadURL(ctx context.Context, key string) (url string, expiresAt time.Time, err error)
}

// TenantResolver resuelve el tenant_id a partir del session_id, porque el hook
// OnIncoming solo entrega session_id (design.md §10.A, a confirmar). Lo
// implementa el fleet repo o equivalente.
type TenantResolver interface {
	ResolveTenant(ctx context.Context, sessionID string) (tenantID string, err error)
}
