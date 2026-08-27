// Package telemetria deja las mediciones de la BANDEJA DEL DUEÑO en el outbox
// `flow_events` (Plan 044 · T5.2, design §10).
//
// ════════════════════════════════════════════════════════════════════════════
// POR QUÉ ES UN PAQUETE Y NO UN MÉTODO MÁS DE `intakes`
// ════════════════════════════════════════════════════════════════════════════
//
// Porque `intakes` NO PUEDE importar `internal/flujos/store`: aquel paquete tiene un
// test IN-PACKAGE (reserved_prefix_test.go) que importa `intakes` para atar su
// prefijo reservado al de allí, y el ciclo —aunque una pata sea de test— no compila.
// Es el mismo nudo que ya resolvió `integrations/crmpush` con el suyo: un adaptador
// pequeño en su propio paquete, que es quien conoce a los dos lados.
//
// Y de paso deja la frontera donde le corresponde. `flow_id`, `flow_version` y `kind`
// son vocabulario de la TABLA (migración 0009), no de la bandeja: quien decide con
// qué flujo se firma una fila de telemetría es quien escribe en esa tabla, no quien
// mide cuántas líneas corrigió un dueño.
package telemetria

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// FlujoBandeja y VersionFlujoBandeja son el `flow_id` / `flow_version` con los que la
// bandeja firma sus filas de `flow_events`.
//
// 🔴 SON SINTÉTICOS Y HAY QUE DECIRLO, por lo mismo que los del pipeline
// (`stages.FlujoCaptacion`): las dos columnas son NOT NULL desde la 0009 porque esa
// tabla nació como el outbox del MOTOR DE FLUJOS, donde todo efecto sale de un flujo
// con su versión. Estas filas no: las emite una API del dueño que no está dentro de
// ningún turno de conversación y cuya solicitud no tiene por qué venir de un flujo.
// Las opciones eran inventar un join que no existe o firmar con un identificador
// propio; se firma.
//
// El prefijo `_` es el espacio RESERVADO de la plataforma (`_shipping`,
// `_intake_llm`): ningún flujo de ningún tenant puede colisionar con él, así que una
// fila con este `flow_id` es SIEMPRE de la bandeja y de nada más.
//
// 🔴 NO ES `_intake_llm`, Y ES A PROPÓSITO. Aquél es el pipeline que INTERPRETA; éste
// es la bandeja donde el dueño DECIDE. Son dos productores con dos contratos de
// payload cuyas versiones se mueven por separado, y ninguna consulta del runbook
// cruza los dos: se filtra por `name`, que es único en toda la tabla.
//
// La versión es 1 y no 0 porque 0 significaría «versión desconocida»: este emisor sí
// tiene una, la de su contrato de payload, y sube el día que el payload cambie.
const (
	FlujoBandeja        = "_intake_inbox"
	VersionFlujoBandeja = 1
)

// kindEvento es el `flow_events.kind` de una fila de TELEMETRÍA ("event", frente a
// "persist", que además proyecta una tabla tipada). Replica el literal —igual que los
// módulos y la etapa `draft` con el suyo— porque el vocabulario es de la tabla
// (migración 0009), no de ningún paquete de Go.
const kindEvento = "event"

// Outbox es lo ÚNICO que este adaptador necesita del motor de flujos: añadir una
// fila. Lo satisfacen `*store.PostgresRepository` y `*store.MemoryRepository`, que
// son los mismos objetos que ya alimentan a la etapa `draft` del pipeline — y tiene
// que ser el mismo: `flow_events` es UNA tabla y los cinco eventos de design §10 se
// leen juntos desde las consultas del runbook.
type Outbox interface {
	InsertFlowEvent(ctx context.Context, ev store.FlowEvent) error
}

// Publicador satisface `intakes.PublicadorDeMetricas` traduciendo una medición de la
// bandeja a una fila de `flow_events`.
type Publicador struct {
	outbox Outbox
}

// New construye el publicador sobre el outbox dado.
//
// No valida el nil ni devuelve error, y es coherente con cómo se usa: el Service lo
// recibe por una opción que se puede no cablear (`intakes.WithMetrics`), así que «no
// hay telemetría» ya se expresa NO pasando la opción. Un publicador construido con
// nil sería un tercer estado que no significa nada distinto y que solo serviría para
// petar en la primera medición.
func New(outbox Outbox) *Publicador {
	return &Publicador{outbox: outbox}
}

// PublicarMetrica deja la fila. El error se DEVUELVE y no se traga: quien decide qué
// hacer con él es el llamante, y el llamante (intakes.Service.publicarMetrica) ya
// tiene la regla escrita —avisar y seguir— junto a las acciones que no puede tumbar.
//
// 🔴 EL `contactID` QUE LLEGA AQUÍ ES EL OPACO (ADR-0010/ADR-0017) y este adaptador
// no lo enriquece ni lo resuelve: `flow_events` es una tabla EN CLARO y ahí no entra
// un número ni un JID. Tampoco toca el `payload`: lo que le den es lo que escribe,
// porque su contrato es de design §10 y no suyo.
func (p *Publicador) PublicarMetrica(ctx context.Context, tenantID, contactID, name string, payload map[string]any) error {
	return p.outbox.InsertFlowEvent(ctx, store.FlowEvent{
		TenantID:    tenantID,
		ContactID:   contactID,
		FlowID:      FlujoBandeja,
		FlowVersion: VersionFlujoBandeja,
		Kind:        kindEvento,
		Name:        name,
		Payload:     payload,
	})
}
