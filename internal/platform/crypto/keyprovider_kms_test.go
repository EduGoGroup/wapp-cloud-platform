package crypto

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// fakeKMS es un doble del KMS: guarda pares (ciphertext → plaintext) y falla
// claro con cualquier otro ciphertext o con otra llave, igual que el KMS real
// ante un blob que no le pertenece.
type fakeKMS struct {
	keyName  string
	claros   map[string][]byte // base64(ciphertext) → plaintext
	llamadas int
	err      error // si != nil, todo Decrypt falla (KMS caído / sin permisos)
}

func (f *fakeKMS) Decrypt(_ context.Context, keyName string, ciphertext []byte) ([]byte, error) {
	f.llamadas++
	if f.err != nil {
		return nil, f.err
	}
	if keyName != f.keyName {
		return nil, errors.New("permission denied: la llave no es esta")
	}
	pt, ok := f.claros[base64.StdEncoding.EncodeToString(ciphertext)]
	if !ok {
		return nil, errors.New("decryption failed: el ciphertext no es de esta llave")
	}
	return pt, nil
}

// wrapFake registra el "cifrado" de plaintext bajo un ciphertext sintético y
// devuelve ese ciphertext en base64 (lo que iría en WAPP_KEK_KMS_KEYRING).
func (f *fakeKMS) wrapFake(etiqueta string, plaintext []byte) string {
	ct := []byte("kms-ct:" + etiqueta)
	b64 := base64.StdEncoding.EncodeToString(ct)
	if f.claros == nil {
		f.claros = map[string][]byte{}
	}
	f.claros[b64] = plaintext
	return b64
}

const llaveKMSDePrueba = "projects/wapp/locations/us/keyRings/wapp/cryptoKeys/kek"

func kekDe(fill byte) []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = fill
	}
	return b
}

// nuevoFakeConKeyring arma un fake con dos KEK ("1" current y "0" retirada) y
// devuelve la especificación de keyring cifrado lista para KMSConfig.
func nuevoFakeConKeyring(t *testing.T) (*fakeKMS, string) {
	t.Helper()
	f := &fakeKMS{keyName: llaveKMSDePrueba}
	return f, "1:" + f.wrapFake("kek1", kekDe(0x11)) + ",0:" + f.wrapFake("kek0", kekDe(0x22))
}

func indexB64DePrueba() string { return base64.StdEncoding.EncodeToString(kekDe(0x44)) }

// TestKMSProvider_DesenvuelveElKeyringYNoVuelveALlamar es el corazón del T9.1: el
// KMS interviene UNA vez por KEK al arrancar y nunca más — envolver/desenvolver
// DEK es AES local. Es lo que sostiene la regla de costos del ADR-0036 §3.
func TestKMSProvider_DesenvuelveElKeyringYNoVuelveALlamar(t *testing.T) {
	f, keyring := nuevoFakeConKeyring(t)

	kp, err := newKMSKeyProvider(context.Background(), KMSConfig{
		KeyName:    llaveKMSDePrueba,
		KeyringB64: keyring,
		CurrentID:  "1",
		IndexB64:   indexB64DePrueba(),
		Prod:       true,
	}, f)
	if err != nil {
		t.Fatalf("newKMSKeyProvider: %v", err)
	}
	if f.llamadas != 2 {
		t.Fatalf("llamadas al KMS en el arranque = %d, want 2 (una por KEK del keyring)", f.llamadas)
	}
	if kp.CurrentKeyID() != "1" {
		t.Fatalf("CurrentKeyID = %q, want 1", kp.CurrentKeyID())
	}

	antes := f.llamadas
	dek := kekDe(0x77)
	wrapped, keyID, err := kp.WrapDEK(dek)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}
	if keyID != "1" {
		t.Fatalf("WrapDEK keyID = %q, want 1", keyID)
	}
	got, err := kp.UnwrapDEK(wrapped, keyID)
	if err != nil {
		t.Fatalf("UnwrapDEK: %v", err)
	}
	if string(got) != string(dek) {
		t.Fatal("UnwrapDEK no devolvió la DEK original")
	}
	kp.BlindIndex("tenant", "573001110001")
	if f.llamadas != antes {
		t.Fatalf("el KMS se llamó %d veces EN CALIENTE (want 0): wrap/unwrap/blind-index deben ser locales",
			f.llamadas-antes)
	}
}

