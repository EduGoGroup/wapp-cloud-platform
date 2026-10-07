//go:build pendiente

package indice_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
)

// ---------------------------------------------------------------------------
// LOS NORMALIZADORES QUE NO CUMPLEN (los usan también indice_test y cache_test)
// ---------------------------------------------------------------------------

// invalidPrefix es el texto de ErrNormalizadorInvalido más la envoltura de fmt.
const invalidPrefix = "catalogo: el normalizador inyectado no cumple el contrato del índice: "

// enyeFoldingNormalizer es el de producción con UN defecto: pliega la ñ como si
// fuera una n con tilde. Es exactamente el error que VerificarNormalizador existe
// para cazar.
func enyeFoldingNormalizer(s string) string {
	return strings.ReplaceAll(textmatch.Normalize(s), "ñ", "n")
}

// nonIdempotentNormalizer PASA todos los casos de la tabla —para cada uno, la
// entrada difiere de su forma normalizada— y solo se delata en la SEGUNDA pasada.
// Es el único modo de que la comprobación de idempotencia sea la que dispare, y no
// un valor equivocado en el primer caso.
func nonIdempotentNormalizer(s string) string {
	n := textmatch.Normalize(s)
	if s == n && s != "" {
		return n + " otra vez"
	}
	return n
}

// exceptionTableNormalizer pasa la tabla y es idempotente sobre ella, pero lleva
// «ano» a «año»: los colapsa sin plegar ninguna ñ de los casos.
func exceptionTableNormalizer(s string) string {
	n := textmatch.Normalize(s)
	if n == "ano" {
		return "año"
	}
	return n
}

// breaking devuelve el normalizador de producción con UNA entrada estropeada.
func breaking(input, output string) indice.Normalizador {
	return func(s string) string {
		if s == input {
			return output
		}
		return textmatch.Normalize(s)
	}
}

// ---------------------------------------------------------------------------
// EL CONTRATO
// ---------------------------------------------------------------------------

// TestVerificarNormalizador_AcceptsTheProductionNormalizer es T-5: el contrato se
// prueba con `textmatch.Normalize` REAL, no con un doble. Si las dos piezas dejan
// de hablar el mismo idioma, se pone rojo aquí.
func TestVerificarNormalizador_AcceptsTheProductionNormalizer(t *testing.T) {
	if err := indice.VerificarNormalizador(textmatch.Normalize); err != nil {
		t.Fatalf("VerificarNormalizador(textmatch.Normalize) = %v; el normalizador de producción cumple el contrato", err)
	}
}

func TestVerificarNormalizador_NilIsErrSinNormalizador(t *testing.T) {
	err := indice.VerificarNormalizador(nil)
	if !errors.Is(err, indice.ErrSinNormalizador) {
		t.Fatalf("VerificarNormalizador(nil) = %v; se esperaba ErrSinNormalizador", err)
	}
	if errors.Is(err, indice.ErrNormalizadorInvalido) {
		t.Errorf("VerificarNormalizador(nil) = %v; «no hay normalizador» no es «no cumple el contrato»", err)
	}
}

