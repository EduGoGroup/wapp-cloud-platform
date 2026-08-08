// Package sigv1 implementa la firma HMAC-SHA256 de las entregas salientes del
// puente CRM (Plan 042, D-042.5). Es un subpaquete deliberadamente sin estado ni
// dependencia de Postgres/HTTP: solo cadena canónica + HMAC, para que el worker
// (que sí hace red) y un futuro verificador de pruebas puedan compartirlo sin
// arrastrar nada más.
package sigv1

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
)

// Sign calcula la firma hexadecimal de un body con el secreto del tenant y el
// timestamp Unix (segundos) de la entrega. Cadena canónica: "v1:<timestamp>:<body
// crudo>" — el mismo esquema que D-042.5 exige para el callback de vuelta
// (intake.status), así que un puente firma y verifica con el MISMO código.
func Sign(secret string, timestampUnix int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(canonical(timestampUnix, body))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify recomputa la firma y la compara en tiempo constante (obligatorio para
// no filtrar el secreto por temporización — D-042.5, manual del autor de
// puentes). Devuelve false ante cualquier firma mal formada, nunca panica.
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
// ESQUEMA de firma, no solo el hex.
func SignatureHeader(signatureHex string) string {
	return "v1=" + signatureHex
}