// TestKMSProvider_ReWrapRotaIgual: la rotación del Plan 012 funciona idéntica con
// el provider KMS — desenvolver con la KEK vieja, envolver con la current, sin
// hablar con el KMS (REQ-17).
func TestKMSProvider_ReWrapRotaIgual(t *testing.T) {
	f, keyring := nuevoFakeConKeyring(t)
	cfg := KMSConfig{KeyName: llaveKMSDePrueba, KeyringB64: keyring, IndexB64: indexB64DePrueba(), Prod: true}

	// Con "0" como current, envuelve una DEK; luego con "1" como current, la rota.
	cfg.CurrentID = "0"
	kpViejo, err := newKMSKeyProvider(context.Background(), cfg, f)
	if err != nil {
		t.Fatalf("provider con current=0: %v", err)
	}
	dek := kekDe(0x55)
	wrapped, viejoID, err := kpViejo.WrapDEK(dek)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}

	cfg.CurrentID = "1"
	kpNuevo, err := newKMSKeyProvider(context.Background(), cfg, f)
	if err != nil {
		t.Fatalf("provider con current=1: %v", err)
	}
	antes := f.llamadas
	nuevoWrapped, nuevoID, err := NewFieldCipher(kpNuevo).ReWrap(wrapped, viejoID)
	if err != nil {
		t.Fatalf("ReWrap: %v", err)
	}
	if nuevoID != "1" {
		t.Fatalf("ReWrap keyID = %q, want 1", nuevoID)
	}
	if f.llamadas != antes {
		t.Fatalf("ReWrap llamó al KMS %d veces, want 0", f.llamadas-antes)
	}
	got, err := kpNuevo.UnwrapDEK(nuevoWrapped, nuevoID)
	if err != nil {
		t.Fatalf("UnwrapDEK tras ReWrap: %v", err)
	}
	if string(got) != string(dek) {
		t.Fatal("la DEK cambió al rotar")
	}
}

