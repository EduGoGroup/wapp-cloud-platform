// Copia de internal/bootstrap/arranque/flow_options_cableadas_test.go @ 80807ba (F0 · 05 §6). Desde F8 (T8.32,
// conmutar(conversacion)) el `flowruntime` que se vigila es internal/modulos/conversacion/runtime, que conserva el
// alias, y la lista de requeridas son TODAS las opciones que el arranque pasa a flowruntime.New (eran tres).
package arranque

import (
	"go/ast"
	"testing"
)

// TestFlowRuntimeOptionsCableadas fija, contra el ÁRBOL DE SINTAXIS de bootstrap.go, que las
// Options del motor de flujos que no tienen otra red están efectivamente cableadas.
//
// POR QUÉ UN TEST DE AST Y NO UNO NORMAL: `flowruntime.New(...)` se invoca inline dentro del
// arranque, con dependencias que exigen Postgres, R2 y el gateway gRPC vivos — no hay costura
// que permita construir las Options y mirarlas. Y la comprobación importa lo suficiente como
// para no dejarla sin red: las Options son variádicas, así que **omitir una compila, pasa el
// vet, pasa el lint y deja el paquete entero en verde**. Ese es exactamente el modo en que
// falló `WithOpeningBuilder`.
//
// LA HISTORIA, para que no se repita: `WithOpeningBuilder` llegó CONSTRUIDA y PROBADA con el
// Plan 043 (T3.8) y se quedó SIN ENCHUFAR hasta el 2026-08-12. Con `opening` a nil, la rama
// Fallback de `handleTrigger` cae SIEMPRE a `startPlainFlow` —el camino que la enmienda E-9 del
// ADR-0029 vino a reemplazar—, que arranca el flujo del tenant SIN evento padre. En un tenant
// cuyo flujo lleva un nodo `cart`, eso es una comanda perdida en silencio contra el NOT NULL de
// `intakes.event_id` (migración 0054). Se midió DOS veces en UAT el mismo día (hallazgos #001 y
// #003 de docs/runbooks/bitacora-errores-uat.md) antes de que nadie mirara el cableado, porque
// no había una sola señal roja que apuntara aquí.
//
// Este test es esa señal. Si mañana alguien reordena el arranque y una de estas Options se cae
// por el camino, el rojo sale aquí y no en la bandeja de solicitudes de un cliente.
func TestFlowRuntimeOptionsCableadas(t *testing.T) {
	// Cada entrada es una Option cuya ausencia NO rompe nada visible: el motor arranca, los
	// tests pasan y la capacidad simplemente no existe en producción.
	//
	// 🔀 F8 · conmutar(conversacion): son las 21 opciones distintas (22 llamadas: WithEventSink
	// va dos veces) que construirRuntimeDeFlujos pasa a flowruntime.New. Al reescribir esa
	// lista contra el runtime nuevo, cualquiera pudo caerse sin dar un solo rojo.
	requeridas := map[string]string{
		"WithOpeningBuilder": "sin ella el fallback cae a startPlainFlow (arranque SIN evento): " +
			"comanda perdida en silencio si el flujo del tenant lleva un nodo cart (E-9, hallazgos #001/#003)",
		"WithDispatcher": "sin él no hay menú del despachador: la elección explícita de tipo — la " +
			"TERCERA puerta del nacimiento del evento (T2.5/REQ-01b) — deja de existir",
		"WithFlowForKind":           "sin él el salto por tipo no sabe qué flujo arrancar para un event_kind",
		"WithEventSink":             "sin ella ningún efecto de módulo se persiste ni sale al puente CRM: el runtime se queda con su LogSink de relleno",
		"WithAggregator":            "sin ella ninguna ventana de captación se abre: el pipeline LLM no recibe un solo job",
		"WithWelcomeStore":          "sin ella no hay bienvenida única («estamos procesando») al primer mensaje",
		"WithResumePolicy":          "sin ella un carrito a medias no se reanuda: el cliente vuelve y empieza de cero",
		"WithPresignClient":         "sin ella el nodo media no puede firmar la URL del adjunto",
		"WithTriggerResolver":       "sin ella ninguna regla de disparo casa: todo entrante cae al fallback",
		"WithEventStore":            "sin ella un event_start arranca su flujo sin parir evento y un event_stop no desactiva nada",
		"WithIntakeAbandoner":       "sin ella abandonar un evento deja su solicitud abierta para siempre",
		"WithSummarySources":        "sin ella los abandonos no dejan resumen en el historial",
		"WithEntitlements":          "sin ella no hay gate por plan: ni hilo del evento ni bienvenida para quien tiene llm_intake",
		"WithReplyLimiter":          "sin ella no hay límite de respuestas por conversación",
		"WithIncomingTimeout":       "sin ella un entrante colgado no tiene plazo",
		"WithMaxConcurrentIncoming": "sin ella no hay semáforo de entrantes concurrentes",
		"WithSelfNumbers":           "sin ella el anti-self-loop no bloquea: dos sesiones del mismo tenant se hablarían para siempre",
		"WithIngestDeduper":         "sin ella un entrante reentregado se procesa dos veces",
		"WithReactiveBlockedHook":   "sin ella los entrantes cortados (passive / self-loop / rate-limit) no se cuentan en /metrics",
		"WithAutoreplyStreakHook":   "sin ella el histograma de rachas de auto-respuesta se queda vacío",
		"WithDepositReminder":       "sin ella el tercer toque del recordatorio de la seña no se dispara nunca",
	}

	_, ficheros := astDelArranque(t)

	// pasadas son las opciones que de verdad viajan como argumento de flowruntime.New: una
	// opción construida en otra parte del arranque y no pasada al runtime no cablea nada.
	vistas, pasadas := map[string]bool{}, map[string]bool{}
	constructores := 0
	inspecciona(ficheros, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if esLlamada(call, "flowruntime", "New") {
			constructores++
			for _, opcion := range runtimeOptionsAmong(call.Args) {
				pasadas[opcion] = true
			}
		}
		// Interesa la forma `flowruntime.WithX(...)`: un selector sobre el paquete.
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "flowruntime" {
			return true
		}
		vistas[sel.Sel.Name] = true
		return true
	})

	if constructores != 1 {
		t.Errorf("flowruntime.New aparece %d veces en la producción de internal/arranque; se espera 1 (EL runtime, T-1)", constructores)
	}
	for opcion, porQue := range requeridas {
		if !vistas[opcion] {
			t.Errorf("flowruntime.%s NO está cableada en internal/arranque — %s", opcion, porQue)
		} else if !pasadas[opcion] {
			t.Errorf("flowruntime.%s se construye pero NO es un argumento de flowruntime.New — %s", opcion, porQue)
		}
	}
	// Y al revés: una opción que el arranque pasa al runtime y que esta lista no conoce es una
	// opción sin red. Quien la añada, la añade aquí con su porqué.
	for opcion := range pasadas {
		if _, ok := requeridas[opcion]; !ok {
			t.Errorf("flowruntime.New recibe flowruntime.%s, que no está en `requeridas`: añádela con lo que se rompe sin ella", opcion)
		}
	}
}

// runtimeOptionsAmong devuelve, de una lista de argumentos, los nombres de los que son una
// llamada `flowruntime.WithX(...)`.
func runtimeOptionsAmong(args []ast.Expr) []string {
	var options []string
	for _, arg := range args {
		opt, ok := arg.(*ast.CallExpr)
		if !ok {
			continue
		}
		if sel, ok := opt.Fun.(*ast.SelectorExpr); ok && campoCompletoDe(sel.X) == "flowruntime" {
			options = append(options, sel.Sel.Name)
		}
	}
	return options
}
