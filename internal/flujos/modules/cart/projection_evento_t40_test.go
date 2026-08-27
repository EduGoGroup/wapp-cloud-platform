// projection_evento_t40_test.go — LA DIRECCIÓN carrito→pipeline del hallazgo #24
// (Plan 044 · T4.0, D-044.46), con los datos de campo del 2026-08-27.
//
// LA ESCENA, tal como quedó en `logs/cloud.log` de UAT a las 00:11:08: la etapa
// `draft` del pipeline ya había parido su borrador sobre el evento y lo había dejado
// en `pending_approval`; el cliente siguió comprando por el carrito y el `item_added`
// llegó DESPUÉS. `GetOpenIntake` resuelve por identidad de negocio y solo ve las
// `open`, así que el borrador ajeno le resultaba INVISIBLE: el proyector creaba una
// fila nueva con un id sorteado y chocaba contra `intakes_event_id_uidx`. El pedido
// de ese turno se perdía.
//
// 🔴 POR QUÉ ESTE TEST NO NECESITA POSTGRES PARA DECIR LA VERDAD. El repositorio en
// memoria NO impone el único parcial, así que aquí el defecto no se manifiesta como
// un 23505 sino como su CAUSA: DOS filas colgando del mismo evento. Es la misma
// afirmación —«un evento tiene A LO SUMO un contenido durable», E-8— comprobada un
// paso antes de que la base tenga que rechazarla, y por eso el criterio se enuncia
// sobre el número de filas y no sobre el error del driver.
package cart

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// eventoCompartido es el evento conversacional del que cuelgan los DOS productores.
// Es un UUID de verdad porque en la base la columna lo es (0054) y el lector por
// evento del Postgres descarta lo que no parsee.
const eventoCompartido = "3f2a1c4e-9b7d-4e21-8a55-6c0f1d2b3a44"

// borradorDelPipeline es la fila que la etapa `draft` deja al interpretar el pedido:
// id derivado del evento, estado `pending_approval`, esperando al dueño.
const borradorDelPipeline = "8c1e7b90-2d43-4f16-9a08-51b7c6d3e4f2"

// itemAgregado es el efecto que emite el carrito al agregar un artículo, con su foto
// de líneas (la clave `items` es la que mira cartLineSnapshot).
func itemAgregado() modules.Effect {
	return modules.Effect{
		Kind: kindEvent,
		Name: EffectItemAdded,
		Payload: map[string]any{
			"items": []map[string]any{
				{"sku": "emp-pino", "label": "Empanada de pino", "qty": 3, "unit_price": 2500.0},
			},
		},
	}
}

// TestProjector_ItemAdded_ReusaElContenidoQueElPipelineDejoEnElEvento es el criterio
// (a) de T4.0: con un borrador del pipeline ya colgado del evento, el `item_added`
// del carrito NO para una segunda solicitud — se suma a la que hay.
//
// Las tres cosas que se afirman, y ninguna sobra:
//
//   - UNA sola fila para el evento (E-8). Es la que falla en el código de hoy: sin la
//     consulta por evento salen DOS.
//   - Es LA QUE YA ESTABA (mismo id): reusar significa escribir sobre ella, no
//     sustituirla por otra que casualmente también sea única.
//   - Su `status` sigue siendo `pending_approval`: el carrito no devuelve a `open` un
//     borrador que ya espera al dueño (D-044.46).
func TestProjector_ItemAdded_ReusaElContenidoQueElPipelineDejoEnElEvento(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryRepository()
	p := NewProjector(repo, intakes.NewMemoryStore(), &envíoEspía{}, intakes.NewMemoryStore())

	meta := projectorMeta()
	meta.EventID = eventoCompartido

	// El PIPELINE llegó primero y dejó su borrador sobre este evento.
	if err := repo.UpsertIntake(ctx, store.Intake{
		ID:        borradorDelPipeline,
		TenantID:  meta.TenantID,
		ContactID: meta.ContactID,
		SessionID: meta.SessionID,
		Status:    intakes.StatusPendingApproval,
		EventID:   meta.EventID,
	}); err != nil {
		t.Fatalf("sembrar el borrador del pipeline: %v", err)
	}

	// Y AHORA el cliente agrega un artículo por el carrito, en la misma conversación.
	if err := p.Project(ctx, meta, itemAgregado()); err != nil {
		t.Fatalf("Project(item_added): %v", err)
	}

	filas := repo.Intakes()
	if len(filas) != 1 {
		t.Fatalf("solicitudes del evento %s: got %d, want 1 — un evento tiene A LO SUMO un "+
			"contenido durable (E-8, intakes_event_id_uidx): %+v", eventoCompartido, len(filas), filas)
	}
	if filas[0].ID != borradorDelPipeline {
		t.Fatalf("el carrito no reusó la fila que ya estaba: got id=%s, want %s",
			filas[0].ID, borradorDelPipeline)
	}
	if filas[0].Status != intakes.StatusPendingApproval {
		t.Errorf("el carrito pisó el estado del borrador: got %q, want %q",
			filas[0].Status, intakes.StatusPendingApproval)
	}

	// Y las líneas del carrito aterrizan en ESA solicitud, que es lo que hace que
	// reusar no sea lo mismo que descartar el pedido del turno.
	lineas := repo.IntakeItems(borradorDelPipeline)
	if len(lineas) != 1 || lineas[0].SKU != "emp-pino" || lineas[0].Qty != 3 {
		t.Errorf("las líneas del carrito no colgaron del borrador reusado: %+v", lineas)
	}
}

// TestProjector_ItemAdded_LaIdentidadDeNegocioSIGUE_MANDANDO es la no-regresión de la
// pregunta vieja: la consulta por evento es la SEGUNDA, no la primera. Con una
// solicitud `open` del contacto, el camino normal sigue siendo el de siempre —se reusa
// y se «toca» con UpsertIntake—, y la nueva pregunta ni siquiera hace falta.
//
// Existe porque el arreglo de T4.0 se podía escribir mal de una forma concreta y
// silenciosa: resolviendo SIEMPRE por evento. Eso rompería el rescate de una solicitud
// abierta que declara un evento distinto del vivo (la rama que ensureOpenIntake loguea
// y no pisa).
func TestProjector_ItemAdded_LaIdentidadDeNegocioSIGUE_MANDANDO(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryRepository()
	p := NewProjector(repo, intakes.NewMemoryStore(), &envíoEspía{}, intakes.NewMemoryStore())

	meta := projectorMeta()
	meta.EventID = eventoCompartido

	if err := p.Project(ctx, meta, itemAgregado()); err != nil {
		t.Fatalf("Project(item_added) #1: %v", err)
	}
	primera := soloSolicitud(t, repo)
	if primera.Status != intakeStatusOpen {
		t.Fatalf("la solicitud nueva del carrito nace open: got %q", primera.Status)
	}

	if err := p.Project(ctx, meta, itemAgregado()); err != nil {
		t.Fatalf("Project(item_added) #2: %v", err)
	}
	segunda := soloSolicitud(t, repo)
	if segunda.ID != primera.ID {
		t.Errorf("el segundo item_added abrió otra solicitud: got %s, want %s", segunda.ID, primera.ID)
	}
}
