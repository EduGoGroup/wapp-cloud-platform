// Porta internal/flujos/contact/resolver.go @ 77df20f

package contact

import (
	"context"
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ErrNoRefs lo devuelve Resolve cuando no recibe ninguna referencia: la lista de refs está vacía,
// nil o []Ref{}, una vez deduplicada. NO es un filtro de refs inválidas: Resolve no descarta una
// Ref vacía ni una no normalizable, porque exige como precondición que cada Ref venga de NewRef
// (R-18). Se inspecciona con errors.Is. Su texto es observable y no cambia: «contact: se requiere
// al menos una contact_ref». Resolve lo devuelve SIN envolver: el texto del error es exactamente
// ese, sin el tenant ni ningún otro detalle.
var ErrNoRefs = errors.New("contact: se requiere al menos una contact_ref")

// ErrNoDestino lo devuelven Destino y Ref.Sendable cuando no hay ninguna referencia direccionable:
// el contacto solo tiene un wa_username (aún no enviable) o ninguna ref. El runtime lo trata como
// un fallo claro, sin emitir un `to` inválido hacia el Edge (R-20). Se inspecciona con errors.Is:
// Ref.Sendable lo envuelve añadiendo el kind. Su texto es observable y no cambia: «contact: sin
// destino enviable para el contact_id».
var ErrNoDestino = errors.New("contact: sin destino enviable para el contact_id")

// ErrContactNotFound lo devuelve Destino cuando el contact_id no existe, no pertenece al tenant
// o es el de un huérfano que una fusión ya borró (R-21, N-01, N-03). Los adaptadores lo envuelven
// con %w y el id entre comillas (%q), así que se inspecciona con errors.Is, no por igualdad. Su
// texto base es observable y no cambia: «contact: contact_id no encontrado». El envoltorio
// también lo es, y es el mismo en las dos implementaciones: el formato es "%w: %q" con el
// contactID tal como se recibió, así que el texto completo es exactamente «contact: contact_id no
// encontrado: "<contactID>"».
var ErrContactNotFound = errors.New("contact: contact_id no encontrado")

// Resolver es el puerto de identidad de contactos: traduce entre las referencias del mundo
// (número, LID, username) y el contact_id opaco con el que opera el motor de flujos. Lo
// implementan MemoryResolver (en memoria) y PostgresResolver (public.contacts, con el value
// cifrado en reposo), y la suite contacthelpertest.Contrato fija lo que las dos prometen.
//
// Todo es por tenant (N-01): las mismas refs en dos tenants son dos contactos distintos, y un
// contact_id solo existe dentro del tenant que lo creó. tenantID y contactID son UUID: con uno mal
// formado Postgres devuelve un error de parseo, NO ErrContactNotFound; la memoria no parsea nada y
// trata los ids como claves opacas (Resolve no los rechaza y Destino devuelve ErrContactNotFound).
// Quien llama pasa siempre UUID bien formados.
type Resolver interface {
	// Resolve devuelve el contact_id (UUID) del contacto que describen las refs, creándolo si
	// hace falta. Todas las refs son del MISMO contacto. Con error devuelve contactID "".
	//
	// Precondición: cada Ref viene de NewRef. Resolve no la valida ni la re-normaliza (otra
	// normalización cambiaría el índice ciego de las filas que ya existen): una Ref vacía o no
	// normalizable cuenta como una ref más, no se descarta.
	//
	// Primero deduplica las refs por (kind, value): una ref repetida en la entrada cuenta una vez.
	// Con la lista vacía —nil o []Ref{}— tras deduplicar devuelve ErrNoRefs (R-18). Después, según
	// cuántos contactos existentes tienen alguna de las refs:
	//   - Ninguno: crea un contact_id nuevo y le ata todas las refs (R-12).
	//   - Uno: reutiliza ese contact_id y ata las refs que faltan (R-14, R-15).
	//   - Varios, con contact_id DISTINTOS: los FUNDE, la reconciliación tardía (R-16). El
	//     canónico es el contact_id más antiguo (ante un empate de antigüedad, el de id menor).
	//     Las refs de los huérfanos pasan al canónico, el huérfano deja de existir (N-03: Destino
	//     de su id da ErrContactNotFound) y su estado conversacional se migra al canónico (ver
	//     StateMigrator); si ambos tenían estado en la misma sesión, se conserva el del canónico
	//     (R-17).
	//
	// El resultado es determinista por (tenant, ref): la misma ref devuelve siempre el mismo
	// contact_id (R-13), dos refs juntas dan un solo contact_id y cada una por separado vuelve a
	// dar ese mismo (R-14). El kind es parte de la clave (N-02): "88887777" como phone_e164 y
	// como wa_lid son dos contactos distintos. Es un get-or-create atómico y seguro para uso
	// concurrente: llamadas simultáneas con la misma ref terminan con UN solo contact_id (R-32).
	//
	// Si pushName no es vacío, lo registra para el contacto; si es vacío, no toca el nombre. Qué
	// nombre sobrevive si llegan varios NO es parte del contrato (Postgres conserva el primero,
	// por fila; la memoria, el último), y pushName nunca cambia el contact_id (N-04).
	//
	// Cualquier otro error es del adaptador (almacén, cifrado, migración del estado) y se devuelve
	// envuelto con %w; no es parte del puerto.
	Resolve(ctx context.Context, tenantID string, refs []Ref, pushName string) (contactID string, err error)
	// Destino devuelve la ref ENVIABLE del contacto contactID, tal como quedó guardada: con el
	// value normalizado, no el crudo con el que llegó (R-23).
	//
	// Elige, entre las refs direccionables (las que Ref.Sendable acepta), por preferencia
	// phone_e164 > wa_username > wa_lid (R-19). wa_username figura en el orden pero hoy no es
	// direccionable, así que en la práctica se degrada a wa_lid: un contacto con teléfono y LID da
	// el teléfono, y uno solo con LID da el LID.
	//
	// Si el contacto tiene VARIAS refs direccionables del kind elegido (dos teléfonos, porque se ató
	// un segundo número o tras una fusión), Destino devuelve UNA de ellas, y cuál NO es parte del
	// contrato. La memoria da la primera que se ató al contacto (en una fusión, las del canónico van
	// antes que las de los huérfanos). Postgres lee las filas del contacto sin ORDER BY y da la
	// primera de ese kind que le llegue: un orden que la base no garantiza y que puede cambiar de una
	// llamada a otra. Lo que sí se promete es el kind (el de mejor preferencia entre los
	// direccionables) y que la ref es del contacto. Quien necesite un destino estable entre varios
	// números no puede apoyarse en Destino.
	//
	// Devuelve ErrNoDestino si el contacto existe pero ninguna de sus refs es direccionable
	// (p. ej. solo un wa_username), y ErrContactNotFound si contactID no existe o es de otro tenant
	// (R-21, N-01). ErrNoDestino llega SIN envolver: el texto del error es exactamente el del
	// centinela, sin el detalle del kind que añade Ref.Sendable. ErrContactNotFound llega envuelto,
	// con el texto exacto que dice su comentario.
	Destino(ctx context.Context, tenantID, contactID string) (Ref, error)
}

// StateMigrator re-clava el estado conversacional de un contact_id en otro dentro del mismo
// tenant: MigrateContactID pasa el estado del huérfano (fromContactID) al canónico (toContactID).
// Es lo que el resolver en memoria llama durante la fusión; PostgresResolver no lo usa, porque
// hace esa migración en SQL dentro de su MISMA transacción (atomicidad).
//
// Política de conflicto (R-17): si el canónico ya tiene estado en la misma sesión, se CONSERVA el
// del canónico y se descarta el del huérfano. El canónico es la identidad autoritativa. Devuelve
// error si no pudo migrar.
type StateMigrator interface {
	MigrateContactID(ctx context.Context, tenantID, fromContactID, toContactID string) error
}

// Sendable devuelve la cadena de destino que el Edge sabe direccionar para esta Ref (R-20): el
// cloud resuelve el destino real (ADR-0005) y el Edge solo completa el servidor.
//   - phone_e164: el número tal cual, sin servidor (el Edge le añade "@s.whatsapp.net").
//   - wa_lid: el LID con su servidor, "<lid>@lid".
//   - cualquier otro kind, wa_username incluido (aún no direccionable): devuelve "" y un error
//     que envuelve ErrNoDestino, con el texto exacto «contact: sin destino enviable para el
//     contact_id: kind "<kind>" no direccionable».
//
// No valida ni re-normaliza el Value: confía en que la Ref viene de NewRef.
func (r Ref) Sendable() (string, error) {
	panic(pendiente.Implementar("contact.Ref.Sendable"))
}

// RefsFrom construye las Ref de un mensaje entrante a partir de la identidad enriquecida que
// manda el Edge —fromPn, el número, y fromLid, el LID— y, SOLO como último recurso, del JID crudo
// from (R-22). Cada una pasa por NewRef.
//   - Devuelve primero la ref de número (si fromPn no es vacío y normaliza como phone_e164) y
//     después la de LID (si fromLid no es vacío y normaliza como wa_lid).
//   - Solo si ninguna de las dos produjo una ref y from no es vacío, usa from como respaldo, y
//     el kind lo infiere por su servidor: si contiene "@lid" es wa_lid; en cualquier otro caso,
//     phone_e164. Si fromPn o fromLid dieron una ref, from se ignora.
//   - Descarta EN SILENCIO lo que no normaliza (el mapeo LID↔número puede no existir todavía): no
//     devuelve error, y puede devolver una lista vacía si nada es utilizable.
//
// N-05, comportamiento actual que se conserva: en el respaldo, un JID de dispositivo como
// "57300…:5@s.whatsapp.net" se normaliza como teléfono CON el dígito del dispositivo, porque
// Normalize conserva todo dígito ("…33" y el "5" dan "…335"); un LID con dispositivo, en cambio,
// pierde el sufijo. Los índices ciegos se calculan sobre esa salida, así que cualquier
// corrección se hace a la vez en el paquete anterior y en este.
func RefsFrom(fromPn, fromLid, from string) []Ref {
	panic(pendiente.Implementar("contact.RefsFrom"))
}
