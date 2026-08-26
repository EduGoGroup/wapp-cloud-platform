package turnoacotado_test

// troceado_test.go — EL CONTADOR DE LLAMADAS Y LOS DOS FRENOS
// (Plan 044 · Ola 3.5 · T3.5-3).
//
// El criterio de la tarea pide un contador de llamadas, y este es su sitio: aquí es
// donde se hacen. El precedente literal es TestP3_TresItems_TresLlamadas
// (intake/stages/p3_test.go:288), que cuenta las llamadas de un fake y las acompaña
// de las dos aserciones que hacen que el número signifique algo — cada llamada lleva
// SU trozo, y ninguna lleva los demás—. Sin ellas, «3 llamadas» sería compatible con
// mandar el turno entero tres veces.
//
// 🔴 Sin red, como todo este paquete.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/turnoacotado"
)

// turneroContador es el doble que CUENTA: guarda cada petición en orden, contesta lo
// que se le diga por llamada y puede tardar. Es distinto del turneroFake de al lado
// porque aquí lo interesante es la SECUENCIA, no la única petición vista.
type turneroContador struct {
	respuestas []string // una por llamada; agotadas ⇒ se repite la última
	err        error    // si != nil, falla a partir de la llamada `fallaEn`
	fallaEn    int
	tarda      time.Duration
	vistas     []llmvia.TurnoRequest
}

func (f *turneroContador) Turno(_ context.Context, _, _ string, t llmvia.TurnoRequest) (string, error) {
	f.vistas = append(f.vistas, t)
	if f.tarda > 0 {
		time.Sleep(f.tarda)
	}
	if f.err != nil && len(f.vistas) >= f.fallaEn {
		return "", f.err
	}
	i := len(f.vistas) - 1
	if i >= len(f.respuestas) {
		i = len(f.respuestas) - 1
	}
	return f.respuestas[i], nil
}

