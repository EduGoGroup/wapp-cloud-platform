package candados

import (
	"go/ast"
	"path"
	"slices"
	"sort"
	"strings"
)

const (
	// processDir es el directorio de los tests de proceso de F9, relativo a la raíz del repo.
	processDir = "test/procesos"
	// internalDir es el árbol que un proceso no puede importar: todo el código del servidor.
	internalDir = "internal"
	// locksPackage es este paquete: lo importan los ficheros-candado de test/procesos.
	locksPackage = "internal/candados"
	// constructorArgsPackage es el paquete de los ARGUMENTOS del constructor de un adaptador
	// Postgres (el FieldCipher y el KeyProvider): una suite de contrato los necesita para
	// construirlo (D-F1-8).
	constructorArgsPackage = "internal/platform/crypto"
	// processContractSuffix distingue al fichero que corre una suite de contrato contra
	// Postgres (H9.5). «contrato» es vocabulario del método (05 E-11, excepción 3).
	processContractSuffix = "_contrato_test.go"
	// constructorPrefix es por lo que empieza el nombre de un constructor.
	constructorPrefix = "New"
)

// processLockPaths son las rutas EXACTAS de los ficheros-candado de test/procesos, los únicos
// que pueden importar internal/candados: el de la base viva (SinBDViva) y el que cablea
// ProcessImports. Van sin etiqueta de compilación, para correr en `make test` (ci-local) sin
// Docker. Ampliar la lista es cambiar esta variable, a la vista de quien revise.
var processLockPaths = []string{
	"test/procesos/domain_imports_test.go",
	"test/procesos/sin_bd_viva_test.go",
}

// helperTestRoots son los árboles donde vive una suite de contrato …helpertest (05 E-3).
var helperTestRoots = []string{"internal/modulos", "internal/nucleo"}

// ProcessImports es el candado de R9.4.d (plan/F9-procesos/requisitos.md): un test de proceso
// entra por las puertas reales (HTTP, gRPC) y lee Postgres por SQL; NO importa paquetes de
// dominio. Juzga FICHERO A FICHERO los imports directos de "<modulo>/internal/…" de los
// ficheros de fuentes que cuelgan de test/procesos (con o sin etiqueta de compilación, tests
// incluidos; los demás ficheros se ignoran). modulo es la ruta del módulo Go; los imports de
// la biblioteca estándar, de terceros y de otros módulos no cuentan. Solo cuentan los imports:
// la ruta del módulo dentro de un literal de cadena no lo es.
//
// Hasta F1-06 la regla era un comando de la spec (go list sobre el PAQUETE), que admitía un
// paquete entero y desde cualquier fichero en cuanto una suite lo necesitaba, y no estaba en
// ningún gate (hallazgos 36 y 37 de F1).
//
// Reglas, por la Ruta del fichero (relativa a la raíz del repo):
//
//  1. Un fichero cualquiera no puede importar NADA de internal/.
//  2. internal/candados solo lo importan los ficheros-candado, por ruta EXACTA
//     (test/procesos/sin_bd_viva_test.go y test/procesos/domain_imports_test.go), y solo si no
//     llevan etiqueta de compilación (//go:build o // +build): con etiqueta no correrían en
//     ci-local. Un fichero-candado no puede importar ningún otro paquete de internal/.
//  3. Un fichero cuyo nombre termina en «_contrato_test.go» (con algo delante) corre una suite
//     de contrato contra Postgres (H9.5, D-F1-8) y puede importar SOLO:
//     a. paquetes …helpertest: el último elemento de la ruta termina en «helpertest» con al
//     menos un carácter delante (D-F1-10), bajo internal/modulos o internal/nucleo, o —fuera
//     de esos dos árboles— uno que sea CLAVE de la lista cerrada ContractAdapterDirs (hoy
//     solo la suite del adaptador SQL de la cara, D-FX-4);
//     b. el paquete del PUERTO que prueba: el directorio padre de un …helpertest importado
//     en ESE MISMO fichero o, si el par está en la lista cerrada ContractAdapterDirs
//     (D-F2-9: la zona hexagonal de iam, cuyos adaptadores no viven en el padre de su
//     suite), el adaptador de ese helpertest. De él solo puede usar el constructor: todo selector
//     <nombre>.<símbolo> sobre el nombre local del import (el alias, o el último elemento
//     de la ruta) debe empezar por «New». Importarlo con punto o en blanco es una
//     violación: no se vería qué usa;
//     c. internal/platform/crypto: los argumentos de ese constructor.
//
// «Solo constructores» es la regla barata: NewMemoryResolver pasa igual que
// NewPostgresResolver. Tampoco se resuelven tipos ni ámbitos: una variable local que se llame
// como el paquete del puerto cuenta como el paquete (mejor morder de más).
//
// Cada import prohibido da UNA violación por fichero (aunque se importe dos veces), y cada
// símbolo prohibido del puerto, una por fichero. Fichero es la Ruta; Motivo empieza por
// "R9.4.d: " y nombra la ruta del import relativa al módulo ("internal/flujos/contact") y,
// si es un símbolo, "<nombre>.<símbolo>". La salida va ordenada por Fichero y, a igual
// Fichero, por Motivo.
func ProcessImports(modulo string, fuentes []Fuente) []Violacion {
	vs := make([]Violacion, 0)
	for _, f := range fuentes {
		if !bajo(f.Ruta, processDir) {
			continue
		}
		for _, motivo := range judgeProcessFile(modulo, f) {
			vs = append(vs, Violacion{Fichero: f.Ruta, Motivo: "R9.4.d: " + motivo})
		}
	}
	sort.Slice(vs, func(i, j int) bool {
		if vs[i].Fichero != vs[j].Fichero {
			return vs[i].Fichero < vs[j].Fichero
		}
		return vs[i].Motivo < vs[j].Motivo
	})
	return vs
}

