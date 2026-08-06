package intakes_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

const tenantSum = "tenant-summary"

func instante(d int) time.Time { return time.Date(2026, 8, d, 12, 0, 0, 0, time.UTC) }

// TestListDetails_CotaNoRecortaEnSilencio: pasarse de MaxExportIntakes devuelve
// ErrTooLarge en vez de un export recortado. Un archivo truncado es peor que uno
// rechazado: quien lo abre cree que tiene todos sus pedidos.
func TestListDetails_CotaNoRecortaEnSilencio(t *testing.T) {
	svc := intakes.NewService(sembrar(intakes.MaxExportIntakes + 1))

	if _, err := svc.ListDetails(context.Background(), tenantSum, intakes.Filter{}); !errors.Is(err, intakes.ErrTooLarge) {
		t.Fatalf("err=%v, quiero ErrTooLarge", err)
	}
	if _, err := svc.Summary(context.Background(), tenantSum, intakes.Filter{}); !errors.Is(err, intakes.ErrTooLarge) {
		t.Fatalf("summary: err=%v, quiero ErrTooLarge (misma cota que el export)", err)
	}
}

// TestListDetails_JustoEnLaCota: exactamente MaxExportIntakes sí pasa. Sin este
// caso, un off-by-one en la comparación rechazaría el export más grande legítimo y
// nadie se enteraría hasta tenerlo delante.
func TestListDetails_JustoEnLaCota(t *testing.T) {
	svc := intakes.NewService(sembrar(intakes.MaxExportIntakes))

	got, err := svc.ListDetails(context.Background(), tenantSum, intakes.Filter{})
	if err != nil {
		t.Fatalf("err=%v, quiero nil", err)
	}
	if len(got) != intakes.MaxExportIntakes {
		t.Fatalf("solicitudes=%d, quiero %d", len(got), intakes.MaxExportIntakes)
	}
}

// TestListDetails_IgnoraLaPaginación: el export no pagina. Un page_size de la lista
// que se colara aquí devolvería 50 solicitudes de las que hay.
func TestListDetails_IgnoraLaPaginación(t *testing.T) {
	svc := intakes.NewService(sembrar(300))

	got, err := svc.ListDetails(context.Background(), tenantSum,
		intakes.Filter{Page: 2, PageSize: 10})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(got) != 300 {
		t.Fatalf("solicitudes=%d, quiero las 300 (la paginación no aplica al export)", len(got))
	}
}

