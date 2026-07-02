package crypto

import (
	"encoding/base64"
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

func newProvider(t *testing.T) KeyProvider {
	t.Helper()
	kp, err := NewEnvKeyProvider(masterB64(), "")
	if err != nil {
		t.Fatalf("NewEnvKeyProvider: %v", err)
	}
	return kp
}

func TestNewEnvKeyProvider_Errors(t *testing.T) {
	tests := []struct {
		name      string
		masterB64 string
		indexB64  string
	}{
		{"master vacía", "", ""},
		{"master base64 inválido", "no-es-base64!!!", ""},
		{"master tamaño incorrecto (16B)", keyB64(16, 0x22), ""},
		{"master tamaño incorrecto (33B)", keyB64(33, 0x22), ""},
		{"indexKey base64 inválido", masterB64(), "no-es-base64!!!"},
		{"indexKey tamaño incorrecto (16B)", masterB64(), keyB64(16, 0x33)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewEnvKeyProvider(tc.masterB64, tc.indexB64); err == nil {
				t.Fatalf("esperaba error, obtuve nil")
			}
		})
	}
}

func TestNewEnvKeyProvider_OK(t *testing.T) {
	if _, err := NewEnvKeyProvider(masterB64(), ""); err != nil {
		t.Fatalf("master válida, index derivada: %v", err)
	}
	if _, err := NewEnvKeyProvider(masterB64(), keyB64(indexKeySize, 0x44)); err != nil {
		t.Fatalf("master válida, index explícita válida: %v", err)
	}
}

func TestWrapUnwrapDEK_RoundTrip(t *testing.T) {
	kp := newProvider(t)
	dek := make([]byte, envelope.DEKSize)
	for i := range dek {
		dek[i] = byte(i)
	}

	wrapped, err := kp.WrapDEK(dek)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}
	if len(wrapped) <= envelope.DEKSize {
		t.Fatalf("wrapped no lleva overhead GCM: len=%d", len(wrapped))
	}

	got, err := kp.UnwrapDEK(wrapped)
	if err != nil {
		t.Fatalf("UnwrapDEK: %v", err)
	}
	if string(got) != string(dek) {
		t.Fatalf("DEK round-trip: got %x want %x", got, dek)
	}
}

func TestUnwrapDEK_TamperedBlob(t *testing.T) {
	kp := newProvider(t)
	dek := make([]byte, envelope.DEKSize)
	wrapped, err := kp.WrapDEK(dek)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}
	wrapped[len(wrapped)-1] ^= 0xFF // manipula el tag
	if _, err := kp.UnwrapDEK(wrapped); err == nil {
		t.Fatalf("esperaba error al desenvolver un blob manipulado")
	}
}

func TestUnwrapDEK_WrongKEK(t *testing.T) {
	kp1 := newProvider(t)
	kp2, err := NewEnvKeyProvider(keyB64(envelope.DEKSize, 0x99), "")
	if err != nil {
		t.Fatalf("NewEnvKeyProvider kp2: %v", err)
	}
	dek := make([]byte, envelope.DEKSize)
	wrapped, err := kp1.WrapDEK(dek)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}
	if _, err := kp2.UnwrapDEK(wrapped); err == nil {
		t.Fatalf("esperaba error al desenvolver con otra KEK")
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

func TestBlindIndex_StableAcrossProviders(t *testing.T) {
	// Misma KEK maestra → misma indexKey derivada → mismo bidx.
	kp1 := newProvider(t)
	kp2 := newProvider(t)
	if kp1.BlindIndex("t", "v") != kp2.BlindIndex("t", "v") {
		t.Fatalf("BlindIndex no estable entre providers con la misma KEK")
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
	kp1, err := NewEnvKeyProvider(masterB64(), keyB64(indexKeySize, 0x01))
	if err != nil {
		t.Fatalf("kp1: %v", err)
	}
	kp2, err := NewEnvKeyProvider(masterB64(), keyB64(indexKeySize, 0x02))
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
