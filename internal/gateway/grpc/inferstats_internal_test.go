package gatewaygrpc

import (
	"context"
	"io"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/inferstats"
)

func srvConStats(t *testing.T) (*Server, *inferstats.Store) {
	t.Helper()
	st := inferstats.New()
	return New(session.NewRegistry(), logger.New(logger.WithWriter(io.Discard)),
		WithInferenceStats(st)), st
}

func latidoConInferencia(sh *cloudlinkv1.SessionHealth) *cloudlinkv1.Heartbeat {
	return &cloudlinkv1.Heartbeat{SessionHealth: sh}
}

// TestObserveInference_TraduceElBloqueDeInferencia: los cuatro campos que la Ola 1.7
// estrenó en SessionHealth llegan al almacén que /metrics lee. Hasta hoy subían y
// morían en la base.
//
// 🔬 MUTACIÓN: quitar `PorRegimen: sh.GetInferenceByRegime()` de observeInference ⇒
// rojo. Es la mutación que prueba que la publicación existe de verdad y no solo el
// colector que la formatea.
func TestObserveInference_TraduceElBloqueDeInferencia(t *testing.T) {
	t.Parallel()
	srv, st := srvConStats(t)
	srv.observeInference(ccDePrueba("s-1"), latidoConInferencia(&cloudlinkv1.SessionHealth{
		InferenceByRegime:     map[string]int64{"frio": 2, "caliente": 40},
		InferenceByClass:      map[string]int64{"interactivo": 30, "lote": 12},
		IntentOmittedByReason: map[string]int64{"sin_capacidad": 4},
		InferencePrefill:      &cloudlinkv1.InferenceLatency{P50Ms: 120, Samples: 42},
	}))

	ag := st.Agrega()
	if ag.PorRegimen["frio"] != 2 || ag.PorRegimen["caliente"] != 40 {
		t.Errorf("régimen = %v", ag.PorRegimen)
	}
	if ag.PorClase["interactivo"] != 30 || ag.PorClase["lote"] != 12 {
		t.Errorf("class = %v", ag.PorClase)
	}
	if ag.OmitidasPorMotivo["sin_capacidad"] != 4 {
		t.Errorf("omitidos = %v", ag.OmitidasPorMotivo)
	}
	if ag.MuestrasPrefill == nil || *ag.MuestrasPrefill != 42 {
		t.Errorf("muestras de prefill = %v, quiero 42 (el `n`, no el p50)", ag.MuestrasPrefill)
	}
	// La fase que NO viene se queda en «no medible», no en cero.
	if ag.MuestrasGeneracion != nil {
		t.Errorf("la generación no venía en el latido y salió %d: ausente no es cero",
			*ag.MuestrasGeneracion)
	}
}

// TestObserveInference_TRESTelefonosDeUnEDGE_NoTriplican es la trampa de esta tarea
// vista desde el Gateway, que es donde se elige la clave.
//
// 🔴 Los contadores los lleva el PROCESO del Edge, pero viajan en el latido de CADA
// sesión suya. Con tres teléfonos llegan tres latidos con LOS MISMOS totales; si la
// clave incluyera la sesión, la flota reportaría el triple de inferencias con una
// serie creíble, monótona y falsa por un factor igual al número de teléfonos.
//
// 🔬 MUTACIÓN: añadir `SessionID: cc.sessionID` a la inferstats.Clave ⇒ rojo (150).
func TestObserveInference_TRESTelefonosDeUnEDGE_NoTriplican(t *testing.T) {
	t.Parallel()
	srv, st := srvConStats(t)
	hb := latidoConInferencia(&cloudlinkv1.SessionHealth{
		InferenceByRegime: map[string]int64{"caliente": 50},
	})
	for _, sid := range []string{"s-1", "s-2", "s-3"} {
		cc := ccDePrueba(sid) // mismo tenant y mismo edge_id: un Edge, tres teléfonos
		srv.observeInference(cc, hb)
	}
	if got := st.Agrega().PorRegimen["caliente"]; got != 50 {
		t.Fatalf("inferencias = %d, quiero 50: tres teléfonos de UN Edge no son tres Edges", got)
	}
	if edges := st.Agrega().Edges; edges != 1 {
		t.Fatalf("Edges reportando = %d, quiero 1", edges)
	}
}

