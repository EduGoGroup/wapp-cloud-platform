package gatewaygrpc

import (
	"slices"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/degradation"
)

// ============================================================================
// LA SIMETRÍA DE LOS TRES VOCABULARIOS (T1.6-3 / T1.6-6)
//
// Hay TRES listas que tienen que decir lo mismo y viven en tres repos/paquetes:
//
//  1. el enum InferenceError del proto (wapp-cloudlink) — lo que el Edge sabe decir;
//  2. las constantes Motivo* de inference.go — lo que el transporte traduce;
//  3. degradation.Reason — lo que se puede escribir en owner_degradation_notices.
//
// Que coincidan LITERALMENTE es lo que permite que el escritor de avisos convierta
// `Motivo()` en `Reason` sin tabla de traducción. Y una coincidencia literal que no
// se comprueba es una coincidencia que dura hasta el próximo commit.
//
// 🔴 ESTOS TESTS VIVEN EN EL LADO DEL ESCRITOR, y no es indiferente: quien escribe
// los literales es este paquete, así que es aquí donde se escribiría el equivocado.
// Puestos en el paquete lector, un literal nuevo aquí no rompería nada hasta que
// alguien lo consumiera — que es justo el fallo que se quiere evitar.
// ============================================================================

// TestLosMotivosDelTransporteSonMotivosDeNotificacion: cada Motivo* de este paquete
// tiene que ser un degradation.Reason válido. Si no lo fuera, el aviso se perdería en
// silencio (motivoDe lo descartaría por no pasar .Valid()) y el dueño no se enteraría
// de una degradación real.
//
// 🔬 MUTACIÓN: cambiar MotivoEdgeSinCapacidad a "edge_saturado" ⇒ rojo aquí.
func TestLosMotivosDelTransporteSonMotivosDeNotificacion(t *testing.T) {
	t.Parallel()
	validos := degradation.Reasons()
	for _, m := range motivosInferencia {
		if !slices.Contains(validos, degradation.Reason(m)) {
			t.Errorf("el motivo %q del transporte NO está en el vocabulario de degradación %v", m, validos)
		}
	}
}

// TestElEnumDelProtoNoCrecioSinDecidirElMotivo es el guardián del caso que se cuela
// solo: el proto añade un InferenceError nuevo y motivoDeFrame lo manda al `default`
// —ollama_down— sin que nada se ponga rojo. El dueño acabaría mirando su Ollama por
// un fallo que no es de Ollama.
//
// Por eso NO se comprueba «que todos mapeen» (todos mapean siempre, para eso está el
// default): se comprueba que el TAMAÑO del enum sea el que se revisó a mano. Un valor
// nuevo obliga a pasar por aquí y a decidir su motivo, que es exactamente lo que se
// quiere que pase.
//
// El número sale de la lista generada del proto, no de este paquete, así que no es
// tautológico: si mañana el .proto cambia, este contador cambia y el test avisa.
func TestElEnumDelProtoNoCrecioSinDecidirElMotivo(t *testing.T) {
	t.Parallel()
	const revisados = 6 // UNSPECIFIED + los cinco errores nombrados (T1.6-1)
	if got := len(cloudlinkv1.InferenceError_name); got != revisados {
		t.Fatalf("el enum InferenceError tiene %d valores y se revisaron %d: "+
			"añade la rama del valor nuevo a motivoDeFrame y DECIDE su motivo "+
			"(el default lo mandaría a ollama_down en silencio)", got, revisados)
	}
	// Y de paso: ningún valor puede producir un motivo fuera del vocabulario.
	for v := range cloudlinkv1.InferenceError_name {
		m := motivoDeFrame(cloudlinkv1.InferenceError(v))
		if !slices.Contains(motivosInferencia, m) {
			t.Errorf("InferenceError(%d) produce el motivo %q, que no está en el vocabulario", v, m)
		}
	}
}

// TestLasViasDelTransporteYLasDeLaNotificacionCoinciden: el eje VÍA también está
// duplicado —tenantllm y degradation lo declaran cada uno por su cuenta, con el
// porqué escrito en degradation.go— y el aviso solo se escribe si los dos dicen lo
// mismo. Se comprueba desde aquí porque este paquete es el que produce los fallos de
// la vía local y sería el primero en notar la divergencia.
func TestLasViasDelTransporteYLasDeLaNotificacionCoinciden(t *testing.T) {
	t.Parallel()
	if !degradation.ValidVia(degradation.ViaLocal) || !degradation.ValidVia(degradation.ViaAPI) {
		t.Fatal("el vocabulario de vías de degradation se contradice a sí mismo")
	}
	if degradation.ViaLocal != "local" || degradation.ViaAPI != "api" {
		t.Fatalf("las vías cambiaron de literal: %q/%q", degradation.ViaLocal, degradation.ViaAPI)
	}
}
