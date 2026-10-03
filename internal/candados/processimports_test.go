package candados

import (
	"strings"
	"testing"
)

// dirsProcesos: el candado recorre test/procesos, tests incluidos.
var dirsProcesos = []string{"test/procesos"}

// TestProcessImportsMuerde: cada regla de ProcessImports dispara sobre su fichero, nombrando la
// ruta del import (o el símbolo), y cada import o símbolo prohibido da UNA violación.
func TestProcessImportsMuerde(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/processimports/muerde", dirsProcesos, true)
	vs := ProcessImports(moduloWapp, fuentes)

	const d = "test/procesos/"
	casos := []struct {
		nombre  string
		fichero string
		trozos  []string
	}{
		{"rule 1: a process imports an old domain package", d + "p1_old_domain_test.go",
			[]string{"R9.4.d: ", "importa internal/flujos/contact", "un proceso entra por las puertas reales"}},
		{"rule 1: a process imports a package a contract suite is allowed to", d + "p2_new_domain_test.go",
			[]string{"R9.4.d: ", "importa internal/nucleo/contact:", "un proceso entra por las puertas reales"}},
		{"rule 1: the suite outside a contract file", d + "p3_suite_outside_contract_test.go",
			[]string{"importa internal/nucleo/contact/contacthelpertest:", "un proceso entra por las puertas reales"}},
		{"rule 1: crypto outside a contract file", d + "p3_suite_outside_contract_test.go",
			[]string{"importa internal/platform/crypto:", "un proceso entra por las puertas reales"}},
		{"rule 2: internal/candados from a file that is not a lock", d + "other_lock_test.go",
			[]string{"importa internal/candados:", "ficheros-candado, por ruta exacta",
				"test/procesos/domain_imports_test.go", "test/procesos/sin_bd_viva_test.go"}},
		{"rule 2: the lock path is exact, not a base name", d + "sub/sin_bd_viva_test.go",
			[]string{"importa internal/candados:", "ficheros-candado, por ruta exacta"}},
		{"rule 2: a lock file with a build tag", d + "domain_imports_test.go",
			[]string{"importa internal/candados con etiqueta de compilación", "ci-local"}},
		{"rule 2: a lock file imports nothing else from internal", d + "sin_bd_viva_test.go",
			[]string{"importa internal/platform/config:", "un proceso entra por las puertas reales"}},
		{"rule 3: a contract suite imports a domain package", d + "wrong_contrato_test.go",
			[]string{"importa internal/flujos/store:", "una suite de contrato solo importa su …helpertest"}},
		{"rule 3: a contract suite imports a platform package that is not crypto", d + "wrong_contrato_test.go",
			[]string{"importa internal/platform/storage/postgres:", "una suite de contrato solo importa"}},
		{"rule 3b: a port symbol that is not a constructor", d + "wrong_contrato_test.go",
			[]string{"usa contact.Normalize de internal/nucleo/contact:", "solo usa el constructor (New…)"}},
		{"rule 3b: a port type", d + "wrong_contrato_test.go",
			[]string{"usa contact.Ref de internal/nucleo/contact:", "solo usa el constructor (New…)"}},
		{"rule 3b: the port without its helpertest in the same file", d + "orphan_contrato_test.go",
			[]string{"importa internal/nucleo/contact:", "una suite de contrato solo importa"}},
		{"rule 3a: a package ending in test but not in helpertest", d + "orphan_contrato_test.go",
			[]string{"importa internal/nucleo/latest:", "una suite de contrato solo importa"}},
		{"rule 3a: the bare suffix is nobody's suite", d + "orphan_contrato_test.go",
			[]string{"importa internal/nucleo/helpertest:", "una suite de contrato solo importa"}},
		{"rule 3a: a helpertest outside modulos and nucleo", d + "orphan_contrato_test.go",
			[]string{"importa internal/platform/xhelpertest:", "una suite de contrato solo importa"}},
		{"rule 3b: the parent of a rejected helpertest is not a port", d + "orphan_contrato_test.go",
			[]string{"importa internal/modulos:", "una suite de contrato solo importa"}},
		{"rule 3b: dot import of the port", d + "dot_contrato_test.go",
			[]string{"importa internal/nucleo/contact como «.»", "impórtalo con nombre"}},
		{"rule 3b: an alias does not hide the symbol", d + "alias_contrato_test.go",
			[]string{"usa port.KindPhoneE164 de internal/nucleo/contact:", "solo usa el constructor (New…)"}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			exigeViolacion(t, vs, c.fichero, c.trozos...)
		})
	}
	if len(vs) != len(casos) {
		t.Errorf("se esperaban %d violaciones (una por import o símbolo prohibido); hay %d: %v", len(casos), len(vs), vs)
	}
	for _, v := range vs {
		if !strings.HasPrefix(v.Motivo, "R9.4.d: ") {
			t.Errorf("el motivo no empieza por «R9.4.d: »: %v", v)
		}
	}
	exigeNingunaEn(t, vs, d+"doc.go")
	exigeOrdenadas(t, vs)
}

