// Copia de internal/bootstrap/arranque/orquestador.go @ 80807ba (F0 · 05 §6): cablea paquetes VIEJOS.
// Package arranque construye el proceso de la Plataforma Cloud: los ~60 objetos del
// monolito modular, en el orden exacto en que se necesitan, y los cuatro listeners.
//
// # ESTE FICHERO ES EL ÍNDICE DEL SISTEMA
//
// Para saber QUÉ EXISTE en la plataforma se lee la lista `fases` de más abajo y luego
// el fichero de la fase que interese. Hasta el 2026-09-04 la respuesta a esa pregunta
// eran las 990 líneas de una sola función (`bootstrap.Run`), que es la deuda D-10 de
// documentations/deuda.md.
//
// # LO QUE NO CAMBIÓ AL PARTIRLA
//
// Ni un objeto, ni un cable, ni el orden en que se construyen, ni —y esto importa más
// de lo que parece— CUÁL ES EL PRIMER ERROR QUE VE quien depura un arranque caído: las
// fases respetan la secuencia original de los constructores que pueden fallar. Un
// arranque con las credenciales de R2 mal puestas y el emisor JWT mal configurado a la
// vez sigue muriendo por donde moría antes.
package arranque

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/logging"
)

// fase es un tramo del arranque. Las nueve implementaciones son tipos vacíos: el
// estado vive en el contenedor, no en la fase, precisamente para que ninguna pueda
// guardarse algo por su cuenta y quedar como segunda fuente de verdad de nada.
type fase interface {
	// nombre identifica la fase en el log y en el error. Es lo que lee quien depura.
	nombre() string
	// requiere son los hitos que la fase da por cumplidos al empezar. Se comprueban
	// ANTES de ejecutarla: sin esto, adelantar una fase se manifiesta como un nil
	// pointer dentro de un constructor ajeno y a saber cuándo.
	requiere() []string
	// ejecutar construye lo que le toca sobre el contenedor y marca sus hitos.
	ejecutar(ctx context.Context, c *contenedor) error
}

// fases es EL ORDEN DEL ARRANQUE, y el orden ES el contrato.
//
// 🔴 NO SE REORDENA SIN LEER `requiere()` DE LAS QUE SE MUEVEN. Las dependencias que
// están declaradas se comprueban al arrancar; lo que no se puede declarar —el orden
// en que aparecen los errores de configuración, que es lo que ve quien depura— se
// respeta por convención y está documentado en cada fase.
var fases = []fase{
	faseInfraestructura{}, // BD y migraciones · PKI · lease · enrolamiento
	faseAutenticacion{},   // el plano de auth de usuario del IAM y el JWKS del Edge
	faseAlmacenes{},       // el cifrado de PII y los ~16 adaptadores de salida
	faseGateway{},         // el gateway gRPC que termina el túnel de cada Edge
	faseCaptacion{},       // el stack LLM P2→P5 que convierte una charla en presupuesto
	faseSolicitudes{},     // la bandeja del dueño y sus dos recordatorios perezosos
	faseFlujos{},          // el Motor de Flujos y los hooks que le cuelgan del gateway
	faseTransporte{},      // los cuatro listeners y sus rutas
	faseFondo{},           // las cinco goroutines de larga vida
}

// Ejecutar corre el ciclo de vida completo del servidor: carga de config,
// construcción de dependencias por fases, arranque de los cuatro listeners y espera
// de parada. Devuelve nil en shutdown limpio o el error del primer fallo fatal.
func Ejecutar(ctx context.Context) error {
	// El prólogo va fuera de las fases porque es lo que las hace posibles: sin config
	// no hay nada que construir, y sin logger un fallo de fase no tendría dónde
	// contarse. Son las dos únicas líneas del arranque que no pueden pertenecer a
	// ninguna fase.
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.New(cfg)

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	c := nuevoContenedor(cfg, log)
	// Cierra el pool de PostgreSQL pase lo que pase, incluida una fase que muera a
	// mitad. Antes de partir el arranque esto era un `defer closeDB(db, log)` a
	// veinte líneas de la apertura; ahora cubre las nueve fases igual.
	defer c.cerrar()

	if err := construir(ctx, c, fases); err != nil {
		return err
	}
	return servir(ctx, c)
}

// construir recorre las fases en orden, comprobando precondiciones y dejando en el log
// por dónde va. Se aborta en la primera que falle: un arranque a medias es peor que uno
// que no arranca, porque el segundo se nota.
//
// El plan entra por parámetro y no se lee de la global: es lo que permite que
// orquestador_test.go ejerza ESTE mecanismo —el corte en el primer error, la
// comprobación de precondiciones— con fases de prueba, en vez de comprobar una copia
// suya escrita a mano.
func construir(ctx context.Context, c *contenedor, plan []fase) error {
	for i, f := range plan {
		if pendiente := c.falta(f.requiere()); pendiente != "" {
			// Esto NO es un fallo de entorno: es un bug de esta lista. Se dice con esas
			// palabras para que nadie salga a mirar el .env.
			return fmt.Errorf("arranque: la fase %q exige el hito %q y ninguna fase anterior lo cumple "+
				"(bug de orden en la lista `fases` de orquestador.go, no un problema de configuración)",
				f.nombre(), pendiente)
		}
		inicio := time.Now()
		if err := f.ejecutar(ctx, c); err != nil {
			return fmt.Errorf("arranque: fase %d/%d %q: %w", i+1, len(plan), f.nombre(), err)
		}
		// 🔴 ESTA LÍNEA NO ES ADORNO, y es de lo poco que este refactor AÑADE. El
		// arranque hace llamadas de red (GCP KMS con WAPP_KEK_PROVIDER=kms, HeadBucket
		// contra R2, el JWKS de identity) y hasta hoy un arranque lento no decía en cuál
		// se estaba colgando: solo había silencio hasta el primer listener.
		//
		// ⚠️ En el VPS esta línea NO va a journald: la unidad escribe a `cloud.log`
		// (StandardOutput=append:), así que se busca ahí y no con `journalctl`.
		c.log.Info("arranque: fase completada",
			"fase", fmt.Sprintf("%d/%d", i+1, len(plan)),
			"nombre", f.nombre(),
			"ms", time.Since(inicio).Milliseconds())
	}
	return nil
}
