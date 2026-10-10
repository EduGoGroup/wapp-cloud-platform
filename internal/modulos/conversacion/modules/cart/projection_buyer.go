// Porta internal/flujos/modules/cart/projection.go @ 9d5a4b6 (trozo «comprador y
// transiciones»: putBuyerField y transitionOpenIntake; el viejo es un solo fichero y
// aquí nace partido, 05 E-13)

// projection_buyer.go son las dos proyecciones que actúan sobre la solicitud ABIERTA
// de (tenant, contacto) sin tocar sus líneas: guardar UN campo del checklist del
// comprador en su fila cifrada (buyer_data_captured) y llevar la solicitud a
// cancelled / expired (cart_cancelled, cart_expired).
//
// No exporta nada: lo que promete se ve por Projector.Project (projection.go).

package cart

import (
	"context"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// putBuyerField materializa UN campo del checklist del comprador en la fila
// CIFRADA de la solicitud (T4.5, D-041.13). Es el único punto del carrito por el
// que un dato personal llega a la base, y por eso conviene tener claro qué hace y
// qué no:
//
//   - Cuelga el dato de la solicitud ABIERTA de (tenant, contacto), la misma que
//     abrió el primer item_added. Es también el motivo por el que el módulo emite
//     estos efectos ANTES de cart_closed: después no habría solicitud abierta.
//   - SIN solicitud abierta devuelve ERROR en vez de tragárselo en silencio. El
//     dispatcher lo loguea sin abortar la conversación (mismo trato que la revisión
//     del cierre), pero queda constancia: un checklist que el cliente rellenó y no
//     se guardó tiene que ser visible, no un hueco.
//   - El valor NO se loguea, NO se devuelve y NO entra en ningún mensaje de error.
//     Lo que puede aparecer en un log de fallo es la clave del campo ("rut"), que
//     es configuración del tenant, jamás lo que el cliente escribió.
func (p *Projector) putBuyerField(ctx context.Context, meta modules.EffectMeta, eff modules.Effect) error {
	key := modules.AsString(eff.Payload["key"])
	value := modules.AsString(eff.Payload["value"])
	if key == "" || value == "" {
		// Efecto vacío o mal formado: nada que guardar. No es un error — el módulo no
		// los produce así, y un replay de un payload viejo no debe reventar.
		return nil
	}
	intake, found, err := p.store.GetOpenIntake(ctx, meta.TenantID, meta.ContactID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("cart: campo %q del comprador sin solicitud abierta que lo reciba", key)
	}
	if err := p.buyer.PutBuyerField(ctx, intake.ID, key, value); err != nil {
		return fmt.Errorf("cart: guardar el campo %q del comprador de la solicitud %s: %w", key, intake.ID, err)
	}
	return nil
}

// transitionOpenIntake lleva la solicitud "open" a cancelled/expired (design.md §3.4).
// Sin solicitud abierta es un no-op sin error (idempotente / nada que transicionar).
func (p *Projector) transitionOpenIntake(ctx context.Context, meta modules.EffectMeta, status string) error {
	intake, found, err := p.store.GetOpenIntake(ctx, meta.TenantID, meta.ContactID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	return p.store.MarkIntakeStatus(ctx, intake.ID, status, intake.Total)
}
