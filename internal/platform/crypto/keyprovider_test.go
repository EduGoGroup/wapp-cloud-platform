package crypto

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/envelope"
)

// keyB64 codifica n bytes con el byte fill en base64 estándar (helper de tests).
func keyB64(n int, fill byte) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = fill
	}
	return base64.StdEncoding.EncodeToString(b)
}

// masterB64 devuelve una KEK maestra válida (32B) para tests.
func masterB64() string { return keyB64(envelope.DEKSize, 0x11) }

// indexB64 devuelve una indexKey explícita válida (32B) para tests.
func indexB64() string { return keyB64(indexKeySize, 0x44) }

// newProvider construye un KeyProvider por el camino compat (KEK maestra única),
// con indexKey explícita para no disparar el warning de derivación.
func newProvider(t *testing.T) KeyProvider {
	t.Helper()
	kp, err := NewEnvKeyProvider(KeyringConfig{MasterB64: masterB64(), IndexB64: indexB64()})
	if err != nil {
		t.Fatalf("NewEnvKeyProvider: %v", err)
	}
	return kp
}

func TestNewEnvKeyProvider_Errors(t *testing.T) {
	tests := []struct {
		name string
		cfg  KeyringConfig
	}{
		{"sin material de clave", KeyringConfig{}},
		{"master base64 inválido", KeyringConfig{MasterB64: "no-es-base64!!!"}},
		{"master tamaño incorrecto (16B)", KeyringConfig{MasterB64: keyB64(16, 0x22)}},
		{"master tamaño incorrecto (33B)", KeyringConfig{MasterB64: keyB64(33, 0x22)}},
		{"indexKey base64 inválido", KeyringConfig{MasterB64: masterB64(), IndexB64: "no-es-base64!!!"}},
		{"indexKey tamaño incorrecto (16B)", KeyringConfig{MasterB64: masterB64(), IndexB64: keyB64(16, 0x33)}},
		{"keyring sin current", KeyringConfig{KeyringB64: "A:" + masterB64(), IndexB64: indexB64()}},
		{"keyring current ausente", KeyringConfig{KeyringB64: "A:" + masterB64(), CurrentID: "B", IndexB64: indexB64()}},
		{"keyring entrada sin id", KeyringConfig{KeyringB64: masterB64(), CurrentID: "A", IndexB64: indexB64()}},
		{"keyring KEK tamaño incorrecto", KeyringConfig{KeyringB64: "A:" + keyB64(16, 0x22), CurrentID: "A", IndexB64: indexB64()}},
		{"keyring KEK duplicada", KeyringConfig{KeyringB64: "A:" + masterB64() + ",A:" + keyB64(32, 0x22), CurrentID: "A", IndexB64: indexB64()}},
		{"prod sin indexKey explícita", KeyringConfig{MasterB64: masterB64(), Prod: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewEnvKeyProvider(tc.cfg); err == nil {
				t.Fatalf("esperaba error, obtuve nil")
			}
		})
	}
}

func TestNewEnvKeyProvider_OK(t *testing.T) {
	oks := []struct {
		name string
		cfg  KeyringConfig
	}{
		{"compat master + index explícita", KeyringConfig{MasterB64: masterB64(), IndexB64: indexB64()}},
		{"compat master + index derivada (dev)", KeyringConfig{MasterB64: masterB64()}},
		{"keyring + current + index", KeyringConfig{KeyringB64: "A:" + masterB64() + ",B:" + keyB64(32, 0x22), CurrentID: "B", IndexB64: indexB64()}},
	}
	for _, tc := range oks {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewEnvKeyProvider(tc.cfg); err != nil {
				t.Fatalf("esperaba OK, obtuve error: %v", err)
			}
		})
	}
}

