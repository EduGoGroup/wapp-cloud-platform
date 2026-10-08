package indice_test

import (
	"encoding/json"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// ---------------------------------------------------------------------------
// ≤ 5 ms p99 POR ÍTEM, CON 2.000 ARTÍCULOS (D-044.44)
// ---------------------------------------------------------------------------
//
// 🔴 ÚNICA EXCEPCIÓN A «SIN RELOJ REAL» (D-F5-3, T-10): este test mide con el reloj
// de pared, y NO se salta de ninguna forma: ni en modo corto ni con un salto
// explícito. Si falla por la máquina, es una parada y una decisión.

// perItemDeadline es el criterio de D-044.44 escrito como número.
const perItemDeadline = 5 * time.Millisecond

// samplesPerItem es cuántos ítems se miden.
//
// 🔴 NO ES UN NÚMERO CÓMODO: un p99 de n=10 es el MÁXIMO disfrazado y no dice nada.
// Con 1.000 muestras el p99 es el décimo peor, que es una cifra que sí se puede
// comparar entre corridas.
const samplesPerItem = 1000

// TestPerformance_P99PerItem mide lo que cuesta UN ÍTEM contra un catálogo de 2.000
// artículos: las cuatro búsquedas que la etapa `match` hará por cada línea del
// pedido (sku, etiqueta, tag y variante), con el normalizador de producción.
//
// # QUÉ SE MIDE Y QUÉ NO
//
// NO se mide la construcción del índice ni la lectura del documento: eso es POR JOB
// y se paga una vez. El criterio dice «por ítem», y por ítem el trabajo son las
// cuatro consultas.
//
// # 🔴 EL CONTROL, PORQUE UN «≤ 5 ms» SIN CONTROL PUEDE SER UNA TAUTOLOGÍA
//
// Un umbral sobre algo que el sistema no puede exceder sale verde midiendo cero. Así
// que el test mide TAMBIÉN la alternativa que este índice sustituye —parsear el
// documento en cada ítem, que es lo que hace `catalogo.ParseCatalog`— y la imprime
// al lado. Esa es la cifra que dice de qué protege el criterio.
//
// El control se IMPRIME, no se afirma: su valor depende de la máquina y convertirlo
// en umbral haría rojo el test en un runner lento por algo que no es una regresión.
func TestPerformance_P99PerItem(t *testing.T) {
	const articles = 2000

	cat := sizedCatalog(articles)
	doc := sizedDocument(t, articles)

	start := time.Now()
	idx, err := indice.Construir(cat, textmatch.Normalize)
	build := time.Since(start)
	if err != nil {
		t.Fatalf("Construir(%d artículos) = %v", articles, err)
	}
	if idx.Articulos() != articles {
		t.Fatalf("Articulos() = %d; se esperaban %d", idx.Articulos(), articles)
	}

	// El acumulador impide que el compilador se lleve las búsquedas por delante: un
	// resultado que nadie usa es código muerto y podría no ejecutarse.
	hits := 0
	samples := make([]time.Duration, samplesPerItem)
	for i := range samplesPerItem {
		q := itemQuery(i)
		t0 := time.Now()
		if _, ok := idx.PorSKU(q.sku); ok {
			hits++
		}
		hits += len(idx.PorEtiqueta(q.label))
		hits += len(idx.PorTag(q.tag))
		hits += len(idx.PorVariante(q.variant))
		samples[i] = time.Since(t0)
	}
	if hits <= samplesPerItem {
		t.Fatalf("aciertos = %d en %d ítems; las búsquedas tienen que estar acertando de verdad", hits, samplesPerItem)
	}

	p99 := percentile(samples, 99)
	p50 := percentile(samples, 50)

	// El control: lo que costaría el mismo ítem SIN índice.
	control := make([]time.Duration, 100)
	for i := range control {
		t0 := time.Now()
		var m map[string]any
		if err := json.Unmarshal(doc, &m); err != nil {
			t.Fatalf("el documento de talla no es JSON: %v", err)
		}
		parsed, err := catalogo.ParseCatalog(model.Content{Raw: m})
		if err != nil {
			t.Fatalf("ParseCatalog(documento de talla) = %v", err)
		}
		if n := countArticles(parsed); n != articles {
			t.Fatalf("el control parseó %d artículos; se esperaban %d", n, articles)
		}
		control[i] = time.Since(t0)
	}

	t.Logf("catálogo de %d artículos · construcción del índice: %v", articles, build)
	t.Logf("POR ÍTEM con índice   (n=%d): p50=%v  p99=%v   ← criterio ≤ %v", samplesPerItem, p50, p99, perItemDeadline)
	t.Logf("POR ÍTEM sin índice   (n=%d): p50=%v  p99=%v   ← el parseo por ítem que esto sustituye",
		len(control), percentile(control, 50), percentile(control, 99))

	if p99 > perItemDeadline {
		t.Errorf("el p99 por ítem sobre %d artículos es %v y el criterio de D-044.44 es %v", articles, p99, perItemDeadline)
	}
}

// itemQuery devuelve las cuatro consultas de un ítem, con las mismas formas que
// genera sizedCatalog y con las mayúsculas y acentos que traería el texto de un
// cliente (el normalizador se ejecuta en cada búsqueda: forma parte del coste).
func itemQuery(i int) struct{ sku, label, tag, variant string } {
	n := strconv.Itoa(i % 2000)
	return struct{ sku, label, tag, variant string }{
		sku:     "SKU-" + n,
		label:   "  ARTÍCULO Número " + n + " de Piña y CAFÉ  ",
		tag:     "TAG-" + strconv.Itoa(i%37),
		variant: "Presentación GRANDE " + n,
	}
}

// percentile devuelve el percentil p de una muestra (interpolación no: el elemento
// que ocupa la posición, que para n=1000 y p=99 es el décimo peor).
func percentile(samples []time.Duration, p int) time.Duration {
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	i := len(sorted) * p / 100
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}
