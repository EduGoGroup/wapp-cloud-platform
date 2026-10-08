// Porta internal/integrations/sigv1/sigv1.go @ 3a21138

// Package sigv1 implementa la firma HMAC-SHA256 de las entregas salientes del
// puente CRM (Plan 042, D-042.5). Es un subpaquete deliberadamente sin estado ni
// dependencia de Postgres/HTTP: solo cadena canónica + HMAC, para que el worker
// (que sí hace red) y un futuro verificador de pruebas puedan compartirlo sin
// arrastrar nada más.
//
// Tampoco tiene reloj ni ventana temporal: el timestamp es un PARÁMETRO que entra
// en la cadena canónica y nada más. La ventana de ±300 s contra el replay la aplica
// quien llama (la cara HTTP del callback del CRM), no este paquete: aquí una firma
// de hace un año con su timestamp de hace un año verifica.
package sigv1

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
)

// Sign calcula la firma de un body con el secreto del tenant y el timestamp Unix
// (segundos) de la entrega: HMAC-SHA256, con el secreto como clave, de la cadena
// canónica "v1:<timestamp>:<body crudo>", devuelto en hexadecimal en MINÚSCULA (64
// caracteres). Es el mismo esquema que D-042.5 exige para el callback de vuelta
// (intake.status), así que un puente firma y verifica con el MISMO código.
//
// El timestamp va en decimal, con su signo si es negativo; el body va crudo, byte a
// byte, sin normalizar ni exigir UTF-8, y puede estar vacío o contener ':' y hasta
// "v1:". Es determinista y no falla: un secreto vacío también firma. No mira ningún
// reloj: el timestamp es el que le dan.
func Sign(secret string, timestampUnix int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(canonical(timestampUnix, body))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify recomputa la firma y la compara en tiempo constante (obligatorio para
// no filtrar el secreto por temporización — D-042.5, manual del autor de
// puentes). Devuelve true solo si signatureHex es la firma de ese secreto, ese
// timestamp y ese body; cambia uno de los tres y es false.
//
// signatureHex es el hex A SECAS, sin el prefijo "v1=" de la cabecera: quien llama
// se lo quita antes. Se comparan los BYTES decodificados, así que el hex en
// mayúsculas o mezclado verifica igual que en minúscula. Devuelve false ante
// cualquier firma mal formada, nunca panica: vacía, con caracteres que no son hex
// (el propio "v1=", espacios, un salto de línea), de longitud impar, truncada o con
// bytes de más.
//
// No aplica ventana temporal: timestampUnix solo entra en la cadena canónica. El
// ±300 s es de quien llama.
func Verify(secret string, timestampUnix int64, body []byte, signatureHex string) bool {
	want := Sign(secret, timestampUnix, body)
	got, err := hex.DecodeString(signatureHex)
	if err != nil {
		return false
	}
	wantBytes, err := hex.DecodeString(want)
	if err != nil {
		return false // defensivo: Sign siempre produce hex válido
	}
	return subtle.ConstantTimeCompare(got, wantBytes) == 1
}

// canonical arma "v1:<timestamp>:<body>" como bytes, sin asignaciones de más de
// las necesarias (el body puede ser grande).
func canonical(timestampUnix int64, body []byte) []byte {
	prefix := fmt.Sprintf("v1:%d:", timestampUnix)
	out := make([]byte, 0, len(prefix)+len(body))
	out = append(out, prefix...)
	out = append(out, body...)
	return out
}

// SignatureHeader formatea el valor del header X-Wapp-Signature (D-042.5):
// "v1=<hex>". Separado de Sign porque el header lleva el prefijo de versión del
// ESQUEMA de firma, no solo el hex. No valida ni normaliza lo que recibe: antepone
// "v1=" y nada más.
func SignatureHeader(signatureHex string) string {
	return "v1=" + signatureHex
}
