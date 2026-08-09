package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

// crud.go añade lo ÚNICO que la superficie HTTP del CRUD (Plan 042 · T5.1)
// necesita y el Store no tenía: enseñar que hay un secreto sin enseñarlo.

// FingerprintHexLen son los hex de SHA-256 que se publican como huella. Ocho, tal
// como los fijó el design (D-042.7): bastan para que el dueño compare de un
// vistazo la huella de la consola con la del secreto que su puente tiene
// configurado, que es para lo único que existe.
const FingerprintHexLen = 8

// Fingerprint devuelve la huella corta de un secreto de firma: los primeros
// FingerprintHexLen hex de su SHA-256 (D-042.7 / REQ-13). Cadena vacía si no hay
// secreto — «no hay» no se disfraza de huella.
//
// Es DIAGNÓSTICO, no una credencial: sirve para responder «¿es este el secreto
// que creo?» sin que el valor viaje. Son 32 bits, y esa parcialidad es
// deliberada: una huella completa sería un oráculo offline contra un secreto
// pobre. El truncado no lo arregla del todo —por eso la API exige una longitud
// mínima al fijarlo—, pero deja la comparación útil y la reconstrucción, no.
func Fingerprint(secret string) string {
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])[:FingerprintHexLen]
}

// SecretFingerprint descifra el secreto del tenant y devuelve SOLO su huella.
// found es false si el tenant no tiene fila o no tiene secreto.
//
// Vive aquí y no en el handler A PROPÓSITO: así el secreto EN CLARO no cruza la
// frontera de este paquete. La capa HTTP —la que serializa respuestas y escribe
// logs, o sea la que puede filtrarlo— recibe ocho caracteres hex y nunca tiene el
// valor en una variable que pueda acabar en un %v.
func (p *Postgres) SecretFingerprint(ctx context.Context, tenantID string) (string, bool, error) {
	secret, found, err := p.GetTenantSecret(ctx, tenantID)
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, nil
	}
	return Fingerprint(secret), true, nil
}
