//go:build pendiente

package cart_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// settingsStore es un cart.ResumeStore que cuenta las lecturas y devuelve lo fijado.
type settingsStore struct {
	calls    int
	tenantID string
	settings store.TenantSettings
	err      error
}

func (s *settingsStore) GetTenantSettings(_ context.Context, tenantID string) (store.TenantSettings, error) {
	s.calls++
	s.tenantID = tenantID
	return s.settings, s.err
}

// Aserciones de compilación: la política es un modules.ResumePolicy y el gemelo en
// memoria del almacén sirve de cart.ResumeStore.
var (
	_ modules.ResumePolicy = (*cart.ResumePolicy)(nil)
	_ cart.ResumeStore     = (*settingsStore)(nil)
	_ cart.ResumeStore     = (*store.MemoryRepository)(nil)
)

// Restart: solo un nivel terminal reinicia; nunca hay aviso, efectos ni error, y el
// almacén no se toca.
func TestResumePolicy_RestartOnlyOnTerminalLevel(t *testing.T) {
	levels := map[string]bool{
		cart.LevelCategories: false, cart.LevelArticles: false, cart.LevelArticle: false,
		cart.LevelVariant: false, cart.LevelQuantity: false, cart.LevelContinue: false,
		cart.LevelSummary: false, cart.LevelItemNoteScope: false, cart.LevelItemNote: false,
		cart.LevelOrderNote: false, cart.LevelBuyerData: false,
		cart.LevelClosed: true, cart.LevelCancelled: true,
		"nivel_que_no_existe": false,
	}
	spy := &settingsStore{err: errors.New("no debía leerse")}
	p := cart.NewResumePolicy(spy)
	for level, want := range levels {
		vars := map[string]any{}
		seedState(t, vars, cartState{Level: level, Started: true})
		restart, notice, effects, err := p.Restart(context.Background(), "tenant-1", "contact-1", vars)
		if restart != want || notice != "" || effects != nil || err != nil {
			t.Errorf("Restart(%s) = %v, %q, %v, %v; quiero %v sin aviso, efectos ni error", level, restart, notice, effects, err, want)
		}
	}
	if spy.calls != 0 {
		t.Errorf("Restart leyó el almacén %d veces, quiero 0", spy.calls)
	}
}

// Restart lee el estado con la tolerancia del módulo: sin estado es el arranque, no
// un terminal; y el tipo nativo vale igual que el round-trip.
func TestResumePolicy_RestartToleratesStateShapes(t *testing.T) {
	p := cart.NewResumePolicy(&settingsStore{})
	cases := []struct {
		name string
		vars map[string]any
		want bool
	}{
		{name: "nil vars", vars: nil, want: false},
		{name: "no state", vars: map[string]any{"otra": 1}, want: false},
		{name: "nil state", vars: map[string]any{stateVarKey: nil}, want: false},
		{name: "unreadable state", vars: map[string]any{stateVarKey: "closed"}, want: false},
		{name: "empty level", vars: map[string]any{stateVarKey: map[string]any{"started": true}}, want: false},
		{name: "native map closed", vars: map[string]any{stateVarKey: map[string]any{"level": "closed"}}, want: true},
		{name: "native string map cancelled", vars: map[string]any{stateVarKey: map[string]string{"level": "cancelled"}}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restart, _, _, err := p.Restart(context.Background(), "t", "c", tc.vars)
			if err != nil || restart != tc.want {
				t.Errorf("Restart = %v, %v; quiero %v, nil", restart, err, tc.want)
			}
		})
	}
}

// Seed siembra el page_size y el checklist del tenant, tal como los da el almacén, y
// no toca ninguna otra clave.
func TestResumePolicy_SeedWritesPageSizeAndBuyerFields(t *testing.T) {
	fields := []store.BuyerField{{Key: "rut", Label: "RUT", Required: true}}
	spy := &settingsStore{settings: store.TenantSettings{PageSize: 3, BuyerFields: fields}}
	vars := map[string]any{"otra": "intacta", cart.VarPageSize: 99}
	if err := cart.NewResumePolicy(spy).Seed(context.Background(), "tenant-7", vars); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if spy.calls != 1 || spy.tenantID != "tenant-7" {
		t.Errorf("lecturas = %d del tenant %q, quiero 1 de tenant-7", spy.calls, spy.tenantID)
	}
	want := map[string]any{"otra": "intacta", cart.VarPageSize: 3, cart.VarBuyerFields: fields}
	if !reflect.DeepEqual(vars, want) {
		t.Errorf("vars = %v\nquiero %v", vars, want)
	}
}

// Lo que Seed siembra es lo que el módulo lee: con el gemelo en memoria del almacén,
// el carrito pagina con el page_size del tenant y pregunta su checklist.
func TestResumePolicy_SeedFeedsTheModule(t *testing.T) {
	repo := store.NewMemoryRepository()
	settings := store.DefaultTenantSettings("tenant-1")
	settings.PageSize = 1
	settings.BuyerFields = []store.BuyerField{{Key: "rut", Label: "RUT", Required: true}}
	repo.SetTenantSettings(settings)

	vars := seededVars()
	if err := cart.NewResumePolicy(repo).Seed(context.Background(), "tenant-1", vars); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	m := cart.New()
	_, outs, _ := drive(t, m, vars, "zzz")
	mustScreen(t, outs, invalidPrefix+"🛒 Elige una categoría:\n1) Bebidas\n3) Más ▾")
	_, outs, _ = drive(t, m, walk(t, m, vars, "1", "1", "2", "1", "2"), "1")
	mustContain(t, outs, "Escribe tu RUT.")

	// Un tenant sin fila hereda los defaults: página de 5 y sin checklist.
	other := seededVars()
	if err := cart.NewResumePolicy(repo).Seed(context.Background(), "tenant-sin-fila", other); err != nil {
		t.Fatalf("Seed (tenant sin fila): %v", err)
	}
	if other[cart.VarPageSize] != store.DefaultPageSize {
		t.Errorf("page_size = %v, quiero el default %d", other[cart.VarPageSize], store.DefaultPageSize)
	}
}

// Si el almacén falla, el error sube envuelto y vars no se toca.
func TestResumePolicy_SeedWrapsStoreError(t *testing.T) {
	boom := errors.New("pg caído")
	vars := map[string]any{"otra": "intacta"}
	err := cart.NewResumePolicy(&settingsStore{err: boom}).Seed(context.Background(), "tenant-1", vars)
	if !errors.Is(err, boom) {
		t.Fatalf("Seed = %v, quiero un error que envuelva el del almacén", err)
	}
	if got, want := err.Error(), "cart: config de tenant (page_size, buyer_fields): pg caído"; got != want {
		t.Errorf("error = %q, quiero %q", got, want)
	}
	if !reflect.DeepEqual(vars, map[string]any{"otra": "intacta"}) {
		t.Errorf("vars = %v, no debía tocarse", vars)
	}
}
