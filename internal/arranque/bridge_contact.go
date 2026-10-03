// Adaptador de tipos entre el contact VIEJO (internal/flujos/contact) y el NUEVO
// (internal/nucleo/contact). No porta ningún fichero: nace en F1 (T1.14, arquitectura §3 del plan
// de F1) para que el arranque nuevo cablee nucleo/contact sin tocar el runtime ni el notificador
// viejos (E-1).

package arranque

import (
	"context"

	viejo "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// contactBridge presenta un contact.Resolver NUEVO como el viejo.Resolver que piden los paquetes
// viejos que el arranque sigue cableando: flowruntime.New (fase 7) e intakes.NewNotifier
// (fase 6, vía intakes.Destinations). Hace falta porque contact.Ref y viejo.Ref son tipos
// distintos para Go aunque tengan los mismos campos: un *contact.PostgresResolver no satisface
// viejo.Resolver. Las alternativas (un alias de tipo en cualquiera de los dos paquetes) editan lo
// viejo o atan el núcleo a flujos; internal/arranque es el único sitio que ve a los dos por diseño.
//
// Delega en next, el puerto nuevo. En producción es el *contact.PostgresResolver que construye
// newContactResolver con el cipher y el KeyProvider de la fase 3 (T1.16); se guarda como puerto,
// no como el tipo concreto, para que el test unitario lo ejercite con un doble y sin BD.
//
// Promesas:
//   - Resolve copia cada viejo.Ref en un contact.Ref campo a campo (Kind y Value), en el mismo
//     orden y sin quitar ni añadir ninguna, y delega en next.Resolve con el mismo ctx, tenantID y
//     pushName. NO re-normaliza ni valida: el viejo tampoco lo hace (confía en refs de NewRef), y
//     otra normalización cambiaría el índice ciego de las filas que ya existen. Devuelve el
//     contactID que da next tal cual.
//   - Destino delega en next.Destino con el mismo ctx, tenantID y contactID, y copia la
//     contact.Ref que recibe en una viejo.Ref campo a campo.
//   - Errores: si el error de next casa (errors.Is) con contact.ErrNoRefs, contact.ErrNoDestino o
//     contact.ErrContactNotFound, devuelve un error cuyo Error() es EXACTAMENTE el mismo texto,
//     byte a byte (también el envoltorio «: "<contactID>"» de not found), y cuyo Unwrap() []error
//     contiene el error original Y el centinela viejo equivalente (viejo.ErrNoRefs,
//     viejo.ErrNoDestino, viejo.ErrContactNotFound): errors.Is casa con el centinela nuevo y con
//     el viejo. Cualquier otro error pasa tal cual, sin envolver. Junto a un error, Resolve
//     devuelve "" y Destino una viejo.Ref vacía.
//
// Vida: nace en F1 · F6 lo deja de usar para el notificador (el intakes nuevo recibe tipos del
// núcleo) · muere en F8, cuando el runtime nuevo recibe un contact.Resolver.
type contactBridge struct {
	// next es el resolver nuevo en el que se delega todo.
	//
	// El nolint es solo del rojo (trampa T-1): con los cuerpos en panic nada lee el campo y el
	// test que lo escribe lleva la etiqueta pendiente, que el linter no ve. El verde (T1.15) lo lee
	// y QUITA esta marca.
	next contact.Resolver //nolint:unused // rojo T1.14: lo lee el verde (T1.15), que quita la marca

}

// contactBridge es un viejo.Resolver (y por tanto un intakes.Destinations). La aserción además
// lo mantiene «usado» para el linter mientras los cuerpos son panic (trampa T-1).
var _ viejo.Resolver = (*contactBridge)(nil)

// Resolve implementa viejo.Resolver: copia las refs al tipo nuevo y delega (ver contactBridge).
func (b *contactBridge) Resolve(ctx context.Context, tenantID string, refs []viejo.Ref, pushName string) (string, error) {
	panic(pendiente.Implementar("arranque.contactBridge.Resolve"))
}

// Destino implementa viejo.Resolver: delega y copia la ref de vuelta al tipo viejo (ver
// contactBridge).
func (b *contactBridge) Destino(ctx context.Context, tenantID, contactID string) (viejo.Ref, error) {
	panic(pendiente.Implementar("arranque.contactBridge.Destino"))
}
