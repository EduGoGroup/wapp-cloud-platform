//go:build pendiente

package candados

import (
	"slices"
	"testing"
)

// inboundPortDir es el único directorio que hoy promete InboundPortDirsWithoutSuite
// (D-F2-5). Los casos de UnFicheroUnTest lo escriben literal, sin pasar por la función: así
// prueban la exención del candado, no la lista.
const inboundPortDir = "internal/modulos/acceso/iam/ports/in"

// TestInboundPortDirsWithoutSuiteList: la lista es exactamente la de D-F2-5, ni más ni menos.
func TestInboundPortDirsWithoutSuiteList(t *testing.T) {
	got := InboundPortDirsWithoutSuite()
	want := []string{inboundPortDir}
	if !slices.Equal(got, want) {
		t.Errorf("InboundPortDirsWithoutSuite() = %q; quiero exactamente %q (D-F2-5)", got, want)
	}
}

// TestInboundPortDirsWithoutSuiteCopy: el slice devuelto es una copia; mutarlo (cambiar un
// elemento, o escribir en su capacidad sobrante) no cambia la siguiente llamada.
func TestInboundPortDirsWithoutSuiteCopy(t *testing.T) {
	first := InboundPortDirsWithoutSuite()
	if len(first) == 0 {
		t.Fatalf("InboundPortDirsWithoutSuite() vacía: no hay nada que mutar")
	}
	first[0] = "internal/modulos/otro/ports/in"
	_ = append(first[:1], "internal/modulos/extra/ports/in")
	want := []string{inboundPortDir}
	if got := InboundPortDirsWithoutSuite(); !slices.Equal(got, want) {
		t.Errorf("tras mutar el slice devuelto, InboundPortDirsWithoutSuite() = %q; quiero %q", got, want)
	}
}

// TestUnFicheroUnTestInboundPortExemption: en un directorio listado, un fichero SOLO de
// interfaces sin test y sin suite queda exento; con una func, var o const más, no. Fuera de
// la lista (puertos de salida, un hermano con el mismo prefijo, un subdirectorio del
// listado), el mismo fichero de solo interfaces sin suite muerde.
func TestUnFicheroUnTestInboundPortExemption(t *testing.T) {
	const iface = "\n// ActiveTenant es un puerto de entrada.\ntype ActiveTenant interface{ Switch() error }\n"
	cases := []struct {
		name  string
		dir   string
		pkg   string
		extra string // declaraciones además de la interfaz
		bites bool
	}{
		{"only interfaces in the listed dir, no test, no suite: exempt", inboundPortDir, "in", "", false},
		{"listed dir with an extra func: needs a test", inboundPortDir, "in", "\nfunc New() int { return 1 }\n", true},
		{"listed dir with an extra var: needs a test", inboundPortDir, "in", "\nvar V = 1\n", true},
		{"listed dir with an extra const: needs a test", inboundPortDir, "in", "\nconst C = 1\n", true},
		{"outbound ports dir is not listed: needs a test", "internal/modulos/acceso/iam/ports/out", "out", "", true},
		{"sibling dir sharing the prefix is not listed: needs a test", "internal/modulos/acceso/iam/ports/inbound", "inbound", "", true},
		{"subdir of the listed dir is not listed: needs a test", inboundPortDir + "/sub", "sub", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ruta := c.dir + "/canje.go"
			vs := UnFicheroUnTest([]Fuente{fuenteEnMemoria(t, ruta, "package "+c.pkg+"\n"+iface+c.extra)})
			if !c.bites {
				exigeCero(t, vs)
				return
			}
			exigeViolacion(t, vs, ruta, "falta canje_test.go")
			if len(vs) != 1 {
				t.Errorf("se esperaba 1 violación; hay %d: %v", len(vs), vs)
			}
		})
	}
}
