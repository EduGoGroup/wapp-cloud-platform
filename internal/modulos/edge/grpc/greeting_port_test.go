package grpc

// El puerto del saludo (sessionGreeter): propio del consumidor, no de fleet.Repository, y
// descubierto por ASERCIÓN DE TIPO sobre s.fleet. Trozo de greeting_test.go, partido por tema.

import (
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet/fleethelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// 🔴 LA GUARDA QUE IMPIDE QUE EL SALUDO SE APAGUE EN SILENCIO. Una aserción de tipo que falla
// no da error: entra por el camino del Debug y NO SALUDA A NADIE. El día que alguien le cambie
// la firma a PendingGreeting/MarkGreeted del adaptador Postgres —lo que el arranque monta de
// verdad— nada se pondría rojo, y el defecto solo se vería en el teléfono de un cliente que no
// recibe su aviso. Esta línea lo convierte en un fallo de COMPILACIÓN.
var _ sessionGreeter = (*fleet.PostgresRepository)(nil)

// El puerto es PROPIO del consumidor: fleet.Repository no declara ninguno de sus dos métodos,
// y por eso el gemelo en memoria de fleet no tiene que implementarlos. Si alguien los promueve
// al repositorio, este test avisa de que la aserción de tipo de greetIfNeeded sobra.
func TestSessionGreeterIsNotPartOfTheFleetRepository(t *testing.T) {
	t.Parallel()
	repository := reflect.TypeFor[fleet.Repository]()
	greeter := reflect.TypeFor[sessionGreeter]()
	if greeter.NumMethod() != 2 {
		t.Fatalf("sessionGreeter tiene %d métodos, se esperaban 2 (PendingGreeting y MarkGreeted)", greeter.NumMethod())
	}
	for i := range greeter.NumMethod() {
		name := greeter.Method(i).Name
		if _, found := repository.MethodByName(name); found {
			t.Errorf("fleet.Repository declara %s: el puerto del saludo dejó de ser propio del consumidor", name)
		}
	}
}

// hidingFleet envuelve un repositorio enseñando SOLO fleet.Repository: es el decorador que no
// reexpone el puerto del saludo, aunque lo que lleva dentro sí sepa saludar.
type hidingFleet struct{ fleet.Repository }

// Una flota que no cumple el puerto NO saluda NI rompe: el job del latido sigue, no se envía
// nada y queda UNA línea de Debug, que es lo único que delataría el saludo apagado.
func TestFleetWithoutTheGreeterPortNeitherGreetsNorBreaks(t *testing.T) {
	t.Parallel()
	inner := newGreeterFleet(ownNumber)
	cases := map[string]fleet.Repository{
		"in-memory fleet":               fleethelpertest.NewMemoria(),
		"decorator that hides the port": hidingFleet{inner},
	}
	for name, repo := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			log, buf := debugLog()
			reg := session.NewRegistry()
			srv := New(reg, log, WithFleet(repo))
			edge := &ackingEdge{srv: srv}
			t.Cleanup(reg.Register("s-1", edge))

			srv.greetIfNeeded(t.Context(), phone("tenant-1", "edge-1", "s-1"))

			if n := len(edge.sent()); n != 0 {
				t.Errorf("una flota sin el puerto envió %d avisos, se esperaba 0", n)
			}
			if got := inner.recorded(); len(got) != 0 {
				t.Errorf("se llamó al puerto escondido tras el decorador: %v", got)
			}
			requireLog(t, buf, "DEBUG",
				"saludo: el repositorio de flota no sabe marcar sesiones saludadas; no se avisa a nadie",
				"session_id=s-1", "edge_id=edge-1")
		})
	}
}
