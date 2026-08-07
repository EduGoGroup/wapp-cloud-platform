// buyerdata_test.go prueba la MITAD SIN BASE DE DATOS de los datos del comprador
// (Plan 041 · T4.5, D-041.13): la fusión campo a campo, el booleano que es lo único
// que se publica, y los dos barridos que demuestran que el valor no se cuela por
// las salidas del dominio (el summary y el detalle).
//
// El cifrado en sí NO se prueba aquí: se prueba contra Postgres de verdad, en
// buyerdata_integration_test.go. Un doble en memoria que "cifra" no demuestra nada.
package intakes_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// Valores RAROS a propósito: se buscan como subcadena en JSON serializado, y un
// literal corriente daría falsos positivos contra otro campo cualquiera.
const (
	rutBuscable       = "11.222.333-Q-xkq"
	direcciónBuscable = "Camino del Alba 909-xkq, casa 3"
)

// conDatosDelComprador siembra una solicitud del tenant con dos campos del
// checklist ya capturados, por el MISMO camino que usa el proyector del carrito.
func conDatosDelComprador(t *testing.T) (*intakes.MemoryStore, string, string) {
	t.Helper()
	store := intakes.NewMemoryStore()
	tenant := uuid.NewString()
	id := uuid.NewString()
	store.Add(tenant, intakes.Intake{
		ID: id, ContactID: "contacto-opaco", SessionID: "sess-a",
		Status: intakes.StatusConfirmed, Total: 5000,
		CreatedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
	}, intakes.Item{SKU: "cafe", Label: "Café", Qty: 2, UnitPrice: 2500})

	ctx := context.Background()
	if err := store.PutBuyerField(ctx, id, "rut", rutBuscable); err != nil {
		t.Fatalf("PutBuyerField(rut): %v", err)
	}
	if err := store.PutBuyerField(ctx, id, "direccion", direcciónBuscable); err != nil {
		t.Fatalf("PutBuyerField(direccion): %v", err)
	}
	return store, tenant, id
}

// TestBuyerData_FusionaCamposEnUnaSolaFicha: cada campo llega en su propio mensaje,
// así que la escritura tiene que FUSIONAR. Si sustituyera, el pedido acabaría con el
// último dato y sin los anteriores — y nadie lo notaría hasta el reparto.
func TestBuyerData_FusionaCamposEnUnaSolaFicha(t *testing.T) {
	store, _, id := conDatosDelComprador(t)

	got := store.BuyerDataOf(id)
	if len(got) != 2 || got["rut"] != rutBuscable || got["direccion"] != direcciónBuscable {
		t.Fatalf("el checklist guardado quedó en %+v; esperaba los dos campos", got)
	}

	// Reescribir un campo corrige ESE y deja el otro en paz (el cliente se equivocó
	// y lo repitió).
	if err := store.PutBuyerField(context.Background(), id, "rut", "otro-valor"); err != nil {
		t.Fatalf("PutBuyerField (corrección): %v", err)
	}
	got = store.BuyerDataOf(id)
	if got["rut"] != "otro-valor" || got["direccion"] != direcciónBuscable {
		t.Fatalf("tras corregir un campo el checklist quedó en %+v", got)
	}
}

// TestBuyerData_SinClaveNoSeGuarda: un campo sin clave sería un dato personal
// inalcanzable en la base. Se rechaza en vez de guardarse bajo "".
func TestBuyerData_SinClaveNoSeGuarda(t *testing.T) {
	store := intakes.NewMemoryStore()
	if err := store.PutBuyerField(context.Background(), uuid.NewString(), "", "algo"); err == nil {
		t.Fatalf("un campo sin clave se aceptó")
	}
}

// TestBuyerData_ElDetalleSoloPublicaElBooleano es el contrato de salida del
// dominio: Get dice que el checklist ESTÁ, y no qué dice. El barrido busca los dos
// valores en el JSON del Detail entero, no en el campo que se sabe que existe.
func TestBuyerData_ElDetalleSoloPublicaElBooleano(t *testing.T) {
	store, tenant, id := conDatosDelComprador(t)

	detail, err := store.Get(context.Background(), tenant, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !detail.BuyerDataPresent {
		t.Fatalf("BuyerDataPresent = false en una solicitud CON datos del comprador")
	}
	blob, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("serializando el Detail: %v", err)
	}
	for _, valor := range []string{rutBuscable, direcciónBuscable} {
		if strings.Contains(string(blob), valor) {
			t.Fatalf("FUGA: el detalle de la solicitud lleva el valor %q dentro:\n%s", valor, blob)
		}
	}
}

// TestBuyerData_SinDatosElBooleanoEsFalso es la otra mitad del anterior: sin esta
// prueba, un `false` constante también pasaría aquélla.
func TestBuyerData_SinDatosElBooleanoEsFalso(t *testing.T) {
	store := intakes.NewMemoryStore()
	tenant := uuid.NewString()
	id := uuid.NewString()
	store.Add(tenant, intakes.Intake{ID: id, Status: intakes.StatusConfirmed})

	detail, err := store.Get(context.Background(), tenant, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.BuyerDataPresent {
		t.Fatalf("BuyerDataPresent = true en una solicitud SIN datos del comprador")
	}
}

// TestBuyerData_NoEntraEnElSummary barre el summary.json COMPLETO (REQ-04 /
// INV-04). El summary se genera para pegárselo a un LLM EXTERNO: es el peor sitio
// posible para un dato personal, porque sale de la plataforma.
//
// Recorre el camino real —ListDetails, que es lo que consume el summary— y no un
// Detail armado a mano: lo que se quiere probar es que esa LECTURA no trae los
// datos del comprador, no que un struct que no los tiene no los publique.
func TestBuyerData_NoEntraEnElSummary(t *testing.T) {
	store, tenant, _ := conDatosDelComprador(t)
	svc := intakes.NewService(store)

	details, err := svc.ListDetails(context.Background(), tenant, intakes.Filter{})
	if err != nil {
		t.Fatalf("ListDetails: %v", err)
	}
	if len(details) != 1 {
		t.Fatalf("ListDetails devolvió %d solicitudes, esperaba 1", len(details))
	}

	summary := intakes.BuildSummary(details, intakes.Filter{}, time.Now())
	blob, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("serializando el summary: %v", err)
	}
	for _, valor := range []string{rutBuscable, direcciónBuscable} {
		if strings.Contains(string(blob), valor) {
			t.Fatalf("FUGA: el summary.json lleva el valor %q, y ese archivo se le entrega a un LLM externo:\n%s",
				valor, blob)
		}
	}
	// Este barrido es sobre el struct del DOMINIO. El del archivo que sale por el
	// cable —donde de verdad importa— está en publicapi (buyer_data_leak_test.go):
	// allí se barre el summary.json y el CSV tal como los recibe quien los descarga.
}
