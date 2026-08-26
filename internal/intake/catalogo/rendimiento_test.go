package catalogo_test

import (
	"encoding/json"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/catalogo"
)

// ---------------------------------------------------------------------------
// CRITERIO (e) — ≤ 5 ms p99 POR ÍTEM, CON 2.000 ARTÍCULOS (D-044.44)
// ---------------------------------------------------------------------------

// PlazoPorItem es el criterio de D-044.44 escrito como número.
const PlazoPorItem = 5 * time.Millisecond

// muestrasPorItem es cuántos ítems se miden.
//
// 🔴 NO ES UN NÚMERO CÓMODO: un p99 de n=10 es el MÁXIMO disfrazado y no dice nada.
// Con 1.000 muestras el p99 es el décimo peor, que es una cifra que sí se puede
// comparar entre corridas.
const muestrasPorItem = 1000

// TestRendimiento_P99PorItem mide lo que cuesta UN ÍTEM contra un catálogo de 2.000
// artículos: las cuatro búsquedas que la etapa `match` hará por cada línea del
// pedido (sku, etiqueta, tag y variante).
//
// # QUÉ SE MIDE Y QUÉ NO
//
// NO se mide la construcción del índice ni la lectura del documento: eso es POR JOB
// y se paga una vez (criterio (a)). El criterio dice «por ítem», y por ítem el
// trabajo son las cuatro consultas.
//
// # 🔴 EL CONTROL, PORQUE UN «≤ 5 ms» SIN CONTROL PUEDE SER UNA TAUTOLOGÍA
//
// Un umbral sobre algo que el sistema no puede exceder sale verde midiendo cero. Así
// que el test mide TAMBIÉN la alternativa que este índice sustituye —parsear el
// documento en cada ítem, que es lo que hace hoy `cart.ParseCatalog` en el camino
// conversacional (catalog.go:266)— y la imprime al lado. Esa es la cifra que dice de
// qué protege el criterio.
//
// El control se IMPRIME, no se assertea: su valor depende de la máquina y convertirlo
// en umbral haría rojo el test en un runner lento por algo que no es una regresión.
// El número real medido va en el informe de la tarea.
func TestRendimiento_P99PorItem(t *testing.T) {
	const articulos = 2000

	cat := catalogoDeTalla(articulos)
	doc := documentoDeTalla(articulos)

	arranque := time.Now()
	idx, err := catalogo.Construir(cat, normalizadorDoble)
	construccion := time.Since(arranque)
	require.NoError(t, err)
	require.Equal(t, articulos, idx.Articulos())

	// El acumulador impide que el compilador se lleve las búsquedas por delante: un
	// resultado que nadie usa es código muerto y podría no ejecutarse.
	acumulado := 0
	muestras := make([]time.Duration, muestrasPorItem)
	for i := range muestrasPorItem {
		q := consultaDeItem(i)
		t0 := time.Now()
		if _, hay := idx.PorSKU(q.sku); hay {
			acumulado++
		}
		acumulado += len(idx.PorEtiqueta(q.etiqueta))
		acumulado += len(idx.PorTag(q.tag))
		acumulado += len(idx.PorVariante(q.variante))
		muestras[i] = time.Since(t0)
	}
	require.Greater(t, acumulado, muestrasPorItem, "las búsquedas tienen que estar acertando de verdad")

	p99 := percentil(muestras, 99)
	p50 := percentil(muestras, 50)

	// El control: lo que costaría el mismo ítem SIN índice.
	control := make([]time.Duration, 100)
	for i := range control {
		t0 := time.Now()
		var m map[string]any
		require.NoError(t, json.Unmarshal(doc, &m))
		parseado, err := cart.ParseCatalog(model.Content{Raw: m})
		require.NoError(t, err)
		require.Equal(t, articulos, contarArticulos(parseado))
		control[i] = time.Since(t0)
	}

	t.Logf("catálogo de %d artículos · construcción del índice: %v", articulos, construccion)
	t.Logf("POR ÍTEM con índice   (n=%d): p50=%v  p99=%v   ← criterio ≤ %v", muestrasPorItem, p50, p99, PlazoPorItem)
	t.Logf("POR ÍTEM sin índice   (n=%d): p50=%v  p99=%v   ← el parseo por ítem que esto sustituye",
		len(control), percentil(control, 50), percentil(control, 99))

	require.LessOrEqualf(t, p99, PlazoPorItem,
		"el p99 por ítem sobre %d artículos es %v y el criterio de D-044.44 es %v", articulos, p99, PlazoPorItem)
}

// consultaDeItem devuelve las cuatro consultas de un ítem, con las mismas formas que
// genera catalogoDeTalla y con las mayúsculas y acentos que traería el texto de un
// cliente (el normalizador se ejecuta en cada búsqueda: forma parte del coste).
func consultaDeItem(i int) struct{ sku, etiqueta, tag, variante string } {
	n := strconv.Itoa(i % 2000)
	return struct{ sku, etiqueta, tag, variante string }{
		sku:      "SKU-" + n,
		etiqueta: "  ARTÍCULO Número " + n + " de Piña y CAFÉ  ",
		tag:      "TAG-" + strconv.Itoa(i%37),
		variante: "Presentación GRANDE " + n,
	}
}

// percentil devuelve el percentil p de una muestra (interpolación no: el elemento
// que ocupa la posición, que para n=1000 y p=99 es el décimo peor).
func percentil(muestras []time.Duration, p int) time.Duration {
	orden := slices.Clone(muestras)
	slices.Sort(orden)
	i := len(orden) * p / 100
	if i >= len(orden) {
		i = len(orden) - 1
	}
	return orden[i]
}

func contarArticulos(cat cart.Catalog) int {
	n := 0
	for _, c := range cat.Categories {
		n += len(c.Items)
	}
	return n
}

// documentoDeTalla serializa catalogoDeTalla a la forma JSON que guarda
// `public.tenant_content` (las claves en minúscula del blob del carrito). Es lo que
// consume el control.
func documentoDeTalla(n int) []byte {
	type varianteJSON struct {
		Code  string  `json:"code"`
		Label string  `json:"label"`
		Price float64 `json:"price"`
	}
	type articuloJSON struct {
		Code     string         `json:"code"`
		SKU      string         `json:"sku"`
		Label    string         `json:"label"`
		Price    float64        `json:"price"`
		Tags     []string       `json:"tags"`
		Variants []varianteJSON `json:"variants"`
	}
	type categoriaJSON struct {
		Code  string         `json:"code"`
		Label string         `json:"label"`
		Items []articuloJSON `json:"items"`
	}

	cat := catalogoDeTalla(n)
	doc := struct {
		Categories []categoriaJSON `json:"categories"`
	}{Categories: make([]categoriaJSON, 0, len(cat.Categories))}

	for _, c := range cat.Categories {
		cj := categoriaJSON{Code: c.Code, Label: c.Label, Items: make([]articuloJSON, 0, len(c.Items))}
		for _, a := range c.Items {
			aj := articuloJSON{Code: a.Code, SKU: a.SKU, Label: a.Label, Price: a.Price, Tags: a.Tags}
			for _, v := range a.Variants {
				aj.Variants = append(aj.Variants, varianteJSON{Code: v.Code, Label: v.Label, Price: v.Price})
			}
			cj.Items = append(cj.Items, aj)
		}
		doc.Categories = append(doc.Categories, cj)
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		panic("documentoDeTalla: " + err.Error()) // fixture del test: no puede fallar
	}
	return raw
}