// TestKMSProvider_FallaClaroYNuncaCaeAlFallback recorre las formas de tener el KMS
// mal configurado. Ninguna puede devolver un provider: un fallback silencioso
// dejaría la KEK en claro sirviendo producción sin que nadie se entere.
func TestKMSProvider_FallaClaroYNuncaCaeAlFallback(t *testing.T) {
	fOK, keyringOK := nuevoFakeConKeyring(t)
	fCaido := &fakeKMS{keyName: llaveKMSDePrueba, err: errors.New("rpc error: PermissionDenied")}
	// Keyring cifrado por OTRA llave: fOK no conoce estos ciphertexts.
	fAjeno := &fakeKMS{keyName: llaveKMSDePrueba}
	keyringAjeno := "1:" + fAjeno.wrapFake("cifrado-por-otra-llave", kekDe(0x33))

	casos := []struct {
		nombre   string
		cfg      KMSConfig
		dec      KMSDecrypter
		quiero   error  // errors.Is; nil = solo se exige error
		contiene string // fragmento esperado del mensaje
	}{
		{
			nombre: "sin nombre de llave",
			cfg:    KMSConfig{KeyringB64: keyringOK, CurrentID: "1"},
			dec:    fOK,
			quiero: ErrKMSKeyNameMissing,
		},
		{
			nombre: "sin keyring",
			cfg:    KMSConfig{KeyName: llaveKMSDePrueba, CurrentID: "1"},
			dec:    fOK,
			quiero: ErrKMSKeyringMissing,
		},
		{
			nombre: "sin current",
			cfg:    KMSConfig{KeyName: llaveKMSDePrueba, KeyringB64: keyringOK},
			dec:    fOK,
			quiero: ErrCurrentKeyMissing,
		},
		{
			nombre: "current ausente del keyring",
			cfg:    KMSConfig{KeyName: llaveKMSDePrueba, KeyringB64: keyringOK, CurrentID: "99"},
			dec:    fOK,
			quiero: ErrCurrentKeyMissing,
		},
		{
			nombre: "KMS sin permisos",
			cfg:    KMSConfig{KeyName: llaveKMSDePrueba, KeyringB64: keyringOK, CurrentID: "1", IndexB64: indexB64DePrueba()},
			dec:    fCaido,
			quiero: ErrKMSDecrypt,
		},
		{
			nombre: "llave equivocada",
			cfg:    KMSConfig{KeyName: "projects/otro/locations/us/keyRings/x/cryptoKeys/y", KeyringB64: keyringOK, CurrentID: "1", IndexB64: indexB64DePrueba()},
			dec:    fOK,
			quiero: ErrKMSDecrypt,
		},
		{
			nombre: "ciphertext de otra llave",
			cfg:    KMSConfig{KeyName: llaveKMSDePrueba, KeyringB64: keyringAjeno, CurrentID: "1", IndexB64: indexB64DePrueba()},
			dec:    fOK,
			quiero: ErrKMSDecrypt,
		},
		{
			nombre:   "keyring que no es base64",
			cfg:      KMSConfig{KeyName: llaveKMSDePrueba, KeyringB64: "1:no-es-base64!!", CurrentID: "1"},
			dec:      fOK,
			contiene: "base64 inválido",
		},
		{
			nombre: "prod sin indexKey explícita",
			cfg:    KMSConfig{KeyName: llaveKMSDePrueba, KeyringB64: keyringOK, CurrentID: "1", Prod: true},
			dec:    fOK,
			quiero: ErrIndexKeyRequiredInProd,
		},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			kp, err := newKMSKeyProvider(context.Background(), c.cfg, c.dec)
			if err == nil {
				t.Fatalf("esperaba error, got provider %v (JAMÁS debe construirse un provider con el KMS mal configurado)", kp)
			}
			if kp != nil {
				t.Fatalf("con error, el provider debe ser nil (nunca un fallback): %v", kp)
			}
			if c.quiero != nil && !errors.Is(err, c.quiero) {
				t.Fatalf("error = %v, want errors.Is(_, %v)", err, c.quiero)
			}
			if c.contiene != "" && !strings.Contains(err.Error(), c.contiene) {
				t.Fatalf("error = %v, want que contenga %q", err, c.contiene)
			}
		})
	}
}

// TestKMSProvider_KEKDeTamañoInvalido: si el KMS devuelve algo que no es una
// AES-256, se falla en el arranque en vez de cifrar con material corrupto.
func TestKMSProvider_KEKDeTamañoInvalido(t *testing.T) {
	f := &fakeKMS{keyName: llaveKMSDePrueba}
	keyring := "1:" + f.wrapFake("corta", []byte("demasiado corta"))

	_, err := newKMSKeyProvider(context.Background(), KMSConfig{
		KeyName:    llaveKMSDePrueba,
		KeyringB64: keyring,
		CurrentID:  "1",
		IndexB64:   indexB64DePrueba(),
	}, f)
	if !errors.Is(err, ErrKEKSize) {
		t.Fatalf("error = %v, want errors.Is(_, ErrKEKSize)", err)
	}
}

