package llmvia_test

// turno_test.go — EL TURNO ACOTADO DEL NIVEL B (Plan 044 · Ola 3.5 · T3.5-2).
//
// Los tests corren SIN RED: el transporte es un doble (frameFake, selector_test.go)
// y no se levanta ningún Ollama. Lo que se ejerce aquí es exactamente lo que este
// método decide —la vía, el plazo, los campos del frame y el paso por el avisador—,
// que es todo lo que le toca decidir.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/degradation"
	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantllm"
)

// contadorFake recoge lo que sale por el observador de degradaciones.
type contadorFake struct{ filas [][3]string }

func (c *contadorFake) cuenta(origen, via, reason string) {
	c.filas = append(c.filas, [3]string{origen, via, reason})
}

func peticion() llmvia.TurnoRequest {
	return llmvia.TurnoRequest{Prompt: "instrucciones + ejemplos + caso", Formato: `{"type":"object"}`}
}

// TestTurno_ArmaElFrameConLosParametrosMEDIDOS: los cinco campos que este método
// pone en el frame no son gustos, son la medición del 2026-08-26 contra Ollama real,
// y cada uno protege algo distinto:
//
//	Timeout = 12 s          → 0,8 × 12 s = 9,6 s queda POR ENCIMA del peor caso
//	                          caliente medido (7,9 s), así que las respuestas SANAS
//	                          no envenenan el breaker del tenant —que es COMPARTIDO
//	                          con el pipeline—; y a la vez corta el caso frío (18 s).
//	Temperature = 0         → esto elige entre opciones que ya existen, no redacta.
//	Format = el esquema     → un JSON Schema, no la cadena "json": el Edge lo
//	                          distingue por su primer byte y lo manda verbatim.
//	MaxOutputTokens = 128   → la salida real son 18-20 tokens; el techo acota al
//	                          modelo degenerado sin estorbar al legítimo.
//	Class = interactivo     → SOLO rótulo, pero es el que separa en el parte lo que
//	                          alguien estaba esperando de lo que corría de fondo.
//
// 🔬 MUTACIÓN: bajar PlazoTurno a 5 s ⇒ rojo. Y en campo: el umbral de lentitud
// caería a 4 s y el Edge marcaría lentas casi todas las respuestas buenas del VPS.
func TestTurno_ArmaElFrameConLosParametrosMEDIDOS(t *testing.T) {
	t.Parallel()
	f := &frameFake{out: `{"usable":true,"value":2,"reason":"ok"}`}
	sel := selector(t, &storeFake{}, llmvia.WithFrame(f))

	raw, err := sel.Turno(context.Background(), "tenant-1", "sesion-7", peticion())
	if err != nil {
		t.Fatalf("Turno: %v", err)
	}
	if raw != f.out {
		t.Fatalf("raw = %q: el texto del modelo se devuelve SIN interpretar", raw)
	}
	if f.visto.Timeout != 12*time.Second {
		t.Errorf("timeout_ms = %v, quiero 12s: con menos, 0,8×timeout deja el umbral de "+
			"lentitud del breaker POR DEBAJO del peor caso sano medido (7,9 s)", f.visto.Timeout)
	}
	if f.visto.Temperature != 0 {
		t.Errorf("temperature = %v, quiero 0", f.visto.Temperature)
	}
	if f.visto.Format != peticion().Formato || f.visto.Format == "json" {
		t.Errorf("format = %q, quiero el JSON Schema serializado tal cual", f.visto.Format)
	}
	if f.visto.MaxOutputTokens != 128 {
		t.Errorf("max_output_tokens = %d, quiero 128", f.visto.MaxOutputTokens)
	}
	if f.visto.Class != gatewaygrpc.ClaseInteractivo {
		t.Errorf("class = %q, quiero %q", f.visto.Class, gatewaygrpc.ClaseInteractivo)
	}
	if f.visto.OriginSessionID != "sesion-7" {
		t.Errorf("origin_session_id = %q: la pregunta tiene que salir por el Edge que "+
			"atendió a ESTA persona, que es el que tiene su prefijo caliente", f.visto.OriginSessionID)
	}
	if f.visto.Warmup {
		t.Error("un turno acotado NO es un calentamiento: alguien está esperando su respuesta")
	}
}

