package runtime

import (
	"context"
	"fmt"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// send empuja cada salida por el Sender en orden y devuelve el último Ack. Una
// salida con Media (nodo media, Plan 017 §4.2) se PRESIGNA y despacha por
// SendMedia; el resto por SendText. Ante el primer error corta y lo devuelve
// (con el último Ack logrado). El estado ya se persistió antes de llamar a send
// (orden Save-antes-de-Send), así que un fallo aquí NO corrompe el estado: se
// devuelve para que el llamante lo LOGUEE (OnIncoming) o lo surface (Start).
func (rt *Runtime) send(ctx context.Context, sessionID, to string, outs []engine.Output) (*cloudlinkv1.Ack, error) {
	var last *cloudlinkv1.Ack
	for _, out := range outs {
		if out.Media != nil {
			ack, err := rt.sendMedia(ctx, sessionID, to, out.Media)
			if err != nil {
				return last, err
			}
			last = ack
			continue
		}
		ack, err := rt.sender.SendText(ctx, sessionID, to, out.Text)
		if err != nil {
			return last, fmt.Errorf("runtime: enviar texto: %w", err)
		}
		last = ack
	}
	return last, nil
}

// sendMedia presigna la key del adjunto y lo despacha por Sender.SendMedia (Plan
// 017 §4.2/§9.C): el runtime presigna, el módulo no. Exige un Presigner cableado
// (WithPresignClient); su ausencia es un error de configuración explícito (un nodo
// media sin almacén), no un pánico. La URL prefirmada es un capability token de
// corta vida; el binario nunca viaja por la nube ni por gRPC (zero-knowledge).
func (rt *Runtime) sendMedia(ctx context.Context, sessionID, to string, ref *model.MediaRef) (*cloudlinkv1.Ack, error) {
	if rt.presigner == nil {
		return nil, fmt.Errorf("runtime: nodo media sin PresignClient configurado (usa WithPresignClient)")
	}
	url, _, err := rt.presigner.GenerateDownloadURL(ctx, ref.Key)
	if err != nil {
		return nil, fmt.Errorf("runtime: presignar media %q: %w", ref.Key, err)
	}
	ack, err := rt.sender.SendMedia(ctx, sessionID, to, url, ref.Filename, ref.Mime, ref.Caption, ref.Kind)
	if err != nil {
		return nil, fmt.Errorf("runtime: enviar media %q: %w", ref.Key, err)
	}
	return ack, nil
}

// sendReply auto-responde al avance de un entrante respetando el tope anti-loop
// (Plan 020 · T0): SOLO si hay salidas consume un token de la conversación antes de
// resolver el destino y enviar; agotado ⇒ no envía (corta el bucle; el estado ya
// avanzó y se persistió, así que no se corrompe). Sin salidas es un no-op que NO
// gasta cuota. Extraído de HandleIncoming para acotar su complejidad ciclomática.
func (rt *Runtime) sendReply(ctx context.Context, tenantID, sessionID, contactID string, key store.Key, outs []engine.Output) error {
	if len(outs) == 0 {
		return nil
	}
	if !rt.replyAllowed(key) {
		return nil
	}
	to, err := rt.destino(ctx, tenantID, contactID)
	if err != nil {
		return err
	}
	_, err = rt.send(ctx, sessionID, to, outs)
	return err
}

// destino resuelve el contact_id a una cadena de destino DIRECCIONABLE por el
// Edge (design.md §10.E): desacopla el envío del JID entrante (doble rol, R4).
func (rt *Runtime) destino(ctx context.Context, tenantID, contactID string) (string, error) {
	dst, err := rt.contacts.Destino(ctx, tenantID, contactID)
	if err != nil {
		return "", fmt.Errorf("runtime: resolver destino: %w", err)
	}
	to, err := dst.Sendable()
	if err != nil {
		return "", fmt.Errorf("runtime: destino no direccionable: %w", err)
	}
	return to, nil
}
