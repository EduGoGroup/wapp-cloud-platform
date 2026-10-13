package stages_test

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/casebank"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/evidence"
)

// ambar_seed_test.go — EL CANDADO DE LAS DOS COPIAS DEL CASO AMBAR.
//
// El fixture de este paquete (`ambarText`) y la semilla del banco de casos
// (`casebank.AmbarCaseText`) son EL MISMO texto, y tienen que serlo: si divergen, los
// tests de P2/P3/P4 medirían contra un texto y el dataset de evaluación contra otro.
//
// 🔴 NO SE PUEDE IMPORTAR UNA DE LA OTRA: la de aquí vive en un `_test.go`; la de allí
// tiene que vivir en producción porque la siembra la usa. La copia es inevitable; que
// se desincronice, no.

// TestAmbarFixture_IsTheSameTextAsTheCaseBankSeed: cambiar una coma en cualquiera de las
// dos constantes lo pone rojo.
func TestAmbarFixture_IsTheSameTextAsTheCaseBankSeed(t *testing.T) {
	if ambarText != casebank.AmbarCaseText {
		t.Errorf("el fixture de stages y la semilla de casebank DIVERGIERON.\n"+
			"stages   (%d bytes): %q\n"+
			"casebank (%d bytes): %q\n"+
			"Las dos son calidad C y describen el mismo caso: si una cambia, la otra también.",
			len(ambarText), ambarText, len(casebank.AmbarCaseText), casebank.AmbarCaseText)
	}
}

// TestAmbarFixture_EvidencesAreStillAnchoredInTheSeed es lo que hace útil al test de
// arriba el día que aparezca el texto REAL: al pegarlo, las evidencias del fixture dejan
// de sostenerse y hay que ajustarlas —nunca el anclaje—. Se comprueba con la regla de
// verdad (`evidence`), que es la que usan las etapas.
func TestAmbarFixture_EvidencesAreStillAnchoredInTheSeed(t *testing.T) {
	seed := evidence.Normalize(casebank.AmbarCaseText)
	anchored := map[string]string{
		"chocolate cake": chocolateCakeEvidence,
		"vanilla cake":   vanillaCakeEvidence,
		"tequenos":       tequenosEvidence,
		"delivery":       deliveryEvidence,
	}
	for name, phrase := range anchored {
		t.Run(name, func(t *testing.T) {
			if !evidence.Contains(seed, phrase) {
				t.Errorf("la evidencia %q ya NO aparece en la semilla del banco: ajústala (nunca al revés)", phrase)
			}
		})
	}

	// Y la inventada tiene que seguir sin aparecer: si el texto real la trajera, los
	// tests de descarte dejarían de probar una alucinación.
	if evidence.Contains(seed, inventedEvidence) {
		t.Errorf("la evidencia inventada %q aparece en la semilla: ya no sirve de alucinación; cámbiala", inventedEvidence)
	}
}
