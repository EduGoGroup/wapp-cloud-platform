package catalogo_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/catalogo"
)

// ---------------------------------------------------------------------------
// CRITERIO (c) — CORRECCIÓN: EL ÍNDICE DA EXACTAMENTE LO MISMO QUE LA BÚSQUEDA
//                 LINEAL INGENUA
// ---------------------------------------------------------------------------

// TestDiferencial_IndiceContraBusquedaLineal interroga a los dos con el mismo
// corpus y exige IGUALDAD ELEMENTO A ELEMENTO, orden incluido.
//
// El oráculo (dobles_test.go) está escrito como lo escribiría quien no tuviera
// índice: dos bucles anidados y una comparación. No mira ni una estructura del
// índice, así que una divergencia solo puede venir de que el índice haya inventado
// o perdido algo.
//
// 🔴 El corpus incluye a propósito los casos en los que un índice mal construido
// coincide con el oráculo POR CASUALIDAD: consultas que no casan nada, la cadena
// vacía, labels que normalizan igual entre sí, y el par «año»/«ano».
func TestDiferencial_IndiceContraBusquedaLineal(t *testing.T) {
	cat := catalogoTramposo()
	idx := indiceTramposo(t)

	t.Run("por sku", func(t *testing.T) {
		for _, sku := range skusDeConsulta() {
			esperado, hayEsperado := oraculoSKU(cat, sku)
			got, hay := idx.PorSKU(sku)
			require.Equalf(t, hayEsperado, hay, "PorSKU(%q): el índice y la búsqueda lineal discrepan en si hay resultado", sku)
			require.Equalf(t, esperado, got, "PorSKU(%q)", sku)
		}
	})

	t.Run("por etiqueta", func(t *testing.T) {
		for _, q := range consultas() {
			require.Equalf(t, oraculoEtiqueta(cat, q), idx.PorEtiqueta(q), "PorEtiqueta(%q)", q)
		}
	})

	t.Run("por tag", func(t *testing.T) {
		for _, q := range consultas() {
			require.Equalf(t, oraculoTag(cat, q), idx.PorTag(q), "PorTag(%q)", q)
		}
	})

	t.Run("por variante", func(t *testing.T) {
		for _, q := range consultas() {
			require.Equalf(t, oraculoVariante(cat, q), idx.PorVariante(q), "PorVariante(%q)", q)
		}
	})
}

// TestDiferencial_ElCorpusDeVerdadEJERCITA es la guarda contra el test diferencial
// hueco: dos implementaciones que devuelven SIEMPRE nil también coinciden.
//
// Sin esto, borrar el cuerpo de las cuatro búsquedas dejaría el diferencial en
// verde. Con esto, el corpus tiene que producir aciertos de los cuatro tipos —y con
// las formas que hacen difícil el problema: varios artículos bajo la misma
// etiqueta, un tag compartido, dos variantes con el mismo label.
func TestDiferencial_ElCorpusDeVerdadEJERCITA(t *testing.T) {
	idx := indiceTramposo(t)

	_, hay := idx.PorSKU("TORTA")
	require.True(t, hay, "el corpus tiene que acertar por sku")

	require.Len(t, idx.PorEtiqueta("café"), 3, "tres labels distintos normalizan a «cafe» y los tres tienen que salir")
	require.Len(t, idx.PorTag("caliente"), 2, "dos artículos comparten el tag «caliente»")
	require.Len(t, idx.PorTag("clásico"), 2, "el tag «clásico» cruza las dos categorías")
	require.Len(t, idx.PorVariante("grande"), 2, "dos variantes del mismo artículo normalizan a «grande»")
	require.Len(t, idx.PorEtiqueta("no existe"), 0, "el corpus tiene que incluir fallos")
}

// TestIndice_LaÑSobrevive es el invariante de T3.1 visto desde el índice: «Piña
// colada» y «Pina colada» son DOS artículos y ninguna consulta puede confundirlos.
func TestIndice_LaÑSobrevive(t *testing.T) {
	idx := indiceTramposo(t)

	conÑ := idx.PorEtiqueta("PIÑA COLADA")
	require.Len(t, conÑ, 1)
	require.Equal(t, "PINA", conÑ[0].Articulo.SKU)

	sinÑ := idx.PorEtiqueta("pina colada")
	require.Len(t, sinÑ, 1)
	require.Equal(t, "PINA-SIN", sinÑ[0].Articulo.SKU)
}

