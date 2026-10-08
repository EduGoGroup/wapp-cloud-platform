package casebankhelpertest_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/casebank"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/casebank/casebankhelpertest"
)

// TestMemory_Contrato corre la suite del puerto contra el doble, sin BD.
func TestMemory_Contrato(t *testing.T) {
	casebankhelpertest.Contrato(t, func(*testing.T) casebankhelpertest.Montaje {
		mem := casebankhelpertest.NewMemory()
		return casebankhelpertest.Montaje{
			Store:   mem,
			TenantA: "t-casebank-a",
			TenantB: "t-casebank-b",
			Rows:    func(_ *testing.T, tenantID string) []casebankhelpertest.Row { return mem.Rows(tenantID) },
		}
	})
}

// TestMemory_Calls_CountsEveryCall: Calls cuenta las llamadas, fallen o no.
func TestMemory_Calls_CountsEveryCall(t *testing.T) {
	ctx := context.Background()
	mem := casebankhelpertest.NewMemory()
	if inserts, exists := mem.Calls(); inserts != 0 || exists != 0 {
		t.Fatalf("un doble nuevo dice Calls() = (%d, %d), se esperaba (0, 0)", inserts, exists)
	}
	_, _ = mem.Insert(ctx, casebank.Case{TenantID: "t", Consented: true, SourceText: "a"})
	_, _ = mem.Insert(ctx, casebank.Case{TenantID: "t", Consented: false, SourceText: "b"}) // rechazada: cuenta
	_, _ = mem.Exists(ctx, "t", "a")
	if inserts, exists := mem.Calls(); inserts != 2 || exists != 1 {
		t.Errorf("Calls() = (%d, %d), se esperaba (2, 1)", inserts, exists)
	}
}

// TestMemory_FailInsertAndFailExists: los fallos provocados salen tal cual, no
// escriben y se quitan con nil.
func TestMemory_FailInsertAndFailExists(t *testing.T) {
	ctx := context.Background()
	mem := casebankhelpertest.NewMemory()
	boom := errors.New("boom")
	c := casebank.Case{TenantID: "t", Consented: true, SourceText: "a"}

	mem.FailInsert(boom)
	if id, err := mem.Insert(ctx, c); !errors.Is(err, boom) || id != 0 {
		t.Errorf("Insert con fallo provocado = (%d, %v), se esperaba (0, boom)", id, err)
	}
	if rows := mem.Rows("t"); len(rows) != 0 {
		t.Errorf("el Insert fallido dejó %d filas", len(rows))
	}
	mem.FailInsert(nil)
	if _, err := mem.Insert(ctx, c); err != nil {
		t.Errorf("Insert tras quitar el fallo = %v", err)
	}

	mem.FailExists(boom)
	if found, err := mem.Exists(ctx, "t", "a"); !errors.Is(err, boom) || found {
		t.Errorf("Exists con fallo provocado = (%t, %v), se esperaba (false, boom)", found, err)
	}
	mem.FailExists(nil)
	if found, err := mem.Exists(ctx, "t", "a"); err != nil || !found {
		t.Errorf("Exists tras quitar el fallo = (%t, %v), se esperaba (true, nil)", found, err)
	}
}

// TestMemory_Rows_ReturnsACopy: tocar lo que devuelve Rows, o el expected que se
// le pasó a Insert, no cambia lo guardado.
func TestMemory_Rows_ReturnsACopy(t *testing.T) {
	mem := casebankhelpertest.NewMemory()
	expected := []byte(`{"a":1}`)
	if _, err := mem.Insert(context.Background(), casebank.Case{
		TenantID: "t", Consented: true, SourceText: "a", Expected: expected,
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	expected[2] = 'z'
	rows := mem.Rows("t")
	rows[0].SourceText = "pisado"
	rows[0].Expected[2] = 'y'
	if got := mem.Rows("t")[0]; got.SourceText != "a" || string(got.Expected) != `{"a":1}` {
		t.Errorf("lo guardado cambió desde fuera: %+v (expected=%s)", got, got.Expected)
	}
}

// TestMemory_ConcurrentUse: bajo -race, inserciones y consultas simultáneas no se
// pisan y cada fila sale con un id propio.
func TestMemory_ConcurrentUse(t *testing.T) {
	const n = 50
	ctx := context.Background()
	mem := casebankhelpertest.NewMemory()
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := mem.Insert(ctx, casebank.Case{TenantID: "t", Consented: true, SourceText: "a"}); err != nil {
				t.Errorf("Insert: %v", err)
			}
			if _, err := mem.Exists(ctx, "t", "a"); err != nil {
				t.Errorf("Exists: %v", err)
			}
			_ = mem.Rows("t")
		}()
	}
	wg.Wait()
	seen := map[int64]bool{}
	for _, r := range mem.Rows("t") {
		seen[r.ID] = true
	}
	if len(seen) != n {
		t.Errorf("%d inserciones dejaron %d ids distintos", n, len(seen))
	}
}