// TestVerificarNormalizador_EachCaseOfTheTable estropea UNA entrada de la tabla
// cada vez: cada caso tiene que disparar por sí solo y decir qué entró, qué salió
// y qué exigía el contrato.
func TestVerificarNormalizador_EachCaseOfTheTable(t *testing.T) {
	cases := []struct {
		name, input, broken, want string
	}{
		{"folds latin diacritics", "Café", "café", "cafe"},
		{"lowercases keeping the enye", "PIÑA COLADA", "pina colada", "piña colada"},
		{"the enye is a letter", "Jalapeño", "jalapeno", "jalapeño"},
		{"recomposes the decomposed enye", "An\u0303o Nuevo", "ano nuevo", "año nuevo"},
		{"collapses inner spaces and trims", "  Torta   de   Chocolate  ", "torta   de   chocolate", "torta de chocolate"},
		{"empty stays empty", "", " ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := indice.VerificarNormalizador(breaking(c.input, c.broken))
			if !errors.Is(err, indice.ErrNormalizadorInvalido) {
				t.Fatalf("con %q → %q: error = %v; se esperaba ErrNormalizadorInvalido", c.input, c.broken, err)
			}
			want := invalidPrefix + `con ` + strconv.Quote(c.input) + ` devolvió ` + strconv.Quote(c.broken) +
				` y el contrato exige ` + strconv.Quote(c.want) + ` — `
			if !strings.HasPrefix(err.Error(), want) {
				t.Errorf("error = %q\nse esperaba que empezara por %q", err.Error(), want)
			}
			if len(err.Error()) == len(want) {
				t.Errorf("error = %q; tras la raya tiene que venir el porqué del caso", err.Error())
			}
		})
	}
}

// TestVerificarNormalizador_CatchesThePlausibleSubstitutes: `strings.ToLower` y un
// plegado ingenuo son las dos sustituciones plausibles, y las dos rompen el match
// EN SILENCIO. El contrato las convierte en un error en el arranque.
func TestVerificarNormalizador_CatchesThePlausibleSubstitutes(t *testing.T) {
	t.Run("strings.ToLower does not fold diacritics", func(t *testing.T) {
		err := indice.VerificarNormalizador(strings.ToLower)
		if !errors.Is(err, indice.ErrNormalizadorInvalido) {
			t.Fatalf("VerificarNormalizador(strings.ToLower) = %v; se esperaba ErrNormalizadorInvalido", err)
		}
		if !strings.Contains(err.Error(), `con "Café" devolvió "café" y el contrato exige "cafe"`) {
			t.Errorf("error = %q; tiene que decir el caso que falló", err.Error())
		}
	})

	t.Run("folding the enye", func(t *testing.T) {
		err := indice.VerificarNormalizador(enyeFoldingNormalizer)
		if !errors.Is(err, indice.ErrNormalizadorInvalido) {
			t.Fatalf("VerificarNormalizador(enyeFoldingNormalizer) = %v; se esperaba ErrNormalizadorInvalido", err)
		}
		if !strings.Contains(err.Error(), `con "PIÑA COLADA" devolvió "pina colada"`) {
			t.Errorf("error = %q; el primer caso que cae es el de PIÑA COLADA (la tabla va en orden)", err.Error())
		}
	})
}

func TestVerificarNormalizador_RequiresIdempotence(t *testing.T) {
	err := indice.VerificarNormalizador(nonIdempotentNormalizer)
	if !errors.Is(err, indice.ErrNormalizadorInvalido) {
		t.Fatalf("VerificarNormalizador(nonIdempotentNormalizer) = %v; se esperaba ErrNormalizadorInvalido", err)
	}
	if !strings.HasPrefix(err.Error(), invalidPrefix+"no es idempotente") {
		t.Errorf("error = %q; tiene que decir cuál de las dos mitades del contrato falló («no es idempotente»)", err.Error())
	}
}

// TestVerificarNormalizador_AnoAndAnioMustNotCollapse: la comprobación que la tabla
// no puede dar. El normalizador pasa los seis casos y es idempotente sobre ellos.
func TestVerificarNormalizador_AnoAndAnioMustNotCollapse(t *testing.T) {
	err := indice.VerificarNormalizador(exceptionTableNormalizer)
	if !errors.Is(err, indice.ErrNormalizadorInvalido) {
		t.Fatalf("VerificarNormalizador(exceptionTableNormalizer) = %v; se esperaba ErrNormalizadorInvalido", err)
	}
	if !strings.HasPrefix(err.Error(), invalidPrefix+"colapsa «año» con «ano»") {
		t.Errorf("error = %q; tiene que decir que colapsa «año» con «ano»", err.Error())
	}
}
