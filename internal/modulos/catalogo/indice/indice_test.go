//go:build pendiente

package indice_test

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
)

// indice_test.go cubre Construir y las búsquedas. El oráculo lineal y los corpus
// están en helpers_test.go; el rendimiento (D-F5-3), en indice_performance_test.go.

// ---------------------------------------------------------------------------
// LITERALES OBSERVABLES (T-4): EL RENOMBRE DEL PAQUETE NO LOS CAMBIA
// ---------------------------------------------------------------------------

func TestErrors_KeepTheCatalogoPrefix(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"ErrSinNormalizador", indice.ErrSinNormalizador, "catalogo: el índice necesita el normalizador de textos (hoy, wapp-shared/textmatch.Normalize)"},
		{"ErrNormalizadorInvalido", indice.ErrNormalizadorInvalido, "catalogo: el normalizador inyectado no cumple el contrato del índice"},
		{"ErrCatalogoDemasiadoGrande", indice.ErrCatalogoDemasiadoGrande, "catalogo: el catálogo excede el tope de artículos del índice"},
	}
	for _, c := range cases {
		if c.err.Error() != c.want {
			t.Errorf("%s = %q; el texto es literal: %q", c.name, c.err.Error(), c.want)
		}
	}
}

func TestMaxArticulos_IsTwoThousand(t *testing.T) {
	if indice.MaxArticulos != 2000 {
		t.Errorf("MaxArticulos = %d; la cota de D-044.44 es 2000", indice.MaxArticulos)
	}
}

// ---------------------------------------------------------------------------
// CORRECCIÓN: EL ÍNDICE DA EXACTAMENTE LO MISMO QUE LA BÚSQUEDA LINEAL INGENUA
// ---------------------------------------------------------------------------

// TestDifferential_IndexAgainstLinearSearch interroga a los dos con el mismo corpus
// y exige IGUALDAD ELEMENTO A ELEMENTO, orden incluido (y nil donde el oráculo da
// nil).
//
// 🔴 El corpus incluye a propósito los casos en los que un índice mal construido
// coincide con el oráculo POR CASUALIDAD: consultas que no casan nada, la cadena
// vacía, labels que normalizan igual entre sí, el par «año»/«ano» y las formas
// adversarias (separadores repetidos, dígitos no ASCII, espacios Unicode).
func TestDifferential_IndexAgainstLinearSearch(t *testing.T) {
	cat := trickyCatalog()
	idx := trickyIndex(t)

	t.Run("by sku", func(t *testing.T) {
		for _, sku := range skuQueries() {
			want, wantOK := linearBySKU(cat, sku)
			got, ok := idx.PorSKU(sku)
			if ok != wantOK {
				t.Errorf("PorSKU(%q): hay = %v; la búsqueda lineal dice %v", sku, ok, wantOK)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("PorSKU(%q) = %+v\nla búsqueda lineal da %+v", sku, got, want)
			}
		}
	})

	searches := []struct {
		name   string
		index  func(string) []indice.Coincidencia
		linear func(catalogo.Catalog, string) []indice.Coincidencia
	}{
		{"PorEtiqueta", idx.PorEtiqueta, linearByLabel},
		{"PorTag", idx.PorTag, linearByTag},
		{"PorVariante", idx.PorVariante, linearByVariant},
	}
	for _, s := range searches {
		t.Run(s.name, func(t *testing.T) {
			for _, q := range queries() {
				got, want := s.index(q), s.linear(cat, q)
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s(%q) = %+v\nla búsqueda lineal da %+v", s.name, q, got, want)
				}
			}
		})
	}
}

// TestDifferential_TheCorpusReallyExercises es la guarda contra el test diferencial
// hueco: dos implementaciones que devuelven SIEMPRE nil también coinciden. El
// corpus tiene que producir aciertos de los cuatro tipos —y con las formas que
// hacen difícil el problema—.
func TestDifferential_TheCorpusReallyExercises(t *testing.T) {
	idx := trickyIndex(t)

	if _, ok := idx.PorSKU("TORTA"); !ok {
		t.Errorf("PorSKU(TORTA): sin acierto; el corpus tiene que acertar por sku")
	}
	cases := []struct {
		name string
		got  []indice.Coincidencia
		want []string
	}{
		{"three labels normalise to «cafe»", idx.PorEtiqueta("café"), []string{"CAFE", "CAFE-2", "TE"}},
		{"two articles share the tag «caliente», each once", idx.PorTag("caliente"), []string{"CAFE", "CAFE-2"}},
		{"the tag «clásico» crosses two categories", idx.PorTag("clásico"), []string{"CAFE", "CAFE"}},
		{"two variants of one article normalise to «grande»", idx.PorVariante("grande"), []string{"TORTA", "TORTA"}},
		{"the corpus includes misses", idx.PorEtiqueta("no existe"), []string{}},
	}
	for _, c := range cases {
		if got := skus(c.got); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: skus = %q; se esperaba %q", c.name, got, c.want)
		}
	}
	for name, got := range map[string][]indice.Coincidencia{
		"PorEtiqueta": idx.PorEtiqueta("no existe"),
		"PorTag":      idx.PorTag("no existe"),
		"PorVariante": idx.PorVariante("no existe"),
	} {
		if got != nil {
			t.Errorf("%s sin acierto = %+v; se esperaba nil", name, got)
		}
	}
}

