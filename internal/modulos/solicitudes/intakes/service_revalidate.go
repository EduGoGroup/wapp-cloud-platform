// Porta internal/intakes/revalidate.go @ 64c181a (la escritura del Service)

// service_revalidate.go — LA ESCRITURA de una revalidación de precios. El cálculo
// (`Revalidate`, `Revalidation`, el payload de la revisión) es puro y vive en
// `revalidate.go`; aquí está el método del `Service` que lo aplica.

package intakes

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ApplyRevalidation ESCRIBE el resultado de una revalidación: deja las líneas como
// dice el diff, cuadra el total de la cabecera y escribe UNA revisión `revalidated`
// con el texto exacto que se le mandó al cliente — todo en la misma unidad de
// trabajo, que es del store (Store.ApplyRevalidation). Devuelve el detalle que
// devuelva el store.
//
// SIN CAMBIOS NO ESCRIBE NADA (REQ-35b): si `rv.Changed()` es false devuelve la
// solicitud tal como está, con una lectura (Store.Get: ErrNotFound si no es del
// tenant) y cero escrituras, sin mirar el texto ni el estado. Así el llamante puede
// invocarla siempre, sin replicar la condición.
//
// Con cambios, `renderedText` es obligatorio: vacío devuelve
// ErrEmptyRevalidationText sin tocar el store — una revisión que registre el cambio
// sin lo que se le dijo al cliente no sirve de rastro. Solo se rechaza la cadena
// VACÍA; el texto no se recorta ni se normaliza.
//
// Solo revalida sobre una solicitud `open`: al store se le pasan como estados
// esperados las variantes guardadas de `open` (StoredVariants) y quien la encuentre
// en otro estado recibe ErrConflict. No es una restricción caprichosa: el rescate
// solo ofrece solicitudes `open` (INV-17), y re-preciar una `confirmed` cambiaría lo
// que el cliente ya aceptó sin que nadie se lo dijera. ErrConflict es además la
// respuesta útil: quien la recibe relee y decide, en vez de reintentar.
//
// El estado NO se toca: aunque se retiren TODAS las líneas, la solicitud se queda
// `open` con cero líneas hasta que un humano la mueva (INV-14 / D-041.16 — nada
// muere por tiempo ni por efecto colateral). Tampoco avisa al cliente, ni empuja al
// CRM, ni publica métrica: el mensaje lo manda quien llama, que es quien trae su
// texto.
//
// ⚠️ Esto escribe UNA de las dos caras de D-041.25. La otra —las líneas del estado
// del carrito en el motor de flujos— tiene que ir en la misma transacción del
// rescate y no es de este paquete.
func (s *Service) ApplyRevalidation(ctx context.Context, tenantID, intakeID string, rv Revalidation, renderedText string) (Detail, error) {
	panic(pendiente.Implementar("intakes.Service.ApplyRevalidation"))
}
