package crypto

import (
	"bytes"
	"testing"
)

func newCipher(t *testing.T) *FieldCipher {
	t.Helper()
	return NewFieldCipher(newProvider(t))
}

func TestFieldCipher_RoundTrip(t *testing.T) {
	c := newCipher(t)
	cases := []string{
		"+573001112233",
		"",
		"unicode: áéíóú ñ 漢字 🔐",
		"con\nsaltos\ty tabs",
		strLong(4096),
	}
	for _, pt := range cases {
		enc, dek, keyID, err := c.Encrypt(pt)
		if err != nil {
			t.Fatalf("Encrypt(%q): %v", pt, err)
		}
		if len(dek) == 0 || len(enc) == 0 {
			t.Fatalf("Encrypt(%q): salida vacía enc=%d dek=%d", pt, len(enc), len(dek))
		}
		if keyID != compatKeyID {
			t.Fatalf("Encrypt(%q): key_id = %q, quería la current %q", pt, keyID, compatKeyID)
		}
		got, err := c.Decrypt(enc, dek, keyID)
		if err != nil {
			t.Fatalf("Decrypt(%q): %v", pt, err)
		}
		if got != pt {
			t.Fatalf("round-trip: got %q want %q", got, pt)
		}
	}
}

func TestFieldCipher_FreshDEKPerCall(t *testing.T) {
	c := newCipher(t)
	enc1, dek1, _, err := c.Encrypt("mismo-valor")
	if err != nil {
		t.Fatalf("Encrypt 1: %v", err)
	}
	enc2, dek2, _, err := c.Encrypt("mismo-valor")
	if err != nil {
		t.Fatalf("Encrypt 2: %v", err)
	}
	// Nonce fresco + DEK fresca ⇒ blobs distintos aunque el plaintext sea igual.
	if string(enc1) == string(enc2) {
		t.Fatalf("value_enc idéntico entre llamadas (no-determinismo esperado)")
	}
	if string(dek1) == string(dek2) {
		t.Fatalf("value_dek idéntico entre llamadas (DEK debe ser fresca por-valor)")
	}
}

func TestFieldCipher_TamperedCiphertext(t *testing.T) {
	c := newCipher(t)
	enc, dek, keyID, err := c.Encrypt("secreto")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	enc[len(enc)-1] ^= 0xFF
	if _, err := c.Decrypt(enc, dek, keyID); err == nil {
		t.Fatalf("esperaba error al descifrar un ciphertext manipulado")
	}
}

func TestFieldCipher_TamperedDEK(t *testing.T) {
	c := newCipher(t)
	enc, dek, keyID, err := c.Encrypt("secreto")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	dek[len(dek)-1] ^= 0xFF
	if _, err := c.Decrypt(enc, dek, keyID); err == nil {
		t.Fatalf("esperaba error al descifrar con la DEK envuelta manipulada")
	}
}

func TestFieldCipher_WrongKEK(t *testing.T) {
	c1 := newCipher(t)
	kp2, err := NewEnvKeyProvider(KeyringConfig{MasterB64: keyB64(32, 0x77), IndexB64: indexB64()})
	if err != nil {
		t.Fatalf("kp2: %v", err)
	}
	c2 := NewFieldCipher(kp2)

	enc, dek, keyID, err := c1.Encrypt("secreto")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// c2 tiene el mismo key_id "1" pero otra KEK: no puede desenvolver la DEK.
	if _, err := c2.Decrypt(enc, dek, keyID); err == nil {
		t.Fatalf("esperaba error al descifrar con otra KEK")
	}
}

// TestFieldCipher_ReWrap es el núcleo del Plan 012: re-envolver la DEK del key_id
// viejo hacia la current SIN tocar value_enc, y que el dato siga descifrando.
func TestFieldCipher_ReWrap(t *testing.T) {
	const pt = "+573001112233"

	// t0: KEK A es la current (cifra el contacto).
	kpA, err := NewEnvKeyProvider(KeyringConfig{KeyringB64: "A:" + keyB64(32, 0x11), CurrentID: "A", IndexB64: indexB64()})
	if err != nil {
		t.Fatalf("kpA: %v", err)
	}
	cA := NewFieldCipher(kpA)
	enc, dekA, idA, err := cA.Encrypt(pt)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if idA != "A" {
		t.Fatalf("Encrypt key_id = %q, quería A", idA)
	}
	encBefore := append([]byte(nil), enc...) // copia para el assert byte-a-byte.

	// t1: se añade KEK B como current; el cipher ve el keyring {A, B}.
	kpAB, err := NewEnvKeyProvider(KeyringConfig{KeyringB64: "A:" + keyB64(32, 0x11) + ",B:" + keyB64(32, 0x22), CurrentID: "B", IndexB64: indexB64()})
	if err != nil {
		t.Fatalf("kpAB: %v", err)
	}
	cAB := NewFieldCipher(kpAB)

	// ReWrap: unwrap con A → wrap con la current (B). NO toca value_enc.
	newDEK, newID, err := cAB.ReWrap(dekA, idA)
	if err != nil {
		t.Fatalf("ReWrap: %v", err)
	}
	if newID != "B" {
		t.Fatalf("ReWrap new key_id = %q, quería B (la current)", newID)
	}
	if !bytes.Equal(enc, encBefore) {
		t.Fatalf("ReWrap alteró value_enc (debe quedar intacto byte-a-byte)")
	}

	// El value_enc ORIGINAL abre con la DEK re-envuelta y la current → mismo plaintext.
	got, err := cAB.Decrypt(enc, newDEK, newID)
	if err != nil {
		t.Fatalf("Decrypt tras ReWrap: %v", err)
	}
	if got != pt {
		t.Fatalf("Decrypt tras ReWrap: got %q want %q", got, pt)
	}

	// La DEK vieja también sigue abriendo con su key_id A (coexistencia sin downtime).
	gotA, err := cAB.Decrypt(enc, dekA, "A")
	if err != nil {
		t.Fatalf("Decrypt con la DEK vieja (A): %v", err)
	}
	if gotA != pt {
		t.Fatalf("Decrypt(A): got %q want %q", gotA, pt)
	}
}

// TestFieldCipher_ReWrap_MissingOldKEK: ReWrap con un key_id ausente → error claro.
func TestFieldCipher_ReWrap_MissingOldKEK(t *testing.T) {
	c := newCipher(t)
	_, dek, _, err := c.Encrypt("x")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, _, err := c.ReWrap(dek, "no-existe"); err == nil {
		t.Fatalf("esperaba error de ReWrap con key_id ausente")
	}
}

func strLong(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return string(b)
}
