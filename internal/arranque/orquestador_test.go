// Copia de internal/bootstrap/arranque/orquestador_test.go @ 80807ba (F0 · 05 §6). Desde F8
// (conmutar(conversacion)) el arranque que este test ejercita no cablea ningún paquete viejo.
package arranque

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/logging"
)

// faseDePrueba deja constancia de si corrió, para poder afirmar que una fase posterior
// a un fallo NO se ejecuta.
type faseDePrueba struct {
	id      string
	exige   []string
	entrega []string
	falla   error
	corrio  *bool
}

func (f faseDePrueba) nombre() string     { return f.id }
func (f faseDePrueba) requiere() []string { return f.exige }

func (f faseDePrueba) ejecutar(_ context.Context, c *contenedor) error {
	if f.corrio != nil {
		*f.corrio = true
	}
	if f.falla != nil {
		return f.falla
	}
	c.marca(f.entrega...)
	return nil
}

func contenedorDePrueba(t *testing.T) *contenedor {
	t.Helper()
	return nuevoContenedor(config.AppConfig{}, logging.New(config.AppConfig{}))
}

// Ejecutar no se ejecuta aquí (exige Postgres y R2: lo cubren la huella y F9); esta línea
// fija su firma y es la mención que pide exportados_cubiertos (05 E-9) en el arranque
// nuevo. Única línea que la copia añade al test viejo (F0 · diseno.md §5.2).
var _ func(context.Context) error = Ejecutar

// TestConstruir_AbortaEnElPrimerFalloYNoSigue es el invariante de fail-fast del
// arranque: un servidor a medias que acepta tráfico es peor que uno que no levanta,
// porque el segundo se nota. Si `construir` siguiera adelante tras un fallo, el proceso
// llegaría a abrir listeners con medio grafo de objetos a nil.
func TestConstruir_AbortaEnElPrimerFalloYNoSigue(t *testing.T) {
	var corrioLaSiguiente bool
	roto := errors.New("la base no responde")

	err := construir(context.Background(), contenedorDePrueba(t), []fase{
		faseDePrueba{id: "primera", entrega: []string{"db"}},
		faseDePrueba{id: "la que falla", falla: roto},
		faseDePrueba{id: "la de después", corrio: &corrioLaSiguiente},
	})

	if !errors.Is(err, roto) {
		t.Fatalf("construir devolvió %v; quiero que envuelva %v — el error de la fase tiene "+
			"que llegar íntegro al llamante o el operador depura a ciegas", err, roto)
	}
	if !strings.Contains(err.Error(), "la que falla") {
		t.Errorf("el error no nombra la fase que falló (%q): es lo primero que se lee al "+
			"depurar un arranque caído", err)
	}
	if !strings.Contains(err.Error(), "2/3") {
		t.Errorf("el error no dice por qué fase iba (%q): sin el ordinal no se sabe cuánto "+
			"del arranque llegó a correr", err)
	}
	if corrioLaSiguiente {
		t.Error("una fase POSTERIOR al fallo se ejecutó: el arranque tiene que cortar en seco. " +
			"Seguir construiría objetos sobre un grafo con agujeros y los listeners acabarían " +
			"abiertos sobre medio sistema.")
	}
}