// TestIndice_AdversarialCorpus fija lo que el índice hace con el corpus adversario
// (reglas.md §5). No «arregla» nada: es lo que hace el viejo.
func TestIndice_AdversarialCorpus(t *testing.T) {
	idx := trickyIndex(t)

	t.Run("unicode spaces in the sku are part of the key", func(t *testing.T) {
		nbsp, ok := idx.PorSKU("CAFE\u00a0")
		if !ok || nbsp.Categoria != "3" {
			t.Errorf("PorSKU(CAFE+NBSP) = %+v, %v; es un sku distinto de CAFE y vive en la categoría 3", nbsp, ok)
		}
		if _, ok := idx.PorSKU("CAFE "); ok {
			t.Errorf("PorSKU(%q) acertó; un espacio ASCII no es el NBSP del catálogo", "CAFE ")
		}
		if lead, ok := idx.PorSKU(" TORTA"); !ok || lead.Articulo.Price != 3.1 {
			t.Errorf("PorSKU(%q) = %+v, %v; el espacio de delante no se recorta", " TORTA", lead, ok)
		}
		if _, ok := idx.PorSKU("ＣＡＦＥ"); ok {
			t.Errorf("PorSKU(ancho completo) acertó; el sku no se pliega")
		}
	})

	t.Run("the empty sku and the blank label are keys like any other", func(t *testing.T) {
		empty, ok := idx.PorSKU("")
		if !ok || empty.Articulo.Price != 3.5 {
			t.Errorf("PorSKU(\"\") = %+v, %v; el artículo de sku vacío se indexa bajo \"\"", empty, ok)
		}
		if got, want := skus(idx.PorEtiqueta("")), []string{"SINLABEL", ""}; !reflect.DeepEqual(got, want) {
			t.Errorf("PorEtiqueta(\"\") skus = %q; se esperaba %q (label vacío y label de solo espacios)", got, want)
		}
	})

	lists := []struct {
		name string
		got  []indice.Coincidencia
		want []string
	}{
		{"NBSP in a label is a space; zero width is not", idx.PorEtiqueta("té verde"), []string{"CAFE\u00a0"}},
		{"zero width stays in the key", idx.PorEtiqueta("té\u200bverde"), []string{" TORTA"}},
		{"two tags equal after unicode spaces: the article once", idx.PorTag("sin gluten"), []string{"CAFE\u00a0"}},
		{"repeated separator in a tag", idx.PorTag("sin;;gluten"), []string{"A@@B"}},
		{"single separator in a tag", idx.PorTag("sin;gluten"), []string{"A@B"}},
		{"repeated pipe in a tag", idx.PorTag("sin||tacc"), []string{"A@@B"}},
		{"single pipe does not match the repeated one", idx.PorTag("sin|tacc"), []string{}},
		{"repeated separator in a label", idx.PorEtiqueta("A@@B"), []string{"A@@B"}},
		{"single separator in a label", idx.PorEtiqueta("a@b"), []string{"A@B"}},
		{"arabic-indic digits are not folded", idx.PorEtiqueta("pack 12"), []string{}},
		{"arabic-indic digits match themselves", idx.PorEtiqueta("PACK ١٢"), []string{"PACK"}},
		{"ideographic space in a variant is a space", idx.PorVariante("12 porciones"), []string{"TORTA", "PACK"}},
		{"fullwidth digits in a variant are not folded", idx.PorVariante("１２ porciones"), []string{"PACK"}},
	}
	for _, c := range lists {
		if got := skus(c.got); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: skus = %q; se esperaba %q", c.name, got, c.want)
		}
	}
	if got := idx.PorVariante("12 porciones"); len(got) == 2 && (got[0].Variante.Price != 30 || got[1].Variante.Price != 32) {
		t.Errorf("PorVariante(12 porciones) precios = %v, %v; se esperaba 30 y 32", got[0].Variante.Price, got[1].Variante.Price)
	}
}

