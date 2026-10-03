// Adaptador de tipos entre el contact VIEJO (internal/flujos/contact) y el NUEVO
// (internal/nucleo/contact). No porta ningún fichero: nace en F1 (T1.14, arquitectura §3 del plan
// de F1) para que el arranque nuevo cablee nucleo/contact sin tocar el runtime ni el notificador
// viejos (E-1).

package arranque

import (
	"context"
	"errors"

	viejo "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
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
	// next es el resolver nuevo en el que se delega todo; el adaptador no guarda más estado.
	next contact.Resolver
}

// contactBridge es un viejo.Resolver (y por tanto un intakes.Destinations): si alguna de las dos
// firmas cambia, esto no compila.
var _ viejo.Resolver = (*contactBridge)(nil)

// Resolve implementa viejo.Resolver: copia las refs al tipo nuevo y delega (ver contactBridge).
func (b *contactBridge) Resolve(ctx context.Context, tenantID string, refs []viejo.Ref, pushName string) (string, error) {
	// Copia campo a campo y nada más: NewRef ya normalizó en origen, y normalizar otra vez aquí
	// podría cambiar el value del que sale el índice ciego de filas que ya existen. Una lista nil
	// llega como lista vacía de longitud 0: next decide (ErrNoRefs), no el adaptador.
	newRefs := make([]contact.Ref, len(refs))
	for i, r := range refs {
		newRefs[i] = contact.Ref{Kind: r.Kind, Value: r.Value}
	}
	contactID, err := b.next.Resolve(ctx, tenantID, newRefs, pushName)
	if err != nil {
		return "", translateContactErr(err)
	}
	return contactID, nil
}

// Destino implementa viejo.Resolver: delega y copia la ref de vuelta al tipo viejo (ver
// contactBridge).
func (b *contactBridge) Destino(ctx context.Context, tenantID, contactID string) (viejo.Ref, error) {
	ref, err := b.next.Destino(ctx, tenantID, contactID)
	if err != nil {
		return viejo.Ref{}, translateContactErr(err)
	}
	return viejo.Ref{Kind: ref.Kind, Value: ref.Value}, nil
}

// sentinelPairs empareja cada centinela del resolver nuevo con su equivalente viejo. El contrato
// de viejo.Resolver promete sus propios centinelas: aunque hoy ningún paquete viejo fuera de
// flujos/contact los compare con errors.Is, el adaptador cumple ese contrato entero para que
// quien lo haga mañana (o un log que lo clasifique) no vea un error distinto según el arranque.
var sentinelPairs = []struct{ current, old error }{
	{contact.ErrNoRefs, viejo.ErrNoRefs},
	{contact.ErrNoDestino, viejo.ErrNoDestino},
	{contact.ErrContactNotFound, viejo.ErrContactNotFound},
}

// translateContactErr envuelve en un bridgeError el error de next que casa con un centinela del
// resolver nuevo, para que case también con el viejo; cualquier otro error (el de la BD, el del
// ctx, ErrInvalidRef) sale tal cual, porque no hay centinela viejo con el que emparejarlo y
// envolverlo solo cambiaría su tipo.
func translateContactErr(err error) error {
	for _, p := range sentinelPairs {
		if errors.Is(err, p.current) {
			return &bridgeError{original: err, oldSentinel: p.old}
		}
	}
	return err
}

// bridgeError es un error del resolver nuevo presentado a la vez como su centinela viejo. Su
// texto es el del original, byte a byte (los dos paquetes comparten literales, y alguno acaba en
// un log o en una respuesta), y Unwrap() []error expone los dos para que errors.Is case con el
// centinela nuevo, con el viejo y con el propio original.
type bridgeError struct {
	original    error
	oldSentinel error
}

// Error devuelve el texto del error original sin añadir nada.
func (e *bridgeError) Error() string { return e.original.Error() }

// Unwrap devuelve el error original y el centinela viejo equivalente (Go 1.20+, árbol de errores).
func (e *bridgeError) Unwrap() []error { return []error{e.original, e.oldSentinel} }
