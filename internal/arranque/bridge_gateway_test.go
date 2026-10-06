package arranque

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	viejo "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	edgesession "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// newGatewayBridgeOverEmptyServer monta el adaptador sobre un edge/grpc.Server real y sin ningún
// Edge conectado: basta para ver que delega (el servidor vacío contesta «offline» y «sin plaza»).
func newGatewayBridgeOverEmptyServer(t *testing.T) *gatewayBridge {
	t.Helper()
	log := sharedlogger.New(sharedlogger.WithWriter(io.Discard))
	return &gatewayBridge{gw: edgegrpc.New(edgesession.NewRegistry(), log)}
}

// TestGatewayBridge_InferRequest_CopiesEveryField afirma la conversión campo a campo de T3.24.
// Va por reflexión y no con nueve comparaciones escritas a mano: el día que el InferRequest gane
// un campo en cualquiera de los dos lados, este test falla en vez de dejarlo viajar a cero.
func TestGatewayBridge_InferRequest_CopiesEveryField(t *testing.T) {
	old := viejo.InferRequest{
		Prompt:          "prompt ya construido",
		Format:          `{"type":"object"}`,
		Temperature:     0.7,
		Timeout:         42 * time.Second,
		OriginSessionID: "origin-session",
		TargetSessionID: "target-session",
		MaxOutputTokens: 512,
		Class:           viejo.ClaseLote,
		Warmup:          true,
	}

	oldV := reflect.ValueOf(old)
	newV := reflect.ValueOf(toEdgeInferRequest(old))

	if oldV.NumField() != newV.NumField() {
		t.Fatalf("los dos InferRequest ya no tienen los mismos campos: viejo %d, nuevo %d",
			oldV.NumField(), newV.NumField())
	}
	for i := range oldV.NumField() {
		name := oldV.Type().Field(i).Name
		if oldV.Field(i).IsZero() {
			t.Fatalf("el campo %s del caso de prueba está a cero: no probaría que se copia", name)
		}
		got := newV.FieldByName(name)
		if !got.IsValid() {
			t.Errorf("el InferRequest nuevo no tiene el campo %s", name)
			continue
		}
		if !reflect.DeepEqual(got.Interface(), oldV.Field(i).Interface()) {
			t.Errorf("campo %s: se copió %v, se esperaba %v", name, got.Interface(), oldV.Field(i).Interface())
		}
	}
}

// TestGatewayBridge_InferRequest_ClassValuesMatch afirma que el vocabulario de Class, que el
// adaptador copia sin traducir, vale lo mismo en los dos paquetes aunque las constantes se
// llamen distinto (E-11).
func TestGatewayBridge_InferRequest_ClassValuesMatch(t *testing.T) {
	if viejo.ClaseInteractivo != edgegrpc.ClassInteractive {
		t.Errorf("clase interactiva: viejo %q, nuevo %q", viejo.ClaseInteractivo, edgegrpc.ClassInteractive)
	}
	if viejo.ClaseLote != edgegrpc.ClassBatch {
		t.Errorf("clase de lote: viejo %q, nuevo %q", viejo.ClaseLote, edgegrpc.ClassBatch)
	}
}

// TestGatewayBridge_Infer_DelegatesAndPassesErrorThrough afirma que Infer delega en el servidor
// nuevo y devuelve su error sin traducir: el *edgegrpc.InferError sigue siéndolo, sigue casando
// con el centinela de sesión offline (el que comparan los consumidores viejos) y sigue
// respondiendo a Motivo(), que es por donde llmvia lo lee.
func TestGatewayBridge_Infer_DelegatesAndPassesErrorThrough(t *testing.T) {
	b := newGatewayBridgeOverEmptyServer(t)

	out, err := b.Infer(context.Background(), "tenant-a", viejo.InferRequest{Prompt: "hola"})

	if out != "" {
		t.Errorf("salida = %q, se esperaba vacía junto a un error", out)
	}
	var inferErr *edgegrpc.InferError
	if !errors.As(err, &inferErr) {
		t.Fatalf("el error no es el *InferError del servidor nuevo: %T %v", err, err)
	}
	if !errors.Is(err, edgesession.ErrSessionOffline) {
		t.Errorf("el error no casa con ErrSessionOffline: %v", err)
	}
	withReason, ok := err.(interface{ Motivo() string }) //nolint:errorlint // se afirma el duck-typing que usa llmvia sobre el error tal cual llega
	if !ok {
		t.Fatalf("el error no expone Motivo(): llmvia no sabría por qué falló")
	}
	if withReason.Motivo() != edgegrpc.ReasonEdgeOffline {
		t.Errorf("Motivo() = %q, se esperaba %q", withReason.Motivo(), edgegrpc.ReasonEdgeOffline)
	}
}

// TestGatewayBridge_PlazaDe_SatisfiesTheCapacityAssertion afirma R3.6.b: el adaptador, visto como
// el objeto opaco que viaja en llmvia.WithFrame, pasa la aserción de tipo con la que el aforo
// por Edge busca PlazaDe. Si dejara de pasarla, el aforo se apagaría con solo un Warn (T-1).
func TestGatewayBridge_PlazaDe_SatisfiesTheCapacityAssertion(t *testing.T) {
	b := newGatewayBridgeOverEmptyServer(t)

	p, ok := any(b).(interface {
		PlazaDe(string, string) (string, bool)
	})
	if !ok {
		t.Fatalf("gatewayBridge no expone PlazaDe(string, string) (string, bool): el aforo por Edge quedaría apagado")
	}

	// Delega: un servidor sin Edge conectado no tiene plaza para nadie.
	edgeID, found := p.PlazaDe("tenant-a", "origin-session")
	if found || edgeID != "" {
		t.Errorf("PlazaDe sobre un servidor vacío = (%q, %v), se esperaba (\"\", false)", edgeID, found)
	}
}
