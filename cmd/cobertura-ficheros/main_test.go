package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture es la raíz de un árbol de prueba del informe de cobertura (internal/candados, T0.5).
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

// retiradas son las líneas que el comando imprimía cuando era un candado con exentos y que
// desde P2 (Jhoan, 2026-10-03; 05 E-9) ya no existen.
var retiradas = []string{"EXENTO", "EXENTOS_POSTGRES", "VIOLACION"}

// sinRetiradas exige que la salida no traiga ninguna de las líneas retiradas.
func sinRetiradas(t *testing.T, salida string) {
	t.Helper()
	for _, r := range retiradas {
		if strings.Contains(salida, r) {
			t.Errorf("la salida aún dice %q, retirado con P2:\n%s", r, salida)
		}
	}
}

// TestEjecutarPasa: el árbol sin ficheros por debajo (uno justo al 80 %, un adaptador Postgres
// marcado que ahora SE MIDE, un rojo y uno sin sentencias) sale con rc=0 y el resumen exacto.
func TestEjecutarPasa(t *testing.T) {
	raiz := fixture("pasa")
	rc, salida := correr(t, "-perfil", filepath.Join(raiz, "perfil.out"), "-raiz", raiz,
		"-dirs", "internal/modulos", "-umbral", "80")
	if rc != rcBien {
		t.Fatalf("rc = %d; quiero %d. Salida:\n%s", rc, rcBien, salida)
	}
	contiene(t, salida,
		"  80.0 %  internal/modulos/m/bien/bien.go",
		" 100.0 %  internal/modulos/m/pg/pg.go",
		"FICHEROS_EVALUADOS=2",
		"POR_DEBAJO=0",
	)
	if strings.Contains(salida, "BAJO ") {
		t.Errorf("no esperaba ficheros por debajo:\n%s", salida)
	}
	sinRetiradas(t, salida)
}

// TestEjecutarInforma: el árbol con ficheros por debajo los nombra con su motivo y aun así
// sale con rc=0 (P2: es un informe, no un gate). El adaptador Postgres marcado sale MEDIDO, al
// 0 %; el fichero con la marca y sin import de Postgres, al 90 %, sin «violación de marca».
func TestEjecutarInforma(t *testing.T) {
	raiz := fixture("muerde")
	rc, salida := correr(t, "-perfil", filepath.Join(raiz, "perfil.out"), "-raiz", raiz,
		"-dirs", " internal/modulos ,")
	if rc != rcBien {
		t.Fatalf("rc = %d; quiero %d: un fichero por debajo no bloquea. Salida:\n%s", rc, rcBien, salida)
	}
	contiene(t, salida,
		"  90.0 %  internal/modulos/m/falso/falso.go",
		"  50.0 %  internal/modulos/m/medio/medio.go",
		"   0.0 %  internal/modulos/m/pg/pg.go",
		"   0.0 %  internal/modulos/m/tarde/tarde.go",
		"BAJO internal/modulos/m/medio/medio.go: cobertura 50.0 % < umbral 80 % (2 de 4 sentencias cubiertas)",
		"BAJO internal/modulos/m/pg/pg.go: cobertura 0.0 % < umbral 80 % (0 de 10 sentencias cubiertas)",
		"BAJO internal/modulos/m/tarde/tarde.go: cobertura 0.0 % < umbral 80 % (0 de 1 sentencias cubiertas)",
		"FICHEROS_EVALUADOS=4",
		"POR_DEBAJO=3",
	)
	if strings.Contains(salida, "BAJO internal/modulos/m/falso/falso.go") {
		t.Errorf("falso.go está al 90 %%: no va por debajo, y la marca ya no es motivo:\n%s", salida)
	}
	if strings.Contains(salida, "rojo.go") {
		t.Errorf("un fichero en rojo no se evalúa:\n%s", salida)
	}
	sinRetiradas(t, salida)
}

// TestEjecutarUmbral: -umbral es la línea de referencia de POR_DEBAJO: el mismo árbol que da 0
// con 80 da 1 con 81. Y sigue sin decidir el código de salida: rc=0.
func TestEjecutarUmbral(t *testing.T) {
	raiz := fixture("pasa")
	rc, salida := correr(t, "-perfil", filepath.Join(raiz, "perfil.out"), "-raiz", raiz,
		"-dirs", "internal/modulos", "-umbral", "81")
	if rc != rcBien {
		t.Fatalf("rc = %d; quiero %d. Salida:\n%s", rc, rcBien, salida)
	}
	contiene(t, salida, "POR_DEBAJO=1",
		"BAJO internal/modulos/m/bien/bien.go: cobertura 80.0 % < umbral 81 % (8 de 10 sentencias cubiertas)")
}

// bridgesFixture es el árbol de prueba de candados.WalkBridges, con su perfil: en
// internal/arranque hay un adaptador (bridge_x.go, 80 %), un otro.go y un bridge.go que no lo
// son, y un subdirectorio con otro adaptador (sub/bridge_y.go), todos en el perfil.
func bridgesFixture() string {
	return filepath.Join("..", "..", "internal", "candados", "testdata", "bridges", "arbol")
}

