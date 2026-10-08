// Porta internal/intakes/requestinfo.go @ 64c181a

// requestinfo.go — LA ACCIÓN «PEDIR MÁS INFORMACIÓN» DEL DUEÑO (D-044.49 §2).
//
// El pipeline deja en la revisión unas preguntas sugeridas —lo que el LLM no supo
// resolver del texto del cliente—, el dueño ELIGE una, la EDITA en su pantalla, y
// esta puerta la manda. La solicitud queda en `needs_info` esperando la respuesta,
// que re-entrará por el flujo normal.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 UN SOLO MENSAJE, Y ES EL DEL DUEÑO (D-044.49 §2)
// ════════════════════════════════════════════════════════════════════════════
//
// La transición a `needs_info` YA tiene aviso automático: «Nos falta un dato para
// avanzar con tu pedido. Te escribimos enseguida por aquí». Ese texto es
// LITERALMENTE el anuncio del mensaje siguiente: mandarlo pegado a la pregunta le
// cuenta al cliente que le vamos a escribir y acto seguido le escribimos. Así que
// esta puerta pasa NoticeByCaller y la plataforma se calla EN ESTA transición — el
// selector de estado de la consola sigue mandándolo igual, que es lo único que el
// cliente recibe cuando el dueño mueve el estado a mano.
//
// ════════════════════════════════════════════════════════════════════════════
// EL ORDEN, Y POR QUÉ ES EL CONTRARIO DEL «MANDA Y LUEGO ESCRIBE»
// ════════════════════════════════════════════════════════════════════════════
//
//  1. la pregunta, que es obligatoria (ErrEmptyQuestion): antes de tocar nada;
//  2. la TRANSICIÓN a `needs_info`, con compare-and-swap;
//  3. el ENVÍO de la pregunta al cliente;
//  4. la métrica.
//
// El envío va DESPUÉS por la misma asimetría que gobierna a Approve: enviar primero
// y fallar la transición le deja al cliente una pregunta sobre un pedido que sigue
// figurando «por aprobar», y el dueño —que ve un error— reintenta y le manda la
// MISMA pregunta dos veces. Escribir primero y fallar el envío deja una solicitud en
// `needs_info` esperando una respuesta que nadie pidió: el dueño lo ve en su
// bandeja, y el fallo queda en el log.
//
// ════════════════════════════════════════════════════════════════════════════
// LO QUE ESTA PUERTA NO HACE
// ════════════════════════════════════════════════════════════════════════════
//
// NO ESCRIBE REVISIÓN. Una revisión retrata el PRESUPUESTO —qué líneas y por
// cuánto—, y preguntar no cambia ni una línea ni el total. Registrar la pregunta
// exigiría una clase nueva de revisión, que es un conjunto CERRADO por la base. La
// pregunta queda en el log del envío y, sobre todo, en el hilo de WhatsApp del
// cliente, que es donde el dueño la lee.
//
// NO EMPUJA AL CRM, y se deduce de lo anterior: el puente recibe REVISIONES con su
// `revision_no`, no estados sueltos. Sin revisión nueva no hay nada que empujar — y
// un empuje repitiendo el número anterior es justo lo que el puente descarta como
// duplicado.

package intakes

import (
	"context"
	"errors"
	"strings"
)

// ErrEmptyQuestion es la petición de información SIN pregunta. Es el criterio
// explícito de la puerta —«jamás sale sola»— y no se sustituye por un texto genérico
// de la plataforma: el genérico que ya existía es precisamente el que esta puerta
// apaga, y un «necesitamos un dato» sin decir cuál deja al cliente sin saber qué
// contestar y al dueño esperando una respuesta que no puede llegar.
var ErrEmptyQuestion = errors.New("la petición de información no trae la pregunta que se le manda al cliente")

