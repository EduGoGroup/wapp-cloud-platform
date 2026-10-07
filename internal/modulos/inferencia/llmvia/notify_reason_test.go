package llmvia

// El mapeo error → motivo (reasonOf; en el paquete viejo, motivoDe), probado DIRECTAMENTE.
// Es un auxiliar no exportado que nace en el verde y lleva regla de negocio —el orden de sus
// ramas es el contrato (T-6)—, así que su test nace con él (05 E-4, P6). La misma tabla,
// vista como conducta por las tres puertas del Selector, está en notify_test.go.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/llm/api"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
)

// reasonError finge un error del transporte que expone Motivo() por duck-typing, igual que
// *edgegrpc.InferError. Es un doble y no el tipo real a propósito: lo que este paquete
// consume es la INTERFAZ ANÓNIMA, y probarlo contra el tipo concreto dejaría sin red el
// desacople que se quiere conservar. Receptor puntero: así «el mismo error» es identidad.
type reasonError struct{ reason string }

func (e *reasonError) Error() string  { return "fallo del transporte: " + e.reason }
func (e *reasonError) Motivo() string { return e.reason }

// TestReasonOf_TheWholeTable fija el mapeo error → motivo de notificación (R4.5.d), incluido
// —y sobre todo— lo que NO notifica. Son las 8 filas de la tabla del diseño, recorridas con
// los 17 casos del test viejo (internal/llmvia/notify_internal_test.go).
//
// 🔴 LA MITAD IMPORTANTE SON LOS `false`. Un canal que avisa de más deja de leerse
// (D-044.32), así que «esto no escribe fila» es una afirmación tan fuerte como su contraria
// y aquí se comprueba igual de explícitamente.
func TestReasonOf_TheWholeTable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		err      error
		want     degradation.Reason
		notifies bool
	}{
		// --- Vía local: el motivo viaja dentro del error del transporte ---
		{"ollama down", &reasonError{edgegrpc.ReasonOllamaDown}, degradation.ReasonOllamaDown, true},
		{"breaker open", &reasonError{edgegrpc.ReasonBreakerOpen}, degradation.ReasonBreakerOpen, true},
		{"no live session", &reasonError{edgegrpc.ReasonEdgeOffline}, degradation.ReasonEdgeOffline, true},
		{"timeout", &reasonError{edgegrpc.ReasonTimeout}, degradation.ReasonTimeout, true},
		{"no lease", &reasonError{edgegrpc.ReasonLeaseInvalid}, degradation.ReasonLeaseInvalid, true},
		{"edge saturated", &reasonError{edgegrpc.ReasonEdgeSinCapacidad}, degradation.ReasonEdgeSinCapacidad, true},
		{"reason wrapped in another error", fmt.Errorf("contexto: %w", &reasonError{"timeout"}), degradation.ReasonTimeout, true},

		// --- Los centinelas de la vía API ---
		{"tenant has no credential", tenantllm.ErrNotConfigured, degradation.ReasonCredencial, true},
		{"api.New without credential", api.ErrInvalidConfig, degradation.ReasonCredencial, true},
		{"upstream provider failed", api.ErrUpstream, degradation.ReasonAPIError, true},

		// --- Lo que NO avisa, y por qué ---
		// El modelo RESPONDIÓ; su salida no era interpretable. El proveedor funciona, el
		// cable funciona, y el llamante tiene un reintento a 0,3 previsto para esto.
		{name: "output quality", err: llm.ErrLLMQuality},
		// Envuelto también: los providers lo meten dentro de errores más gordos y la rama
		// de calidad va la PRIMERA justo para que no se lo trague otra.
		{name: "wrapped quality", err: fmt.Errorf("clasificando: %w", llm.ErrLLMQuality)},
		// Una fila con `provider` fuera del CHECK. Nada se ha caído: la config está mal
		// escrita. Contarlo como `credencial` mandaría al dueño a rotar una clave buena.
		{name: "unsupported provider", err: api.ErrUnsupportedProvider},
		// El vocabulario es CERRADO: un motivo que el enum no conoce NO se escribe.
		{name: "reason invented by the transport", err: &reasonError{"se_rompio_algo"}},
		// Y un motivo SANO tampoco, aunque venga con la forma correcta.
		{name: "healthy reason shaped like a reason", err: &reasonError{"fastlane"}},
		{name: "any other error", err: errors.New("vete a saber")},
		{name: "no error", err: nil},

		// --- Lo que la tabla del diseño nombra y los 17 casos viejos no recorrían ---
		{name: "empty reason from the transport", err: &reasonError{""}},
		{name: "unsupported provider wrapped by the constructor", err: fmt.Errorf("api.New: %w", api.ErrUnsupportedProvider)},
		{"credential error wrapped by the selection", fmt.Errorf("llmvia: credencial del tenant no disponible: %w", tenantllm.ErrNotConfigured),
			degradation.ReasonCredencial, true},
		{"wrapped upstream", fmt.Errorf("anthropic: %w", api.ErrUpstream), degradation.ReasonAPIError, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := reasonOf(tc.err)
			if ok != tc.notifies {
				t.Fatalf("¿avisa? = %v, quería %v (motivo = %q)", ok, tc.notifies, got)
			}
			if got != tc.want {
				t.Fatalf("motivo = %q, quería %q", got, tc.want)
			}
		})
	}
}

