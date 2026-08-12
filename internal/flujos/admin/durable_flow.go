package admin

import (
	"context"
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// DurableFlowChecker es el puerto ESTRECHO que T2.6 (la marca derivada) y T2.7 (la
// validación 422, D-054.8) necesitan: «¿la definición VIGENTE de flowID para
// tenantID tiene algún nodo cuyo módulo produce contenido durable?». Es la MISMA
// pregunta para las dos tareas — un solo puerto, no el DefinitionStore entero
// (H2 del contrato de entrada de este frente).
//
// Es un parámetro POSICIONAL de los tres constructores del CRUD
// (Create/List/DeleteTriggerHandler), nunca una Option: omitirlo rompe la
// compilación en los dos sitios que los cablean (bootstrap.go, publicapi.go) hasta
// que se enchufe — el defecto exacto que este plan existe para no repetir
// (WithOpeningBuilder sin cablear, T1).
//
// nil es un valor VÁLIDO a propósito: un llamante que conscientemente pasa nil
// (normalmente un test que no ejercita contenido durable) obtiene «ningún flujo es
// durable» — fail-open, nunca un panic. Eso es distinto de OMITIR el parámetro,
// que no compila. Ver durableContent, el único punto que lee este valor.
type DurableFlowChecker interface {
	FlowHasDurableContent(ctx context.Context, tenantID, flowID string) (bool, error)
}

// FlowDefinitionReader es la porción mínima de store.DefinitionReader que
// EngineDurableFlowChecker necesita: la versión VIGENTE de una definición. Lo
// satisfacen *store.PostgresRepository y *store.MemoryRepository sin cambios —
// puerto estrecho, mismo patrón que events.RuleLister (events/kinds.go).
type FlowDefinitionReader interface {
	LatestDefinition(ctx context.Context, tenantID, flowID string) (model.Flow, error)
}

// DurableFlowEngine es la porción mínima de *engine.Engine que
// EngineDurableFlowChecker necesita (Plan 054 · F1). Lo satisface *engine.Engine
// sin cambios; se declara como interfaz aquí para que este paquete no importe
// internal/flujos/engine entero por un solo método.
type DurableFlowEngine interface {
	FlowProducesDurableContent(f model.Flow) bool
}

// EngineDurableFlowChecker adapta un FlowDefinitionReader + un DurableFlowEngine a
// DurableFlowChecker (D-054.6/D-054.8): resuelve la definición vigente del flujo y
// le pregunta al motor si ALGÚN nodo produce contenido durable — el mismo
// predicado exacto que startLocked ya hace cumplir en tiempo de arranque
// (D-054.5), consultado aquí en tiempo de CONFIGURACIÓN.
//
// Fail-open si el flujo no existe (store.ErrDefinitionNotFound): este plan NO
// introduce una validación de existencia de flow_id — el README dice
// explícitamente que el CRUD «no resuelve la configuración por el usuario», y
// añadir esa validación de rebote sería un cambio de comportamiento que nadie
// pidió. Un flow_id inexistente sigue aceptándose con 201, EXACTAMENTE como hoy
// (H2 del contrato de entrada).
//
// Cualquier OTRO error (fallo de BD, etc.) SÍ se propaga: ahí no se sabe la
// respuesta, y «no pude averiguarlo» no debe comportarse igual que «no es
// durable» — el mismo principio fail-closed-ante-la-duda que
// entitlements.RequireFeature aplica a la resolución de features.
type EngineDurableFlowChecker struct {
	defs   FlowDefinitionReader
	engine DurableFlowEngine
}

// NewEngineDurableFlowChecker construye el adapter sobre el store de definiciones
// y el motor ya cableados en bootstrap.go/publicapi.go (los MISMOS objetos que ya
// alimentan DefinitionHandler/StartHandler y el runtime: cero dependencias
// nuevas, solo una lectura nueva sobre ellas).
func NewEngineDurableFlowChecker(defs FlowDefinitionReader, eng DurableFlowEngine) *EngineDurableFlowChecker {
	return &EngineDurableFlowChecker{defs: defs, engine: eng}
}

// FlowHasDurableContent implementa DurableFlowChecker.
func (c *EngineDurableFlowChecker) FlowHasDurableContent(ctx context.Context, tenantID, flowID string) (bool, error) {
	def, err := c.defs.LatestDefinition(ctx, tenantID, flowID)
	if err != nil {
		if errors.Is(err, store.ErrDefinitionNotFound) {
			return false, nil
		}
		return false, err
	}
	return c.engine.FlowProducesDurableContent(def), nil
}

// durableContent es el único punto que invoca checker: nil-safe (fail-open, ver el
// docstring de DurableFlowChecker) para que un llamante que no ejercita contenido
// durable (producción con el adapter siempre cableado; tests que pasan nil a
// propósito) no tenga que ocuparse de un caso especial.
func durableContent(ctx context.Context, checker DurableFlowChecker, tenantID, flowID string) (bool, error) {
	if checker == nil {
		return false, nil
	}
	return checker.FlowHasDurableContent(ctx, tenantID, flowID)
}
