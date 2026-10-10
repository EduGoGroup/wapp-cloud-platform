// Porta internal/flujos/runtime/runtime_engine.go @ e0159171

package runtime

// runtime_engine_streak.go es el corte por tema de runtime_engine.go (05 E-13): la cara del
// Runtime hacia la racha de auto-respuestas (RT-11; Plan 049 · Opción A). Solo se movieron
// declaraciones; el contador lo construye New (runtime_engine.go) y vive en streak.go.

// WithAutoreplyStreakHook inyecta el observador de rachas de auto-respuestas (Plan 049 ·
// Opción A). Recibe la LONGITUD de cada racha al CERRARSE su episodio, UNA vez por episodio:
// nunca en cada auto-respuesta, nunca una longitud <= 0.
//
// Sin él (nil) el contador sigue contando y MaxAutoreplyStreak sigue contestando. El hook
// llega a recibir las rachas se pase antes o después que cualquier otra opción: New no
// congela su valor al construir el contador.
func WithAutoreplyStreakHook(fn func(streak int)) Option {
	return func(rt *Runtime) { rt.onAutoreplyStreak = fn }
}

// MaxAutoreplyStreak devuelve la racha de auto-respuestas VIVA más larga en este instante
// (Plan 049 · Opción A), o 0 si no hay ninguna. Es la fuente del gauge
// wapp_flow_autoreply_streak_max: su firma, func() int, es la que el arranque inyecta.
//
// RT-11 · 🔴 NO es un getter puro (trampa T-7: `/metrics` no es inocuo). En el mismo
// recorrido BARRE las rachas vencidas por inactividad (30 min sin auto-respuesta): las borra
// y las reporta al hook de WithAutoreplyStreakHook. Es el único sitio donde se cierra el
// episodio de la conversación que se abandona y no vuelve. Una vencida no cuenta para el
// máximo, y dos llamadas seguidas no reportan dos veces la misma.
//
// El instante lo pone el reloj del runtime (WithClock), no un parámetro.
//
// Pensada para el scrape, no para el camino caliente: recorre todas las conversaciones vivas
// bajo el candado del contador.
//
// Sobre un *Runtime nil, o sobre uno que no salió de New, devuelve 0 sin entrar en pánico.
func (rt *Runtime) MaxAutoreplyStreak() int {
	if rt == nil || rt.now == nil {
		return 0
	}
	return rt.autoreplyStreaks.Max(rt.now())
}
