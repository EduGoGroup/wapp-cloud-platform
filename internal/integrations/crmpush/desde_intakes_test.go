package crmpush

// desde_intakes_test.go — la SEGUNDA puerta arma el MISMO documento que la primera.
//
// 🔴 LO QUE ESTE FICHERO EXISTE PARA IMPEDIR. Si las revisiones del dueño armaran su
// propio cuerpo en vez de pasar por Build, el defecto que T4.10 acaba de arreglar
// —un campo clave clavado— volvería repartido en dos sitios, y el segundo no lo
// vigila ningún candado del primero. Por eso el test central no comprueba «encoló»:
// comprueba que lo encolado lleva el número y el estado que le dio el llamante.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// detalleDePrueba es una solicitud del dominio TAL COMO la devuelve
// Service.ReplaceItems: cabecera + líneas. El estado es `pending_approval` —donde
// deja la fila una corrección— porque es justo el caso que el literal `"confirmed"`
// arruinaba.
func detalleDePrueba(status string) intakes.Detail {
	return intakes.Detail{
		Intake: intakes.Intake{
			ID:        intakeDePrueba,
			ContactID: "contact-opaco-xyz",
			SessionID: "sess-negocio",
			Status:    status,
			Total:     24.8,
		},
		Items: []intakes.Item{
			{SKU: "A1", Label: "Café", Customization: "sin azúcar", Qty: 2, UnitPrice: 9.9},
			{SKU: "B2", Label: "Té", Qty: 1, UnitPrice: 5.0},
		},
	}
}

// adaptadorDePrueba arma el adaptador sobre los dos dobles y devuelve la cola para
// inspeccionarla.
func adaptadorDePrueba(abierto bool) (*RevisionPusher, *colaFalsa) {
	p, q := pusherDePrueba(abierto, nil, nil)
	return NewRevisionPusher(p, logDescartado()), q
}

// TestRevisionPusher_EncolaElNúmeroYElEstadoQueLeDieron es el test central: el
// cuerpo que acaba en webhook_outbox lleva la revisión 5 y `pending_approval`.
//
// Los dos valores están elegidos para que un literal reintroducido CHOQUE: con el
// `1` que T4.10 retiró, el revision_no saldría 1; con el `"confirmed"` que retira
// esta mitad, el estado saldría confirmed. Ninguno de los dos pasa por casualidad.
func TestRevisionPusher_EncolaElNúmeroYElEstadoQueLeDieron(t *testing.T) {
	a, q := adaptadorDePrueba(true)

	a.PushRevision(context.Background(), tenantDePrueba, detalleDePrueba(estadoPendingApproval), 5)

	if len(q.llamadas) != 1 {
		t.Fatalf("se encoló %d veces, quiero 1", len(q.llamadas))
	}
	if q.llamadas[0].kind != "intake.push" {
		t.Fatalf("kind = %q, quiero intake.push", q.llamadas[0].kind)
	}
	var cuerpo map[string]any
	if err := json.Unmarshal(q.llamadas[0].payload, &cuerpo); err != nil {
		t.Fatalf("el cuerpo encolado no es JSON válido: %v", err)
	}
	if cuerpo["revision_no"] != float64(5) {
		t.Fatalf("revision_no = %#v, quiero 5. El puente hace UPSERT por (intake_id, revision_no): "+
			"con el número equivocado la corrección se pierde como duplicado", cuerpo["revision_no"])
	}
	if cuerpo["lifecycle_status"] != estadoPendingApproval {
		t.Fatalf("lifecycle_status = %#v, quiero %q. Una corrección deja la solicitud POR APROBAR; "+
			"decirle `confirmed` al CRM le hace creer que el pedido está cerrado",
			cuerpo["lifecycle_status"], estadoPendingApproval)
	}
	if cuerpo["intake_id"] != intakeDePrueba {
		t.Fatalf("intake_id = %#v", cuerpo["intake_id"])
	}
}

