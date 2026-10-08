// Porta internal/integrations/crud.go @ 36d5a04

package integrations

import "github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"

// crud.go lleva lo ÚNICO puro que la superficie HTTP del CRUD (Plan 042 · T5.1)
// necesita y el Store no tenía: enseñar que hay un secreto sin enseñarlo. El método
// que lee el secreto de la base y devuelve su huella, (*Postgres).SecretFingerprint,
// vivía aquí en el fichero viejo (crud.go:42) y nace en postgres.go (D-F6-6).

// FingerprintHexLen son los hex de SHA-256 que se publican como huella. Ocho, tal
// como los fijó el design (D-042.7): bastan para que el dueño compare de un
// vistazo la huella de la consola con la del secreto que su puente tiene
// configurado, que es para lo único que existe.
const FingerprintHexLen = 8

// Fingerprint devuelve la huella corta de un secreto de firma: los primeros
// FingerprintHexLen hex (en minúsculas) de su SHA-256 (D-042.7 / REQ-13). Cadena
// vacía si no hay secreto — «no hay» no se disfraza de huella.
//
// Es DIAGNÓSTICO, no una credencial: sirve para responder «¿es este el secreto
// que creo?» sin que el valor viaje. Son 32 bits, y esa parcialidad es
// deliberada: una huella completa sería un oráculo offline contra un secreto
// pobre. El truncado no lo arregla del todo —por eso la API exige una longitud
// mínima al fijarlo—, pero deja la comparación útil y la reconstrucción, no.
//
// El secreto se toma byte a byte, sin recortar ni normalizar: un espacio de más
// es otro secreto y da otra huella. Es una función pura: el mismo secreto da
// siempre la misma huella.
func Fingerprint(secret string) string {
	panic(pendiente.Implementar("integrations.Fingerprint"))
}
