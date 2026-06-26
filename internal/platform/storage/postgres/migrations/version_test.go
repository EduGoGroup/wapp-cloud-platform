package migrations

import "testing"

// TestComputeFilesHash_Deterministic verifica que el hash es estable, no vacío
// y no es el centinela de error (los archivos embebidos se leen bien).
func TestComputeFilesHash_Deterministic(t *testing.T) {
	h1 := ComputeFilesHash()
	h2 := ComputeFilesHash()

	if h1 != h2 {
		t.Fatalf("hash no determinista: %q != %q", h1, h2)
	}
	if h1 == "" || h1 == "error" {
		t.Fatalf("hash inválido: %q", h1)
	}
	if len(h1) != hashLen {
		t.Fatalf("longitud de hash: got %d, want %d", len(h1), hashLen)
	}
}

// TestSchemaVersion_NotEmpty protege contra dejar la versión en blanco.
func TestSchemaVersion_NotEmpty(t *testing.T) {
	if SchemaVersion == "" {
		t.Fatal("SchemaVersion no puede estar vacío")
	}
}

// TestIsUpToDate cubre la lógica de comparación versión+hash.
func TestIsUpToDate(t *testing.T) {
	rec := schemaRecord{Version: "0.1.0", ContentHash: "abc"}

	if !isUpToDate(rec, "0.1.0", "abc") {
		t.Error("debería estar al día con misma versión y hash")
	}
	if isUpToDate(rec, "0.2.0", "abc") {
		t.Error("no debería estar al día con versión distinta")
	}
	if isUpToDate(rec, "0.1.0", "xyz") {
		t.Error("no debería estar al día con hash distinto")
	}
}
