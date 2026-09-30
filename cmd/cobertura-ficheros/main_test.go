package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture es la raíz de un árbol de prueba del candado de cobertura (internal/candados, T0.5).
func fixture(caso string) string {
	return filepath.Join("..", "..", "internal", "candados", "testdata", "cobertura", caso)
}

// correr ejecuta el comando con args y devuelve rc y salida.
func correr(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var salida bytes.Buffer
	rc := ejecutar(args, &salida)
	return rc, salida.String()
}

// contiene exige que salida tenga cada una de las líneas dadas, completas.
func contiene(t *testing.T, salida string, lineas ...string) {
	t.Helper()
	todas := strings.Split(salida, "\n")
	for _, quiero := range lineas {
		hallada := false
		for _, l := range todas {
			if l == quiero {
				hallada = true
				break
			}
		}
		if !hallada {
			t.Errorf("falta la línea %q en la salida:\n%s", quiero, salida)
		}
	}
}

// TestEjecutarPasa: el árbol que pasa (un fichero justo al 80 %, un exento legítimo, un
// rojo y uno sin sentencias) sale con rc=0 y el resumen exacto.
func TestEjecutarPasa(t *testing.T) {
	raiz := fixture("pasa")
	rc, salida := correr(t, "-perfil", filepath.Join(raiz, "perfil.out"), "-raiz", raiz,
		"-dirs", "internal/modulos", "-umbral", "80")
	if rc != rcBien {
		t.Fatalf("rc = %d; quiero %d. Salida:\n%s", rc, rcBien, salida)
	}
	contiene(t, salida,
		"  80.0 %  internal/modulos/m/bien/bien.go",
		"EXENTO postgres  internal/modulos/m/pg/pg.go",
		"FICHEROS_EVALUADOS=1",
		"POR_DEBAJO=0",
		"EXENTOS_POSTGRES=1",
	)
	if strings.Contains(salida, "VIOLACION") {
		t.Errorf("no esperaba violaciones:\n%s", salida)
	}
}

// TestEjecutarMuerde: el árbol que muerde da rc=1 y nombra cada violación con su motivo:
// un fichero al 50 %, uno al 0 % cuya marca no está en la cabecera y una marca ilegítima.
func TestEjecutarMuerde(t *testing.T) {
	raiz := fixture("muerde")
	rc, salida := correr(t, "-perfil", filepath.Join(raiz, "perfil.out"), "-raiz", raiz,
		"-dirs", " internal/modulos ,")
	if rc != rcViolacion {
		t.Fatalf("rc = %d; quiero %d. Salida:\n%s", rc, rcViolacion, salida)
	}
	contiene(t, salida,
		"  90.0 %  internal/modulos/m/falso/falso.go",
		"  50.0 %  internal/modulos/m/medio/medio.go",
		"   0.0 %  internal/modulos/m/tarde/tarde.go",
		"EXENTO postgres  internal/modulos/m/pg/pg.go",
		"FICHEROS_EVALUADOS=3",
		"POR_DEBAJO=3",
		"EXENTOS_POSTGRES=1",
	)
	for _, quiero := range []string{
		"VIOLACION internal/modulos/m/medio/medio.go: cobertura 50.0 % < umbral 80 %",
		"VIOLACION internal/modulos/m/tarde/tarde.go: cobertura 0.0 % < umbral 80 %",
		"VIOLACION internal/modulos/m/falso/falso.go: marca de adaptador postgres",
	} {
		if !strings.Contains(salida, quiero) {
			t.Errorf("falta %q en la salida:\n%s", quiero, salida)
		}
	}
	if strings.Contains(salida, "rojo.go") {
		t.Errorf("un fichero en rojo no se evalúa:\n%s", salida)
	}
}

// TestEjecutarUmbral: el mismo árbol que pasa al 80 muerde al 81 (el umbral se respeta).
func TestEjecutarUmbral(t *testing.T) {
	raiz := fixture("pasa")
	rc, salida := correr(t, "-perfil", filepath.Join(raiz, "perfil.out"), "-raiz", raiz,
		"-dirs", "internal/modulos", "-umbral", "81")
	if rc != rcViolacion {
		t.Fatalf("rc = %d; quiero %d. Salida:\n%s", rc, rcViolacion, salida)
	}
	contiene(t, salida, "POR_DEBAJO=1",
		"VIOLACION internal/modulos/m/bien/bien.go: cobertura 80.0 % < umbral 81 % (8 de 10 sentencias cubiertas, D-12)")
}