// TestCompat_KeyIDInicial: solo MASTER_B64 → key_id inicial "1" es la current.
func TestCompat_KeyIDInicial(t *testing.T) {
	kp, err := NewEnvKeyProvider(KeyringConfig{MasterB64: masterB64(), IndexB64: indexB64()})
	if err != nil {
		t.Fatalf("NewEnvKeyProvider: %v", err)
	}
	if kp.CurrentKeyID() != compatKeyID {
		t.Fatalf("compat: current key_id = %q, quería %q", kp.CurrentKeyID(), compatKeyID)
	}
	// Round-trip por el camino compat.
	dek := make([]byte, envelope.DEKSize)
	wrapped, id, err := kp.WrapDEK(dek)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}
	if id != compatKeyID {
		t.Fatalf("WrapDEK devolvió key_id %q, quería %q", id, compatKeyID)
	}
	got, err := kp.UnwrapDEK(wrapped, id)
	if err != nil {
		t.Fatalf("UnwrapDEK: %v", err)
	}
	if string(got) != string(dek) {
		t.Fatalf("round-trip DEK: got %x want %x", got, dek)
	}
}

// TestWrapDEK_UsesCurrent: WrapDEK envuelve con la current y devuelve su key_id.
func TestWrapDEK_UsesCurrent(t *testing.T) {
	kp, err := NewEnvKeyProvider(KeyringConfig{
		KeyringB64: "A:" + masterB64() + ",B:" + keyB64(32, 0x22),
		CurrentID:  "B",
		IndexB64:   indexB64(),
	})
	if err != nil {
		t.Fatalf("NewEnvKeyProvider: %v", err)
	}
	if kp.CurrentKeyID() != "B" {
		t.Fatalf("CurrentKeyID = %q, quería B", kp.CurrentKeyID())
	}
	dek := make([]byte, envelope.DEKSize)
	wrapped, id, err := kp.WrapDEK(dek)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}
	if id != "B" {
		t.Fatalf("WrapDEK key_id = %q, quería B (la current)", id)
	}
	// Se desenvuelve con B; con A (que existe pero no envolvió) debe fallar.
	if _, err := kp.UnwrapDEK(wrapped, "B"); err != nil {
		t.Fatalf("UnwrapDEK con B: %v", err)
	}
	if _, err := kp.UnwrapDEK(wrapped, "A"); err == nil {
		t.Fatalf("esperaba error al desenvolver con la KEK equivocada (A)")
	}
}

// TestUnwrapDEK_MissingKeyID: key_id ausente → error claro (fail-safe §10.J), no panic.
func TestUnwrapDEK_MissingKeyID(t *testing.T) {
	kp := newProvider(t)
	dek := make([]byte, envelope.DEKSize)
	wrapped, _, err := kp.WrapDEK(dek)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}
	_, err = kp.UnwrapDEK(wrapped, "no-existe")
	if err == nil {
		t.Fatalf("esperaba error por key_id ausente")
	}
	if !errors.Is(err, ErrKEKNotInKeyring) {
		t.Fatalf("esperaba ErrKEKNotInKeyring, obtuve: %v", err)
	}
}

func TestUnwrapDEK_TamperedBlob(t *testing.T) {
	kp := newProvider(t)
	dek := make([]byte, envelope.DEKSize)
	wrapped, id, err := kp.WrapDEK(dek)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}
	wrapped[len(wrapped)-1] ^= 0xFF // manipula el tag
	if _, err := kp.UnwrapDEK(wrapped, id); err == nil {
		t.Fatalf("esperaba error al desenvolver un blob manipulado")
	}
}

func TestBlindIndex_Deterministic(t *testing.T) {
	kp := newProvider(t)
	a := kp.BlindIndex("tenant-1", "+573001112233")
	b := kp.BlindIndex("tenant-1", "+573001112233")
	if a != b {
		t.Fatalf("BlindIndex no determinista: %s != %s", a, b)
	}
	if a == "" {
		t.Fatalf("BlindIndex vacío")
	}
}

