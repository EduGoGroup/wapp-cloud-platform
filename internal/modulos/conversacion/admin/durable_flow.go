// Porta internal/flujos/admin/durable_flow.go @ 724f3035

package admin

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// DurableFlowChecker es el puerto ESTRECHO que la marca derivada del listado (T2.6) y
// la validación 422 (T2.7, D-054.8) necesitan: «¿la definición VIGENTE de flowID para
// tenantID tiene algún nodo cuyo módulo produce contenido durable?». Es la MISMA
// pregunta para las dos tareas: un solo puerto, no el store de definiciones entero.
//
// Es un parámetro POSICIONAL de los tres constructores del CRUD
// (Create/List/DeleteTriggerHandler), nunca una opción: omitirlo no compila.
//
// nil es un valor VÁLIDO a propósito: el llamante que pasa nil obtiene «ningún flujo
// es durable» (fail-open), nunca un panic. Lo prueban los tests de triggers.go.
type DurableFlowChecker interface {
	FlowHasDurableContent(ctx context.Context, tenantID, flowID string) (bool, error)
}

// FlowDefinitionReader es la porción mínima del lector de definiciones que
// EngineDurableFlowChecker necesita: la versión VIGENTE de una definición. Lo
// satisfacen *store.PostgresRepository y *store.MemoryRepository sin adaptador.
type FlowDefinitionReader interface {
	LatestDefinition(ctx context.Context, tenantID, flowID string) (model.Flow, error)
}

// DurableFlowEngine es la porción mínima de *engine.Engine que
// EngineDurableFlowChecker necesita (Plan 054 · F1). Lo satisface *engine.Engine sin
// adaptador; es interfaz para que este paquete no importe el motor por un método.
type DurableFlowEngine interface {
	FlowProducesDurableContent(f model.Flow) bool
}

// EngineDurableFlowChecker adapta un FlowDefinitionReader y un DurableFlowEngine a
// DurableFlowChecker (D-054.6/D-054.8): resuelve la definición vigente del flujo y le
// pregunta al motor si ALGÚN nodo produce contenido durable, el mismo predicado que el
// runtime hace cumplir al arrancar (D-054.5), consultado aquí en tiempo de
// CONFIGURACIÓN. No guarda estado de llamada.
type EngineDurableFlowChecker struct{}

// NewEngineDurableFlowChecker construye el adaptador sobre el lector de definiciones y
// el motor ya cableados en el arranque. Devuelve siempre un puntero no nil; no valida
// sus argumentos y NO los consulta al construir: la primera lectura ocurre en
// FlowHasDurableContent.
func NewEngineDurableFlowChecker(defs FlowDefinitionReader, eng DurableFlowEngine) *EngineDurableFlowChecker {
	panic(pendiente.Implementar("admin.NewEngineDurableFlowChecker"))
}

// FlowHasDurableContent implementa DurableFlowChecker. Pide UNA vez la definición
// vigente con el ctx, el tenantID y el flowID recibidos, y:
//
//   - si la lectura va bien, devuelve lo que diga el motor para ESA definición —la
//     que devolvió el lector, sin tocarla— y error nil;
//   - si el flujo no existe (el error es store.ErrDefinitionNotFound por errors.Is,
//     también envuelto), devuelve (false, nil) y NO consulta al motor. Fail-open a
//     propósito: el CRUD no valida la existencia de flow_id, y un flow_id inexistente
//     se sigue aceptando como siempre;
//   - ante CUALQUIER otro error devuelve (false, ese mismo error) y no consulta al
//     motor: «no pude averiguarlo» no se comporta igual que «no es durable».
func (c *EngineDurableFlowChecker) FlowHasDurableContent(ctx context.Context, tenantID, flowID string) (bool, error) {
	panic(pendiente.Implementar("admin.EngineDurableFlowChecker.FlowHasDurableContent"))
}
