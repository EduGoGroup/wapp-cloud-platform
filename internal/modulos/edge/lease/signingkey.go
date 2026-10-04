// Porta internal/gateway/lease/signingkey.go @ 8896f13

package lease

import (
	"crypto/ed25519"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// KeySource describe de dónde salió la clave de firma resuelta, para que el
// llamador (T5) lo registre en log.
type KeySource string

const (
	// KeySourceFile indica que la clave se cargó de un archivo PEM PKCS#8.
	KeySourceFile KeySource = "file"
	// KeySourceBase64 indica que la clave se cargó de un valor base64 de config.
	KeySourceBase64 KeySource = "base64"
	// KeySourceGenerated indica que no había clave configurada y se generó una
	// efímera de desarrollo (NO usar en producción: cambia en cada arranque).
	KeySourceGenerated KeySource = "generated-dev"
)

// GenerateDevKey genera una clave Ed25519 efímera (dev/tests). No toca disco.
// Cada llamada devuelve una clave DISTINTA: no hay ninguna clave fija ni por
// defecto en el código.
func GenerateDevKey() (ed25519.PrivateKey, error) {
	panic(pendiente.Implementar("lease.GenerateDevKey"))
}

// ParsePrivateKeyBase64 decodifica una clave privada Ed25519 desde base64
// estándar. Acepta la semilla de 32 bytes (ed25519.SeedSize) o la clave
// expandida de 64 bytes (ed25519.PrivateKeySize). Un base64 que no decodifica
// da "lease: base64 de clave inválido: …"; cualquier otro tamaño (incluida la
// cadena vacía), "lease: tamaño de clave Ed25519 inesperado: N bytes".
func ParsePrivateKeyBase64(s string) (ed25519.PrivateKey, error) {
	panic(pendiente.Implementar("lease.ParsePrivateKeyBase64"))
}

// LoadPrivateKeyPEM carga una clave privada Ed25519 desde un archivo PEM PKCS#8.
// Errores: el fichero no se puede leer ("lease: leer clave PEM …"), no es PEM
// ("… no es PEM válido"), no es PKCS#8 ("lease: parsear clave PKCS#8: …") o es
// PKCS#8 de otro algoritmo ("lease: la clave PEM no es Ed25519").
func LoadPrivateKeyPEM(path string) (ed25519.PrivateKey, error) {
	panic(pendiente.Implementar("lease.LoadPrivateKeyPEM"))
}

// ResolveSigningKey resuelve la clave de firma del lease con precedencia
// archivo PEM > base64 > generación efímera de dev. Devuelve también de dónde
// salió, para que el arranque (T5) lo registre y, si fue generada, exponga la
// pública (PublicKeyBase64) para configurar el Edge.
//
// Promesas:
//   - con pemFile gana el fichero aunque también haya base64 (KeySourceFile);
//   - sin fichero y con base64, la de base64 (KeySourceBase64);
//   - sin ninguna de las dos, una EFÍMERA recién generada (KeySourceGenerated).
//     NUNCA una clave por defecto fija (reglas.md T-10): sin clave configurada,
//     la plataforma invalida a todos los Edge al reiniciar, y eso no se
//     «arregla» aquí;
//   - una clave configurada es ESTABLE entre llamadas (misma clave); la
//     generada no (cada llamada, otra);
//   - si la fuente elegida falla, devuelve el error CON su fuente, y no cae a
//     la siguiente: un fichero ilegible no degrada a base64 ni a efímera.
func ResolveSigningKey(pemFile, base64Key string) (ed25519.PrivateKey, KeySource, error) {
	panic(pendiente.Implementar("lease.ResolveSigningKey"))
}
