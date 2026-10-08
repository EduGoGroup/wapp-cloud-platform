// Porta internal/intakes/reanalisis.go @ 64c181a

// postgres_reanalysis.go es la lectura de la FOTO que el re-análisis necesita de una
// solicitud. En el paquete viejo el método vivía en reanalisis.go; aquí nace con el
// adaptador (D-F6-6 ampliada) y reanalisis.go se queda con el tipo y el servicio.

package intakes

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ReanalysisTargetOf devuelve la foto de la solicitud que se va a re-analizar:
// sesión, contacto, evento padre, estado y el número de su última revisión. Es UNA
// lectura suelta que trae las cinco cosas a la vez.
//
//   - Status sale normalizado;
//   - EventID sale "" para una solicitud legada sin evento declarado (event_id NULL);
//   - LastRevisionNo sale 0 para una solicitud SIN revisiones, que sigue devolviendo
//     su fila: «este pedido no tiene revisiones» no es un 404.
//
// Devuelve ErrNotFound, sin envolver, si la solicitud no existe EN ESE TENANT. «No
// existe» y «es de otro tenant» son la misma respuesta (INV-8): distinguirlas
// convertiría el endpoint en un oráculo de qué ids existen. También ErrNotFound, sin
// tocar la base, si intakeID no es un UUID o si el receptor es nil (un *Postgres nil
// no tiene base que consultar y no debe hacer panic).
//
// Fallo de la base ⇒ (ReanalysisTarget{}, err) con
// "intakes: leer la solicitud a re-analizar: ".
func (p *Postgres) ReanalysisTargetOf(ctx context.Context, tenantID, intakeID string) (ReanalysisTarget, error) {
	panic(pendiente.Implementar("intakes.Postgres.ReanalysisTargetOf"))
}