// TestObserveInference_NoSeCuelgaDeFleet: la recogida NO comparte guarda con
// persistHealth.
//
// 🔴 ES UN FALLO QUE HABRÍA SIDO MUDO. persistHealth se rinde sin repositorio de flota
// —no hay dónde durabilizar, y esa guarda es correcta para él—, pero un despliegue sin
// `fleet` sigue sirviendo /metrics: colgar la recogida de ahí la haría desaparecer sin
// un solo error, y el síntoma sería un /metrics con las series a cero.
//
// 🔬 MUTACIÓN: mover la llamada a observeInference DENTRO de persistHealth, debajo del
// `if s.fleet == nil` ⇒ rojo.
func TestObserveInference_NoSeCuelgaDeFleet(t *testing.T) {
	t.Parallel()
	srv, st := srvConStats(t) // SIN WithFleet
	if srv.fleet != nil {
		t.Fatal("este test necesita un Server sin fleet")
	}
	srv.observeInference(ccDePrueba("s-1"), latidoConInferencia(&cloudlinkv1.SessionHealth{
		InferenceByClass: map[string]int64{"lote": 7},
	}))
	if got := st.Agrega().PorClase["lote"]; got != 7 {
		t.Fatalf("sin fleet no se recogió nada (%d): /metrics se quedaría a cero sin un solo error", got)
	}
}

// TestObserveInference_SinSaludNiIdentidadNoRevienta: un Edge viejo (sin SessionHealth)
// y un stream sin identidad mTLS son estados NORMALES, no fallos.
func TestObserveInference_SinSaludNiIdentidadNoRevienta(t *testing.T) {
	t.Parallel()
	srv, st := srvConStats(t)
	srv.observeInference(ccDePrueba("s-1"), &cloudlinkv1.Heartbeat{}) // Edge viejo
	cc := ccDePrueba("s-2")
	cc.hasIdentity = false
	srv.observeInference(cc, latidoConInferencia(&cloudlinkv1.SessionHealth{
		InferenceByClass: map[string]int64{"lote": 1},
	}))
	if edges := st.Agrega().Edges; edges != 0 {
		t.Fatalf("se registraron %d Edges desde latidos que no traían nada que registrar", edges)
	}
}

// TestElLatidoALIMENTA_ElAlmacen cierra el agujero que ningún otro test de este
// fichero ve: que `observeInference` esté BIEN no sirve de nada si el latido no lo
// llama.
//
// 🔴 LA MUTACIÓN QUE LO PROVOCÓ: borrar la línea `s.observeInference(cc, hb)` del job
// del latido dejaba TODOS los demás tests en verde —llaman al método directamente— y
// el /metrics vacío en campo. Es la misma familia que el cable de `gw.OnWarmup`: un
// método probado sin consumidor.
//
// Por eso este test entra por `route`, que es la puerta real del frame, y no por el
// método. Espera al carril porque el trabajo del latido es asíncrono desde el
// Plan 050 · Ola 1.
func TestElLatidoALIMENTA_ElAlmacen(t *testing.T) {
	t.Parallel()
	srv, st := srvConStats(t)
	lane := newWorkLane(context.Background(), 8, 2*time.Second, laneLog())
	defer func() { lane.seal(); lane.drain(time.Second) }()

	srv.route(lane, ccDePrueba("s-1"), &cloudlinkv1.EdgeToCloud{
		SessionId: "s-1",
		Payload: &cloudlinkv1.EdgeToCloud_Heartbeat{Heartbeat: &cloudlinkv1.Heartbeat{
			LeaseCounter: 1,
			SessionHealth: &cloudlinkv1.SessionHealth{
				InferenceByRegime: map[string]int64{"frio": 11},
			},
		}},
	})

	limite := time.After(3 * time.Second)
	for st.Agrega().PorRegimen["frio"] != 11 {
		select {
		case <-limite:
			t.Fatalf("el latido no alimentó el almacén (frio=%d): el frame llega y se durabiliza, "+
				"pero /metrics se quedaría a cero sin un solo error",
				st.Agrega().PorRegimen["frio"])
		case <-time.After(5 * time.Millisecond):
		}
	}
}