// TestIndice_TheEnyeSurvives es el invariante de T3.1 visto desde el índice: «Piña
// colada» y «Pina colada» son DOS artículos y ninguna consulta puede confundirlos.
func TestIndice_TheEnyeSurvives(t *testing.T) {
	idx := trickyIndex(t)
	if got, want := skus(idx.PorEtiqueta("PIÑA COLADA")), []string{"PINA"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PorEtiqueta(PIÑA COLADA) skus = %q; se esperaba %q", got, want)
	}
	if got, want := skus(idx.PorEtiqueta("pina colada")), []string{"PINA-SIN"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PorEtiqueta(pina colada) skus = %q; se esperaba %q", got, want)
	}
}

// TestIndice_TheVariantTravelsWithItsPrice: sin la variante en la coincidencia,
// quien cotiza no sabría qué cobrar (D-041.4). Las dos variantes que normalizan
// igual salen las dos, en su orden y con precios distintos.
func TestIndice_TheVariantTravelsWithItsPrice(t *testing.T) {
	idx := trickyIndex(t)
	got := idx.PorVariante("Grande")
	if len(got) != 2 {
		t.Fatalf("PorVariante(Grande) = %d coincidencias; se esperaban 2", len(got))
	}
	wantVariants := []catalogo.Variant{{Code: "V1", Label: "Grande", Price: 25}, {Code: "V2", Label: "grande", Price: 26}}
	for i, m := range got {
		if !m.HayVariante {
			t.Errorf("coincidencia %d: HayVariante = false; una coincidencia por variante tiene que decirlo", i)
		}
		if m.Articulo.SKU != "TORTA" || m.Categoria != "2" || m.CategoriaLabel != "Postres" {
			t.Errorf("coincidencia %d = %s / %s / %s; se esperaba TORTA de 2 / Postres", i, m.Articulo.SKU, m.Categoria, m.CategoriaLabel)
		}
		if m.Variante != wantVariants[i] {
			t.Errorf("coincidencia %d: Variante = %+v; se esperaba %+v", i, m.Variante, wantVariants[i])
		}
	}

	// Por las otras tres vías la variante queda a cero y HayVariante en false.
	bySKU, _ := idx.PorSKU("TORTA")
	others := append([]indice.Coincidencia{bySKU}, idx.PorEtiqueta("torta de chocolate")...)
	others = append(others, idx.PorTag("clásico")...)
	if len(others) != 4 {
		t.Fatalf("se esperaban 4 coincidencias sin variante (1 por sku, 1 por etiqueta, 2 por tag); hay %d", len(others))
	}
	for i, m := range others {
		if m.HayVariante || m.Variante != (catalogo.Variant{}) {
			t.Errorf("coincidencia %d sin variante: HayVariante = %v, Variante = %+v; se esperaba false y cero", i, m.HayVariante, m.Variante)
		}
	}
}

// TestIndice_RepeatedSKU_FirstWins: un sku duplicado es un catálogo roto que el
// runtime no puede rechazar (el import sí). La regla es «el primero en orden de
// documento», y se fija aquí para que no cambie por accidente.
func TestIndice_RepeatedSKU_FirstWins(t *testing.T) {
	got, ok := trickyIndex(t).PorSKU("CAFE")
	if !ok {
		t.Fatalf("PorSKU(CAFE): sin acierto")
	}
	if got.Categoria != "1" || got.CategoriaLabel != "Bebidas" || got.Articulo.Label != "Café" {
		t.Errorf("PorSKU(CAFE) = %s / %s / %q; el CAFE de Bebidas va antes que el de Postres",
			got.Categoria, got.CategoriaLabel, got.Articulo.Label)
	}
}

// TestIndice_TheSKUIsNotNormalised: el sku es un identificador opaco del dueño, no
// texto del cliente. Si se normalizara, "cafe" casaría "CAFE".
func TestIndice_TheSKUIsNotNormalised(t *testing.T) {
	got, ok := trickyIndex(t).PorSKU("cafe")
	if ok {
		t.Errorf("PorSKU(cafe) acertó; el sku se compara literal")
	}
	if !reflect.DeepEqual(got, indice.Coincidencia{}) {
		t.Errorf("PorSKU(cafe) = %+v; sin acierto devuelve la coincidencia cero", got)
	}
}

