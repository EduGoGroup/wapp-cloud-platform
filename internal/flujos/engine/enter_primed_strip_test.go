// enter_primed_strip_test.go cubre los criterios (a) y (b) de T4.3 (Plan 046 · Ola 4,
// REQ-18): cuando NADIE consume la señal de intención, EnterPrimed la BARRE de Vars en
// vez de dejarla caer al renderFrom y de ahí al Save.
//
// 🔒 QUÉ SE ESTÁ PROTEGIENDO. VarIntentParams y VarIntentName llevan TEXTO EXTRAÍDO DEL
// MENSAJE DEL CLIENTE. El contrato dice que quien las consume las limpia, y el carrito
// lo cumple —pero ese contrato solo corre cuando ALGUIEN consume. Las seis ramas por
// las que tryPrime devuelve handled=false no consumen nada, así que sin este barrido
// las claves sobreviven hasta el JSONB de public.flow_state y se quedan ahí para
// siempre. La fuga es la del DISCO, no la del struct.
//
// 💥 MUTACIÓN: quitar la línea `st.Vars = modules.StripIntentSignal(st.Vars)` de
// EnterPrimed (engine.go) ⇒ los dos tests de este fichero se ponen ROJOS. Es el estado
// exacto en el que nacieron: antes de T4.3 los params SÍ quedaban en Vars.
package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
)

// sourceQueFalla hace que content.Resolve devuelva error, que es la rama de tryPrime
// donde el módulo SÍ tiene capacidad Primer pero el catálogo no se puede resolver.
// Esa rama degrada a handled=false a propósito (no aborta el arranque), y es
// justamente por donde los params se escapaban con un módulo que sí sabía consumirlos.
type sourceQueFalla struct{}

func (sourceQueFalla) Resolve(_ context.Context, _ string, _ model.Node) (model.Content, error) {
	return model.Content{}, errors.New("catálogo no disponible")
}

// varsConSeñal arma unas Vars con la señal de intención MÁS una variable de negocio
// cualquiera. La segunda no es decorado: comprueba que el barrido quita las dos claves
// y NO se lleva por delante nada más.
func varsConSeñal() map[string]any {
	return map[string]any{
		modules.VarIntentParams: map[string]any{"producto": "empanada de pino"},
		modules.VarIntentName:   "comprar",
		"pedido_previo":         "abc-123",
	}
}

// assertSinSeñal es la aserción de los dos criterios, con el porqué en el mensaje.
func assertSinSeñal(t *testing.T, vars map[string]any, caso string) {
	t.Helper()
	if v, ok := vars[modules.VarIntentParams]; ok {
		t.Fatalf("%s: intent_params sigue en Vars (%v). Nadie consumió la señal, así que "+
			"nadie la limpió: de aquí cae al renderFrom y al Save, y el TEXTO DEL CLIENTE "+
			"queda en claro en el JSONB de public.flow_state para siempre (REQ-18)", caso, v)
	}
	if v, ok := vars[modules.VarIntentName]; ok {
		t.Fatalf("%s: intent_name sigue en Vars (%v) — se barrió una clave de las dos, "+
			"que es peor que no barrer ninguna: parece hecho", caso, v)
	}
	if got := vars["pedido_previo"]; got != "abc-123" {
		t.Fatalf("%s: el barrido se llevó una variable que NO es de la señal "+
			"(pedido_previo = %v, quiero abc-123)", caso, got)
	}
}