// TestEjecutarAlcanceInexistente: un dir que no existe aporta cero ficheros y no es error.
func TestEjecutarAlcanceInexistente(t *testing.T) {
	raiz := fixture("pasa")
	rc, salida := correr(t, "-perfil", filepath.Join(raiz, "perfil.out"), "-raiz", raiz,
		"-dirs", "internal/nonato")
	if rc != rcBien {
		t.Fatalf("rc = %d; quiero %d. Salida:\n%s", rc, rcBien, salida)
	}
	contiene(t, salida, "FICHEROS_EVALUADOS=0", "POR_DEBAJO=0", "EXENTOS_POSTGRES=0")
}

// TestEjecutarErrores: uso incorrecto o fallo de E/S → rc=2, con el motivo en la salida.
func TestEjecutarErrores(t *testing.T) {
	raiz := fixture("pasa")
	perfil := filepath.Join(raiz, "perfil.out")

	tmp := t.TempDir()
	malo := filepath.Join(tmp, "malo.out")
	if err := os.WriteFile(malo, []byte("esto no es un perfil\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roto := filepath.Join(tmp, "arbol")
	if err := os.MkdirAll(filepath.Join(roto, "internal", "x"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roto, "internal", "x", "x.go"), []byte("package x\nfunc {"), 0o600); err != nil {
		t.Fatal(err)
	}

	casos := []struct {
		nombre string
		args   []string
		motivo string
	}{
		{"sin perfil", []string{"-dirs", "internal/modulos"}, "falta -perfil"},
		{"sin dirs", []string{"-perfil", perfil}, "falta -dirs"},
		{"dirs vacíos", []string{"-perfil", perfil, "-dirs", " , "}, "falta -dirs"},
		{"umbral cero", []string{"-perfil", perfil, "-dirs", "a", "-umbral", "0"}, "fuera de (0, 100]"},
		{"umbral excesivo", []string{"-perfil", perfil, "-dirs", "a", "-umbral", "101"}, "fuera de (0, 100]"},
		{"flag desconocido", []string{"-nada"}, "flag provided but not defined"},
		{"sobrantes", []string{"-perfil", perfil, "-dirs", "a", "extra"}, "argumentos sobrantes"},
		{"perfil inexistente", []string{"-perfil", filepath.Join(tmp, "no.out"), "-dirs", "a"}, "no.out"},
		{"perfil mal formado", []string{"-perfil", malo, "-dirs", "a"}, "línea 1"},
		{"fuente que no parsea", []string{"-perfil", perfil, "-raiz", roto, "-dirs", "internal"}, "x.go"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			rc, salida := correr(t, c.args...)
			if rc != rcUso {
				t.Fatalf("rc = %d; quiero %d. Salida:\n%s", rc, rcUso, salida)
			}
			if !strings.Contains(salida, c.motivo) {
				t.Errorf("la salida no nombra %q:\n%s", c.motivo, salida)
			}
			if strings.Contains(salida, "FICHEROS_EVALUADOS=") {
				t.Errorf("con error de uso no hay resumen:\n%s", salida)
			}
		})
	}
}

// TestEjecutarAyuda: -h imprime el uso y sale con rc=2 sin mensaje de error añadido.
func TestEjecutarAyuda(t *testing.T) {
	rc, salida := correr(t, "-h")
	if rc != rcUso {
		t.Fatalf("rc = %d; quiero %d", rc, rcUso)
	}
	if !strings.Contains(salida, "-perfil") || strings.Contains(salida, "cobertura-ficheros: flag") {
		t.Errorf("salida de ayuda inesperada:\n%s", salida)
	}
}

// TestCasar: gana la clave entera; si no, la Ruta más larga que es sufijo tras «/».
func TestCasar(t *testing.T) {
	rutas := map[string]bool{"a/b.go": true, "x/a/b.go": true, "c.go": true}
	casos := []struct {
		k, quiero string
		ok        bool
	}{
		{"c.go", "c.go", true},
		{"mod/x/a/b.go", "x/a/b.go", true},
		{"mod/y/a/b.go", "a/b.go", true},
		{"mod/zc.go", "", false},
	}
	for _, c := range casos {
		got, ok := casar(c.k, rutas)
		if got != c.quiero || ok != c.ok {
			t.Errorf("casar(%q) = %q, %v; quiero %q, %v", c.k, got, ok, c.quiero, c.ok)
		}
	}
}

// fallido es un io.Writer que siempre falla.
type fallido struct{}

func (fallido) Write([]byte) (int, error) { return 0, os.ErrClosed }

// TestEjecutarSalidaRota: si la tabla no se puede escribir, el veredicto no se puede leer →
// rc=2, aunque no haya violaciones.
func TestEjecutarSalidaRota(t *testing.T) {
	raiz := fixture("pasa")
	rc := ejecutar([]string{"-perfil", filepath.Join(raiz, "perfil.out"), "-raiz", raiz,
		"-dirs", "internal/modulos"}, fallido{})
	if rc != rcUso {
		t.Fatalf("rc = %d; quiero %d", rc, rcUso)
	}
}