// TestEjecutarBridges: -bridges suma al alcance SOLO los bridge_<x>.go directos del directorio
// (D-F1-16): bridge_x.go sale medido y ni otro.go, ni bridge.go, ni el adaptador del
// subdirectorio aparecen, aunque el perfil los traiga. Sin -bridges no entra ninguno.
func TestEjecutarBridges(t *testing.T) {
	raiz := bridgesFixture()
	perfil := filepath.Join(raiz, "perfil.out")

	rc, salida := correr(t, "-perfil", perfil, "-raiz", raiz, "-dirs", "internal/modulos",
		"-bridges", " internal/arranque ,")
	if rc != rcBien {
		t.Fatalf("rc = %d; quiero %d. Salida:\n%s", rc, rcBien, salida)
	}
	contiene(t, salida,
		"cobertura-ficheros: más los bridge_*.go de internal/arranque",
		"  80.0 %  internal/arranque/bridge_x.go",
		"FICHEROS_EVALUADOS=1",
		"POR_DEBAJO=0",
	)
	for _, fuera := range []string{"otro.go", "bridge.go", "bridge_y.go", "dentro.go"} {
		if strings.Contains(salida, fuera) {
			t.Errorf("%s no es un adaptador directo y no debe salir:\n%s", fuera, salida)
		}
	}

	rc, salida = correr(t, "-perfil", perfil, "-raiz", raiz, "-dirs", "internal/modulos")
	if rc != rcBien {
		t.Fatalf("sin -bridges: rc = %d; quiero %d. Salida:\n%s", rc, rcBien, salida)
	}
	contiene(t, salida, "FICHEROS_EVALUADOS=0", "POR_DEBAJO=0")
	if strings.Contains(salida, "bridge_") {
		t.Errorf("sin -bridges no entra ningún adaptador:\n%s", salida)
	}
}

// TestEjecutarBridgesSinRepetir: un adaptador que llega por -dirs y por -bridges cuenta una
// vez, y la tabla sigue ordenada por ruta.
func TestEjecutarBridgesSinRepetir(t *testing.T) {
	raiz := bridgesFixture()
	rc, salida := correr(t, "-perfil", filepath.Join(raiz, "perfil.out"), "-raiz", raiz,
		"-dirs", "internal/arranque/sub,internal/arranque", "-bridges", "internal/arranque,internal/arranque/sub")
	if rc != rcBien {
		t.Fatalf("rc = %d; quiero %d. Salida:\n%s", rc, rcBien, salida)
	}
	contiene(t, salida, "FICHEROS_EVALUADOS=5", "POR_DEBAJO=4")
	if n := strings.Count(salida, "%  internal/arranque/bridge_x.go"); n != 1 {
		t.Errorf("bridge_x.go sale %d veces en la tabla; quiero 1:\n%s", n, salida)
	}
	if n := strings.Count(salida, "%  internal/arranque/sub/bridge_y.go"); n != 1 {
		t.Errorf("sub/bridge_y.go sale %d veces en la tabla; quiero 1:\n%s", n, salida)
	}
}

// TestLeerFuentesOrden: -dirs y -bridges juntos dan las fuentes ordenadas por Ruta, aunque el
// adaptador vaya alfabéticamente antes que lo recorrido por -dirs.
func TestLeerFuentesOrden(t *testing.T) {
	fuentes, err := leerFuentes(opciones{raiz: bridgesFixture(),
		dirs: []string{"internal/arranque/sub"}, bridges: []string{"internal/arranque"}})
	if err != nil {
		t.Fatalf("leerFuentes: %v", err)
	}
	got := make([]string, 0, len(fuentes))
	for _, f := range fuentes {
		got = append(got, f.Ruta)
	}
	quiero := []string{"internal/arranque/bridge_x.go", "internal/arranque/sub/bridge_y.go"}
	if strings.Join(got, " ") != strings.Join(quiero, " ") {
		t.Errorf("leerFuentes = %v; quiero %v", got, quiero)
	}
}

// TestSplitList: comas, espacios y vacíos.
func TestSplitList(t *testing.T) {
	if got := splitList(" a , ,b,"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("splitList = %q; quiero [a b]", got)
	}
	if got := splitList(" , "); len(got) != 0 {
		t.Errorf("splitList de solo comas = %q; quiero vacío", got)
	}
}

// TestEjecutarAlcanceInexistente: un dir que no existe aporta cero ficheros y no es error.
func TestEjecutarAlcanceInexistente(t *testing.T) {
	raiz := fixture("pasa")
	rc, salida := correr(t, "-perfil", filepath.Join(raiz, "perfil.out"), "-raiz", raiz,
		"-dirs", "internal/nonato")
	if rc != rcBien {
		t.Fatalf("rc = %d; quiero %d. Salida:\n%s", rc, rcBien, salida)
	}
	contiene(t, salida, "FICHEROS_EVALUADOS=0", "POR_DEBAJO=0")
	sinRetiradas(t, salida)
}

// TestEjecutarErrores: uso incorrecto o fallo de E/S → rc=2, con el motivo en la salida. Que
// el comando sea un informe no los ablanda: un error real sí rompe.
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
	if err := os.WriteFile(filepath.Join(roto, "internal", "x", "bridge_x.go"), []byte("package x\nfunc {"), 0o600); err != nil {
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
		{"adaptador que no parsea", []string{"-perfil", perfil, "-raiz", roto, "-dirs", "a", "-bridges", "internal/x"}, "bridge_x.go"},
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

// TestEjecutarSalidaRota: si la tabla no se puede escribir, el informe no se puede leer →
// rc=2, aunque no haya ficheros por debajo.
func TestEjecutarSalidaRota(t *testing.T) {
	raiz := fixture("pasa")
	rc := ejecutar([]string{"-perfil", filepath.Join(raiz, "perfil.out"), "-raiz", raiz,
		"-dirs", "internal/modulos"}, fallido{})
	if rc != rcUso {
		t.Fatalf("rc = %d; quiero %d", rc, rcUso)
	}
}