// TestIndice_WalkInDocumentOrder: Articulos, Etiqueta y En forman el recorrido de
// solo lectura, en el orden exacto del documento.
func TestIndice_WalkInDocumentOrder(t *testing.T) {
	idx := trickyIndex(t)
	walk := linearWalk(trickyCatalog())

	if idx.Articulos() != len(walk) || idx.Articulos() != 15 {
		t.Fatalf("Articulos() = %d; el catálogo tramposo tiene %d (15)", idx.Articulos(), len(walk))
	}
	for n, want := range walk {
		if got := idx.Etiqueta(n); got != want.Articulo.Label {
			t.Errorf("Etiqueta(%d) = %q; se esperaba la etiqueta CRUDA %q", n, got, want.Articulo.Label)
		}
		if got := idx.En(n); !reflect.DeepEqual(got, want) {
			t.Errorf("En(%d) = %+v\nse esperaba %+v", n, got, want)
		}
	}
	if got := idx.Etiqueta(2); got != "  CAFÉ  " {
		t.Errorf("Etiqueta(2) = %q; la etiqueta sale sin normalizar", got)
	}
	for _, n := range []int{-1, len(walk), len(walk) + 1} {
		if got := idx.Etiqueta(n); got != "" {
			t.Errorf("Etiqueta(%d) = %q; fuera de rango devuelve \"\"", n, got)
		}
		if got := idx.En(n); !reflect.DeepEqual(got, indice.Coincidencia{}) {
			t.Errorf("En(%d) = %+v; fuera de rango devuelve la coincidencia cero", n, got)
		}
	}
}

// TestIndice_IsNotAFuente es «el índice no puede leer» dicho sobre los tipos
// (D-F5-2): una búsqueda por ítem no tiene con qué ir a Postgres. El día que
// alguien le cuelgue un LeerCatalogo al índice, esto se pone rojo.
func TestIndice_IsNotAFuente(t *testing.T) {
	if _, ok := any((*indice.Indice)(nil)).(indice.Fuente); ok {
		t.Errorf("*Indice satisface Fuente: 🔴 el índice NO debe poder leer; si puede, la garantía deja de ser estructural")
	}
	if _, ok := any((*indice.Indice)(nil)).(indice.LectorContenido); ok {
		t.Errorf("*Indice satisface LectorContenido: el índice no debe poder leer tenant_content")
	}
}

// TestConstruir_HasSeenNoBytes:Construir indexa un catálogo, no un documento. La
// procedencia la pone la caché.
func TestConstruir_HasSeenNoBytes(t *testing.T) {
	idx := trickyIndex(t)
	if idx.Hash() != "" {
		t.Errorf("Hash() = %q; un índice de Construir no tiene huella", idx.Hash())
	}
	if !idx.Sello().IsZero() {
		t.Errorf("Sello() = %v; un índice de Construir no tiene sello", idx.Sello())
	}
}

// TestIndice_ConcurrentReads: el índice es de solo lectura una vez construido. Con
// -race, una búsqueda que escribiera en sus mapas se delata aquí.
func TestIndice_ConcurrentReads(t *testing.T) {
	idx := trickyIndex(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for _, q := range queries() {
				searchAll(idx, q)
			}
		})
	}
	wg.Wait()
}

// searchAll hace las seis lecturas del índice con una misma consulta.
func searchAll(idx *indice.Indice, q string) {
	idx.PorSKU(q)
	idx.PorEtiqueta(q)
	idx.PorTag(q)
	idx.PorVariante(q)
	idx.Etiqueta(len(q))
	idx.En(len(q))
}

// ---------------------------------------------------------------------------
// LA COTA DE TAMAÑO (D-044.44)
// ---------------------------------------------------------------------------

// TestConstruir_Bound2000In2001Out es un test de FRONTERA: los dos lados del límite
// se construyen de verdad y se comprueban por separado. Los números van literales a
// propósito —2000 y 2001— para que comparar contra la constante que se quiere
// proteger no haga pasar el test con cualquier valor.
func TestConstruir_Bound2000In2001Out(t *testing.T) {
	idx, err := indice.Construir(sizedCatalog(2000), textmatch.Normalize)
	if err != nil {
		t.Fatalf("Construir(2000 artículos) = %v; 2.000 son exactamente el tope: tienen que entrar", err)
	}
	if idx.Articulos() != 2000 {
		t.Errorf("Articulos() = %d; se esperaban 2000", idx.Articulos())
	}

	idx, err = indice.Construir(sizedCatalog(2001), textmatch.Normalize)
	if !errors.Is(err, indice.ErrCatalogoDemasiadoGrande) {
		t.Fatalf("Construir(2001 artículos) = %v; se esperaba ErrCatalogoDemasiadoGrande", err)
	}
	if idx != nil {
		t.Errorf("Construir(2001 artículos) devolvió un índice; se rechaza, no se trunca")
	}
	want := "catalogo: el catálogo excede el tope de artículos del índice: trae 2001 artículos y el tope es 2000"
	if err.Error() != want {
		t.Errorf("error = %q; se esperaba %q", err.Error(), want)
	}
}

