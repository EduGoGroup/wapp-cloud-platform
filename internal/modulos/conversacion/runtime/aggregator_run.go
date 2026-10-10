// Porta internal/flujos/runtime/aggregator.go @ e0159171

package runtime

import (
	"context"
	"time"
)

// Trozo de aggregator.go (E-13): Run, la única goroutine del agregador (el tick, el
// despertador y la parada).

// Run arranca el barrido periódico y BLOQUEA hasta que ctx se cancele; entonces
// vuelve. Sin broker (ADR-0003): un ticker de Go, ni cron ni cola externa. Sobre un
// receptor nil o un agregador sin jobs vuelve en el acto. Lo arranca el arranque del
// proceso en su propia goroutine (trampa T-4: olvidarlo deja las ventanas abiertas
// para siempre, en silencio).
//
// Hace, en orden: UN RecoverAtBoot al entrar y, después, un Sweep por cada una de dos
// señales, hasta la cancelación:
//
//   - EL TICK, cada intervalo de barrido (5 s, o WithSweepInterval): es quien vigila
//     los dos plazos de la ventana, que no tienen evento que los anuncie, y quien cubre
//     un aviso perdido. En producción NADIE llama a Sweep a mano: lo llama este tick.
//   - EL DESPERTADOR de OnClassified (AG-4): un intent seguro cierra su ventana AHORA,
//     sin esperar al tick.
//
// AG-6: NO hay un timer por ventana. Un timer vivo en memoria es lo que un despliegue
// se lleva por delante; el plazo se recalcula en cada barrido desde las fechas de la
// fila. Si el proceso muere con una ventana abierta no se pierde nada: el proceso
// nuevo arranca, barre y cierra lo que venció.
//
// # La parada no es un error (D-F9-10)
//
// Contexto cancelado → Run vuelve SIN loguear a ERROR. Con ctx ya cancelado, un fallo
// del almacén (listar o cerrar) o del compositor NO se registra en ERROR, ni en el
// RecoverAtBoot inicial ni en un barrido que la cancelación pilló a medias: no es una
// avería, es el proceso apagándose. (El viejo sí las registraba: «agregador: no se
// pudieron listar las ventanas vivas».) Con el contexto VIVO, esos mismos fallos SÍ
// van a ERROR, como dice Sweep.
func (s *IntakeAggregator) Run(ctx context.Context) {
	if s == nil || s.jobs == nil {
		return
	}
	s.RecoverAtBoot(ctx)
	t := time.NewTicker(s.sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
			// EL ADELANTO POR EVENTO (T1.8-1 (h)): un intent seguro acaba de llegar y su
			// ventana se cierra AHORA, no dentro de hasta 5 s. `Sweep` es el mismo del
			// tick y hace una pasada COMPLETA —se lleva TODAS las pistas y vuelve a
			// mirar la tabla—, que es lo que hace que un aviso descartado por buffer
			// lleno no pierda trabajo.
			s.Sweep(ctx)
		case <-t.C:
			// EL BARRENDERO, y NO se retira con el despertador puesto: los dos plazos de
			// la ventana híbrida (silencio 45 s, techo 120 s) NO TIENEN EVENTO que los
			// anuncie —nadie avisa de que el cliente ha dejado de escribir—, así que
			// alguien tiene que ir a mirar el reloj. Es también quien cierra las ventanas
			// que quedaron vivas de un proceso anterior y quien cubre un aviso perdido.
			s.Sweep(ctx)
		}
	}
}
