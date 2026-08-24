package llmvia

import (
	"errors"
	"fmt"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/llm/api"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantllm"
)

// motivoError finge un error de transporte que expone Motivo() por duck-typing, igual
// que *gatewaygrpc.InferError. Se usa un fake y no el tipo real a propósito: lo que
// este paquete consume es la INTERFAZ ANÓNIMA, y probarlo contra el tipo concreto
// dejaría sin red el desacople que se quiere conservar.
type motivoError struct{ m string }

func (e motivoError) Error() string  { return "fallo del transporte: " + e.m }
func (e motivoError) Motivo() string { return e.m }

// TestMotivoDe_LaTablaCompleta fija el mapeo error → motivo de notificación, incluido
// —y sobre todo— lo que NO notifica.
//
// 🔴 LA MITAD IMPORTANTE SON LOS `false`. Un canal que avisa de más deja de leerse
// (D-044.32), así que «esto no escribe fila» es una afirmación tan fuerte como su
// contraria y aquí se comprueba igual de explícitamente.
func TestMotivoDe_LaTablaCompleta(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		nombre string
		err    error
		quiero degradation.Reason
		avisa  bool
	}{
		// --- Vía local: el motivo viaja dentro del error del transporte ---
		{"ollama caído", motivoError{"ollama_down"}, degradation.ReasonOllamaDown, true},
		{"breaker abierto", motivoError{"breaker_open"}, degradation.ReasonBreakerOpen, true},
		{"sin sesión viva", motivoError{"edge_offline"}, degradation.ReasonEdgeOffline, true},
		{"timeout", motivoError{"timeout"}, degradation.ReasonTimeout, true},
		{"sin lease", motivoError{"lease_invalid"}, degradation.ReasonLeaseInvalid, true},
		{"edge saturado", motivoError{"edge_sin_capacidad"}, degradation.ReasonEdgeSinCapacidad, true},
		{"motivo envuelto en otro error", fmt.Errorf("contexto: %w", motivoError{"timeout"}),
			degradation.ReasonTimeout, true},

		// --- Vía API ---
		{"el tenant no tiene credencial", tenantllm.ErrNotConfigured, degradation.ReasonCredencial, true},
		{"api.New sin credencial", api.ErrInvalidConfig, degradation.ReasonCredencial, true},
		{"el proveedor externo falló", api.ErrUpstream, degradation.ReasonAPIError, true},

		// --- Lo que NO avisa, y por qué ---
		{
			// El modelo RESPONDIÓ; su salida no era interpretable. El proveedor funciona,
			// el cable funciona, y el caller tiene un reintento a 0.3 previsto para esto.
			nombre: "calidad de la salida", err: llm.ErrLLMQuality,
		},
		{
			// Envuelto también: los providers lo meten dentro de errores más gordos y la
			// rama de calidad va la PRIMERA justo para que no se lo trague otra.
			nombre: "calidad envuelta", err: fmt.Errorf("clasificando: %w", llm.ErrLLMQuality),
		},
		{
			// Una fila con `provider` fuera del CHECK. Nada se ha caído: la config está
			// mal escrita. Contarlo como `credencial` mandaría al dueño a rotar una clave
			// que está perfecta.
			nombre: "proveedor no soportado", err: api.ErrUnsupportedProvider,
		},
		{
			// El vocabulario es CERRADO: un motivo que el enum no conoce NO se escribe.
			nombre: "motivo inventado por el transporte", err: motivoError{"se_rompio_algo"},
		},
		{
			// Y un motivo SANO tampoco, aunque venga con la forma correcta.
			nombre: "motivo sano con forma de motivo", err: motivoError{"fastlane"},
		},
		{"error cualquiera", errors.New("vete a saber"), "", false},
		{"sin error", nil, "", false},
	} {
		t.Run(tc.nombre, func(t *testing.T) {
			t.Parallel()
			got, ok := motivoDe(tc.err)
			if ok != tc.avisa {
				t.Fatalf("avisa = %v, quiero %v (motivo=%q)", ok, tc.avisa, got)
			}
			if ok && got != tc.quiero {
				t.Fatalf("motivo = %q, quiero %q", got, tc.quiero)
			}
		})
	}
}

// TestMotivoDe_NuncaDevuelveUnMotivoInvalido es la red estructural sobre la tabla de
// arriba: pase lo que pase, lo que sale de aquí tiene que poder escribirse en
// owner_degradation_notices. Un motivo fuera del enum haría que Notifier.Record
// devolviera ErrMotivoDesconocido y el aviso se perdería justo cuando hacía falta.
func TestMotivoDe_NuncaDevuelveUnMotivoInvalido(t *testing.T) {
	t.Parallel()
	entradas := []error{
		motivoError{"ollama_down"}, motivoError{"basura"}, motivoError{""},
		api.ErrUpstream, api.ErrInvalidConfig, api.ErrUnsupportedProvider,
		tenantllm.ErrNotConfigured, llm.ErrLLMQuality, errors.New("x"), nil,
	}
	for _, err := range entradas {
		if r, ok := motivoDe(err); ok && !r.Valid() {
			t.Fatalf("motivoDe(%v) devolvió %q, que NO es un motivo válido", err, r)
		}
	}
}