// TestRevisionPusher_NormalizaElEstadoLegado: si la solicitud está guardada con la
// clave legada del carrito, el contrato emite la canónica. La regla vive en Build y
// esta puerta no puede saltársela.
func TestRevisionPusher_NormalizaElEstadoLegado(t *testing.T) {
	a, q := adaptadorDePrueba(true)

	a.PushRevision(context.Background(), tenantDePrueba, detalleDePrueba(estadoClosedLegacy), 2)

	var cuerpo map[string]any
	if err := json.Unmarshal(q.llamadas[0].payload, &cuerpo); err != nil {
		t.Fatalf("el cuerpo encolado no es JSON válido: %v", err)
	}
	if cuerpo["lifecycle_status"] != estadoConfirmed {
		t.Fatalf("lifecycle_status = %#v, quiero %q: el contrato JAMÁS emite `closed`",
			cuerpo["lifecycle_status"], estadoConfirmed)
	}
}

// TestRevisionPusher_LasLíneasCruzanConSuPersonalización: la personalización de
// LÍNEA es dato de producción y viaja (D-041.17); el dinero no se mueve por
// personalizar (INV-13).
func TestRevisionPusher_LasLíneasCruzanConSuPersonalización(t *testing.T) {
	a, q := adaptadorDePrueba(true)

	a.PushRevision(context.Background(), tenantDePrueba, detalleDePrueba(estadoPendingApproval), 3)

	var cuerpo Payload
	if err := json.Unmarshal(q.llamadas[0].payload, &cuerpo); err != nil {
		t.Fatalf("el cuerpo encolado no es JSON válido: %v", err)
	}
	if len(cuerpo.Items) != 2 {
		t.Fatalf("items = %d, quiero 2", len(cuerpo.Items))
	}
	if cuerpo.Items[0].Customization != "sin azúcar" || cuerpo.Items[1].Customization != "" {
		t.Fatalf("personalización mal traducida: %+v", cuerpo.Items)
	}
	if cuerpo.Items[0].UnitPrice != 9.9 || cuerpo.Total != 24.8 {
		t.Fatalf("unit_price=%v total=%v; la traducción movió el dinero", cuerpo.Items[0].UnitPrice, cuerpo.Total)
	}
}

// TestRevisionPusher_GateCerradoNoEncola: el mismo gate que el sink del cierre, y
// el mismo desenlace — un tenant sin puente CRM activo no recibe nada y eso no es
// una avería.
func TestRevisionPusher_GateCerradoNoEncola(t *testing.T) {
	a, q := adaptadorDePrueba(false)

	a.PushRevision(context.Background(), tenantDePrueba, detalleDePrueba(estadoPendingApproval), 5)

	if len(q.llamadas) != 0 {
		t.Fatalf("gate cerrado no debe encolar, encoló %d", len(q.llamadas))
	}
}

// TestRevisionPusher_NoTumbaAlLlamante: ni un adaptador a medias ni un store que
// panica pueden llevarse por delante la respuesta de una corrección que YA está
// escrita — si lo hicieran, el dueño reintentaría y crearía una revisión de más.
func TestRevisionPusher_NoTumbaAlLlamante(t *testing.T) {
	t.Run("adaptador a medias", func(t *testing.T) {
		for nombre, a := range map[string]*RevisionPusher{
			"nil":        nil,
			"sin pusher": NewRevisionPusher(nil, logDescartado()),
			"sin log":    NewRevisionPusher(NewPusher(logDescartado(), &colaFalsa{}, &gateFalso{}), nil),
		} {
			t.Run(nombre, func(t *testing.T) {
				a.PushRevision(context.Background(), tenantDePrueba, detalleDePrueba(estadoPendingApproval), 5)
			})
		}
	})

	t.Run("el store panica", func(t *testing.T) {
		a := NewRevisionPusher(NewPusher(logDescartado(), colaQuePanica{},
			&gateFalso{abiertos: map[string]bool{tenantDePrueba: true}}), logDescartado())
		a.PushRevision(context.Background(), tenantDePrueba, detalleDePrueba(estadoPendingApproval), 5)
	})
}

// colaQuePanica imita el peor fallo posible del store: no un error, un pánico.
type colaQuePanica struct{}

func (colaQuePanica) EnqueueWebhook(context.Context, string, string, json.RawMessage) (int64, error) {
	panic("conexión en un estado imposible")
}
