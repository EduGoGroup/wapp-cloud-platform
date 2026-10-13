package candados

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// bridgePrefix es el prefijo del nombre de un adaptador de arranque: bridge_<x>.go, con su
// bridge_<x>_test.go (05 §4.2).
const bridgePrefix = "bridge_"

// WalkBridges parsea los adaptadores de arranque de UN directorio: los hijos DIRECTOS de
// raiz/dir cuyo nombre base es bridge_<x>.go, y devuelve una Fuente por fichero, con la misma
// forma que las de Recorrer (Ruta con barras relativa a raiz, ordenadas por Ruta, un Fset
// común a la llamada).
//
// Existe por D-F1-16 (P5, Jhoan, 2026-10-03; 05 §4.2): los candados de fichero deben ver los
// bridge_*.go de internal/arranque, y SOLO esos. El directorio entero no puede entrar en el
// alcance (D-F0-1: es el arranque copiado, y casi ninguno de sus ficheros tiene test homónimo),
// y Recorrer solo sabe de directorios enteros.
//
// Promesas:
//   - NO es recursiva: un bridge_<x>.go en un subdirectorio de raiz/dir no sale;
//   - un fichero del directorio que no es un adaptador (otro.go, bridge.go, bridge_.go) no
//     sale, tenga o no test;
//   - con incluirTests = false solo salen los bridge_<x>.go de producción; con true, además,
//     sus bridge_<x>_test.go (EsTest = true). bridge_test.go NO es el test de un adaptador —es
//     el de bridge.go—, así que no sale nunca;
//   - una entrada que es un directorio no sale aunque se llame bridge_<x>.go;
//   - un dir (o una raiz) que no existe aporta cero ficheros y NO es un error, como en Recorrer;
//   - un adaptador que no parsea es un error que nombra la ruta, sin resultado parcial.
func WalkBridges(raiz, dir string, incluirTests bool) ([]Fuente, error) {
	inicio := filepath.Join(raiz, filepath.FromSlash(dir))
	entradas, err := os.ReadDir(inicio)
	if errors.Is(err, fs.ErrNotExist) {
		return make([]Fuente, 0), nil
	}
	if err != nil {
		return nil, fmt.Errorf("candados: leyendo %s: %w", inicio, err)
	}
	vistos := make(map[string]string) // Ruta con barras → ruta en disco
	for _, e := range entradas {
		if e.IsDir() || !isBridgeFile(e.Name(), incluirTests) {
			continue
		}
		vistos[path.Join(filepath.ToSlash(dir), e.Name())] = filepath.Join(inicio, e.Name())
	}
	return parseFiles(vistos)
}

// isBridgeFile dice si name —un nombre base— es el de un adaptador de arranque: bridge_<x>.go
// con al menos un carácter en <x>, o, si withTests, su test bridge_<x>_test.go. La comparación
// es exacta, con mayúsculas y minúsculas.
func isBridgeFile(name string, withTests bool) bool {
	stem, isGo := strings.CutSuffix(name, ".go")
	if !isGo {
		return false
	}
	if rest, isTest := strings.CutSuffix(stem, "_test"); isTest {
		if !withTests {
			return false
		}
		stem = rest
	}
	return len(stem) > len(bridgePrefix) && strings.HasPrefix(stem, bridgePrefix)
}