// internalImport es un import directo de internal/ de un fichero: su ruta relativa al módulo
// y el nombre local con que el fichero lo ve (el alias, «.», «_» o el último elemento).
type internalImport struct {
	rel   string
	local string
}

// internalImports devuelve los imports de internal/ de f, sin repetir ruta (gana el primero).
func internalImports(modulo string, f Fuente) []internalImport {
	var out []internalImport
	seen := make(map[string]bool)
	for _, imp := range f.Archivo.Imports {
		rel, ok := relativizar(modulo, imp.Path.Value)
		if !ok || !bajo(rel, internalDir) || seen[rel] {
			continue
		}
		seen[rel] = true
		local := path.Base(rel)
		if imp.Name != nil {
			local = imp.Name.Name
		}
		out = append(out, internalImport{rel: rel, local: local})
	}
	return out
}

// judgeProcessFile aplica las reglas 1–3 a un fichero de test/procesos y devuelve los motivos.
func judgeProcessFile(modulo string, f Fuente) []string {
	imports := internalImports(modulo, f)
	isLock := slices.Contains(processLockPaths, f.Ruta)
	isContract := isProcessContractFile(f.Ruta)
	// Los puertos que este fichero puede importar: el padre de cada …helpertest que importa y,
	// si el par está en ContractAdapterDirs (D-F2-9), su adaptador.
	ports := make(map[string]bool)
	if isContract {
		adapters := ContractAdapterDirs()
		for _, imp := range imports {
			if isHelperTestImport(imp.rel) {
				ports[path.Dir(imp.rel)] = true
				if adapter, ok := adapters[imp.rel]; ok {
					ports[adapter] = true
				}
			}
		}
	}

	var motivos []string
	for _, imp := range imports {
		switch {
		case imp.rel == locksPackage:
			switch {
			case !isLock:
				motivos = append(motivos, "importa "+imp.rel+": solo pueden hacerlo los ficheros-candado, por ruta exacta ("+
					strings.Join(processLockPaths, ", ")+")")
			case hasBuildConstraint(f.Archivo):
				motivos = append(motivos, "importa "+imp.rel+" con etiqueta de compilación: un fichero-candado va sin "+
					"etiqueta, para correr en ci-local")
			}
		case !isContract:
			motivos = append(motivos, "importa "+imp.rel+": un proceso entra por las puertas reales y no importa nada de "+
				"internal/ (solo los *_contrato_test.go, y solo su suite …helpertest, el constructor del puerto y crypto)")
		case isHelperTestImport(imp.rel) || imp.rel == constructorArgsPackage:
			// Regla 3a y 3c.
		case ports[imp.rel]:
			motivos = append(motivos, judgePortUse(f.Archivo, imp)...)
		default:
			motivos = append(motivos, "importa "+imp.rel+": una suite de contrato solo importa su …helpertest (bajo "+
				strings.Join(helperTestRoots, " o ")+"), el paquete del puerto que ese helpertest prueba y "+
				constructorArgsPackage)
		}
	}
	return motivos
}

// judgePortUse aplica la regla 3b: del paquete del puerto, solo el constructor.
func judgePortUse(file *ast.File, imp internalImport) []string {
	if imp.local == "." || imp.local == "_" {
		return []string{"importa " + imp.rel + " como «" + imp.local + "»: no se ve qué usa del puerto; impórtalo con nombre"}
	}
	seen := make(map[string]bool)
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if ok && x.Name == imp.local && !strings.HasPrefix(sel.Sel.Name, constructorPrefix) {
			seen[sel.Sel.Name] = true
		}
		return true
	})
	motivos := make([]string, 0, len(seen))
	for name := range seen {
		motivos = append(motivos, "usa "+imp.local+"."+name+" de "+imp.rel+": del paquete del puerto, una suite de "+
			"contrato solo usa el constructor ("+constructorPrefix+"…)")
	}
	return motivos
}

// isProcessContractFile dice si ruta es un fichero que corre una suite de contrato: su nombre
// base termina en «_contrato_test.go» con al menos un carácter delante.
func isProcessContractFile(ruta string) bool {
	base := path.Base(ruta)
	return len(base) > len(processContractSuffix) && strings.HasSuffix(base, processContractSuffix)
}

// isHelperTestImport dice si rel es un paquete de suite de contrato importable: su último
// elemento cumple isHelperTestPackage (el sufijo compuesto de D-F1-10, con algo delante) y
// cuelga de internal/modulos o de internal/nucleo, sin ser uno de los dos. Fuera de esos dos
// árboles solo vale el que es CLAVE de la lista cerrada de ContractAdapterDirs, por igualdad:
// ni un hermano ni un subdirectorio suyo heredan la excepción.
func isHelperTestImport(rel string) bool {
	if !isHelperTestPackage(path.Base(rel)) {
		return false
	}
	for _, root := range helperTestRoots {
		if rel != root && bajo(rel, root) {
			return true
		}
	}
	_, listed := contractAdapterDirs[rel]
	return listed
}

// hasBuildConstraint dice si file lleva una etiqueta de compilación (//go:build o // +build)
// antes de la cláusula package.
func hasBuildConstraint(file *ast.File) bool {
	for _, group := range file.Comments {
		if group.Pos() >= file.Package {
			break
		}
		for _, c := range group.List {
			if strings.HasPrefix(c.Text, "//go:build") || strings.HasPrefix(c.Text, "// +build") ||
				strings.HasPrefix(c.Text, "//+build") {
				return true
			}
		}
	}
	return false
}