// TestBlindIndex_StableAcrossRotation: rotar la KEK (distinto keyring/current) NO
// cambia BlindIndex, porque la indexKey es independiente del keyring (§10.C).
func TestBlindIndex_StableAcrossRotation(t *testing.T) {
	// t0: solo KEK A es current.
	kpA, err := NewEnvKeyProvider(KeyringConfig{
		KeyringB64: "A:" + masterB64(),
		CurrentID:  "A",
		IndexB64:   indexB64(), // MISMA indexKey explícita.
	})
	if err != nil {
		t.Fatalf("kpA: %v", err)
	}
	// t1: se añade KEK B y pasa a ser current (rotación).
	kpB, err := NewEnvKeyProvider(KeyringConfig{
		KeyringB64: "A:" + masterB64() + ",B:" + keyB64(32, 0x22),
		CurrentID:  "B",
		IndexB64:   indexB64(), // MISMA indexKey explícita.
	})
	if err != nil {
		t.Fatalf("kpB: %v", err)
	}
	if kpA.CurrentKeyID() == kpB.CurrentKeyID() {
		t.Fatalf("las currents deberían diferir (A vs B): %q", kpA.CurrentKeyID())
	}
	if kpA.BlindIndex("t", "v") != kpB.BlindIndex("t", "v") {
		t.Fatalf("BlindIndex cambió al rotar la KEK (indexKey NO independiente)")
	}
}

func TestBlindIndex_DiffersByTenantAndValue(t *testing.T) {
	kp := newProvider(t)
	base := kp.BlindIndex("tenant-1", "valor")
	if base == kp.BlindIndex("tenant-2", "valor") {
		t.Fatalf("BlindIndex igual para tenants distintos")
	}
	if base == kp.BlindIndex("tenant-1", "otro") {
		t.Fatalf("BlindIndex igual para valores distintos")
	}
	// El separador 0x00 evita colisiones por concatenación ambigua.
	if kp.BlindIndex("ab", "c") == kp.BlindIndex("a", "bc") {
		t.Fatalf("BlindIndex colisiona por concatenación ambigua (falta separador)")
	}
}

func TestBlindIndex_DiffersByIndexKey(t *testing.T) {
	// indexKey explícita distinta → bidx distinto (misma KEK maestra).
	kp1, err := NewEnvKeyProvider(KeyringConfig{MasterB64: masterB64(), IndexB64: keyB64(indexKeySize, 0x01)})
	if err != nil {
		t.Fatalf("kp1: %v", err)
	}
	kp2, err := NewEnvKeyProvider(KeyringConfig{MasterB64: masterB64(), IndexB64: keyB64(indexKeySize, 0x02)})
	if err != nil {
		t.Fatalf("kp2: %v", err)
	}
	if kp1.BlindIndex("t", "v") == kp2.BlindIndex("t", "v") {
		t.Fatalf("BlindIndex no depende de la indexKey")
	}
}

func TestBlindIndex_HexLength(t *testing.T) {
	kp := newProvider(t)
	// HMAC-SHA256 = 32 bytes → 64 hex chars.
	if got := kp.BlindIndex("t", "v"); len(got) != 64 || strings.TrimLeft(got, "0123456789abcdef") != "" {
		t.Fatalf("bidx no es hex de 64 chars: %q (len=%d)", got, len(got))
	}
}

// TestDevDerivesIndex_FailFastProd: en dev, sin indexKey, se deriva por HKDF
// (construye OK); en prod, sin indexKey, fail-fast.
func TestDevDerivesIndex_FailFastProd(t *testing.T) {
	if _, err := NewEnvKeyProvider(KeyringConfig{MasterB64: masterB64(), Prod: false}); err != nil {
		t.Fatalf("dev sin indexKey debería derivar y construir OK: %v", err)
	}
	_, err := NewEnvKeyProvider(KeyringConfig{MasterB64: masterB64(), Prod: true})
	if err == nil {
		t.Fatalf("prod sin indexKey debería fallar-rápido")
	}
	if !errors.Is(err, ErrIndexKeyRequiredInProd) {
		t.Fatalf("esperaba ErrIndexKeyRequiredInProd, obtuve: %v", err)
	}
}
