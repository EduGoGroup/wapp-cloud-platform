package cart

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// content del nodo con el catálogo resuelto (lo que el engine entrega a Prime).
func primeContent() model.Content { return model.Content{Raw: catalogRaw()} }

// intentVars arma unas Vars con intent_params sembrados (map nativo, como el runtime).
func intentVars(params map[string]string) map[string]any {
	p := make(map[string]any, len(params))
	for k, v := range params {
		p[k] = v
	}
	return map[string]any{modules.VarIntentParams: p, modules.VarIntentName: "pedido"}
}

// effectNames extrae los nombres de los efectos declarados.
func effectNames(effs []modules.Effect) []string {
	out := make([]string, 0, len(effs))
	for _, e := range effs {
		out = append(out, e.Name)
	}
	return out
}

func TestPrime_ClearMatch_PreAddsAndConfirms(t *testing.T) {
	m := New()
	res, handled := m.Prime(model.Node{}, primeContent(), intentVars(map[string]string{"producto": "cafe", "cantidad": "2"}))
	if !handled {
		t.Fatal("un producto que casa claro debe pre-cargar (handled=true)")
	}
	st := loadState(res.Vars)
	if st.Level != LevelContinue {
		t.Fatalf("tras el pre-add debe saltar a la confirmación de ítem (continue), got %q", st.Level)
	}
	if len(st.Lines) != 1 || st.Lines[0].SKU != "CAFE" || st.Lines[0].Qty != 2 {
		t.Fatalf("línea pre-agregada inesperada: %+v", st.Lines)
	}
	if !st.Started {
		t.Fatal("el pre-add es el arranque: Started debe quedar true")
	}
	mustContain(t, res.Outputs, "Agregué", "Café", "Finalizar pedido")
	if got := effectNames(res.Effects); len(got) != 2 || got[0] != EffectCartStarted || got[1] != EffectItemAdded {
		t.Fatalf("el pre-add debe declarar cart_started + item_added, got %v", got)
	}
	// Params CONSUMIDOS una sola vez: ya no están en Vars.
	if _, ok := res.Vars[modules.VarIntentParams]; ok {
		t.Fatal("intent_params debe consumirse (limpiarse de Vars) tras el pre-add")
	}
	if _, ok := res.Vars[modules.VarIntentName]; ok {
		t.Fatal("intent_name debe limpiarse junto con los params")
	}
}

func TestPrime_MatchAcrossCategory_SetsFocus(t *testing.T) {
	m := New()
	res, handled := m.Prime(model.Node{}, primeContent(), intentVars(map[string]string{"producto": "flan"}))
	if !handled {
		t.Fatal("flan casa claro en Postres")
	}
	st := loadState(res.Vars)
	if st.CatCode != "2" || st.SKU != "FLAN" || len(st.Lines) != 1 {
		t.Fatalf("debe fijar la categoría/artículo en foco y agregar Flan, got %+v", st)
	}
}

func TestPrime_QuantityInvalidDefaultsToOne(t *testing.T) {
	m := New()
	for _, qty := range []string{"", "abc", "0", "-3"} {
		res, handled := m.Prime(model.Node{}, primeContent(), intentVars(map[string]string{"producto": "cafe", "cantidad": qty}))
		if !handled {
			t.Fatalf("qty=%q: debe pre-cargar", qty)
		}
		st := loadState(res.Vars)
		if len(st.Lines) != 1 || st.Lines[0].Qty != 1 {
			t.Fatalf("qty=%q inválida/ausente debe caer a 1, got %+v", qty, st.Lines)
		}
	}
}

func TestPrime_Ambiguous_FallsBackToNormalFlow(t *testing.T) {
	m := New()
	// "cafe flan" casa Café y Flan con el mismo score ⇒ ambiguo ⇒ flujo normal (L1),
	// sin inventar una línea.
	res, handled := m.Prime(model.Node{}, primeContent(), intentVars(map[string]string{"producto": "cafe flan"}))
	if !handled {
		t.Fatal("con intent_params presentes Prime consume y maneja (handled=true), aun sin match")
	}
	st := loadState(res.Vars)
	if st.Level != LevelCategories || len(st.Lines) != 0 {
		t.Fatalf("ambiguo debe arrancar normal desde categorías sin líneas, got %+v", st)
	}
	if len(res.Effects) != 0 {
		t.Fatalf("sin pre-add no debe declarar efectos, got %v", effectNames(res.Effects))
	}
	mustContain(t, res.Outputs, "Elige una categoría")
	if _, ok := res.Vars[modules.VarIntentParams]; ok {
		t.Fatal("los params deben consumirse aun sin match")
	}
}

func TestPrime_NoMatch_FallsBackToNormalFlow(t *testing.T) {
	m := New()
	res, handled := m.Prime(model.Node{}, primeContent(), intentVars(map[string]string{"producto": "zzz-inexistente"}))
	if !handled {
		t.Fatal("con params presentes maneja (handled=true)")
	}
	st := loadState(res.Vars)
	if st.Level != LevelCategories || len(st.Lines) != 0 {
		t.Fatalf("sin match debe arrancar normal, got %+v", st)
	}
	mustContain(t, res.Outputs, "Elige una categoría")
}

func TestPrime_NoProducto_FallsBackToNormalFlow(t *testing.T) {
	m := New()
	res, handled := m.Prime(model.Node{}, primeContent(), intentVars(map[string]string{"cantidad": "3"}))
	if !handled {
		t.Fatal("con params (aunque sin producto) maneja")
	}
	if st := loadState(res.Vars); st.Level != LevelCategories || len(st.Lines) != 0 {
		t.Fatalf("sin producto debe arrancar normal, got %+v", st)
	}
}

func TestPrime_NoIntentParams_NotHandled(t *testing.T) {
	m := New()
	// Vars sin intent_params (arranque normal / API): Prime es un no-op.
	_, handled := m.Prime(model.Node{}, primeContent(), map[string]any{})
	if handled {
		t.Fatal("sin intent_params Prime NO debe manejar (⇒ Render normal, no-regresión)")
	}
}

func TestPrime_CatalogUnavailable_NotHandled(t *testing.T) {
	m := New()
	// Con params pero sin catálogo resuelto: degrada al Render normal (catalogUnavailable).
	_, handled := m.Prime(model.Node{}, model.Content{}, intentVars(map[string]string{"producto": "cafe"}))
	if handled {
		t.Fatal("catálogo no disponible ⇒ Prime NO maneja (deja el Render normal)")
	}
}
