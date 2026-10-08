// Porta internal/intakes/crm.go @ 64c181a

// crm.go es el vocabulario del REFLEJO del CRM sobre una solicitud (verbo
// intake.status del contrato wapp-crm-v1, D-042.6; ADR-0031: «cuando HAY CRM, el CRM
// manda»). Lleva solo lo PURO: los estados canónicos y el resultado del reflejo. La
// sentencia que lo aplica vive con el adaptador Postgres (D-F6-6).

package intakes

import "github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"

// Estados CANÓNICOS del contrato wapp-crm-v1 (verbo intake.status, D-042.6). Son un
// vocabulario PROPIO de la frontera con el CRM y NO estados del ciclo de vida de
// wApp: viven en su columna (intakes.crm_status) y jamás pisan intakes.status.
//
// Ojo con `rejected`: existe en los DOS vocabularios y significa cosas distintas.
// Aquí es «el CRM del cliente rechazó el pedido»; en status.go es una transición del
// ciclo de vida que decide el dueño desde su bandeja. Que compartan literal es una
// coincidencia del castellano, no un puente entre las dos máquinas.
//
// Son CUATRO y la lista es cerrada, en el mismo orden que el enum del schema
// publicado (intake.status.schema.json) y que el CHECK de la migración 0048. Los
// tres sitios dicen lo mismo a propósito: el schema rechaza en la frontera, el
// dominio rechaza con IsCRMStatus y el CHECK rechaza en la base. Ninguno sustituye a
// otro. Cada uno de los cuatro tiene su texto para el cliente; un quinto sin texto
// dejaría al cliente sin enterarse de su cambio.
const (
	CRMStatusPaid      = "paid"
	CRMStatusPreparing = "preparing"
	CRMStatusDelivered = "delivered"
	CRMStatusRejected  = "rejected"
)

// IsCRMStatus dice si un estado pertenece al vocabulario canónico del CRM: true
// para los cuatro CRMStatus… y false para todo lo demás.
//
// La comparación es EXACTA, byte a byte: no pliega mayúsculas ni recorta espacios
// («Paid», « paid» y «paid\n» no son canónicos), el vacío no lo es, y tampoco los
// estados del ciclo de vida de wApp que no comparten literal («confirmed»,
// «closed»).
func IsCRMStatus(status string) bool {
	panic(pendiente.Implementar("intakes.IsCRMStatus"))
}

// CRMReflection es el resultado de reflejar un estado del CRM sobre una solicitud.
//
// Found y Changed son preguntas DISTINTAS y las dos importan: sin Found no se puede
// responder 404 a una solicitud ajena o inexistente —las dos salen igual, que es lo
// que impide usar el callback como oráculo de qué ids existen (INV-8)—, y sin
// Changed no se sabe si hay que avisar al cliente: un puente con reintentos manda el
// mismo estado muchas veces y el cliente no puede recibir el mismo mensaje una vez
// por reintento.
//
// El valor cero es «no encontrada»: Found=false, Changed=false y la solicitud vacía.
type CRMReflection struct {
	Found   bool
	Changed bool
	// Intake es la solicitud YA reflejada. Solo tiene contenido con Found: es lo que
	// necesita el notificador (contacto y sesión) sin volver a consultar.
	Intake Intake
}