func resolutorC(t *testing.T, f *turneroContador) *turnoacotado.Resolver {
	t.Helper()
	r, err := turnoacotado.New(f)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

// troceada arma la consulta CON TROZOS que el carrito eleva: las opciones son el
// catálogo aplanado (posición → etiqueta) y los trozos, lo que la cascada no casó.
func troceada(trozos ...string) modules.Consulta {
	return modules.Consulta{
		Clase: modules.ClaseOpcion, Nivel: "categories", Trozos: trozos,
		Texto: strings.Join(trozos, " y "),
		Opciones: []modules.OpcionConsulta{
			{Codigo: "0", Etiqueta: "Pizzas"},
			{Codigo: "1", Etiqueta: "Hamburguesas"},
			{Codigo: "2", Etiqueta: "Empanadas"},
			{Codigo: "3", Etiqueta: "Jugos"},
		},
	}
}

// eligeLaOpcion es la respuesta cruda de un modelo que eligió la posición n.
func eligeLaOpcion(n int) string {
	return `{"usable": true, "value": ` + strconv.Itoa(n) + `, "reason": "ok"}`
}

// ---------------------------------------------------------------------------
// EL CRITERIO: una llamada CHICA por trozo, jamás una con los N dentro
// ---------------------------------------------------------------------------

// TestTroceado_UnaLlamadaPorTrozo es el contador del criterio de T3.5-3, con las dos
// aserciones que lo hacen significar algo.
//
// 💥 MUTACIONES EJECUTADAS (las dos rojas):
//   - en `resolverTrozos`, `break` al final de la primera vuelta ⇒ llamadas = 1.
//   - recorrer `for i := range c.Trozos` y construir la sub-consulta con
//     `Texto: c.Texto` (el turno ENTERO) en vez del trozo ⇒ las tres aserciones de
//     «cada llamada lleva SU trozo» caen. Es exactamente la llamada monstruo que la
//     medición de campo refutó, hecha tres veces. (Se muta el bucle a la vez porque
//     con `Texto: c.Texto` a secas `trozo` queda sin usar y la mutación NO compila —
//     y una mutación que no compila prueba el sistema de tipos, no el test).
func TestTroceado_UnaLlamadaPorTrozo(t *testing.T) {
	f := &turneroContador{respuestas: []string{eligeLaOpcion(1), eligeLaOpcion(3), eligeLaOpcion(4)}}
	c := troceada("napolitanas", "completos", "gaseosas")

	v, err := resolutorC(t, f).ResolverConsulta(context.Background(), "t1", "s1", c)
	if err != nil {
		t.Fatalf("ResolverConsulta: %v", err)
	}
	if len(f.vistas) != 3 {
		t.Fatalf("llamadas al modelo = %d, se esperaban 3 (UNA por trozo)", len(f.vistas))
	}
	for i, trozo := range c.Trozos {
		mio := `Respuesta del cliente: "` + trozo + `"`
		if !strings.Contains(f.vistas[i].Prompt, mio) {
			t.Errorf("la llamada %d no pregunta por su trozo %q", i+1, trozo)
		}
		for j, otro := range c.Trozos {
			if j == i {
				continue
			}
			if strings.Contains(f.vistas[i].Prompt, `Respuesta del cliente: "`+otro+`"`) {
				t.Errorf("la llamada %d lleva ADEMÁS el trozo %q: eso es la llamada monstruo", i+1, otro)
			}
		}
	}
	quiero := []string{"0", "2", "3"}
	if strings.Join(v.Codigos, "|") != strings.Join(quiero, "|") {
		t.Fatalf("Codigos = %v, quiero %v", v.Codigos, quiero)
	}
	if v.Codigo != "" {
		t.Fatalf("una consulta troceada no puede devolver un Codigo único: %q", v.Codigo)
	}
}

// ---------------------------------------------------------------------------
// FRENO 1 · el tope de llamadas
// ---------------------------------------------------------------------------

// TestTroceado_ElTopeCortaLasLlamadas fija que el turno no gasta más de
// MaxLlamadasPorTurno inferencias, pase lo que pase, y que lo que se queda fuera sale
// como código VACÍO —o sea, visible— en vez de desaparecer.
func TestTroceado_ElTopeCortaLasLlamadas(t *testing.T) {
	f := &turneroContador{respuestas: []string{eligeLaOpcion(1)}}
	c := troceada("uno", "dos", "tres", "cuatro", "cinco")

	v, err := resolutorC(t, f).ResolverConsulta(context.Background(), "t1", "s1", c)
	if err != nil {
		t.Fatalf("ResolverConsulta: %v", err)
	}
	if len(f.vistas) != turnoacotado.MaxLlamadasPorTurno {
		t.Fatalf("llamadas = %d, el tope es %d", len(f.vistas), turnoacotado.MaxLlamadasPorTurno)
	}
	if len(v.Codigos) != len(c.Trozos) {
		t.Fatalf("Codigos = %d, tiene que venir alineado con los %d trozos", len(v.Codigos), len(c.Trozos))
	}
	for i := turnoacotado.MaxLlamadasPorTurno; i < len(v.Codigos); i++ {
		if v.Codigos[i] != "" {
			t.Fatalf("el trozo %d pasó del tope y trae código %q", i, v.Codigos[i])
		}
	}
}

// ---------------------------------------------------------------------------
// FRENO 2 · el presupuesto del turno
// ---------------------------------------------------------------------------

// TestTroceado_PresupuestoAgotado_ConservaLoYaResuelto es la respuesta a «¿qué pasa
// si el presupuesto se acaba a mitad?»: lo ya resuelto NO se pierde, y la llamada que
// no cabe NI SE ARRANCA.
//
// El ctx del llamante trae poco más que el suelo de una llamada, así que la primera
// entra y la segunda ya no: es la misma aritmética de producción con los números
// escalados para que el test dure milisegundos y no veinte segundos.
func TestTroceado_PresupuestoAgotado_ConservaLoYaResuelto(t *testing.T) {
	// Los márgenes son ANCHOS a los dos lados a propósito (300 ms antes de la primera
	// llamada, 100 ms después de la segunda comprobación): un test de reloj con
	// márgenes de decenas de milisegundos es un flake esperando a una máquina cargada,
	// y aquí lo que se afirma es la DECISIÓN, no la precisión del cronómetro.
	f := &turneroContador{respuestas: []string{eligeLaOpcion(1)}, tarda: 400 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), turnoacotado.SueloPorLlamada+300*time.Millisecond)
	defer cancel()

	v, err := resolutorC(t, f).ResolverConsulta(ctx, "t1", "s1", troceada("napolitanas", "completos"))
	if err != nil {
		t.Fatalf("ResolverConsulta: %v", err)
	}
	if len(f.vistas) != 1 {
		t.Fatalf("llamadas = %d, quiero 1 (la segunda no cabía y no debía arrancarse)", len(f.vistas))
	}
	if v.Codigos[0] != "0" || v.Codigos[1] != "" {
		t.Fatalf("Codigos = %v, quiero conservar la primera y dejar vacía la segunda", v.Codigos)
	}
	if !v.ResueltoAlguno() {
		t.Fatal("un troceado parcial tiene que contar como resuelto: si no, el engine lo tira")
	}
}

