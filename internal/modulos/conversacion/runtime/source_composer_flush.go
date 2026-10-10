// Porta internal/flujos/runtime/source_composer.go @ e0159171

package runtime

import (
	"context"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// Trozo de source_composer.go (E-13): ComposeAtFlush, el compositor que además ESCRIBE
// el sobre sobre una ventana ya cerrada.

// ComposeAtFlush compone el `source_text` y lo GUARDA con PutSourceText sobre la última
// ventana `pending` de la tupla: es Compose + PutSourceText, en dos actos.
//
// 🔴 YA NO LA LLAMA EL AGREGADOR. Desde F8-06b (D-F7-9, D-F8-13) el cierre de una
// ventana llama a Compose ANTES de cerrar y cierra con el sobre en UNA sentencia
// (intake.JobStore.CloseWithSourceText): ahí no hay hueco entre «cerrada» y «con sobre».
// Este método se queda para su ÚNICO llamante, el re-análisis (captacion/reanalisis),
// que abre un job ya `pending` y le pone el sobre después: en ESE camino cierre y sobre
// siguen siendo dos sentencias, y se arregla en T8.40. Devolver error no reabre ni corta
// nada: el llamante lo LOGUEA y el job se queda en `pending` con el sobre vacío.
//
// Los pasos, en orden, todos con el ctx recibido:
//
//  1. No-op: sobre un receptor nil, o un compositor construido con log, thread, jobs o
//     cipher a nil, devuelve nil sin leer, escribir ni loguear.
//  2. a 5. Los de Compose, con sus mismos textos de error y su mismo Warn: clave
//     incompleta, lectura del hilo, composición (con CERO MENSAJES no se escribe NADA,
//     ni siquiera si hubo contexto: devuelve nil tras el aviso) y cifrado.
//  6. Guarda el sobre de TRES piezas —intake.SourceText{Enc, DEK, KEKID}, las que
//     devolvió el cipher— con PutSourceText(ctx, key, sobre), UNA vez. Descifrarlo con
//     el mismo keyring devuelve exactamente Composed.Text. Si falla, devuelve
//     "compositor: guardar el literal de la ventana del evento <event_id>: <error>"
//     (envuelve el error).
//  7. Si PutSourceText contesta false sin error (la fila ya tenía sobre, o la ventana
//     no está en `pending`), NO es un error —es idempotencia— y devuelve nil, dejando
//     en Debug "compositor: la ventana ya tenía literal; no se sobrescribe" con las
//     claves "tenant_id" y "event_id".
//  8. Si escribió, devuelve nil y deja en Debug "compositor: literal compuesto y
//     cifrado" con las claves de Compose. 🔴 SOLO si escribió: a diferencia de Compose,
//     aquí ese Debug significa «guardado», y por eso no se emite en el paso 7.
//
// 🔴 NINGUNA línea de log, en ningún nivel, y NINGÚN error llevan contenido del hilo
// (REQ-10c): solo identificadores y números.
func (c *SourceTextComposer) ComposeAtFlush(ctx context.Context, key intake.WindowKey) error {
	if c == nil || c.log == nil || c.thread == nil || c.jobs == nil || c.cipher == nil {
		return nil
	}
	env, composed, err := c.seal(ctx, key)
	if err != nil {
		return err
	}
	if composed.Empty() {
		return nil
	}

	written, err := c.jobs.PutSourceText(ctx, key, env)
	if err != nil {
		return fmt.Errorf("compositor: guardar el literal de la ventana del evento %s: %w", key.EventID, err)
	}
	if !written {
		// No había dónde escribir: la fila ya tenía sobre, o la ventana no está en
		// `pending`. No es un error —es idempotencia— pero se dice, porque si pasa
		// siempre significa que alguien está componiendo dos veces.
		c.log.Debug("compositor: la ventana ya tenía literal; no se sobrescribe",
			"tenant_id", key.TenantID, "event_id", key.EventID)
		return nil
	}
	c.logSealed(key, composed)
	return nil
}