// RequestInfo le manda al cliente la pregunta que escribió el dueño y deja la
// solicitud en `needs_info` esperando su respuesta.
//
// 🔴 INV-1 (R-01) — NINGÚN CAMINO AUTOMÁTICO LLEGA AQUÍ. Preguntarle algo al cliente
// es responderle al cliente, y eso solo lo hace la dueña con su POST: ni el motor,
// ni el carrito, ni el pipeline, ni la cola, ni el propio dominio. Lo que lo
// sostiene no es este comentario sino el barrido del AST (inv1_aprobar_test.go).
//
// Las validaciones, en ESTE orden; la primera que falla decide el error y no se
// escribe ni se manda nada:
//
//  1. ErrNoQuoteSender — el servicio no tiene QuoteSender (R-02). Sin esta guarda la
//     solicitud quedaría en `needs_info` esperando la respuesta a una pregunta que
//     nunca salió;
//  2. ErrEmptyQuestion — `question` vacía o solo espacios en blanco. El recorte es
//     solo para DECIDIR: lo que sale por el cable es el original byte a byte;
//  3. ErrNotFound — la solicitud no es del tenant (404 opaco, INV-8);
//  4. *TransitionError / ErrConflict — los de SetStatus, tal cual.
//
// El estado de origen NO se valida aquí con una constante propia —al revés que
// Approve con ApprovableStatus— porque no hay nada que estrechar: la máquina de
// estados ya dice que a `needs_info` solo se llega desde `pending_approval`, y el
// *TransitionError de SetStatus trae además los destinos legales. Una segunda copia
// de esa regla aquí solo podría desincronizarse de la primera.
//
// Después: la transición es SetStatus a `needs_info` con NoticeByCaller (el aviso
// genérico NO sale); el envío es QuoteSender.SendQuestion con la solicitud YA
// transicionada y la pregunta sin tocar —no pasa por QuoteText: a una pregunta no se
// le adjunta la plantilla de seña—; y la métrica es EventInfoRequested (ver
// WithMetrics), después del envío: lo que el evento afirma es que se le pidió
// información al cliente, y eso ocurre cuando la pregunta sale.
//
// Devuelve el detalle recompuesto para que la consola repinte sin una segunda
// lectura: la solicitud transicionada con las líneas, las revisiones y
// BuyerDataPresent que ya estaban — esta puerta no toca ninguna de las tres. No
// escribe revisión y no empuja al CRM.
func (s *Service) RequestInfo(ctx context.Context, tenantID, intakeID, question string) (Detail, error) {
	if s.quotes == nil {
		return Detail{}, ErrNoQuoteSender
	}
	// TrimSpace solo para DECIDIR si hay pregunta: lo que sale por el cable es el
	// original byte a byte, igual que la cotización de Approve. Recortarlo sería
	// reescribir lo que el dueño escribió.
	if strings.TrimSpace(question) == "" {
		return Detail{}, ErrEmptyQuestion
	}

	// El recurso se resuelve ANTES que el cuerpo (mismo criterio que SetStatus,
	// ReplaceItems y Approve): una solicitud ajena responde ErrNotFound y no revela
	// por el código de error que existe (INV-8). Se lee aquí y no solo dentro de
	// SetStatus porque el detalle de la respuesta necesita las líneas y el histórico.
	current, err := s.store.Get(ctx, tenantID, intakeID)
	if err != nil {
		return Detail{}, err
	}

	updated, err := s.SetStatus(ctx, tenantID, intakeID, StatusNeedsInfo, NoticeByCaller)
	if err != nil {
		return Detail{}, err
	}

	// El mensaje al cliente. No devuelve error a propósito: una transición ya escrita
	// no se deshace porque el teléfono esté apagado.
	s.quotes.SendQuestion(ctx, tenantID, updated, question)

	// La métrica. Va DESPUÉS del envío y no entre la transición y él: lo que el
	// evento afirma es que se le pidió información al cliente, y eso ocurre cuando la
	// pregunta sale. El payload no lleva la pregunta —ni un trozo—, solo cuántas
	// fueron (ver publishInfoRequestMetric).
	s.publishInfoRequestMetric(ctx, tenantID, updated)

	return Detail{
		Intake:           updated,
		Items:            current.Items,
		Revisions:        current.Revisions,
		BuyerDataPresent: current.BuyerDataPresent,
	}, nil
}
