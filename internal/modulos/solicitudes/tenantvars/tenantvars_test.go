package tenantvars_test

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/tenantvars"
)

// Las dos implementaciones del puerto lo son: lo comprueba el compilador.
var (
	_ tenantvars.Store = (*tenantvars.MemoryStore)(nil)
	_ tenantvars.Store = (*tenantvars.Postgres)(nil)
)

// recordingStore es un Store mínimo: apunta lo que recibe y devuelve lo que se le sembró.
type recordingStore struct {
	rows         []tenantvars.Variable
	listTenant   string
	replaceVars  map[string]string
	replaceOwner string
}

func (s *recordingStore) List(_ context.Context, tenantID string) ([]tenantvars.Variable, error) {
	s.listTenant = tenantID
	return s.rows, nil
}

func (s *recordingStore) Replace(_ context.Context, tenantID string, vars map[string]string) error {
	s.replaceOwner, s.replaceVars = tenantID, vars
	return nil
}

// TestVariable_CarriesKeyValueAndChangeMark: una Variable es clave, valor y marca de cambio, y es
// comparable (dos variables con los mismos tres campos son iguales).
func TestVariable_CarriesKeyValueAndChangeMark(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	v := tenantvars.Variable{Key: "moneda", Value: "Bs", UpdatedAt: at}
	if v.Key != "moneda" || v.Value != "Bs" || !v.UpdatedAt.Equal(at) {
		t.Errorf("Variable = %+v, quería (moneda, Bs, %v)", v, at)
	}
	if v != (tenantvars.Variable{Key: "moneda", Value: "Bs", UpdatedAt: at}) {
		t.Errorf("dos variables con los mismos campos no son iguales: %+v", v)
	}
}

// TestStore_IsUsableThroughThePort: un consumidor que solo conoce el puerto lee y reemplaza por
// él, con el tenant como argumento de cada llamada (INV-8) y el mapa clave→valor tal cual.
func TestStore_IsUsableThroughThePort(t *testing.T) {
	impl := &recordingStore{rows: []tenantvars.Variable{{Key: "moneda", Value: "Bs"}}}
	var store tenantvars.Store = impl

	got, err := store.List(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("List: error inesperado %v", err)
	}
	if len(got) != 1 || got[0].Key != "moneda" || got[0].Value != "Bs" {
		t.Errorf("List = %+v, quería la variable sembrada", got)
	}
	if impl.listTenant != "tenant-1" {
		t.Errorf("List llegó con el tenant %q, quería tenant-1", impl.listTenant)
	}

	if err := store.Replace(context.Background(), "tenant-2", map[string]string{"saludo": "¡Hola!"}); err != nil {
		t.Fatalf("Replace: error inesperado %v", err)
	}
	if impl.replaceOwner != "tenant-2" || len(impl.replaceVars) != 1 || impl.replaceVars["saludo"] != "¡Hola!" {
		t.Errorf("Replace llegó con (%q, %v), quería (tenant-2, saludo=¡Hola!)", impl.replaceOwner, impl.replaceVars)
	}
}