// TestIndice_LaVarianteViajaConSuPrecio: sin la variante en la coincidencia, quien
// cotiza no sabría qué cobrar (D-041.4). Las dos variantes que normalizan igual
// salen las dos, y con precios distintos.
func TestIndice_LaVarianteViajaConSuPrecio(t *testing.T) {
	got := indiceTramposo(t).PorVariante("Grande")
	require.Len(t, got, 2)
	for _, c := range got {
		require.True(t, c.HayVariante, "una coincidencia por variante tiene que decirlo")
		require.Equal(t, "TORTA", c.Articulo.SKU)
	}
	require.Equal(t, 25.0, got[0].Variante.Price)
	require.Equal(t, 26.0, got[1].Variante.Price)

	// Y por las otras tres vías la variante queda a cero y HayVariante en false.
	porSKU, _ := indiceTramposo(t).PorSKU("TORTA")
	require.False(t, porSKU.HayVariante)
	require.Equal(t, cart.Variant{}, porSKU.Variante)
}

// TestIndice_SkuRepetido_GanaElPrimero: un sku duplicado es un catálogo roto que el
// runtime no puede rechazar (el import sí). La regla es «el primero en orden de
// documento», y se fija aquí para que no cambie por accidente.
func TestIndice_SkuRepetido_GanaElPrimero(t *testing.T) {
	got, hay := indiceTramposo(t).PorSKU("CAFE")
	require.True(t, hay)
	require.Equal(t, "1", got.Categoria, "el CAFE de Bebidas va antes que el de Postres")
	require.Equal(t, "Café", got.Articulo.Label)
}

// TestIndice_ElSkuNoSeNormaliza: el sku es un identificador opaco del dueño, no
// texto del cliente. Si se normalizara, "cafe" casaría "CAFE" y dos skus que el
// dueño escribió distintos pasarían a ser el mismo.
func TestIndice_ElSkuNoSeNormaliza(t *testing.T) {
	_, hay := indiceTramposo(t).PorSKU("cafe")
	require.False(t, hay, "el sku se compara literal")
}

// ---------------------------------------------------------------------------
// LA COTA DE TAMAÑO (D-044.44)
// ---------------------------------------------------------------------------

// TestCota_2000Entra_2001NoEntra es un test de FRONTERA, no un «≤ N» sobre algo que
// el sistema no puede exceder: los dos lados del límite se construyen de verdad y
// se comprueban por separado. Los números van literales a propósito —2000 y 2001—
// para que comparar contra la constante que se quiere proteger no haga pasar el
// test con cualquier valor.
func TestCota_2000Entra_2001NoEntra(t *testing.T) {
	idx, err := catalogo.Construir(catalogoDeTalla(2000), normalizadorDoble)
	require.NoError(t, err, "2.000 artículos son exactamente el tope: tienen que entrar")
	require.Equal(t, 2000, idx.Articulos())

	_, err = catalogo.Construir(catalogoDeTalla(2001), normalizadorDoble)
	require.ErrorIs(t, err, catalogo.ErrCatalogoDemasiadoGrande)
	require.Contains(t, err.Error(), "2001", "el error tiene que decir cuántos traía")
	require.Contains(t, err.Error(), "2000", "…y cuál era el tope")
}

// TestCota_SeCuentanLosArticulosDeTODASLasCategorias: el tope es del documento
// entero, no por categoría. Diez categorías de 250 artículos son 2.500 y sobran.
func TestCota_SeCuentanLosArticulosDeTODASLasCategorias(t *testing.T) {
	cat := cart.Catalog{}
	for c := range 10 {
		cate := cart.Category{Code: strconv.Itoa(c), Label: "cat"}
		for a := range 250 {
			cate.Items = append(cate.Items, cart.Article{SKU: fmt.Sprintf("S-%d-%d", c, a), Label: "x"})
		}
		cat.Categories = append(cat.Categories, cate)
	}
	_, err := catalogo.Construir(cat, normalizadorDoble)
	require.ErrorIs(t, err, catalogo.ErrCatalogoDemasiadoGrande)
}

