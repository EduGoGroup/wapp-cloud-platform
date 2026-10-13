//go:build integracion && edgereal

package procesos

// La reanudación del carrito, vista desde fuera (fase FW · TW.11 · cierra la deuda D-39).
//
// El arranque cablea `flowruntime.WithResumePolicy(cart.NodeTypeCart, …)`: en cada mensaje de un
// carrito vivo, la política relee los ajustes de la empresa y los siembra en el estado de la
// conversación. Sin ella el carrito sigue andando con los valores por defecto, y por eso
// TestP3_CartConversation no nota su falta: su empresa no tiene ajustes propios. Esta sí: pagina de
// uno en uno.

import (
	"context"
	"strings"
	"testing"
	"time"
)

const (
	// resumePageSize es el tamaño de página propio de la empresa (el valor por defecto es 5).
	resumePageSize = 1
	// resumeArticlesFirst y resumeArticlesNext son las dos páginas de Bebidas cuando se pagina de
	// uno en uno: el primer artículo con la opción de ver más, y el segundo. Con el tamaño por
	// defecto la pantalla es cartArticles, los dos artículos juntos.
	resumeArticlesFirst = "Bebidas:\n1) Café · $2.50\n3) Más ▾\n0) ← Volver"
	resumeArticlesNext  = "Bebidas:\n2) Té · $2.00\n0) ← Volver"
	// resumeMore es el código de «Más ▾» en esa lista: el siguiente al mayor código de artículo.
	resumeMore = "3"
	// resumeStateQuery lee el nivel del carrito y el tamaño de página sembrado en su estado.
	resumeStateQuery = `SELECT concat_ws('|', coalesce(vars->'cart'->>'level', ''), coalesce(vars->>'cart_page_size', 'sin sembrar'))
		FROM public.flow_state WHERE tenant_id::text = $1 AND session_id = $2`

	resumeReconnectTimeout = 90 * time.Second
)

// resumeSetPageSize fija el tamaño de página del carrito de la empresa. Es un upsert: crea la fila
// de ajustes si no existe y no toca el resto.
//
// 🔧 POR QUÉ NO HAY PUERTA HTTP: ninguna ruta de /api/v1 ni de /admin escribe page_size; es un
// ajuste que hoy solo cambia el operador por SQL (ver ventanaInmediata, en fixtures_test.go).
func resumeSetPageSize(t *testing.T, sc edgeRealScene, size int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), topeFixture)
	defer cancel()
	// tenant_settings.tenant_id es TEXT (la PK de 0013), no UUID.
	if _, err := sc.DB.ExecContext(ctx, `
		INSERT INTO public.tenant_settings (tenant_id, page_size) VALUES ($1, $2)
		ON CONFLICT (tenant_id) DO UPDATE SET page_size = EXCLUDED.page_size`, sc.Tenant, size); err != nil {
		t.Fatalf("fijar el tamaño de página de la empresa: %v", err)
	}
}

// TestEdgeReal_CartResumePolicy interrumpe un carrito y lo reanuda, con Edge real:
//
//   - La empresa pagina de uno en uno (tenant_settings.page_size = 1).
//   - «carrito» abre el evento. La primera pantalla la pinta el arranque del flujo, que aún no
//     tiene ajustes sembrados: salen las dos categorías.
//   - INTERRUPCIÓN: el servidor se reinicia. El carrito queda a medias en Postgres y el Edge real
//     se queda sin stream.
//   - El cliente vuelve y elige «1» (Bebidas). El Edge real reconecta solo y entrega; el servidor
//     retoma el carrito donde estaba y, antes de avanzar, la política de reanudación siembra los
//     ajustes de la empresa: la lista sale de UNO en uno, con «Más ▾». Sin la política saldrían los
//     dos artículos juntos (cartArticles) y este caso falla: es su mutante.
//   - «3» (Más) pasa a la segunda página, y el estado guarda el tamaño de página de la empresa.
func TestEdgeReal_CartResumePolicy(t *testing.T) {
	t.Parallel()
	sc := edgeRealNewScene(t, "erresume", "er-reanuda")
	p3Scene{edgeEscenario: sc.edgeEscenario, Pub: sc.Pub}.installCart(t)
	resumeSetPageSize(t, sc, resumePageSize)
	sc.expectText(t, sc.SelfPn, p3PassiveNotice(t))
	sc.setProfile(t, "active")

	sc.cartSay(t, 1, cartKeyword)
	sc.expectText(t, cartPn, p3Welcome)
	sc.expectText(t, cartPn, cartCategories)
	edgeEsperarValor(t, sc.DB, "|sin sembrar", "el carrito recién abierto (en las categorías, que es el nivel inicial, y sin ajustes sembrados)", resumeStateQuery, sc.Tenant, sc.SessionID)

	previous := serverRestart(t, sc.S)
	if strings.Contains(previous, `"level":"ERROR"`) {
		t.Errorf("el servidor dejó alguna línea ERROR antes de reiniciarse")
	}
	edgeEsperar(t, resumeReconnectTimeout, "que el Edge real reconecte con el servidor reiniciado", func() bool {
		for _, l := range sc.S.LineasLog() {
			if l["msg"] == "heartbeat" && l["session_id"] == sc.SessionID {
				return true
			}
		}
		return false
	})

	sc.cartSay(t, 2, "1")
	sc.expectText(t, cartPn, resumeArticlesFirst)
	sc.cartSay(t, 3, resumeMore)
	sc.expectText(t, cartPn, resumeArticlesNext)
	edgeEsperarValor(t, sc.DB, "articles|1", "el carrito reanudado, con el tamaño de página de la empresa", resumeStateQuery, sc.Tenant, sc.SessionID)
	sc.expectNoPendingText(t, "tras la segunda página")
	edgeSinErrores(t, sc.S, nil)
}