// TestConstruir_BoundCountsAllCategories: el tope es del documento entero, no por
// categoría. Diez categorías de 250 artículos son 2.500 y sobran, aunque ninguna
// pase de 2.000 por sí sola.
func TestConstruir_BoundCountsAllCategories(t *testing.T) {
	cat := catalogo.Catalog{}
	for c := range 10 {
		category := catalogo.Category{Code: strconv.Itoa(c), Label: "cat"}
		for a := range 250 {
			category.Items = append(category.Items, catalogo.Article{SKU: fmt.Sprintf("S-%d-%d", c, a), Label: "x"})
		}
		cat.Categories = append(cat.Categories, category)
	}
	_, err := indice.Construir(cat, textmatch.Normalize)
	if !errors.Is(err, indice.ErrCatalogoDemasiadoGrande) {
		t.Fatalf("Construir(10 × 250) = %v; se esperaba ErrCatalogoDemasiadoGrande", err)
	}
	want := "catalogo: el catálogo excede el tope de artículos del índice: trae 2500 artículos y el tope es 2000"
	if err.Error() != want {
		t.Errorf("error = %q; se esperaba %q", err.Error(), want)
	}
}

// TestConstruir_EmptyCatalog: un catálogo sin categorías es un índice vacío, no un
// error.
func TestConstruir_EmptyCatalog(t *testing.T) {
	idx, err := indice.Construir(catalogo.Catalog{}, textmatch.Normalize)
	if err != nil {
		t.Fatalf("Construir(catálogo vacío) = %v; se esperaba un índice vacío", err)
	}
	if idx.Articulos() != 0 {
		t.Errorf("Articulos() = %d; se esperaba 0", idx.Articulos())
	}
	if _, ok := idx.PorSKU(""); ok {
		t.Errorf("PorSKU(\"\") acertó en un índice vacío")
	}
	if got := idx.PorEtiqueta(""); got != nil {
		t.Errorf("PorEtiqueta(\"\") = %+v en un índice vacío; se esperaba nil", got)
	}
}

// ---------------------------------------------------------------------------
// EL NORMALIZADOR ES OBLIGATORIO (T-5)
// ---------------------------------------------------------------------------

func TestConstruir_RequiresAValidNormalizer(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		idx, err := indice.Construir(trickyCatalog(), nil)
		if !errors.Is(err, indice.ErrSinNormalizador) {
			t.Errorf("Construir(…, nil) = %v; se esperaba ErrSinNormalizador", err)
		}
		if idx != nil {
			t.Errorf("Construir(…, nil) devolvió un índice; no nace a medias")
		}
	})

	t.Run("one that folds the enye", func(t *testing.T) {
		idx, err := indice.Construir(trickyCatalog(), enyeFoldingNormalizer)
		if !errors.Is(err, indice.ErrNormalizadorInvalido) {
			t.Errorf("Construir(…, enyeFoldingNormalizer) = %v; se esperaba ErrNormalizadorInvalido", err)
		}
		if idx != nil {
			t.Errorf("Construir(…, enyeFoldingNormalizer) devolvió un índice; no nace a medias")
		}
	})

	t.Run("the normalizer is checked before the size", func(t *testing.T) {
		_, err := indice.Construir(sizedCatalog(2001), nil)
		if !errors.Is(err, indice.ErrSinNormalizador) {
			t.Errorf("Construir(2001 artículos, nil) = %v; se esperaba ErrSinNormalizador", err)
		}
	})

	t.Run("the injected normalizer is the one that searches", func(t *testing.T) {
		// Un normalizador válido que además cuenta sus llamadas: si el índice usara
		// otro (o ninguno) al buscar, el contador no se movería.
		var mu sync.Mutex
		calls := 0
		var counting indice.Normalizador = func(s string) string {
			mu.Lock()
			calls++
			mu.Unlock()
			return textmatch.Normalize(s)
		}
		idx, err := indice.Construir(trickyCatalog(), counting)
		if err != nil {
			t.Fatalf("Construir(…, counting) = %v", err)
		}
		before := calls
		if got := skus(idx.PorEtiqueta("CAFÉ")); len(got) != 3 {
			t.Errorf("PorEtiqueta(CAFÉ) skus = %q; se esperaban 3", got)
		}
		if calls == before {
			t.Errorf("la búsqueda no llamó al normalizador inyectado")
		}
	})
}
