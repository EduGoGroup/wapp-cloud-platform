// Command casebank siembra casos en el BANCO DE CASOS del pipeline de captación
// (`intake_case_bank`, Plan 044 · Ola 5 · T5.3) y sale. Nada más: no abre
// listeners, no toca WhatsApp y no aplica migraciones.
//
// # POR QUÉ ES UN BINARIO Y NO UN PASO DEL ARRANQUE, NI UN `INSERT` EN LA MIGRACIÓN
//
// Porque toda fila del banco lleva `tenant_id` y `consented`, y ninguna de las
// otras dos vías puede afirmar honestamente ese par:
//
//   - un INSERT en la migración metería la fila en TODAS las bases, con un tenant
//     inventado y un consentimiento que nadie dio (por eso la 0082 no siembra);
//   - un paso del arranque haría lo mismo cada vez que sube el servidor, y encima
//     en el momento en que menos se mira.
//
// La siembra es un ACTO DE OPERADOR, y ese acto ES el registro del
// consentimiento: quien la ejecuta está afirmando que el tenant consintió.
//
// Uso:
//
//	casebank -tenant t-xxxx -consentido       siembra el caso Ambar para ese tenant
//	casebank -tenant t-xxxx                   se niega, y dice por qué
//
// La configuración es la MISMA que la del servidor (config.Load, prefijo WAPP_):
// la cadena de conexión sale de las variables de entorno, nunca de un argumento,
// para que no haya dos formas de decir a qué base se apunta (mismo criterio que
// cmd/migrate).
//
// ⚠️ NO aplica migraciones: si `intake_case_bank` no existe, falla y hay que
// correr `migrate` antes. Es deliberado — un sembrador que además migre acabaría
// usándose para migrar.
//
// 🔴 EL CASO QUE SIEMBRA HOY ES CALIDAD C: texto REDACTADO, no la transcripción
// real del cliente. La propia fila lo declara en `expected._procedencia`. Ver
// internal/casebank/semilla.go.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	stdlog "log"
	"os/signal"
	"syscall"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/casebank"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

func main() {
	tenant := flag.String("tenant", "", "tenant dueño del caso (obligatorio)")
	consentido := flag.Bool("consentido", false,
		"afirma que ESE tenant consintió conservar este texto como material de evaluación")
	flag.Parse()

	if err := run(*tenant, *consentido); err != nil {
		stdlog.Fatalf("casebank: %v", err)
	}
}

// errSinTenant es el rechazo del único argumento obligatorio. Es una variable y
// no un `errors.New` inline para que el test pueda nombrarlo con `errors.Is` en
// vez de comparar el texto del mensaje.
var errSinTenant = errors.New("falta -tenant: un caso sin dueño no se puede consentir ni reutilizar")

// peticion es lo que el operador tecleó, ya parseado. Existe como tipo —en vez de
// dos parámetros sueltos— para que la función de abajo tenga UNA entrada y se
// pueda tabular en un test.
type peticion struct {
	tenant     string
	consentido bool
}

// preparar traduce lo que el operador pidió a las DOS piezas que la siembra
// necesita: el caso y el anonimizador. No toca red, ni base, ni reloj, ni
// entorno.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 POR QUÉ ESTO ESTÁ EXTRAÍDO DE `run`, Y NO ES UN CAPRICHO DE TESTABILIDAD
// ════════════════════════════════════════════════════════════════════════════
//
// Mientras estas cuatro líneas vivieron dentro de `run` —entre un `config.Load`,
// un `postgres.Open` y un `SELECT current_database()`— NO LAS PROBABA NADIE, y
// una auditoría (2026-08-27) metió DOS mutaciones aquí que la suite entera dejó
// pasar con `vet` en 0:
//
//   - construir el anonimizador SIN `casebank.NombresDelCaso()` ⇒ los nombres
//     propios se siembran EN CLARO, que es PII en la nube (ADR-0034);
//   - poner `caso.Consented = true` fijo, ignorando el flag ⇒ el consentimiento
//     deja de significar nada y este binario pasa a afirmar por su cuenta algo
//     que solo el operador puede afirmar.
//
// Las dos son de una línea, las dos parecen inocentes en un diff, y las dos
// atacan justo la frase que el docstring de arriba llama el sentido de este
// binario: «la siembra es un ACTO DE OPERADOR, y ese acto ES el registro del
// consentimiento». Una doctrina que ningún test vigila es una intención.
//
// La frontera está puesta donde acaba la decisión y empieza el I/O: todo lo que
// esta función devuelve se puede afirmar sin una base delante, y todo lo que
// `run` hace después necesita una y no decide nada.
func preparar(p peticion) (casebank.Caso, casebank.Anonimizador, error) {
	if p.tenant == "" {
		return casebank.Caso{}, casebank.Anonimizador{}, errSinTenant
	}
	caso := casebank.CasoAmbar(p.tenant)
	// 🔴 El flag NO se suma al consentimiento del fixture: lo SUSTITUYE. Sin
	// `-consentido`, el guard de casebank rechaza y el operador ve por qué —
	// que es justo lo que este binario existe para hacer explícito.
	caso.Consented = p.consentido
	return caso, casebank.NuevoAnonimizador(casebank.NombresDelCaso()...), nil
}

func run(tenant string, consentido bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 🔴 LA DECISIÓN VA PRIMERO, Y ANTES DE ABRIR NADA. `preparar` es lo único
	// que este binario decide, y decidirlo aquí significa que un `-tenant`
	// vacío falla SIN haber abierto una conexión — y, sobre todo, que la parte
	// que la auditoría del 2026-08-27 encontró sin vigilar está bajo test.
	caso, anonimizador, err := preparar(peticion{tenant: tenant, consentido: consentido})
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db, err := postgres.Open(ctx, postgres.Config{DSN: cfg.DB.DSN()})
	if err != nil {
		return fmt.Errorf("base de datos no disponible: %w", err)
	}
	defer closeDB(db)

	// Se dice a qué base se ha conectado ANTES de escribir en ella: el DSN se
	// arma de cinco variables distintas y una fila sembrada en la base
	// equivocada no da error, solo queda ahí (mismo aviso que cmd/migrate).
	var base string
	if err := db.QueryRowContext(ctx, "SELECT current_database()").Scan(&base); err != nil {
		return fmt.Errorf("consultando current_database(): %w", err)
	}
	stdlog.Printf("base de datos: %s (host %s:%d, usuario %s)", base, cfg.DB.Host, cfg.DB.Port, cfg.DB.User)

	svc, err := casebank.NewServicio(casebank.NewPostgres(db), anonimizador)
	if err != nil {
		return err
	}

	id, sembro, err := svc.Sembrar(ctx, caso)
	if err != nil {
		return err
	}
	if !sembro {
		stdlog.Printf("el caso ya estaba sembrado para el tenant %s: no se escribió nada", tenant)
		return nil
	}
	stdlog.Printf("caso sembrado: id=%d tenant=%s calidad=C (texto REDACTADO, ver expected._procedencia)", id, tenant)
	return nil
}

func closeDB(db *sql.DB) {
	if err := db.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
		stdlog.Printf("error cerrando la base de datos: %v", err)
	}
}
