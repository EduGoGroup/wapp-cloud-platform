// Porta internal/intake/stages/plazo.go @ 4cd9cfb

package stages

import (
	"context"
	"time"
)

// deadline.go — EL PLAZO **POR LLAMADA** de las etapas que hablan con el modelo.
//
// # POR QUÉ NO VALE ACOTAR EN EL WORKER
//
// Lo natural sería que el worker envolviera cada `Run` en un `context.WithTimeout` y no
// tocar este paquete. NO SIRVE, y el motivo es P3: su `Run` hace N llamadas dentro. Un
// plazo puesto por fuera se REPARTE entre ellas —la primera se lleva casi todo y la
// última casi nada— y el breaker del adaptador se queda ciego. Un plazo POR LLAMADA
// solo se puede aplicar donde están las llamadas, y están aquí.
//
// # POR QUÉ ES UNA OPCIÓN VARIÁDICA Y NO UN PARÁMETRO MÁS
//
// Porque no pasar nada tiene un significado legítimo: sin plazo propio, se hereda el
// ctx del llamante.
//
// 🔴 EL DEFAULT ES «SIN PLAZO», Y ESO NO ES SEGURO — ES COMPATIBLE. Una etapa construida
// sin la opción manda al adaptador un ctx sin deadline y el adaptador local cae a sus
// 30 s ⇒ umbral de lento en 24 s ⇒ una P3 caliente de 27 s ya cuenta como lenta. Quien
// construya etapas PARA PRODUCCIÓN tiene que pasar la opción (R-03); un default distinto
// de cero aquí dentro escondería la decisión en el constructor, que es justo lo que
// DefaultZone evita en P4 obligando al llamante a escribirla.
//
// 🔴 ES EL TIPO DE OPCIÓN DE P2, P3 Y P4 Y DE NADIE MÁS (R-03). `match` y `draft` no
// llaman al modelo y llevan sus propios tipos de opción: así «match con plazo» no
// compila.

// Option (antes `Opción`) configura una etapa LLM —P2, P3 o P4— al construirla.
type Option func(*callLimits)

// callLimits son los ajustes que comparten las tres etapas. Es UNA struct y no un campo
// suelto por etapa para que añadir el siguiente ajuste no obligue a tocar tres
// constructores.
type callLimits struct {
	// perCall es cuánto puede durar UNA llamada al modelo. <= 0 significa «sin plazo
	// propio»: se hereda el ctx del llamante tal cual.
	perCall time.Duration
}

// WithCallTimeout (antes `ConPlazoPorLlamada`) fija cuánto puede durar CADA llamada al
// modelo de la etapa. En P3 se aplica a cada ítem del fan-out, no al fan-out entero.
//
// Un valor <= 0 se ignora y deja el comportamiento heredado (sin plazo propio), que es
// lo mismo que no pasar la opción. No se rechaza con error porque el llamante natural es
// una configuración con default, y un cero ahí significa «no configurado», no «cero
// segundos».
func WithCallTimeout(d time.Duration) Option {
	return func(l *callLimits) {
		if d > 0 {
			l.perCall = d
		}
	}
}

// newCallLimits aplica las opciones en orden; una opción nil se salta.
func newCallLimits(opts []Option) callLimits {
	var l callLimits
	for _, opt := range opts {
		if opt != nil {
			opt(&l)
		}
	}
	return l
}

// bound envuelve el ctx con el plazo por llamada. Devuelve SIEMPRE una función de
// cancelación llamable —nunca nil— para que el llamante pueda hacer `defer cancel()` sin
// preguntar si hay plazo: un `if` alrededor del defer es la forma clásica de filtrar el
// context cuando alguien añade un `return` en medio.
//
// 🔴 NO se usa `context.WithTimeoutCause`: el error que sale de aquí lo clasifica el
// worker por FAMILIA (`llm.ErrLLMQuality` o no), y un `DeadlineExceeded` es
// infraestructura venga con la causa que venga.
func (l callLimits) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	if l.perCall <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, l.perCall)
}