// TestTurno_NoSeQuedaSinPresupuestoConUnCtxDeLaMedidaDelPlazo es la regresión del
// motivo por el que este método existe en vez de entrar por local.Provider.run.
//
// Aquel descuenta MargenVeredicto (7 s) del deadline del llamante SIEMPRE: con un
// ctx de 12 s dejaría 5, y con uno de 5 devolvería ErrSinPresupuesto sin tocar el
// cable. Aquí el margen se SUMA a nuestra espera en vez de restarse del plazo del
// Edge, así que un llamante con 12 s exactos sigue teniendo su turno entero.
func TestTurno_NoSeQuedaSinPresupuestoConUnCtxDeLaMedidaDelPlazo(t *testing.T) {
	t.Parallel()
	f := &frameFake{out: "{}"}
	sel := selector(t, &storeFake{}, llmvia.WithFrame(f))

	ctx, cancel := context.WithTimeout(context.Background(), llmvia.PlazoTurno)
	defer cancel()
	if _, err := sel.Turno(ctx, "tenant-1", "s-1", peticion()); err != nil {
		t.Fatalf("Turno con un ctx de %v: %v", llmvia.PlazoTurno, err)
	}
	if f.visto.Timeout != llmvia.PlazoTurno {
		t.Fatalf("timeout_ms = %v, quiero el plazo entero (%v): el margen del veredicto no "+
			"se resta del presupuesto del Edge, se suma a lo que esperamos nosotros",
			f.visto.Timeout, llmvia.PlazoTurno)
	}
}

// TestTurno_LaViaAPINoSirveElTurnoYNoTocaElCable: el tenant en vía API se queda sin
// ESTE escalón —su carrito sigue funcionando, con el reprompt de siempre— y sobre
// todo NO se le manda la pregunta a un Edge que no es suyo. El error es NOMBRADO
// para que quien lo reciba pueda decir «aquí no hay nada que hacer» en vez de
// «falló», que es lo que distingue una degradación de una avería.
func TestTurno_LaViaAPINoSirveElTurnoYNoTocaElCable(t *testing.T) {
	t.Parallel()
	f := &frameFake{out: "{}"}
	avisos := &avisoFake{}
	cnt := &contadorFake{}
	sel := selector(t, &storeFake{fila: tenantllm.Config{Via: tenantllm.ViaAPI, Provider: "anthropic", Model: "m"}, hay: true},
		llmvia.WithFrame(f), llmvia.WithNotifier(avisos), llmvia.WithDegradacionObservada(cnt.cuenta))

	_, err := sel.Turno(context.Background(), "tenant-api", "s-1", peticion())
	if !errors.Is(err, llmvia.ErrViaSinTurnoAcotado) {
		t.Fatalf("err = %v, quiero ErrViaSinTurnoAcotado", err)
	}
	if f.visto.Prompt != "" {
		t.Error("se mandó el frame por la vía local a un tenant que está en vía API")
	}
	if avisos.n != 0 || len(cnt.filas) != 0 {
		t.Errorf("una vía que no sirve este escalón NO es una degradación del equipo del "+
			"dueño: avisos=%d, contadas=%v", avisos.n, cnt.filas)
	}
}

// TestTurno_UnFalloDeLaViaAvisaAlDuenoYCuentaComoCaidaANivelA es el corazón del
// encargo: armar el frame por nuestra cuenta NO puede saltarse el envoltorio que
// avisa al dueño (ADR-0044 §5). Un Ollama caído tiene que llegarle al cliente igual
// lo pida el presupuesto que lo pida el carrito.
//
// Y la MISMA llamada produce el dato de campo que desbloquea el desalojo (D-044.41),
// con el origen que lo hace legible: `turno` es, por construcción, alguien esperando
// delante del teléfono.
//
// 🔬 MUTACIÓN: quitar la línea `s.avisar(...)` de Turno ⇒ rojo por los dos lados.
func TestTurno_UnFalloDeLaViaAvisaAlDuenoYCuentaComoCaidaANivelA(t *testing.T) {
	t.Parallel()
	avisos := &avisoFake{}
	cnt := &contadorFake{}
	sel := selector(t, &storeFake{}, llmvia.WithFrame(&frameFake{err: transporteError{m: "timeout"}}),
		llmvia.WithNotifier(avisos), llmvia.WithDegradacionObservada(cnt.cuenta))

	if _, err := sel.Turno(context.Background(), "tenant-1", "s-1", peticion()); err == nil {
		t.Fatal("quiero el error del transporte")
	}
	if avisos.n != 1 || avisos.reason != degradation.ReasonTimeout || avisos.via != tenantllm.ViaLocal {
		t.Errorf("aviso = {n:%d reason:%q via:%q}, quiero UNO con motivo timeout por la vía local",
			avisos.n, avisos.reason, avisos.via)
	}
	quiero := [3]string{llmvia.OrigenTurno, tenantllm.ViaLocal, string(degradation.ReasonTimeout)}
	if len(cnt.filas) != 1 || cnt.filas[0] != quiero {
		t.Errorf("contadas = %v, quiero exactamente %v", cnt.filas, quiero)
	}
}