// TestConstruir_UnaPrecondicionQueNadieCumpleAbortaElArranque acredita que declarar un
// hito en `requiere()` sirve de algo.
//
// 🔴 ES LO QUE CONVIERTE EL ORDEN DE `fases` EN UN CONTRATO. Hasta que el arranque se
// partió en fases, el orden solo estaba escrito en comentarios («adelantarlo es seguro:
// solo depende de ctx/cfg/db»). Sin esta comprobación, adelantar una fase se manifiesta
// como un nil pointer DENTRO de un constructor ajeno, con una traza que apunta a un
// paquete que no tiene la culpa.
func TestConstruir_UnaPrecondicionQueNadieCumpleAbortaElArranque(t *testing.T) {
	var corrio bool

	err := construir(context.Background(), contenedorDePrueba(t), []fase{
		faseDePrueba{id: "la adelantada", exige: []string{"gateway"}, corrio: &corrio},
		faseDePrueba{id: "la que lo entregaba", entrega: []string{"gateway"}},
	})

	if err == nil {
		t.Fatal("una fase que exige un hito que ninguna anterior cumple arrancó igual: la " +
			"declaración de `requiere()` no la comprueba nadie y es decorativa")
	}
	if corrio {
		t.Error("la fase se ejecutó pese a la precondición incumplida: se comprueba DESPUÉS " +
			"de correr, que es tarde")
	}
	// El mensaje tiene que mandar a mirar el código y no el entorno: confundir un bug de
	// orden con un problema de configuración cuesta una tarde de .env.
	if !strings.Contains(err.Error(), "gateway") || !strings.Contains(err.Error(), "orden") {
		t.Errorf("el error (%q) no nombra el hito que falta ni dice que es un bug de orden", err)
	}
}

// TestConstruir_ElOrdenDeclaradoSatisfaceTodasLasPrecondiciones recorre la lista REAL de
// fases con un contenedor vacío y comprueba que ninguna exige algo que no esté ya
// cumplido.
//
// No ejecuta ninguna fase de verdad —eso necesitaría Postgres, R2 y el gateway vivos—:
// lo que verifica es la CONSISTENCIA de las declaraciones, que es justo lo que un
// reordenamiento rompe. Como cada fase declara `requiere()` pero sus hitos se marcan
// dentro de `ejecutar`, aquí se dan por cumplidos los de las anteriores; el test no
// puede decir que una fase entrega lo que promete, pero sí que nadie pide algo que se
// entrega DESPUÉS.
func TestConstruir_ElOrdenDeclaradoSatisfaceTodasLasPrecondiciones(t *testing.T) {
	c := contenedorDePrueba(t)
	// Los hitos que produce cada fase, en el mismo orden que `fases`. Se escriben aquí
	// porque en producción los marca `ejecutar`, que no se puede correr en un test.
	// 🔴 SI AÑADES UNA FASE, AÑADE SU FILA: una fila de menos deja el test verde y sin
	// mirar la fase nueva.
	entregas := [][]string{
		{"db", "metricas", "pki", "lease", "enroll"}, // infraestructura
		{"auth"},                  // autenticación
		{"cipher", "almacenes"},   // almacenes
		{"gateway"},               // gateway
		{"selector", "captacion"}, // captación
		{"solicitudes"},           // solicitudes
		{"flujos"},                // flujos
		{"transporte"},            // transporte
		{},                        // fondo (no entrega nada: es la última)
	}
	if len(entregas) != len(fases) {
		t.Fatalf("hay %d fases y %d filas de entregas: falta actualizar este test al añadir "+
			"o quitar una fase", len(fases), len(entregas))
	}

	for i, f := range fases {
		if pendiente := c.falta(f.requiere()); pendiente != "" {
			t.Errorf("la fase %d %q exige el hito %q y ninguna anterior lo entrega: el orden de "+
				"`fases` en orquestador.go está mal", i+1, f.nombre(), pendiente)
		}
		c.marca(entregas[i]...)
	}
}

// TestFases_NombresUnicosYNoVacios: el nombre es lo único que el operador ve en el log
// y en el error. Dos fases con el mismo nombre mandan a depurar la equivocada.
func TestFases_NombresUnicosYNoVacios(t *testing.T) {
	vistos := map[string]int{}
	for i, f := range fases {
		n := f.nombre()
		if strings.TrimSpace(n) == "" {
			t.Errorf("la fase %d no tiene nombre: su error saldría como `fase %d/%d \"\"`", i+1, i+1, len(fases))
		}
		if antes, repetido := vistos[n]; repetido {
			t.Errorf("las fases %d y %d se llaman igual (%q): un fallo mandaría a depurar la que no es",
				antes+1, i+1, n)
		}
		vistos[n] = i
	}
}