// TestReasonOf_TheBranchOrderIsTheContract (T-6): cada caso es un error que casa con DOS
// ramas a la vez, y afirma cuál gana. Con las ramas en otro orden cada uno daría otra cosa:
// es lo que convierte «el orden es el contrato» en algo que se pone rojo.
func TestReasonOf_TheBranchOrderIsTheContract(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		err      error
		want     degradation.Reason
		notifies bool
	}{
		// 1 antes que 2: la calidad gana aunque el error traiga además un motivo válido. El
		// modelo respondió; que el envoltorio nombre un motivo no lo convierte en avería.
		{name: "quality beats a transport reason", err: errors.Join(&reasonError{edgegrpc.ReasonTimeout}, llm.ErrLLMQuality)},
		// 1 antes que 3.
		{name: "quality beats an api sentinel", err: errors.Join(api.ErrUpstream, llm.ErrLLMQuality)},
		// 2 antes que 3: el motivo del transporte gana a los centinelas…
		{"a transport reason beats an api sentinel", errors.Join(api.ErrUpstream, &reasonError{edgegrpc.ReasonTimeout}),
			degradation.ReasonTimeout, true},
		// … y 🔴 un motivo NO válido CIERRA la búsqueda: no se sigue mirando hacia abajo
		// para «rescatar» una fila con otro motivo.
		{name: "an invalid transport reason does not fall through", err: errors.Join(&reasonError{"fastlane"}, tenantllm.ErrNotConfigured)},
		// Dentro de 3, el orden de los centinelas.
		{"no credential beats unsupported provider", errors.Join(api.ErrUnsupportedProvider, tenantllm.ErrNotConfigured),
			degradation.ReasonCredencial, true},
		// 🔴 ErrUnsupportedProvider ENVUELVE ErrInvalidConfig: va antes, o se contaría como
		// credencial y mandaría al dueño a rotar una clave que está perfecta.
		{name: "unsupported provider beats invalid config", err: errors.Join(api.ErrInvalidConfig, api.ErrUnsupportedProvider)},
		{name: "unsupported provider beats upstream", err: errors.Join(api.ErrUpstream, api.ErrUnsupportedProvider)},
		{"invalid config beats upstream", errors.Join(api.ErrUpstream, api.ErrInvalidConfig), degradation.ReasonCredencial, true},
		{"no credential beats upstream", errors.Join(api.ErrUpstream, tenantllm.ErrNotConfigured), degradation.ReasonCredencial, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := reasonOf(tc.err)
			if ok != tc.notifies || got != tc.want {
				t.Fatalf("reasonOf = (%q, %v), quería (%q, %v)", got, ok, tc.want, tc.notifies)
			}
		})
	}
	// La premisa del caso estrella: si un día api dejara de envolver, el orden daría igual.
	if !errors.Is(api.ErrUnsupportedProvider, api.ErrInvalidConfig) {
		t.Error("api.ErrUnsupportedProvider ya no envuelve api.ErrInvalidConfig: revisa el orden de las ramas")
	}
}

// TestReasonOf_NeverReturnsAnInvalidReason es la red estructural sobre las tablas de arriba:
// pase lo que pase, lo que sale de aquí tiene que poder escribirse en
// owner_degradation_notices. Un motivo fuera del enum haría que el escritor lo rechazara y
// el aviso se perdería justo cuando hacía falta. Y sin aviso, el motivo va vacío.
func TestReasonOf_NeverReturnsAnInvalidReason(t *testing.T) {
	t.Parallel()
	for _, err := range []error{
		&reasonError{"ollama_down"}, &reasonError{"basura"}, &reasonError{""},
		api.ErrUpstream, api.ErrInvalidConfig, api.ErrUnsupportedProvider,
		tenantllm.ErrNotConfigured, llm.ErrLLMQuality, errors.New("x"), nil,
	} {
		r, ok := reasonOf(err)
		if ok && !r.Valid() {
			t.Errorf("reasonOf(%v) devolvió %q, que NO es un motivo válido", err, r)
		}
		if !ok && r != "" {
			t.Errorf("reasonOf(%v) = (%q, false): sin aviso el motivo va vacío", err, r)
		}
	}
}
