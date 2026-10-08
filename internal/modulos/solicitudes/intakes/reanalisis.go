// Porta internal/intakes/reanalisis.go @ 64c181a

// reanalisis.go — lo que el RE-ANÁLISIS necesita del dominio de solicitudes
// (D-044.15).
//
// Son DOS piezas pequeñas y ninguna de las dos es una máquina de estados nueva:
//
//  1. `ReanalysisTarget` — la foto que responde «¿de qué evento cuelga esta
//     solicitud, y por qué revisión iba?». La puerta de re-análisis la necesita para
//     construir la clave de ventana del job y para anotar de qué revisión viene. La
//     lectura que la rellena (`ReanalysisTargetOf`) es del store y nace con él.
//  2. `Service.PushRevisionByID` — el empuje al puente CRM pedido por id, que es lo
//     único que la etapa `draft` del pipeline puede pedir: allí no hay `Detail` ni
//     forma de construirlo.
//
// 🔴 INV-10 — EL RE-ANÁLISIS NO TRANSICIONA NADA, y por eso aquí no hay un
// `Reanalyze` que haga de gemelo de `Approve`. El pedido se queda donde estaba
// —`pending_approval`, `needs_info`, lo que sea— y lo único que cambia es que le
// cuelga una revisión más cuando el pipeline termine, minutos después y por otro
// proceso. Escribir aquí una transición sería inventar un estado «re-analizando» que
// el ciclo de vida (D-041.10) no tiene y que ninguna pantalla sabría pintar.

package intakes

import (
	"context"
	"fmt"
)

// ReanalysisTarget es la foto de una solicitud vista por la puerta de re-análisis:
// lo justo para abrir el job y anotar el rastro, y ni un campo más.
//
// No es `Detail` recortado y no debe convertirse en uno: `Detail` arrastra líneas,
// revisiones enteras y el descifrado del literal con su poda perezosa. Pedir todo
// eso para leer cuatro columnas convertiría una comprobación previa —que puede
// acabar en un rechazo sin escribir nada— en la lectura más cara del endpoint.
type ReanalysisTarget struct {
	// SessionID y ContactID son dos de las cuatro columnas de la clave de ventana
	// del job. ContactID es el OPACO, nunca un número.
	SessionID string
	ContactID string
	// EventID es el evento conversacional del que cuelga la solicitud (D-043.21).
	// VACÍO en una solicitud LEGADA, y ese vacío no es un error de lectura: es un
	// pedido que nació antes de que existieran los eventos, así que no tiene hilo
	// del que re-analizar. Quien llama lo traduce a «fuente no disponible».
	EventID string
	// Status es la clave CANÓNICA del ciclo de vida, ya normalizada (el `closed`
	// legado sale como `confirmed`).
	//
	// ⚠️ SE PUBLICA Y HOY NO SE FILTRA POR ÉL. El re-análisis NO exige un estado de
	// partida —re-interpretar un pedido ya confirmado es legítimo: es cómo el dueño
	// descubre que la máquina se equivocó—. Está aquí porque INV-10 exige poder
	// afirmar que la solicitud NO cambia de estado, y para afirmarlo hay que
	// haberlo leído antes.
	Status string
	// LastRevisionNo es el correlativo VIGENTE de la solicitud: la revisión a la
	// que el re-análisis va a suceder. 0 = no tiene ninguna.
	LastRevisionNo int
}

// PushRevisionByID encola para el puente CRM la revisión `revisionNo` de una
// solicitud, leyéndola por id: lee el detalle con Service.Get y se lo pasa a
// PushRevisionToCRM con ese número.
//
// # POR QUÉ EXISTE ESTE GEMELO DE PushRevisionToCRM
//
// Porque la TERCERA puerta que empuja revisiones no es un handler: es la etapa
// `draft` del pipeline, que corre en un worker, minutos después de la petición y sin
// un `Detail` en la mano —y construirlo obligaría a que una etapa del pipeline
// conociera la bandeja entera del dueño—. Lo único que puede aportar es el par
// (solicitud, revisión); la lectura la hace este Service.
//
// 🔴 EL `revisionNo` SIGUE SIENDO OBLIGATORIO Y EXPLÍCITO (R-12), por lo mismo que en
// PushRevisionToCRM: se empuja el número RECIBIDO, aunque no sea el de la última
// revisión del detalle leído. Deducirlo aquí sería adivinar, y adivinar mal
// significa que el CRM descarta el empuje en silencio.
//
// NIL-SAFE (T-7): sobre un *Service nil, o sin CRMPusher cableado, devuelve nil sin
// leer nada. El pipeline recibe este método por una clausura que se resuelve al
// llamar, cuando el Service puede no existir todavía o no llevar puente.
//
// Un fallo de LECTURA sí se devuelve, envuelto con el id
// ("intakes: leer la solicitud <id> para empujarla al CRM: …", ErrNotFound incluido
// si no es del tenant), y no se empuja nada: quien llama tiene que poder decir en su
// log que la revisión existía y no se pudo empujar, en vez de dejar el hueco mudo.
//
// INV-10: no transiciona ni escribe nada en la solicitud. ⚠️ Pero la lectura es
// Service.Get, así que —como en el paquete viejo— cuenta como un TOQUE de los
// recordatorios perezosos cuando hay puente cableado.
func (s *Service) PushRevisionByID(ctx context.Context, tenantID, intakeID string, revisionNo int) error {
	if s == nil || s.crm == nil {
		return nil
	}
	detail, err := s.Get(ctx, tenantID, intakeID)
	if err != nil {
		return fmt.Errorf("intakes: leer la solicitud %s para empujarla al CRM: %w", intakeID, err)
	}
	s.PushRevisionToCRM(ctx, tenantID, detail, revisionNo)
	return nil
}
