package crypto

import "testing"

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
		enc, dek, err := c.Encrypt(pt)
		if err != nil {
			t.Fatalf("Encrypt(%q): %v", pt, err)
		}
		if len(dek) == 0 || len(enc) == 0 {
			t.Fatalf("Encrypt(%q): salida vacía enc=%d dek=%d", pt, len(enc), len(dek))
		}
		got, err := c.Decrypt(enc, dek)
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
	enc1, dek1, err := c.Encrypt("mismo-valor")
	if err != nil {
		t.Fatalf("Encrypt 1: %v", err)
	}
	enc2, dek2, err := c.Encrypt("mismo-valor")
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
	enc, dek, err := c.Encrypt("secreto")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	enc[len(enc)-1] ^= 0xFF
	if _, err := c.Decrypt(enc, dek); err == nil {
		t.Fatalf("esperaba error al descifrar un ciphertext manipulado")
	}
}

func TestFieldCipher_TamperedDEK(t *testing.T) {
	c := newCipher(t)
	enc, dek, err := c.Encrypt("secreto")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	dek[len(dek)-1] ^= 0xFF
	if _, err := c.Decrypt(enc, dek); err == nil {
		t.Fatalf("esperaba error al descifrar con la DEK envuelta manipulada")
	}
}

func TestFieldCipher_WrongKEK(t *testing.T) {
	c1 := newCipher(t)
	kp2, err := NewEnvKeyProvider(keyB64(32, 0x77), "")
	if err != nil {
		t.Fatalf("kp2: %v", err)
	}
	c2 := NewFieldCipher(kp2)

	enc, dek, err := c1.Encrypt("secreto")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// c2 no puede desenvolver la DEK envuelta por otra KEK.
	if _, err := c2.Decrypt(enc, dek); err == nil {
		t.Fatalf("esperaba error al descifrar con otra KEK")
	}
}

func strLong(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return string(b)
}
