package intakes

import (
	"context"
	"errors"
	"strings"
)

// requestinfo.go — LA ACCIÓN «PEDIR MÁS INFORMACIÓN» DEL DUEÑO (Plan 044 · Ola 4 ·
// T4.4, D-044.49 §2).
//
// El pipeline deja en la revisión unas `suggested_questions` —lo que el LLM no supo
// resolver del texto del cliente—, el dueño ELIGE una, la EDITA en su pantalla, y
// este POST la manda. La solicitud queda en `needs_info` esperando la respuesta, que
// re-entrará por el flujo normal y abrirá un job nuevo del mismo evento.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 UN SOLO MENSAJE, Y ES EL DEL DUEÑO (D-044.49 §2)
// ════════════════════════════════════════════════════════════════════════════
//
// La transición a `needs_info` YA tenía aviso automático desde el 041: «Nos falta un
// dato para avanzar con tu pedido. Te escribimos enseguida por aquí»
// (statusTemplates, notifier.go). Ese texto es LITERALMENTE el anuncio del mensaje
// siguiente: mandarlo pegado a la pregunta le cuenta al cliente que le vamos a
// escribir y acto seguido le escribimos. Así que esta puerta pasa NoticeByCaller y la
// plataforma se calla EN ESTA transición — el `<select>` de estado del 041 sigue
// mandándolo igual, que es lo único que el cliente recibe cuando el dueño mueve el
// estado a mano.
//
// ════════════════════════════════════════════════════════════════════════════
// EL ORDEN, Y POR QUÉ ES EL CONTRARIO DEL «MANDA Y LUEGO ESCRIBE»
// ════════════════════════════════════════════════════════════════════════════
//
//  1. la pregunta, que es obligatoria (ErrEmptyQuestion): antes de tocar nada;
//  2. la TRANSICIÓN a `needs_info`, con compare-and-swap;
//  3. el ENVÍO de la pregunta al cliente.
//
// El envío va DESPUÉS por la misma asimetría que gobierna a `approve` (approve.go):
// enviar primero y fallar la transición le deja al cliente una pregunta sobre un
// pedido que sigue figurando «por aprobar», y el dueño —que ve un 500— reintenta y le
// manda la MISMA pregunta dos veces. Escribir primero y fallar el envío deja una
// solicitud en `needs_info` esperando una respuesta que nadie pidió: el dueño lo ve
// en su bandeja, y el fallo queda en el log con su command_id.
//
// ════════════════════════════════════════════════════════════════════════════
// LO QUE ESTA PUERTA NO HACE
// ════════════════════════════════════════════════════════════════════════════
//
// NO ESCRIBE REVISIÓN. Una revisión retrata el PRESUPUESTO —qué líneas y por
// cuánto—, y preguntar no cambia ni una línea ni el total. Registrar la pregunta
// exigiría una clase nueva en `intake_revisions.kind`, que es un conjunto CERRADO por
// un CHECK de la migración 0045: una clase nueva es una migración, y ni el enunciado
// de T4.4 ni su criterio la piden. La pregunta queda en el log del envío y, sobre
// todo, en el hilo de WhatsApp del cliente, que es donde el dueño la lee.
//
// NO EMPUJA AL CRM, y se deduce de lo anterior: el puente recibe REVISIONES con su
// `revision_no` (T4.10 / D-044.19), no estados sueltos. Sin revisión nueva no hay
// nada que empujar — y un empuje repitiendo el `revision_no` anterior es justo lo que
// el manual del integrador manda descartar como duplicado.

// ErrEmptyQuestion es la petición de información SIN pregunta. Es el criterio
// explícito de T4.4 —«jamás sale sola»— y no se sustituye por un texto genérico de la
// plataforma: el genérico que ya existía es precisamente el que esta puerta apaga, y
// un «necesitamos un dato» sin decir cuál deja al cliente sin saber qué contestar y
// al dueño esperando una respuesta que no puede llegar.
var ErrEmptyQuestion = errors.New("la petición de información no trae la pregunta que se le manda al cliente")

// RequestInfo le manda al cliente la pregunta que escribió el dueño y deja la
// solicitud en `needs_info` esperando su respuesta.
//
// El estado de origen NO se valida aquí con una constante propia —al revés que
// Approve, que declara ApprovableStatus— porque no hay nada que estrechar: la máquina
// de estados ya dice que a `needs_info` solo se llega desde `pending_approval`
// (status.go), así que SetStatus rechaza cualquier otro origen con un *TransitionError
// que además trae los destinos legales. Una segunda copia de esa regla en este
// fichero solo podría desincronizarse de la primera.
//
// Devuelve el detalle recompuesto para que la consola repinte sin un segundo GET, con
// las líneas y las revisiones que ya estaban: esta puerta no toca ninguna de las dos.
//
// 🔴 INV-1 — NINGÚN CAMINO AUTOMÁTICO LLEGA AQUÍ. Preguntarle algo al cliente es
// responderle al cliente, y eso solo lo hace el dueño con su POST. Lo que lo sostiene
// no es este comentario sino el barrido del AST de inv1_pedirinfo_ast_test.go.
//
// Errores: ErrNoQuoteSender (servicio sin canal), ErrEmptyQuestion (sin pregunta),
// ErrNotFound (no es del tenant), *TransitionError (no está en `pending_approval`),
// ErrConflict (alguien la movió entre la lectura y la escritura).
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

	// El mensaje al cliente. No devuelve error a propósito (notifier.go, regla 1):
	// una transición ya escrita no se deshace porque el teléfono esté apagado.
	s.quotes.SendQuestion(ctx, tenantID, updated, question)

	return Detail{
		Intake:           updated,
		Items:            current.Items,
		Revisions:        current.Revisions,
		BuyerDataPresent: current.BuyerDataPresent,
	}, nil
}
