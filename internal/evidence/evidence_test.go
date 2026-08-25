package evidence_test

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/evidence"
)

// TestContains_LasTresDecisionesDeLaRegla fija las tres decisiones del paquete, que es
// lo que hay que proteger tras la mudanza de T2.2: la regla salió de
// `intakeahead/saneo.go` y tiene que seguir diciendo EXACTAMENTE lo mismo.
//
// 💥 MUTACIÓN que lo pone rojo: en Normalize, añadir un plegado de acentos ⇒ el caso
// «café/cafe» pasaría a aceptarse. Otra: quitar el strings.ToLower ⇒ cae el caso de las
// mayúsculas. Otra: devolver true con la frase vacía ⇒ cae el último caso.
func TestContains_LasTresDecisionesDeLaRegla(t *testing.T) {
	const texto = "Hola, quería UNA TORTA\nde chocolate para el café de la tarde"
	norm := evidence.Normalize(texto)

	casos := []struct {
		nombre string
		frase  string
		quiero bool
	}{
		{"la frase literal aparece", "de chocolate para el café", true},
		{"las mayúsculas no cuentan", "una torta", true},
		{"el salto de línea es un espacio", "quería una torta de chocolate", true},
		{"🔴 los acentos SÍ cuentan: reescribir no es copiar", "para el cafe de la tarde", false},
		{"una palabra suelta no basta: la granularidad es la frase", "torta chocolate", false},
		{"lo que no está, no está", "y dos bandejas de tequeños", false},
		{"la frase vacía no respalda nada", "", false},
		{"solo blancos tampoco", "   \n\t ", false},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := evidence.Contains(norm, c.frase); got != c.quiero {
				t.Fatalf("Contains(%q) = %v, se esperaba %v", c.frase, got, c.quiero)
			}
		})
	}
}

// TestNormalize_ColapsaBlancosYBaja comprueba la normalización por su cuenta: se exporta
// para que quien compare N frases contra el MISMO texto lo normalice UNA vez, así que es
// parte del contrato y no un detalle interno.
func TestNormalize_ColapsaBlancosYBaja(t *testing.T) {
	got := evidence.Normalize("  Dos\tTORTAS\n\n y   un   paquete  ")
	if want := "dos tortas y un paquete"; got != want {
		t.Fatalf("Normalize = %q, se esperaba %q", got, want)
	}
}