// ---------------------------------------------------------------------------
// El fallo de la vía a mitad, y en la primera
// ---------------------------------------------------------------------------

// TestTroceado_FalloAMitad_DevuelveLoParcial fija la mitad menos obvia de la política:
// una vía que se cae en la segunda llamada NO invalida la primera. Esa inferencia ya
// se pagó con la plaza única del Edge y el pedido de la clienta la merece.
func TestTroceado_FalloAMitad_DevuelveLoParcial(t *testing.T) {
	f := &turneroContador{
		respuestas: []string{eligeLaOpcion(1)},
		err:        errors.New("edge sin capacidad"), fallaEn: 2,
	}
	v, err := resolutorC(t, f).ResolverConsulta(context.Background(), "t1", "s1", troceada("napolitanas", "completos"))
	if err != nil {
		t.Fatalf("el fallo a mitad no puede propagarse habiendo trabajo resuelto: %v", err)
	}
	if v.Codigos[0] != "0" {
		t.Fatalf("se tiró la llamada que SÍ había contestado: %v", v.Codigos)
	}
	if v.Motivo != modules.MotivoFallo {
		t.Fatalf("Motivo = %q, quiero %q para que el desenlace no mienta", v.Motivo, modules.MotivoFallo)
	}
}

// TestTroceado_FalloEnLaPrimera_SePropaga fija el otro lado: sin nada que salvar, el
// troceado se comporta EXACTAMENTE como el turno de un solo texto —error hacia
// arriba, que es lo que dispara DesenlaceFallo y el aviso al dueño—.
func TestTroceado_FalloEnLaPrimera_SePropaga(t *testing.T) {
	fallo := errors.New("edge sin capacidad")
	f := &turneroContador{respuestas: []string{""}, err: fallo, fallaEn: 1}
	_, err := resolutorC(t, f).ResolverConsulta(context.Background(), "t1", "s1", troceada("napolitanas", "completos"))
	if !errors.Is(err, fallo) {
		t.Fatalf("err = %v, quiero el fallo de la vía intacto", err)
	}
}

// TestTroceado_ViaAPI_NoEsUnaAveria fija que un tenant en vía API no genera ni un
// error ni un aviso al dueño por trocear: para él este escalón no existe y su turno
// cae al Nivel A de siempre.
func TestTroceado_ViaAPI_NoEsUnaAveria(t *testing.T) {
	f := &turneroContador{respuestas: []string{""}, err: llmvia.ErrViaSinTurnoAcotado, fallaEn: 1}
	v, err := resolutorC(t, f).ResolverConsulta(context.Background(), "t1", "s1", troceada("napolitanas", "completos"))
	if err != nil {
		t.Fatalf("la vía API no es una avería: %v", err)
	}
	if v.Motivo != modules.MotivoSinResolutor {
		t.Fatalf("Motivo = %q, quiero %q", v.Motivo, modules.MotivoSinResolutor)
	}
}

// TestTroceado_SinTrozos_ElCaminoDeSiempre es la regresión cero del contrato aditivo:
// una consulta sin Trozos hace UNA llamada y devuelve un Codigo único, byte a byte
// como antes de esta tarea.
func TestTroceado_SinTrozos_ElCaminoDeSiempre(t *testing.T) {
	f := &turneroContador{respuestas: []string{eligeLaOpcion(2)}}
	c := troceada()
	c.Trozos, c.Texto = nil, "quiero el segundo"

	v, err := resolutorC(t, f).ResolverConsulta(context.Background(), "t1", "s1", c)
	if err != nil {
		t.Fatalf("ResolverConsulta: %v", err)
	}
	if len(f.vistas) != 1 || v.Codigo != "1" || v.Codigos != nil {
		t.Fatalf("llamadas=%d Codigo=%q Codigos=%v, quiero 1 llamada y el camino de siempre",
			len(f.vistas), v.Codigo, v.Codigos)
	}
}
