//go:build pendiente

package integrations_test

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
)

// Aserción de compilación de lo que crud.go promete: una función pura de cadena a cadena.
var _ func(string) string = integrations.Fingerprint

// TestFingerprintHexLen_IsEight: la huella publicada son ocho hex (D-042.7).
func TestFingerprintHexLen_IsEight(t *testing.T) {
	if integrations.FingerprintHexLen != 8 {
		t.Errorf("FingerprintHexLen = %d, quería 8", integrations.FingerprintHexLen)
	}
}

// TestFingerprint_KnownVectors: la huella son los primeros ocho hex, en minúsculas, del SHA-256
// del secreto. Los vectores están calculados FUERA (`printf '%s' … | shasum -a 256`); los dos
// primeros son los del test de integración viejo del CRUD.
func TestFingerprint_KnownVectors(t *testing.T) {
	cases := []struct {
		name   string
		secret string
		want   string
	}{
		{"crud secret", "secreto-de-firma-del-puente-jjx-2026", "e5c47775"},
		{"rotated secret", "secreto-ROTADO-del-puente-jjx-2026", "b7d7294f"},
		{"one letter", "a", "ca978112"},
		{"non ascii", "contraseña-ñandú", "9384edfd"},
		{"only a space is a secret too", " ", "36a9e7f1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := integrations.Fingerprint(c.secret)
			if got != c.want {
				t.Errorf("Fingerprint = %q, quería %q", got, c.want)
			}
			if len(got) != integrations.FingerprintHexLen {
				t.Errorf("la huella mide %d, quería FingerprintHexLen (%d)", len(got), integrations.FingerprintHexLen)
			}
			if got != strings.ToLower(got) {
				t.Errorf("la huella %q no va en minúsculas", got)
			}
		})
	}
}

// TestFingerprint_NoSecret_NoFingerprint: sin secreto no hay huella; «no hay» no se disfraza del
// hash de la cadena vacía (e3b0c442).
func TestFingerprint_NoSecret_NoFingerprint(t *testing.T) {
	if got := integrations.Fingerprint(""); got != "" {
		t.Errorf("Fingerprint(\"\") = %q, quería la cadena vacía", got)
	}
}

// TestFingerprint_TakesTheSecretVerbatim: el secreto no se recorta ni se normaliza: un espacio de
// más, otra caja u otro orden son otro secreto y dan otra huella. Y es pura: el mismo secreto da
// siempre la misma.
func TestFingerprint_TakesTheSecretVerbatim(t *testing.T) {
	base := integrations.Fingerprint("s3cr3t-del-puente")
	if again := integrations.Fingerprint("s3cr3t-del-puente"); again != base {
		t.Errorf("el mismo secreto dio dos huellas: %q y %q", base, again)
	}
	for _, other := range []string{"s3cr3t-del-puente ", " s3cr3t-del-puente", "S3CR3T-DEL-PUENTE", "s3cr3t-del-puente\n"} {
		if got := integrations.Fingerprint(other); got == base {
			t.Errorf("Fingerprint(%q) = %q, igual que la del secreto sin tocar: lo está normalizando", other, got)
		}
	}
}

// TestFingerprint_NeverContainsTheSecret: la huella no es el secreto ni un trozo suyo, aunque el
// secreto sea corto y hexadecimal.
func TestFingerprint_NeverContainsTheSecret(t *testing.T) {
	for _, secret := range []string{"abc", "deadbeef", "0123456789abcdef"} {
		got := integrations.Fingerprint(secret)
		if got == secret || strings.Contains(got, secret) || strings.HasPrefix(secret, got) {
			t.Errorf("Fingerprint(%q) = %q: deja ver el secreto", secret, got)
		}
	}
}
