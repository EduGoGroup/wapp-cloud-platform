package stages_test

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/casebank"
)

// ambar_semilla_test.go — EL CANDADO DE LAS DOS COPIAS DEL CASO AMBAR.
//
// El fixture de este paquete (`textoAmbar`, en ambar_fixture_test.go, T2.2) y la
// semilla del banco de casos (`casebank.TextoCasoAmbar`, T5.3) son EL MISMO
// texto, y tienen que serlo: si divergen, los tests de P2/P3/P4 estarían midiendo
// contra un texto y el dataset de evaluación contra otro, y la comparación entre
// «lo que el pipeline hace en el test» y «lo que el pipeline hace en el eval»
// dejaría de significar nada — en silencio.
//
// 🔴 NO SE PUEDE IMPORTAR UNA DE LA OTRA: la de aquí vive en un `_test.go` y no
// existe fuera de la compilación de test; la de allí tiene que vivir en código de
// producción porque la siembra la usa. La copia es inevitable; que la copia se
// desincronice, no.
//
// 💥 Mutación: cambiar una coma en cualquiera de las dos constantes ⇒ rojo.
func TestElCasoAmbarDelFixtureYElDelBancoSonELMISMO(t *testing.T) {
	if textoAmbar != casebank.TextoCasoAmbar {
		t.Errorf("el fixture de stages y la semilla de casebank DIVERGIERON.\n"+
			"stages   (%d bytes): %q\n"+
			"casebank (%d bytes): %q\n"+
			"Las dos son calidad C y describen el mismo caso: si una cambia, la otra también.",
			len(textoAmbar), textoAmbar, len(casebank.TextoCasoAmbar), casebank.TextoCasoAmbar)
	}
}

// TestLasEvidenciasDelFixtureSiguenSiendoSubcadenasDeLaSemilla es la mitad que
// hace útil al test de arriba el día que el texto REAL de Ambar aparezca: al
// pegarlo, las cuatro evidencias del fixture dejan de ser subcadenas y hay que
// ajustarlas — el aviso lo da esto, y no un fallo raro en el anclaje de P2.
func TestLasEvidenciasDelFixtureSiguenSiendoSubcadenasDeLaSemilla(t *testing.T) {
	evidencias := map[string]string{
		"torta de chocolate": evidenciaTortaChocolate,
		"torta de vainilla":  evidenciaTortaVainilla,
		"tequeños":           evidenciaTequenos,
		"entrega":            evidenciaEntrega,
	}
	for nombre, ev := range evidencias {
		t.Run(nombre, func(t *testing.T) {
			// La comparación es insensible a mayúsculas por lo mismo que el
			// anclaje real: `evidenciaTortaChocolate` empieza en minúscula donde
			// el texto dice «Una torta».
			if !contieneIgnorandoMayusculas(casebank.TextoCasoAmbar, ev) {
				t.Errorf("la evidencia %q ya NO aparece en la semilla del banco: ajústala (nunca al revés)", ev)
			}
		})
	}
}

func contieneIgnorandoMayusculas(texto, sub string) bool {
	return len(sub) > 0 && indexIgnorandoMayusculas(texto, sub) >= 0
}

// indexIgnorandoMayusculas evita `strings.ToLower` sobre el texto entero: con
// acentos, plegar mayúsculas cambia longitudes y desplazaría los índices. Aquí
// solo hace falta la presencia, así que se compara rodaja a rodaja.
func indexIgnorandoMayusculas(texto, sub string) int {
	for i := 0; i+len(sub) <= len(texto); i++ {
		if igualIgnorandoMayusculas(texto[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

func igualIgnorandoMayusculas(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