// TestKMSProvider_IndexKeyCifradaPorElKMS: con la indexKey también custodiada, no
// queda ningún secreto en claro en el entorno y el índice ciego sigue saliendo
// igual que con la indexKey explícita (mismo material ⇒ mismo bidx).
func TestKMSProvider_IndexKeyCifradaPorElKMS(t *testing.T) {
	f, keyring := nuevoFakeConKeyring(t)
	idxCT := f.wrapFake("idx", kekDe(0x44))

	kpCifrada, err := newKMSKeyProvider(context.Background(), KMSConfig{
		KeyName:            llaveKMSDePrueba,
		KeyringB64:         keyring,
		CurrentID:          "1",
		IndexCiphertextB64: idxCT,
		Prod:               true,
	}, f)
	if err != nil {
		t.Fatalf("newKMSKeyProvider con indexKey cifrada: %v", err)
	}

	kpClara, err := newKMSKeyProvider(context.Background(), KMSConfig{
		KeyName:    llaveKMSDePrueba,
		KeyringB64: keyring,
		CurrentID:  "1",
		IndexB64:   indexB64DePrueba(),
		Prod:       true,
	}, f)
	if err != nil {
		t.Fatalf("newKMSKeyProvider con indexKey en claro: %v", err)
	}

	if a, b := kpCifrada.BlindIndex("t", "v"), kpClara.BlindIndex("t", "v"); a != b {
		t.Fatalf("el índice ciego cambió según de dónde venga la indexKey: %q vs %q (rompería la PK de contacts)", a, b)
	}
}

// TestNewKeyProvider_Seleccion cubre el selector: default y "env" dan el
// comportamiento de siempre (regresión cero), un valor desconocido NO cae al
// default, y "kms" mal configurado falla sin construir nada.
func TestNewKeyProvider_Seleccion(t *testing.T) {
	envCfg := KeyringConfig{MasterB64: base64.StdEncoding.EncodeToString(kekDe(0x11)), IndexB64: indexB64DePrueba()}

	t.Run("default vacío = env", func(t *testing.T) {
		kp, err := NewKeyProvider(context.Background(), ProviderConfig{Env: envCfg})
		if err != nil {
			t.Fatalf("NewKeyProvider: %v", err)
		}
		if kp.CurrentKeyID() != compatKeyID {
			t.Fatalf("CurrentKeyID = %q, want %q (camino compat idéntico al de antes)", kp.CurrentKeyID(), compatKeyID)
		}
	})

	t.Run("env explícito", func(t *testing.T) {
		kp, err := NewKeyProvider(context.Background(), ProviderConfig{Provider: "  ENV  ", Env: envCfg})
		if err != nil {
			t.Fatalf("NewKeyProvider: %v", err)
		}
		if kp.CurrentKeyID() != compatKeyID {
			t.Fatalf("CurrentKeyID = %q, want %q", kp.CurrentKeyID(), compatKeyID)
		}
	})

	t.Run("provider desconocido no cae al default", func(t *testing.T) {
		kp, err := NewKeyProvider(context.Background(), ProviderConfig{Provider: "gcp", Env: envCfg})
		if !errors.Is(err, ErrUnknownKeyProvider) {
			t.Fatalf("error = %v, want errors.Is(_, ErrUnknownKeyProvider)", err)
		}
		if kp != nil {
			t.Fatalf("un provider desconocido NO debe devolver un KeyProvider: %v", kp)
		}
	})

	t.Run("kms sin configurar falla y no usa el material env", func(t *testing.T) {
		kp, err := NewKeyProvider(context.Background(), ProviderConfig{Provider: ProviderKMS, Env: envCfg, Prod: true})
		if err == nil {
			t.Fatal("esperaba error con provider=kms sin WAPP_KEK_KMS_KEY")
		}
		if kp != nil {
			t.Fatalf("con el KMS mal configurado NO puede haber provider (sería el fallback silencioso): %v", kp)
		}
		if !errors.Is(err, ErrKMSKeyNameMissing) {
			t.Fatalf("error = %v, want errors.Is(_, ErrKMSKeyNameMissing)", err)
		}
	})
}
