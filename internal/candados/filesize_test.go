package candados

import (
	"maps"
	"strings"
	"testing"
)

// fuenteDeLineas devuelve una fuente de Go válida con exactamente n líneas (n ≥ 2).
func fuenteDeLineas(t *testing.T, ruta string, n int) Fuente {
	t.Helper()
	src := "package p\n" + strings.Repeat("\n", n-2) + "var _ = 0\n"
	return fuenteEnMemoria(t, ruta, src)
}

// TestFileSizeLimits: el objetivo y el tope son 500 y 600 (500 + 20 %).
func TestFileSizeLimits(t *testing.T) {
	if FileLinesTarget != 500 || FileLinesLimit != 600 {
		t.Errorf("FileLinesTarget=%d FileLinesLimit=%d; quiero 500 y 600 (05 E-13)", FileLinesTarget, FileLinesLimit)
	}
}

// TestFileSizeBites: pasar de 600 viola, en producción y en test; 600 justas y la franja
// 501–600 no; un fichero de la lista cerrada viola solo si pasa de su techo.
func TestFileSizeBites(t *testing.T) {
	const grandfathered = "internal/nucleo/contact/repository_postgres.go"
	ceiling := OversizedFiles()[grandfathered]
	if ceiling == 0 {
		t.Fatalf("%s no está en OversizedFiles: el caso no prueba nada", grandfathered)
	}
	fuentes := []Fuente{
		fuenteDeLineas(t, "internal/modulos/m/big.go", FileLinesLimit+1),
		fuenteDeLineas(t, "internal/modulos/m/big_test.go", 900),
		fuenteDeLineas(t, "internal/modulos/m/exact.go", FileLinesLimit),
		fuenteDeLineas(t, "internal/modulos/m/tolerated.go", FileLinesTarget+1),
		fuenteDeLineas(t, "internal/modulos/m/small.go", 10),
		fuenteDeLineas(t, grandfathered, ceiling),
	}
	vs := FileSize(fuentes)
	exigeViolacion(t, vs, "internal/modulos/m/big.go", "E-13: ", "601 líneas", "600", "por tema")
	exigeViolacion(t, vs, "internal/modulos/m/big_test.go", "E-13: ", "900 líneas")
	exigeNingunaEn(t, vs, "internal/modulos/m/exact.go")
	exigeNingunaEn(t, vs, "internal/modulos/m/tolerated.go")
	exigeNingunaEn(t, vs, "internal/modulos/m/small.go")
	exigeNingunaEn(t, vs, grandfathered)
	if len(vs) != 2 {
		t.Errorf("se esperaban 2 violaciones; hay %d: %v", len(vs), vs)
	}
	exigeOrdenadas(t, vs)

	grown := FileSize([]Fuente{fuenteDeLineas(t, grandfathered, ceiling+1)})
	exigeViolacion(t, grown, grandfathered, "E-13: ", "techo")
	if len(grown) != 1 {
		t.Errorf("un fichero de la lista que crece da una violación; hay %d: %v", len(grown), grown)
	}
}

// TestOversizedFilesList: la lista es exactamente la medida el 2026-10-04 (D-R-7), con sus
// techos, y todos pasan de FileLinesLimit (si no, sobran en la lista).
func TestOversizedFilesList(t *testing.T) {
	want := map[string]int{
		"internal/arranque/bridge_contact_test.go":        809,
		"internal/arranque/huellatest/huellatest.go":      732,
		"internal/arranque/huellatest/huellatest_test.go": 669,
		"internal/candados/sinbdviva_openers_test.go":     629,
		"internal/nucleo/contact/repository_postgres.go":  618,
	}
	got := OversizedFiles()
	if !maps.Equal(got, want) {
		t.Errorf("OversizedFiles() = %v; quiero exactamente %v (D-R-7)", got, want)
	}
	for ruta, techo := range got {
		if techo <= FileLinesLimit {
			t.Errorf("%s con techo %d no pasa de %d: sobra en la lista", ruta, techo, FileLinesLimit)
		}
	}
}

// TestOversizedFilesCopy: el mapa devuelto es una copia.
func TestOversizedFilesCopy(t *testing.T) {
	first := OversizedFiles()
	for k := range first {
		first[k] = 1 << 20
	}
	first["internal/modulos/x/huge.go"] = 1 << 20
	for k, v := range OversizedFiles() {
		if v == 1<<20 || k == "internal/modulos/x/huge.go" {
			t.Fatalf("tras mutar la copia, OversizedFiles() cambió: %s=%d", k, v)
		}
	}
}