// TestEnterPrimed_ModuloSinPrimer_BarreLaSeñal es el criterio (a): un nodo inicial que
// no implementa Primer (un menú) con intent_params sembrados.
//
// 📌 Complementa a TestEnterPrimed_NonPrimerModule_IgnoresParams, que ya existía y
// afirma que el menú los IGNORA. «Ignorarlos» era exactamente el problema: los ignoraba
// y los dejaba escritos.
func TestEnterPrimed_ModuloSinPrimer_BarreLaSeñal(t *testing.T) {
	e := newEngine()
	vars := varsConSeñal()

	st, outs, effects, err := e.EnterPrimed(context.Background(), menuFlowForPrime(),
		model.Conversation{Vars: vars})
	if err != nil {
		t.Fatalf("EnterPrimed: %v", err)
	}

	assertSinSeñal(t, st.Vars, "módulo sin Primer")

	// No-regresión: el barrido no cambia a dónde va ni qué muestra.
	if st.CurrentNode != "root" || len(outs) == 0 || len(effects) != 0 {
		t.Fatalf("el barrido alteró el arranque: node=%q outs=%d eff=%d", st.CurrentNode, len(outs), len(effects))
	}
}

// TestEnterPrimed_CatalogoNoResoluble_BarreLaSeñal es el criterio (b): el nodo inicial
// SÍ es un cart —un módulo con capacidad Primer, que en el camino feliz consumiría la
// señal él mismo— pero el contenido no se resuelve, así que tryPrime degrada a
// handled=false y el Prime del carrito NUNCA llega a correr. Sin este barrido, la ruta
// del módulo que sí sabe limpiar es precisamente la que dejaba la fuga.
func TestEnterPrimed_CatalogoNoResoluble_BarreLaSeñal(t *testing.T) {
	reg := modules.NewRegistry()
	reg.Register(cart.New())
	var src content.Source = sourceQueFalla{}
	e := engine.New(reg, engine.WithContentSource(src))

	f := model.Flow{
		FlowID:  "compra",
		Version: 1,
		Initial: "root",
		Nodes:   map[string]model.Node{"root": {Type: cart.NodeTypeCart}},
	}

	st, _, _, err := e.EnterPrimed(context.Background(), f, model.Conversation{Vars: varsConSeñal()})
	if err == nil {
		// renderFrom vuelve a resolver y devuelve el mismo error una sola vez: que lo
		// haga o no depende del módulo, pero el barrido tiene que haber ocurrido igual.
		assertSinSeñal(t, st.Vars, "catálogo no resoluble (sin error)")
		return
	}
	assertSinSeñal(t, st.Vars, "catálogo no resoluble")
}

// TestStripIntentSignal_NoMutaElMapaRecibido protege el contrato que EnterPrimed
// documenta en su cabecera: «no muta el estado recibido salvo por reasignación de
// campos». Los mapas en Go son referencias, así que un delete() directo habría mutado
// también el mapa del llamante —el que start.go acaba de sembrar—, y eso es un efecto
// a distancia que nadie espera de una función de lectura.
func TestStripIntentSignal_NoMutaElMapaRecibido(t *testing.T) {
	original := varsConSeñal()

	limpio := modules.StripIntentSignal(original)

	if _, ok := original[modules.VarIntentParams]; !ok {
		t.Fatal("StripIntentSignal mutó el mapa recibido: el llamante perdió intent_params")
	}
	if _, ok := limpio[modules.VarIntentParams]; ok {
		t.Fatal("StripIntentSignal devolvió un mapa que aún tiene intent_params")
	}
}

// TestStripIntentSignal_SinSeñalDevuelveElMismoMapa es la razón de que esto no cueste
// una copia por conversación: el caso común —todo arranque que no venga del
// clasificador— no clona nada.
func TestStripIntentSignal_SinSeñalDevuelveElMismoMapa(t *testing.T) {
	vars := map[string]any{"pedido_previo": "abc-123"}

	limpio := modules.StripIntentSignal(vars)

	if len(limpio) != len(vars) {
		t.Fatalf("sin señal no debe cambiar nada: %d claves vs %d", len(limpio), len(vars))
	}
	limpio["marca"] = true
	if _, ok := vars["marca"]; !ok {
		t.Fatal("sin señal debe devolver EL MISMO mapa (sin copiar), y devolvió una copia")
	}
}