// TestProcessImportsPasa: los dos ficheros-candado sin etiqueta, una suite de contrato con su
// helpertest, el constructor del puerto (también con alias, y cualquier New…) y crypto, y un
// proceso que solo importa la biblioteca estándar, terceros y otros módulos no dan violación.
// Tampoco la ruta del módulo dentro de un literal, ni un valor local llamado como el puerto en
// un fichero que no lo importa.
func TestProcessImportsPasa(t *testing.T) {
	fuentes := recorrerCaso(t, "testdata/processimports/pasa", dirsProcesos, true)
	exigeCero(t, ProcessImports(moduloWapp, fuentes))
}

// TestProcessImportsBordes: el mismo import dos veces da UNA violación y el mismo símbolo usado
// dos veces, una; los ficheros de fuera de test/procesos se ignoran (el candado es de los
// procesos, no del árbol); «_contrato_test.go» a secas no es una suite; el import en blanco del
// puerto muerde; y una variable local con el nombre del puerto cuenta como el puerto (el
// candado no resuelve ámbitos y prefiere morder de más).
func TestProcessImportsBordes(t *testing.T) {
	const contactPath = moduloWapp + "/internal/nucleo/contact"
	fuentes := []Fuente{
		fuenteEnMemoria(t, "test/procesos/twice_test.go", `package procesos
import (
	a "`+contactPath+`"
	b "`+contactPath+`"
)
var _, _ = a.Normalize, b.Normalize
`),
		fuenteEnMemoria(t, "internal/arranque/outside_test.go", `package arranque
import "`+contactPath+`"
var _ = contact.Normalize
`),
		fuenteEnMemoria(t, "test/procesosx/outside_test.go", `package procesosx
import "`+contactPath+`"
var _ = contact.Normalize
`),
		fuenteEnMemoria(t, "test/procesos/_contrato_test.go", `package procesos
import (
	"`+contactPath+`"
	"`+contactPath+`/contacthelpertest"
)
var _, _ = contact.NewPostgresResolver, contacthelpertest.Contrato
`),
		fuenteEnMemoria(t, "test/procesos/blank_contrato_test.go", `package procesos
import (
	_ "`+contactPath+`"
	"`+contactPath+`/contacthelpertest"
)
var _ = contacthelpertest.Contrato
`),
		fuenteEnMemoria(t, "test/procesos/shadow_contrato_test.go", `package procesos
import (
	"`+contactPath+`"
	"`+contactPath+`/contacthelpertest"
)
func shadow() {
	_, _ = contact.NewPostgresResolver, contacthelpertest.Contrato
	contact := struct{ ID string }{}
	_, _ = contact.ID, contact.ID
}
`),
	}
	vs := ProcessImports(moduloWapp, fuentes)
	exigeViolacion(t, vs, "test/procesos/twice_test.go", "importa internal/nucleo/contact:")
	exigeViolacion(t, vs, "test/procesos/_contrato_test.go", "importa internal/nucleo/contact:", "un proceso entra")
	exigeViolacion(t, vs, "test/procesos/_contrato_test.go", "importa internal/nucleo/contact/contacthelpertest:")
	exigeViolacion(t, vs, "test/procesos/blank_contrato_test.go", "importa internal/nucleo/contact como «_»")
	exigeViolacion(t, vs, "test/procesos/shadow_contrato_test.go", "usa contact.ID de internal/nucleo/contact:")
	exigeNingunaEn(t, vs, "internal/arranque/outside_test.go")
	exigeNingunaEn(t, vs, "test/procesosx/outside_test.go")
	if len(vs) != 5 {
		t.Errorf("se esperaban 5 violaciones; hay %d: %v", len(vs), vs)
	}
	exigeOrdenadas(t, vs)
}
