package cart

import (
	"context"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// buyerEffect es el efecto que emite el módulo tras capturar un campo.
func buyerEffect(key, value string) modules.Effect {
	return buyerDataEffect(key, value)
}

// TestProjector_BuyerData_CuelgaElCampoDeLaSolicitudAbierta: el campo se guarda en
// la solicitud que el carrito abrió con el primer item_added, no en una nueva. Es
// lo que hace que el checklist y las líneas acaben en el MISMO pedido.
func TestProjector_BuyerData_CuelgaElCampoDeLaSolicitudAbierta(t *testing.T) {
	repo := store.NewMemoryRepository()
	buyer := intakes.NewMemoryStore()
	p := NewProjector(repo, intakes.NewMemoryStore(), &envíoEspía{}, buyer)
	ctx := context.Background()
	meta := projectorMeta()

	if err := p.Project(ctx, meta, modules.Effect{Name: EffectItemAdded}); err != nil {
		t.Fatalf("Project(item_added): %v", err)
	}
	if err := p.Project(ctx, meta, buyerEffect("rut", "12.345.678-K")); err != nil {
		t.Fatalf("Project(buyer_data_captured): %v", err)
	}

	abiertas := repo.Intakes()
	if len(abiertas) != 1 {
		t.Fatalf("solicitudes: %d, esperaba 1", len(abiertas))
	}
	if got := buyer.BuyerDataOf(abiertas[0].ID)["rut"]; got != "12.345.678-K" {
		t.Fatalf("el campo no llegó a la solicitud abierta: %+v", buyer.BuyerDataOf(abiertas[0].ID))
	}
}

// TestProjector_BuyerData_SinSolicitudAbiertaEsError: un dato que el cliente ya
// tecleó y no se pudo guardar tiene que ser VISIBLE. Tragárselo devolviendo nil
// dejaría al dueño con un pedido sin los datos que pidió y sin ni una línea de log.
func TestProjector_BuyerData_SinSolicitudAbiertaEsError(t *testing.T) {
	repo := store.NewMemoryRepository()
	p := NewProjector(repo, intakes.NewMemoryStore(), &envíoEspía{}, intakes.NewMemoryStore())

	err := p.Project(context.Background(), projectorMeta(), buyerEffect("rut", "12.345.678-K"))
	if err == nil {
		t.Fatalf("sin solicitud abierta el proyector no avisó")
	}
	// Y el error NO puede llevar el valor: acaba en el log del dispatcher.
	if strings.Contains(err.Error(), "12.345.678-K") {
		t.Fatalf("el error del proyector lleva el dato del cliente dentro: %v", err)
	}
	if !strings.Contains(err.Error(), "rut") {
		t.Fatalf("el error no dice de qué campo se trata: %v", err)
	}
}

// TestProjector_BuyerData_PayloadIncompletoEsNoOp: un efecto sin clave o sin valor
// no es un fallo (el módulo no los produce así, y un replay de un payload viejo no
// debe reventar la conversación). Simplemente no hay nada que guardar.
func TestProjector_BuyerData_PayloadIncompletoEsNoOp(t *testing.T) {
	repo := store.NewMemoryRepository()
	buyer := intakes.NewMemoryStore()
	p := NewProjector(repo, intakes.NewMemoryStore(), &envíoEspía{}, buyer)
	ctx := context.Background()

	for _, eff := range []modules.Effect{
		buyerEffect("", "valor suelto"),
		buyerEffect("rut", ""),
		{Kind: modules.KindPrivate, Name: EffectBuyerDataCaptured},
	} {
		if err := p.Project(ctx, projectorMeta(), eff); err != nil {
			t.Fatalf("un payload incompleto devolvió error: %v", err)
		}
	}
	if len(repo.Intakes()) != 0 {
		t.Fatalf("un payload incompleto abrió una solicitud")
	}
}

// TestProjector_BuyerData_LoReconoce fija que el proyector se hace cargo del
// efecto. Es la mitad complementaria de la regla del PersistSink: como el efecto NO
// se escribe en flow_events, si aquí no se reconociera, el dato no se guardaría en
// ninguna parte y nadie lo notaría.
func TestProjector_BuyerData_LoReconoce(t *testing.T) {
	if !(Projector{}).Handles(EffectBuyerDataCaptured) {
		t.Fatalf("el proyector del carrito no reconoce %q: el dato del comprador se perdería entero",
			EffectBuyerDataCaptured)
	}
}
