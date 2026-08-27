// draft_evento_t40_test.go — LA DIRECCIÓN pipeline→carrito del hallazgo #24
// (Plan 044 · T4.0, D-044.46), con los datos de campo del 2026-08-26/27.
//
// LA ESCENA, tal como quedó en `logs/cloud.log` de UAT a las 22:48:42: el carrito
// había parido su solicitud `open` sobre el evento CINCUENTA Y CUATRO MILISEGUNDOS
// antes, con un id SORTEADO (`uuid.NewString`); la etapa `draft` llegó con su id
// DERIVADO del evento y fue derecha al upsert, sin preguntar. El id derivado la
// defiende de sí misma —un reintento suyo reescribe su propia fila— pero no del otro
// productor: contra la fila del carrito el INSERT chocaba con `intakes_event_id_uidx`
// (SQLSTATE 23505), y el job se reintentaba diez veces durante veintinueve minutos
// para morir igual.
//
// Es el ESPEJO exacto de projection_evento_t40_test.go (el mismo choque visto desde
// el otro productor) y, como aquél, se afirma sobre el número de filas del evento: el
// repositorio en memoria no impone el único parcial, así que el defecto se manifiesta
// aquí como su causa —DOS contenidos durables para un evento— en vez de como el error
// del driver.
package stages_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// solicitudDelCarrito es la fila que `cart.ensureOpenIntake` deja al primer
// `item_added`: id SORTEADO (por eso no coincide con el que `draft` derivaría) y
// estado `open`, que significa «el cliente sigue comprando».
const solicitudDelCarrito = "b7d41f28-6c05-4e93-a1d2-3f8e0b5c9a17"

// TestDraft_SiElEventoYaTieneContenido_SeSumaEnVezDeParirOtro es el criterio (b) de
// T4.0, y con él el (c).
//
// Las cuatro afirmaciones:
//
//   - UNA sola fila para el evento (E-8). Es la que falla en el código de hoy: sin la
//     consulta por evento la etapa hace su upsert y salen DOS.
//   - El borrador cuelga de LA QUE YA ESTABA: `out.IntakeID` es el id del carrito, no
//     el derivado del evento.
//   - Su `status` sigue siendo `open` — criterio (c) literal. Hoy el upsert lo pisaba
//     con `pending_approval`, que es llamar al dueño a mitad de compra.
//   - La revisión `interpreted` existe y cuelga de esa misma solicitud: reusar la fila
//     no puede costar la entrega de la etapa.
func TestDraft_SiElEventoYaTieneContenido_SeSumaEnVezDeParirOtro(t *testing.T) {
	ctx := context.Background()
	b := draftDe(t, messageTSDeAmbar.Add(elapsedDeAmbar))
	job := jobAmbarP4()

	// El CARRITO llegó primero, 54 ms antes.
	require.NoError(t, b.flujos.UpsertIntake(ctx, store.Intake{
		ID:        solicitudDelCarrito,
		TenantID:  job.Key.TenantID,
		ContactID: job.Key.ContactID,
		SessionID: job.Key.SessionID,
		Status:    intakes.StatusOpen,
		EventID:   job.Key.EventID,
	}), "sembrar la solicitud del carrito")

	out, err := b.draft.Run(ctx, job, entradaDeAmbar(matchDeAmbarSinDeco(t)))
	require.NoError(t, err)

	filas := b.flujos.Intakes()
	require.Len(t, filas, 1,
		"un evento tiene A LO SUMO un contenido durable (E-8, intakes_event_id_uidx): %+v", filas)
	require.Equal(t, solicitudDelCarrito, out.IntakeID,
		"el borrador tiene que colgar de la solicitud que YA existía, no de una segunda")
	require.Equal(t, intakes.StatusOpen, filas[0].Status,
		"el pipeline NO pisa el estado de una solicitud ajena: un carrito open sigue comprando (D-044.46)")

	revs := b.solicitudes.Revisions(solicitudDelCarrito)
	require.Len(t, revs, 1, "la revisión interpretada cuelga del intake reusado")
	require.Equal(t, intakes.RevisionKindInterpreted, revs[0].Kind)
	require.Equal(t, intakes.RevisionBySystem, revs[0].CreatedBy)
}

// TestDraft_SinContenidoPrevio_LaSolicitudSIGUE_NACIENDO_EnPendingApproval es la
// no-regresión del camino normal, que es la mitad que un arreglo mal escrito rompe sin
// hacer ruido: cuando el evento NO tiene contenido, la etapa sigue pariendo su fila y
// sigue naciendo en `pending_approval` (design §7.4, la razón está en el docstring de
// Run). Si la consulta por evento devolviera «encontrado» de más, este test se cae.
func TestDraft_SinContenidoPrevio_LaSolicitudSIGUE_NACIENDO_EnPendingApproval(t *testing.T) {
	ctx := context.Background()
	b := draftDe(t, messageTSDeAmbar.Add(elapsedDeAmbar))

	out, err := b.draft.Run(ctx, jobAmbarP4(), entradaDeAmbar(matchDeAmbarSinDeco(t)))
	require.NoError(t, err)

	filas := b.flujos.Intakes()
	require.Len(t, filas, 1)
	require.Equal(t, out.IntakeID, filas[0].ID)
	require.Equal(t, intakes.StatusPendingApproval, filas[0].Status)
	require.Len(t, b.solicitudes.Revisions(out.IntakeID), 1)
}
