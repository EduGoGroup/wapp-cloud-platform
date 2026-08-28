package telemetria_test

// telemetria_test.go — EL ADAPTADOR QUE FIRMA LAS FILAS DE LA BANDEJA (T5.2).
//
// Es poco código y aun así tiene un criterio que sostener: qué `flow_id`, qué
// `flow_version` y qué `kind` lleva cada fila. Son las tres columnas por las que una
// consulta separa la telemetría de la bandeja del resto del outbox, y las tres
// estaban clavadas a un literal — la clase de valor que en este plan ya nació mal dos
// veces.

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes/telemetria"
)

// outboxFalso retiene lo escrito y puede fallar a voluntad.
type outboxFalso struct {
	filas []store.FlowEvent
	err   error
}

func (o *outboxFalso) InsertFlowEvent(_ context.Context, ev store.FlowEvent) error {
	if o.err != nil {
		return o.err
	}
	o.filas = append(o.filas, ev)
	return nil
}

// TestPublicador_FirmaLaFilaConElFlujoSinteticoDeLaBandeja: las tres columnas que la
// bandeja no elige (flow_id, flow_version, kind) salen de aquí, y el resto viaja tal
// cual desde el dominio.
func TestPublicador_FirmaLaFilaConElFlujoSinteticoDeLaBandeja(t *testing.T) {
	o := &outboxFalso{}
	p := telemetria.New(o)

	payload := map[string]any{"lines_corrected": 2, "lines_total": 4}
	if err := p.PublicarMetrica(context.Background(), "tenant-1", "contacto-opaco-1",
		intakes.EventoLineaCorregida, payload); err != nil {
		t.Fatalf("PublicarMetrica: %v", err)
	}

	if len(o.filas) != 1 {
		t.Fatalf("filas escritas = %d, quiero 1", len(o.filas))
	}
	got := o.filas[0]
	want := store.FlowEvent{
		TenantID:    "tenant-1",
		ContactID:   "contacto-opaco-1",
		FlowID:      telemetria.FlujoBandeja,
		FlowVersion: telemetria.VersionFlujoBandeja,
		Kind:        "event",
		Name:        intakes.EventoLineaCorregida,
		Payload:     payload,
	}
	if got.TenantID != want.TenantID || got.ContactID != want.ContactID ||
		got.FlowID != want.FlowID || got.FlowVersion != want.FlowVersion ||
		got.Kind != want.Kind || got.Name != want.Name {
		t.Fatalf("la fila salió %+v; quiero %+v", got, want)
	}
	// El payload viaja TAL CUAL: el adaptador no añade claves ni las traduce, porque
	// el contrato de design §10 es del dominio y no suyo.
	if len(got.Payload) != len(payload) || got.Payload["lines_total"] != 4 {
		t.Fatalf("el adaptador tocó el payload: %v", got.Payload)
	}
}

// TestPublicador_ElFlujoEsReservadoYNoEsElDelPipeline: las dos mitades del prefijo.
//
// El `_` reserva el identificador para la plataforma (ningún flujo de tenant puede
// colisionar), y NO coincidir con `_intake_llm` es lo que permite separar por SQL lo
// que INTERPRETÓ la máquina de lo que DECIDIÓ el dueño.
func TestPublicador_ElFlujoEsReservadoYNoEsElDelPipeline(t *testing.T) {
	if telemetria.FlujoBandeja[0] != '_' {
		t.Fatalf("flow_id=%q: sin el prefijo reservado, un flujo de un tenant podría colisionar",
			telemetria.FlujoBandeja)
	}
	if telemetria.FlujoBandeja == "_intake_llm" {
		t.Fatal("la bandeja firma con el flujo del PIPELINE: las dos telemetrías dejarían de poder separarse")
	}
	if telemetria.VersionFlujoBandeja == 0 {
		t.Fatal("flow_version 0 significa «versión desconocida», y este emisor sí tiene contrato")
	}
}

// TestPublicador_DevuelveElErrorDelOutbox: el adaptador no se traga el fallo. Quien
// decide qué hacer con él es el dominio, que ya tiene la regla escrita —avisar y
// seguir— junto a las acciones que no puede tumbar.
func TestPublicador_DevuelveElErrorDelOutbox(t *testing.T) {
	fallo := errors.New("la base no responde")
	p := telemetria.New(&outboxFalso{err: fallo})

	err := p.PublicarMetrica(context.Background(), "tenant-1", "contacto-opaco-1",
		intakes.EventoAprobado, map[string]any{"rev": 1})
	if !errors.Is(err, fallo) {
		t.Fatalf("error=%v; el adaptador se tragó el fallo del outbox", err)
	}
}

// TestPublicador_SatisfaceElPuertoDelDominio es el cable en tiempo de compilación: si
// alguien cambia la firma de un lado, esto no compila en vez de dejar el arranque sin
// telemetría en silencio.
func TestPublicador_SatisfaceElPuertoDelDominio(t *testing.T) {
	var _ intakes.PublicadorDeMetricas = telemetria.New(&outboxFalso{})
}
