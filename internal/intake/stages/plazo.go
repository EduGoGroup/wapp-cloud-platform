package stages

import (
	"context"
	"time"
)

// ════════════════════════════════════════════════════════════════════════════
// 🔴 EL PLAZO **POR LLAMADA** — LA MITAD QUE T2.3 DEJÓ SEÑALADA Y T2.5 CIERRA
// ════════════════════════════════════════════════════════════════════════════
//
// El bloque de cabecera de p3.go dejó el problema medido y sin resolver, con sus
// dos lados malos escritos. Éste es el mecanismo que lo resuelve, y el hueco que
// queda después. Lo primero, el mecanismo; el número vive en `pipeline`.
//
// # POR QUÉ NO VALE ACOTAR EN EL WORKER
//
// Lo natural sería que el worker envolviera cada `Etapa.Run` en un
// `context.WithTimeout` y no tocar este paquete. NO SIRVE, y el motivo es P3: su
// `Run` hace N llamadas dentro. Un plazo puesto por fuera se REPARTE entre ellas
// —la primera se lleva casi todo y la última casi nada—, que es exactamente el
// segundo lado malo del bloque de p3.go («la primera llamada se lleva `restante −
// 7 s` y el breaker se queda ciego»), solo que a escala de etapa en vez de a
// escala de job. Un plazo POR LLAMADA solo se puede aplicar donde están las
// llamadas, y están aquí.
//
// # POR QUÉ ES UNA OPCIÓN VARIÁDICA Y NO UN PARÁMETRO MÁS
//
// Porque `NewP2`/`NewP3`/`NewP4` ya tienen llamantes (T2.2–T2.4 y sus tests) y un
// parámetro nuevo los rompe a todos para expresar «no me importa». Con la
// variádica, no pasar nada significa hoy lo mismo que significaba ayer: sin plazo
// propio, se hereda el del llamante — que es el comportamiento que esos tests
// afirman y que esta tarea NO debe cambiar.
//
// 🔴 EL DEFAULT ES «SIN PLAZO», Y ESO NO ES SEGURO — ES COMPATIBLE. Una etapa
// construida sin la opción manda al adaptador un ctx sin deadline y `local.plazo`
// (local.go:443) cae a sus `DefaultTimeout` = 30 s ⇒ umbral de lento en 24 s ⇒ una
// P3 caliente de 27 s ya cuenta como lenta. Quien construya etapas PARA PRODUCCIÓN
// tiene que pasar la opción; quien no la pase se queda con la avería medida. La
// alternativa —un default distinto de cero aquí dentro— escondería la decisión en
// el constructor, que es justo lo que `ZonaPorDefecto` evita en P4 obligando al
// llamante a escribirla.
// ════════════════════════════════════════════════════════════════════════════

// Opción configura una etapa al construirla. Es la forma de darle a P2/P3/P4 lo
// que no cabía en su firma sin romper a sus llamantes.
type Opción func(*plazos)

// plazos son los ajustes que comparten las tres etapas. Es UNA struct y no un
// campo suelto por etapa para que añadir el siguiente ajuste no obligue a tocar
// tres constructores.
type plazos struct {
	// porLlamada es cuánto puede durar UNA llamada al modelo. <= 0 significa «sin
	// plazo propio»: se hereda el ctx del llamante tal cual.
	porLlamada time.Duration
}

// ConPlazoPorLlamada fija cuánto puede durar CADA llamada al modelo de esta etapa.
// En P3 se aplica a cada ítem del fan-out, no al fan-out entero: ése es el punto.
//
// Un valor <= 0 se ignora y deja el comportamiento heredado (sin plazo propio), que
// es lo mismo que no pasar la opción. No se rechaza con error porque el llamante
// natural es una configuración con default, y un cero ahí significa «no configurado»,
// no «cero segundos».
func ConPlazoPorLlamada(d time.Duration) Opción {
	return func(p *plazos) {
		if d > 0 {
			p.porLlamada = d
		}
	}
}

// nuevosPlazos aplica las opciones. Vive aquí y no repetido en los tres
// constructores.
func nuevosPlazos(opts []Opción) plazos {
	var p plazos
	for _, opt := range opts {
		if opt != nil {
			opt(&p)
		}
	}
	return p
}

// acotar envuelve el ctx con el plazo por llamada. Devuelve SIEMPRE una función de
// cancelación llamable —nunca nil— para que el llamante pueda hacer `defer cancel()`
// sin preguntar si hay plazo: un `if` alrededor del defer es la forma clásica de
// filtrar el context cuando alguien añade un `return` en medio.
//
// 🔴 NO se usa `context.WithTimeoutCause`: el error que sale de aquí lo clasifica el
// worker por FAMILIA (`llm.ErrLLMQuality` o no), y un `DeadlineExceeded` es
// infraestructura venga con la causa que venga. Una causa extra no cambiaría ninguna
// decisión y sí daría la impresión de que sí.
func (p plazos) acotar(ctx context.Context) (context.Context, context.CancelFunc) {
	if p.porLlamada <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, p.porLlamada)
}
