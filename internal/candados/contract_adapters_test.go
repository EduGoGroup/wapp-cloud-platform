//go:build pendiente

package candados

import (
	"maps"
	"testing"
)

// Rutas del único par que hoy promete ContractAdapterDirs (D-F2-9). Los casos de
// ProcessImports las escriben literales, sin pasar por la función: así prueban el candado, no
// la lista.
const (
	iamOutHelperTest = "internal/modulos/acceso/iam/ports/out/outhelpertest"
	iamPostgresDir   = "internal/modulos/acceso/iam/infra/postgres"
)

// TestContractAdapterDirsList: la lista es exactamente la de D-F2-9, ni más ni menos.
func TestContractAdapterDirsList(t *testing.T) {
	got := ContractAdapterDirs()
	want := map[string]string{iamOutHelperTest: iamPostgresDir}
	if !maps.Equal(got, want) {
		t.Errorf("ContractAdapterDirs() = %q; quiero exactamente %q (D-F2-9)", got, want)
	}
}

// TestContractAdapterDirsCopy: el mapa devuelto es una copia; mutarlo (cambiar, añadir o
// borrar un par) no cambia la siguiente llamada.
func TestContractAdapterDirsCopy(t *testing.T) {
	first := ContractAdapterDirs()
	if len(first) == 0 {
		t.Fatalf("ContractAdapterDirs() vacía: no hay nada que mutar")
	}
	first[iamOutHelperTest] = "internal/modulos/acceso/iam/infra/memory"
	first["internal/modulos/otro/ports/out/outhelpertest"] = "internal/modulos/otro/infra/postgres"
	delete(first, iamOutHelperTest)
	want := map[string]string{iamOutHelperTest: iamPostgresDir}
	if got := ContractAdapterDirs(); !maps.Equal(got, want) {
		t.Errorf("tras mutar el mapa devuelto, ContractAdapterDirs() = %q; quiero %q", got, want)
	}
}

// TestProcessImportsContractAdapter: con el par de D-F2-9, una suite de contrato que importa
// outhelpertest puede importar el adaptador iam/infra/postgres y usar sus constructores (con
// o sin alias). Muerde si usa algo que no es constructor, si importa el adaptador sin su
// helpertest (o con el helpertest de otro puerto), si importa otro adaptador hermano no
// listado (infra/memory), un subdirectorio del listado o un hermano con prefijo común, y si
// lo importa con punto.
func TestProcessImportsContractAdapter(t *testing.T) {
	const (
		out      = moduloWapp + "/" + iamOutHelperTest
		pg       = moduloWapp + "/" + iamPostgresDir
		ent      = moduloWapp + "/internal/modulos/acceso/entitlements/entitlementshelpertest"
		memory   = moduloWapp + "/internal/modulos/acceso/iam/infra/memory"
		pgSub    = pg + "/sub"
		pgPrefix = pg + "x"
	)
	fuentes := []Fuente{
		fuenteEnMemoria(t, "test/procesos/iam_contrato_test.go", `package procesos
import (
	"`+out+`"
	iampg "`+pg+`"
)
var _, _, _ = outhelpertest.Contrato, iampg.NewMembershipRepo, iampg.NewRoleRepo
`),
		fuenteEnMemoria(t, "test/procesos/plain_contrato_test.go", `package procesos
import (
	"`+out+`"
	"`+pg+`"
)
var _, _ = outhelpertest.Contrato, postgres.NewAuditRepo
`),
		fuenteEnMemoria(t, "test/procesos/symbol_contrato_test.go", `package procesos
import (
	"`+out+`"
	"`+pg+`"
)
var _, _, _ = outhelpertest.Contrato, postgres.NewAuditRepo, postgres.GrantTenantAccess
`),
		fuenteEnMemoria(t, "test/procesos/alone_contrato_test.go", `package procesos
import "`+pg+`"
var _ = postgres.NewAuditRepo
`),
		fuenteEnMemoria(t, "test/procesos/other_suite_contrato_test.go", `package procesos
import (
	"`+ent+`"
	"`+pg+`"
)
var _, _ = entitlementshelpertest.Contrato, postgres.NewAuditRepo
`),
		fuenteEnMemoria(t, "test/procesos/sibling_contrato_test.go", `package procesos
import (
	"`+out+`"
	"`+memory+`"
	"`+pgSub+`"
	"`+pgPrefix+`"
)
var _, _, _, _ = outhelpertest.Contrato, memory.NewStore, sub.NewX, postgresx.NewX
`),
		fuenteEnMemoria(t, "test/procesos/dot_adapter_contrato_test.go", `package procesos
import (
	"`+out+`"
	. "`+pg+`"
)
var _, _ = outhelpertest.Contrato, NewAuditRepo
`),
	}
	vs := ProcessImports(moduloWapp, fuentes)
	exigeNingunaEn(t, vs, "test/procesos/iam_contrato_test.go")
	exigeNingunaEn(t, vs, "test/procesos/plain_contrato_test.go")
	exigeViolacion(t, vs, "test/procesos/symbol_contrato_test.go",
		"usa postgres.GrantTenantAccess de "+iamPostgresDir+":", "solo usa el constructor (New…)")
	exigeViolacion(t, vs, "test/procesos/alone_contrato_test.go",
		"importa "+iamPostgresDir+":", "una suite de contrato solo importa")
	exigeViolacion(t, vs, "test/procesos/other_suite_contrato_test.go",
		"importa "+iamPostgresDir+":", "una suite de contrato solo importa")
	exigeViolacion(t, vs, "test/procesos/sibling_contrato_test.go",
		"importa internal/modulos/acceso/iam/infra/memory:", "una suite de contrato solo importa")
	exigeViolacion(t, vs, "test/procesos/sibling_contrato_test.go",
		"importa "+iamPostgresDir+"/sub:", "una suite de contrato solo importa")
	exigeViolacion(t, vs, "test/procesos/sibling_contrato_test.go",
		"importa "+iamPostgresDir+"x:", "una suite de contrato solo importa")
	exigeViolacion(t, vs, "test/procesos/dot_adapter_contrato_test.go",
		"importa "+iamPostgresDir+" como «.»", "impórtalo con nombre")
	if len(vs) != 7 {
		t.Errorf("se esperaban 7 violaciones; hay %d: %v", len(vs), vs)
	}
	exigeOrdenadas(t, vs)
}