// ---------------------------------------------------------------------------
// EL CONTRATO DEL NORMALIZADOR (la frontera con T3.1)
// ---------------------------------------------------------------------------

func TestNormalizador_ElIndiceNoNaceSinEl(t *testing.T) {
	_, err := catalogo.Construir(catalogoTramposo(), nil)
	require.ErrorIs(t, err, catalogo.ErrSinNormalizador)
	require.ErrorIs(t, catalogo.VerificarNormalizador(nil), catalogo.ErrSinNormalizador)
}

// TestNormalizador_ElContratoCazaAlQuePliegaLaÑ es el valor real de
// VerificarNormalizador: `strings.ToLower` y un plegado ingenuo son las dos
// sustituciones plausibles, y las dos rompen el match EN SILENCIO. El contrato las
// convierte en un error en el arranque.
func TestNormalizador_ElContratoCazaAlQuePliegaLaÑ(t *testing.T) {
	t.Run("el doble cumple", func(t *testing.T) {
		require.NoError(t, catalogo.VerificarNormalizador(normalizadorDoble))
	})

	t.Run("strings.ToLower no cumple: no pliega los diacríticos", func(t *testing.T) {
		err := catalogo.VerificarNormalizador(strings.ToLower)
		require.ErrorIs(t, err, catalogo.ErrNormalizadorInvalido)
		require.Contains(t, err.Error(), "cafe")
	})

	t.Run("plegar la ñ no cumple", func(t *testing.T) {
		err := catalogo.VerificarNormalizador(normalizadorTramposo)
		require.ErrorIs(t, err, catalogo.ErrNormalizadorInvalido)
	})

	t.Run("un normalizador no idempotente no cumple", func(t *testing.T) {
		// PASA todos los casos de la tabla —para cada uno, la entrada difiere de su
		// forma normalizada— y solo se delata en la SEGUNDA pasada. Es el único modo
		// de que la comprobación de idempotencia sea la que dispare, y no un valor
		// equivocado en el primer caso.
		noIdempotente := func(s string) string {
			n := normalizadorDoble(s)
			if s == n && s != "" {
				return n + " otra vez"
			}
			return n
		}
		err := catalogo.VerificarNormalizador(noIdempotente)
		require.ErrorIs(t, err, catalogo.ErrNormalizadorInvalido)
		require.Contains(t, err.Error(), "idempotente", "el error tiene que decir cuál de las dos mitades del contrato falló")
	})

	t.Run("el índice rechaza al que no cumple", func(t *testing.T) {
		_, err := catalogo.Construir(catalogoTramposo(), normalizadorTramposo)
		require.ErrorIs(t, err, catalogo.ErrNormalizadorInvalido)
	})
}

// ---------------------------------------------------------------------------
// FIXTURE DE TALLA
// ---------------------------------------------------------------------------

// catalogoDeTalla genera un catálogo de n artículos repartidos en 20 categorías,
// cada uno con sku propio, tres tags y dos variantes. No hay ningún fixture grande
// en el repo (`testdata/catalog_v2.json` tiene 3 artículos), así que el catálogo de
// la cota y el del rendimiento se generan aquí.
func catalogoDeTalla(n int) cart.Catalog {
	const categorias = 20
	cat := cart.Catalog{Categories: make([]cart.Category, categorias)}
	for c := range categorias {
		cat.Categories[c] = cart.Category{Code: strconv.Itoa(c + 1), Label: "Categoría " + strconv.Itoa(c+1)}
	}
	for i := range n {
		c := i % categorias
		s := strconv.Itoa(i)
		cat.Categories[c].Items = append(cat.Categories[c].Items, cart.Article{
			Code:  s,
			SKU:   "SKU-" + s,
			Label: "Artículo número " + s + " de piña y café",
			Price: float64(i) + 0.5,
			Tags:  []string{"tag-" + strconv.Itoa(i%37), "clásico", "año-" + strconv.Itoa(i%11)},
			Variants: []cart.Variant{
				{Code: "V1", Label: "Presentación grande " + s, Price: float64(i) + 1},
				{Code: "V2", Label: "Presentación pequeña " + s, Price: float64(i)},
			},
		})
	}
	return cat
}