// sembrar arma un store con n solicitudes del mismo tenant, una por instante
// distinto para que el orden sea total.
func sembrar(n int) *intakes.MemoryStore {
	st := intakes.NewMemoryStore()
	base := instante(1)
	for i := range n {
		st.Add(tenantSum, intakes.Intake{
			ID:        fmt.Sprintf("intake-%05d", i),
			Status:    intakes.StatusConfirmed,
			Total:     100,
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}
	return st
}

// TestBuildSummary_Agrega: totales, desglose por estado y ranking sobre un caso
// hecho a mano. El `closed` legado cuenta en el MISMO cubo que `confirmed`.
func TestBuildSummary_Agrega(t *testing.T) {
	details := []intakes.Detail{
		{
			Intake: intakes.Intake{ID: "b", Status: intakes.StatusConfirmed, Total: 5000, CreatedAt: instante(2)},
			Items: []intakes.Item{
				{SKU: "torta", Label: "Torta grande", Qty: 2, UnitPrice: 2000},
				{SKU: "vela", Label: "Velas", Qty: 1, UnitPrice: 1000},
			},
		},
		{
			Intake: intakes.Intake{ID: "a", Status: intakes.StatusClosedLegacy, Total: 3000, CreatedAt: instante(1)},
			Items:  []intakes.Item{{SKU: "torta", Label: "Torta chica", Qty: 1, UnitPrice: 3000}},
		},
		{Intake: intakes.Intake{ID: "c", Status: intakes.StatusCancelled, Total: 700, CreatedAt: instante(3)}},
	}

	sum := intakes.BuildSummary(details, intakes.Filter{From: instante(1)}, instante(9))

	if sum.Intakes != 3 || sum.Revenue != 8700 {
		t.Fatalf("intakes=%d revenue=%v; quiero 3 y 8700 (la cancelada también suma)",
			sum.Intakes, sum.Revenue)
	}
	if sum.ByStatus["confirmed"] != 2 || sum.ByStatus["cancelled"] != 1 || len(sum.ByStatus) != 2 {
		t.Fatalf("by_status=%v; quiero confirmed=2 (una de ellas `closed` legado) y cancelled=1",
			sum.ByStatus)
	}

	if len(sum.TopItems) != 2 {
		t.Fatalf("top_items=%+v, quiero 2", sum.TopItems)
	}
	// torta: 2×2000 + 1×3000 = 7000 con qty 3; vela: 1000.
	if sum.TopItems[0] != (intakes.TopItem{SKU: "torta", Label: "Torta grande", QtyTotal: 3, Revenue: 7000}) {
		t.Fatalf("top_items[0]=%+v; quiero torta agregada con la etiqueta MÁS RECIENTE", sum.TopItems[0])
	}
	if sum.TopItems[1].SKU != "vela" {
		t.Fatalf("top_items[1]=%+v, quiero vela", sum.TopItems[1])
	}
	if !sum.GeneratedAt.Equal(instante(9)) || !sum.From.Equal(instante(1)) || !sum.To.IsZero() {
		t.Fatalf("cabecera del summary = %v/%v/%v", sum.GeneratedAt, sum.From, sum.To)
	}
}

// TestBuildSummary_RankingDeterminista: dos SKUs con la MISMA facturación y la
// misma cantidad desempatan por SKU. Sin ese desempate el orden lo decidiría el
// recorrido de un mapa Go —aleatorio por diseño— y dos llamadas seguidas darían
// respuestas distintas sobre los mismos datos.
func TestBuildSummary_RankingDeterminista(t *testing.T) {
	details := []intakes.Detail{{
		Intake: intakes.Intake{ID: "a", Status: intakes.StatusConfirmed, Total: 300, CreatedAt: instante(1)},
		Items: []intakes.Item{
			{SKU: "zeta", Label: "Zeta", Qty: 1, UnitPrice: 100},
			{SKU: "alfa", Label: "Alfa", Qty: 1, UnitPrice: 100},
			{SKU: "mid", Label: "Mid", Qty: 1, UnitPrice: 100},
		},
	}}

	for range 20 {
		sum := intakes.BuildSummary(details, intakes.Filter{}, instante(9))
		got := []string{sum.TopItems[0].SKU, sum.TopItems[1].SKU, sum.TopItems[2].SKU}
		if got[0] != "alfa" || got[1] != "mid" || got[2] != "zeta" {
			t.Fatalf("ranking=%v; quiero [alfa mid zeta] en todas las corridas", got)
		}
	}
}

// TestBuildSummary_RankingAcotado: el ranking se corta en MaxTopItems y deja arriba
// los que más facturan.
func TestBuildSummary_RankingAcotado(t *testing.T) {
	items := make([]intakes.Item, 0, intakes.MaxTopItems+5)
	for i := range intakes.MaxTopItems + 5 {
		items = append(items, intakes.Item{
			SKU: fmt.Sprintf("sku-%02d", i), Qty: 1, UnitPrice: float64(i + 1),
		})
	}
	sum := intakes.BuildSummary([]intakes.Detail{{
		Intake: intakes.Intake{ID: "a", Status: intakes.StatusConfirmed, CreatedAt: instante(1)},
		Items:  items,
	}}, intakes.Filter{}, instante(9))

	if len(sum.TopItems) != intakes.MaxTopItems {
		t.Fatalf("top_items=%d, quiero %d", len(sum.TopItems), intakes.MaxTopItems)
	}
	if sum.TopItems[0].SKU != fmt.Sprintf("sku-%02d", intakes.MaxTopItems+4) {
		t.Fatalf("top_items[0]=%+v, quiero el de mayor facturación", sum.TopItems[0])
	}
}
