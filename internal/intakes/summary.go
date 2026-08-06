package intakes

import (
	"cmp"
	"slices"
	"time"
)

// MaxTopItems acota cuántos artículos entran en el ranking del summary. El summary
// está pensado para pegárselo a un LLM externo (design D-041.15) y un ranking de
// 800 SKUs no informa: gasta contexto.
const MaxTopItems = 20

// TopItem es una línea del ranking de artículos del summary: lo agregado por SKU
// en TODAS las solicitudes que casan con el filtro.
type TopItem struct {
	SKU      string
	Label    string
	QtyTotal int
	Revenue  float64 // Σ qty × unit_price de ese SKU
}

// Summary es la foto agregada de las solicitudes que casan con un filtro: los
// totales, el desglose por estado, el ranking de artículos y el detalle crudo.
//
// CERO PII, y aquí no es una precaución sino la razón de ser del objeto: el
// summary se genera para pegárselo a un LLM EXTERNO. Por eso Details se publica
// sin ContactID (el opaco tampoco viaja) y jamás con datos del comprador: el
// proyector a JSON no puede "aprovechar" que el Detail los trae dentro.
type Summary struct {
	GeneratedAt time.Time
	From        time.Time // cota inferior del filtro (cero ⇒ sin cota)
	To          time.Time // cota superior EXCLUSIVA (cero ⇒ sin cota)

	Intakes  int            // cuántas solicitudes casan con el filtro
	Revenue  float64        // Σ de los totales de esas solicitudes
	ByStatus map[string]int // estado canónico → cuántas

	TopItems []TopItem
	Details  []Detail // las solicitudes con sus líneas, en el orden del listado
}

// BuildSummary agrega las solicitudes ya leídas. Es PURA (no toca el store ni el
// reloj: el instante entra como argumento) para que su aritmética se pueda probar
// sin base de datos ni HTTP.
//
// Qué cuenta y qué no:
//   - Revenue suma el TOTAL de la cabecera, no la suma de las líneas: la cabecera
//     es la cifra que se acordó con el cliente y la que la bandeja muestra.
//   - NO se excluye ningún estado. Una solicitud cancelada suma en Revenue igual
//     que una confirmada, y quien lee decide qué hacer con eso mirando ByStatus o
//     acotando con ?status=. Filtrar por dentro sería una política escondida:
//     dos lecturas del mismo número darían cifras distintas según quién las mire.
func BuildSummary(details []Detail, f Filter, generatedAt time.Time) Summary {
	s := Summary{
		GeneratedAt: generatedAt.UTC(),
		From:        f.From,
		To:          f.To,
		Intakes:     len(details),
		ByStatus:    map[string]int{},
		Details:     details,
	}

	agg := map[string]*TopItem{}
	for _, d := range details {
		s.Revenue += d.Total
		s.ByStatus[NormalizeStatus(d.Status)]++

		for _, it := range d.Items {
			top, ok := agg[it.SKU]
			if !ok {
				// Los detalles llegan de más reciente a más antiguo, así que la
				// PRIMERA etiqueta que se ve de un SKU es la más reciente: si el
				// catálogo lo renombró, el ranking usa el nombre de hoy.
				top = &TopItem{SKU: it.SKU, Label: it.Label}
				agg[it.SKU] = top
			}
			top.QtyTotal += it.Qty
			top.Revenue += float64(it.Qty) * it.UnitPrice
		}
	}

	s.TopItems = rankTopItems(agg)
	return s
}

// rankTopItems ordena el ranking por facturación descendente (lo que mueve dinero
// primero), desempatando por cantidad y por SKU. El desempate por SKU no es
// cosmético: sin él, dos ejecuciones sobre los mismos datos podrían devolver
// órdenes distintos —el recorrido de un mapa Go es aleatorio— y el summary dejaría
// de ser reproducible.
func rankTopItems(agg map[string]*TopItem) []TopItem {
	out := make([]TopItem, 0, len(agg))
	for _, top := range agg {
		out = append(out, *top)
	}
	slices.SortFunc(out, func(a, b TopItem) int {
		if c := cmp.Compare(b.Revenue, a.Revenue); c != 0 {
			return c
		}
		if c := cmp.Compare(b.QtyTotal, a.QtyTotal); c != 0 {
			return c
		}
		return cmp.Compare(a.SKU, b.SKU)
	})
	if len(out) > MaxTopItems {
		out = out[:MaxTopItems]
	}
	return out
}
