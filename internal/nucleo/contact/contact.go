// Package contact es la identidad de contacto del núcleo: un contacto se identifica por una o más
// referencias {kind, value} (Ref) que el puerto Resolver traduce a un contact_id opaco (UUID).
// La unidad de deduplicación es (tenant, kind, value), con el value ya NORMALIZADO.
//
// El paquete tiene tres capas, cada una en sus ficheros:
//   - contact.go: el dominio puro, sin estado ni E/S. Los kinds, Ref, ValidateKind, Normalize y
//     NewRef.
//   - resolver.go: el puerto Resolver (con StateMigrator), sus errores centinela y las ayudas de
//     destino que no necesitan almacén, Ref.Sendable y RefsFrom.
//   - repository_memory.go y repository_postgres.go: las dos implementaciones del puerto, en
//     memoria (MemoryResolver) y sobre public.contacts con el value cifrado en reposo
//     (PostgresResolver). La suite contacthelpertest.Contrato fija lo que las dos prometen.
//
// Porta internal/flujos/contact/contact.go @ 77df20f.
package contact

import (
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Kinds de contact_ref soportados. Sus valores literales viajan a la columna contacts.kind y
// forman parte de la clave de deduplicación: no cambian.
const (
	// KindPhoneE164 identifica al contacto por su número en formato E.164 normalizado: solo
	// dígitos, sin "+" ni separadores. Su valor literal es "phone_e164".
	KindPhoneE164 = "phone_e164"
	// KindWALID identifica al contacto por su LID de WhatsApp: la parte de usuario, numérica y
	// en forma canónica, sin el servidor "@lid". Su valor literal es "wa_lid".
	KindWALID = "wa_lid"
	// KindWAUsername identifica al contacto por su username de WhatsApp. Su valor literal es
	// "wa_username". Está PREPARADO y no ejercido: ValidateKind y Normalize lo aceptan, pero
	// hoy no es direccionable (Ref.Sendable lo rechaza con ErrNoDestino) hasta que WhatsApp
	// defina el formato de username.
	KindWAUsername = "wa_username"
)

// ErrInvalidRef es la base de todo error de referencia inválida: un kind desconocido o un value
// que no normaliza. Se inspecciona con errors.Is. Su texto es «contact_ref inválida» y es
// observable: las rutas de arranque de flujo devuelven al cliente HTTP, en un 400,
// «contact_ref inválida: » seguido del texto del error que reciben, así que el texto de TODO
// error que envuelve a este centinela (los de ValidateKind, Normalize y NewRef) es contrato y
// no cambia (R-10).
var ErrInvalidRef = errors.New("contact_ref inválida")

// Ref es una referencia a un contacto: el par (Kind, Value), donde Value ya está NORMALIZADO.
// La unidad de deduplicación es (tenant, Kind, Value): el mismo Value con otro Kind es otro
// contacto. Ref es comparable (sirve de clave de mapa), y su valor cero no es una referencia
// válida. Se construye siempre con NewRef, que garantiza la normalización.
type Ref struct {
	Kind  string
	Value string
}

// ValidateKind comprueba que kind es uno de los tres soportados: KindPhoneE164, KindWALID o
// KindWAUsername. Devuelve nil si lo es.
//
// Si no, devuelve un error que envuelve ErrInvalidRef, con el texto exacto
// «contact_ref inválida: kind desconocido "<kind>"» (R-01). La comparación es exacta y sensible
// a mayúsculas: "", "phone", "lid", "email" y "PHONE_E164" son desconocidos.
func ValidateKind(kind string) error {
	panic(pendiente.Implementar("contact.ValidateKind"))
}

// Normalize valida el kind y devuelve el value normalizado: el que se guarda, se deduplica y se
// indexa. Las reglas dependen del kind:
//   - phone_e164 (R-02, R-03, R-04): conserva SOLO los dígitos ASCII 0-9 y descarta cualquier
//     otro carácter ("+", espacios, guiones, paréntesis, puntos, letras). No recorta ceros a la
//     izquierda. Dos formatos del mismo número dan el mismo value. Es inválido si no queda
//     ningún dígito o si quedan más de 15, el máximo de E.164 sin contar el "+".
//   - wa_lid (R-05, R-06): la parte de usuario del LID en forma canónica. Descarta los blancos
//     de borde (también los que queden pegados al sufijo descartado) y todo lo que siga al
//     primero de "@" (servidor), "_" (agente) o ":" (dispositivo), así que "123_1:2@lid",
//     "123:2@lid", "123@lid" y "  123  " dan "123". Lo que queda tiene que ser no vacío y
//     numérico (solo dígitos ASCII).
//   - wa_username (R-07): en minúsculas y sin blancos de borde. Es inválido si queda vacío. No
//     se le quita ningún sufijo: "_" y "." son parte del nombre.
//
// Con un kind desconocido (R-08) devuelve el mismo error que ValidateKind. Todo error envuelve
// ErrInvalidRef, se devuelve con el value "" y tiene un texto exacto que no cambia:
//   - «contact_ref inválida: kind desconocido "<kind>"».
//   - «contact_ref inválida: phone_e164 sin dígitos».
//   - «contact_ref inválida: phone_e164 con <n> dígitos excede el máximo 15», con <n> la
//     cantidad de dígitos que quedaron.
//   - «contact_ref inválida: wa_lid vacío».
//   - «contact_ref inválida: wa_lid con parte de usuario no numérica (longitud <n>)», con <n> la
//     longitud en bytes de la parte de usuario.
//   - «contact_ref inválida: wa_username vacío: "<blancos>"», con el value recibido entre
//     comillas (%q).
//
// Higiene de logs (R-11, vale a medias): estos errores suben tal cual a los logs y al cliente, así
// que los de phone_e164 y wa_lid NUNCA contienen el value recibido (puede ser un número de
// teléfono, PII): describen la causa con una cuenta o una longitud. La única excepción es el de
// wa_username vacío, que incluye el value entre comillas porque por construcción son solo
// blancos y no es PII.
//
// La salida es la base de tres índices ciegos: contacts.value_bidx, fleet_sessions.self_pn_bidx
// y la comprobación anti-self-loop del runtime. Cambiar UN byte de lo que devuelve parte contactos
// y rompe esas comparaciones sin dar un solo error: no se "mejora" sin migrar esos índices.
func Normalize(kind, value string) (string, error) {
	panic(pendiente.Implementar("contact.Normalize"))
}

// NewRef construye la Ref de (kind, value): el kind recibido tal cual y el value que devuelve
// Normalize. Es el ÚNICO constructor recomendado (R-09): una Ref armada a mano ni se valida ni se
// normaliza, y el Resolver no la re-normaliza, porque confía en que viene de aquí.
//
// Si Normalize falla, devuelve la Ref cero y ese mismo error, con el mismo texto.
func NewRef(kind, value string) (Ref, error) {
	panic(pendiente.Implementar("contact.NewRef"))
}