// TestDegradacion_SeCuentaAUNQUENoHayaNotificador custodia una colocación, y la
// colocación es la decisión: el contador va ANTES del `if s.notifier == nil`.
//
// Colgarlo del notificador ataría el dato que desbloquea D-044.41 a que haya base de
// datos cableada, y encima lo dejaría SUBCONTADO por el dedupe de la tabla: diez
// timeouts de la misma ventana escriben UN aviso y son DIEZ caídas a Nivel A. Son
// dos preguntas distintas —«qué le cuento al dueño» y «cuánto pasa»— y se responden
// por separado.
//
// 🔬 MUTACIÓN: mover s.contarDegradacion por debajo del `if s.notifier == nil` ⇒ rojo.
func TestDegradacion_SeCuentaAUNQUENoHayaNotificador(t *testing.T) {
	t.Parallel()
	cnt := &contadorFake{}
	sel := selector(t, &storeFake{}, llmvia.WithFrame(&frameFake{err: transporteError{m: "ollama_down"}}),
		llmvia.WithDegradacionObservada(cnt.cuenta)) // SIN notifier, a propósito

	if _, err := sel.Turno(context.Background(), "tenant-1", "s-1", peticion()); err == nil {
		t.Fatal("quiero el error del transporte")
	}
	if len(cnt.filas) != 1 || cnt.filas[0][2] != string(degradation.ReasonOllamaDown) {
		t.Fatalf("contadas = %v, quiero una fila con motivo ollama_down aunque no haya tabla", cnt.filas)
	}
}

// TestDegradacion_LoQueNoTIENEMotivoNoSeCuenta: la misma regla dura de motivoDe
// («lo que no mapea, no avisa») gobierna el contador, y por el mismo motivo. Un
// fallo de CALIDAD —el modelo respondió y su salida no era interpretable— no es una
// degradación de la vía: el equipo del cliente está perfectamente. Si contara aquí,
// la serie que tiene que responder «¿un lote está ahogando los turnos?» se llenaría
// de ruido que no habla de eso.
func TestDegradacion_LoQueNoTIENEMotivoNoSeCuenta(t *testing.T) {
	t.Parallel()
	cnt := &contadorFake{}
	sel := selector(t, &storeFake{}, llmvia.WithFrame(&frameFake{err: errors.New("json roto")}),
		llmvia.WithDegradacionObservada(cnt.cuenta))

	if _, err := sel.Turno(context.Background(), "tenant-1", "s-1", peticion()); err == nil {
		t.Fatal("quiero el error")
	}
	if len(cnt.filas) != 0 {
		t.Fatalf("contadas = %v: un error sin motivo del vocabulario cerrado no es una caída "+
			"a Nivel A de la vía y no puede ensuciar la serie", cnt.filas)
	}
}

// TestTurno_SinCableFallaAlPrimerTurnoYConElErrorDeSIEMPRE: un selector sin frame es
// un bug de arranque. Se dice con el vocabulario que ya existe (local.ErrSinTransporte)
// en vez de estrenar un tercer nombre para el mismo problema.
func TestTurno_SinCableFallaAlPrimerTurnoYConElErrorDeSIEMPRE(t *testing.T) {
	t.Parallel()
	sel := selector(t, &storeFake{})
	_, err := sel.Turno(context.Background(), "tenant-1", "s-1", peticion())
	if err == nil || !strings.Contains(err.Error(), "transporte") {
		t.Fatalf("err = %v, quiero el error de «sin transporte»", err)
	}
}
