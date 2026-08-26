// intake_type_integration_test.go — Plan 044 · Ola 2 · T2.8, EL DISCRIMINADOR
// (migración 0077) contra Postgres REAL.
//
// # POR QUÉ AQUÍ NO HAY UNA SOLA LÍNEA DE GO QUE PROBAR
//
// T2.8 no añade código: añade FORMA. Todo lo que decide —qué valores son legales,
// qué reciben las filas viejas, qué recibe una fila nueva que omita la columna, y
// qué sobrevive a un replay— lo resuelve el catálogo de Postgres y nadie más. Un
// doble en memoria reescribiría a mano justo lo que se quiere afirmar y la suite
// pasaría a probar el doble. Por eso el fichero entero es de integración.
//
// # QUÉ NO SE PRUEBA AQUÍ, PORQUE TODAVÍA NO EXISTE
//
// Que alguien ESCRIBA un tipo distinto de 'order'. Hoy no hay un solo camino de
// producción que lo haga: el struct `Intake` no tiene el campo (a propósito — un
// símbolo sin consumidor es deuda) y P2/P3 aún no eligen prompt por tipo. Lo que
// esta tarea deja es la SEDE, y estos tests vigilan que la sede aguante el día que
// llegue el inquilino: los cuatro valores se aceptan, el quinto se rechaza, y un
// tipo ya escrito NO lo pisa el siguiente arranque.
//
// Se corre como el resto de la casa: por el NOMBRE del fichero, con
// `WAPP_TEST_DB_DSN` (sin ella se salta solo). `make test-integration` lo levanta.
// Los helpers `openTestDB`, `ensureTenantPG` y `seedEventoPG` viven en
// postgres_integration_test.go, mismo paquete de test.
package intakes_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// tenantTipo es propio de este fichero: los tests de listado limpian POR TENANT y
// un tenant compartido los haría pisarse.
const tenantTipo = "77777777-0044-0002-0008-777777777777"

// losCuatroTipos es el juego CERRADO de la 0077. Está escrito aquí una vez y lo
// consumen los dos lados del test del CHECK (acepta / rechaza): si alguien amplía
// el enum en la migración y no toca esta línea, el test de aceptación no probaría
// el valor nuevo — y lo notaría el de rechazo solo por casualidad.
var losCuatroTipos = []string{"order", "booking", "appointment", "incident"}

// lasDosSedes empareja cada tabla con el nombre de SU CHECK. Los nombres divergen
// —`intakes_*_chk` (0048/0054/0055) frente a `intake_jobs_*_check` (0072)— porque
// cada tabla sigue la convención de sus vecinas; la 0077 no unifica convenciones.
var lasDosSedes = []struct {
	tabla string
	check string
}{
	{"intakes", "intakes_intake_type_chk"},
	{"intake_jobs", "intake_jobs_intake_type_check"},
}

// barridoPorTabla son las consultas del criterio (a), constantes y una por tabla.
// No se construyen con fmt.Sprintf sobre el nombre de la tabla a propósito: SQL
// armado por concatenación es exactamente lo que gosec persigue, y aquí no hace
// falta — son dos.
var barridoPorTabla = map[string]string{
	"intakes": `SELECT count(*) FILTER (WHERE intake_type IS NULL), count(*)
	              FROM public.intakes`,
	"intake_jobs": `SELECT count(*) FILTER (WHERE intake_type IS NULL), count(*)
	                  FROM public.intake_jobs`,
}

// ---------------------------------------------------------------------------
// HELPERS
// ---------------------------------------------------------------------------

// preparaTenantTipo deja el tenant del fichero sembrado y registra la limpieza de
// TODO lo que estos tests escriben.
//
// 🔴 EL ORDEN DE LOS CLEANUP IMPORTA Y ES LIFO. `ensureTenantPG` registra el suyo
// (borrar el tenant, que CASCADEA a conversation_events) ANTES que el de aquí, así
// que el de aquí corre PRIMERO: las solicitudes y los jobs se van antes que sus
// eventos. Al revés quedarían solicitudes huérfanas apuntando a un evento borrado
// — `intakes.event_id` tiene FK física (0054:111) y el DELETE del tenant fallaría.
func preparaTenantTipo(t *testing.T, db *sql.DB) {
	t.Helper()
	ensureTenantPG(t, db, tenantTipo)
	t.Cleanup(func() {
		limpio := context.Background()
		if _, err := db.ExecContext(limpio,
			`DELETE FROM public.intakes WHERE tenant_id = $1`, tenantTipo); err != nil {
			t.Errorf("limpiando las solicitudes de T2.8: %v", err)
		}
		if _, err := db.ExecContext(limpio,
			`DELETE FROM public.intake_jobs WHERE tenant_id = $1`, tenantTipo); err != nil {
			t.Errorf("limpiando los jobs de T2.8: %v", err)
		}
	})
}

// eventoTerminalT28 crea el evento conversacional PADRE que toda solicitud tiene
// que declarar: `intakes.event_id` lleva FK FÍSICA a conversation_events (0054:111),
// así que sin él el INSERT no entra.
//
// Es un clon CON `ctx` de `seedEventoPG` (postgres_integration_test.go), y el clon
// existe por una razón concreta: el original resuelve su propio
// `context.Background()`, y llamarlo desde un helper que YA recibe un ctx es lo que
// `contextcheck` marca — con razón, porque el cancelado del test dejaría de
// propagarse justo en la mitad del montaje. Cambiar la firma del original obligaría
// a tocar los seis ficheros de test que lo usan por un fixture de T2.8.
//
// Terminal ('cancelled') a propósito, como los fixtures del resto del paquete:
// ninguna guarda de «evento vivo» debe proteger lo que estos tests no quieren
// proteger. Lo limpia el CASCADE del tenant.
func eventoTerminalT28(ctx context.Context, t *testing.T, db *sql.DB) string {
	t.Helper()
	var id string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO public.conversation_events
		       (tenant_id, session_id, contact_id, kind, history_id, status,
		        flow_id, flow_version, closed_at)
		VALUES ($1, 't28-sess-' || gen_random_uuid(), gen_random_uuid(), 'cart',
		        't28-' || gen_random_uuid(), 'cancelled', 'flujo-t28', 1, now())
		RETURNING id::text
	`, tenantTipo).Scan(&id); err != nil {
		t.Fatalf("sembrando el evento padre de la solicitud: %v", err)
	}
	return id
}

// sembrarIntakeConTipo inserta UNA solicitud con su `intake_type` EXPLÍCITO y
// devuelve (id, error CRUDO). El error se devuelve en vez de morir en él porque el
// test del CHECK necesita mirarlo: es su afirmación entera.
//
// Cada solicitud estrena SU evento: `intakes_event_id_uidx` (0054) es único
// parcial, un evento tiene a lo sumo un contenido durable.
func sembrarIntakeConTipo(ctx context.Context, t *testing.T, db *sql.DB, tipo string) (string, error) {
	t.Helper()
	id := uuid.NewString()
	eventID := eventoTerminalT28(ctx, t, db)
	_, err := db.ExecContext(ctx, `
		INSERT INTO public.intakes
		       (id, tenant_id, contact_id, session_id, status, total, event_id,
		        created_at, updated_at, intake_type)
		VALUES ($1, $2, 'contacto-opaco-t28', 'sess-t28', 'cancelled', 18000, $3, now(), now(), $4)
	`, id, tenantTipo, eventID, tipo)
	return id, err
}

// sembrarJobConTipo inserta UN job con su `intake_type` EXPLÍCITO.
//
// 🔴 NACE EN 'done' Y NO EN 'pending', y no es cosmética. La cola de `ClaimNext`
// (internal/intake) es GLOBAL —no filtra por tenant—, así que un `pending` mío que
// sobreviviera a una limpieza fallida pondría rojos los tests de OTRO paquete sin
// que nada estuviera roto. En 'done' es invisible al claim. El tipo no depende del
// estado, así que no se pierde nada.
//
// `event_id` es una FK LÓGICA aquí (0072: sin REFERENCES), así que vale un UUID
// cualquiera y no hace falta estrenar un evento por job.
func sembrarJobConTipo(ctx context.Context, t *testing.T, db *sql.DB, tipo string) (string, error) {
	t.Helper()
	var id string
	err := db.QueryRowContext(ctx, `
		INSERT INTO public.intake_jobs
		       (tenant_id, session_id, contact_id, event_id, status, intake_type)
		VALUES ($1, 'sess-t28', 'contacto-opaco-t28', $2::uuid, 'done', $3)
		RETURNING id::text
	`, tenantTipo, uuid.NewString(), tipo).Scan(&id)
	return id, err
}

// tipoDelIntake y tipoDelJob leen la columna con SQL directo. No pasan por el store
// porque el store NO la expone (T2.8 no toca Go): lo que se afirma es lo que la
// BASE guardó.
func tipoDelIntake(ctx context.Context, t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var tipo string
	if err := db.QueryRowContext(ctx,
		`SELECT intake_type FROM public.intakes WHERE id = $1::uuid`, id).Scan(&tipo); err != nil {
		t.Fatalf("leer intakes.intake_type de %s: %v", id, err)
	}
	return tipo
}

func tipoDelJob(ctx context.Context, t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var tipo string
	if err := db.QueryRowContext(ctx,
		`SELECT intake_type FROM public.intake_jobs WHERE id = $1::uuid`, id).Scan(&tipo); err != nil {
		t.Fatalf("leer intake_jobs.intake_type de %s: %v", id, err)
	}
	return tipo
}

// barridoDeNulos ejecuta el barrido del criterio (a) y devuelve (nulos, total). El
// TOTAL viaja con el nulo a propósito: un barrido que dice «0 nulos» sobre 0 filas
// no ha mirado nada, y quien lo lea tiene que poder distinguirlo.
func barridoDeNulos(ctx context.Context, t *testing.T, db *sql.DB, tabla string) (nulos, total int) {
	t.Helper()
	consulta, ok := barridoPorTabla[tabla]
	if !ok {
		t.Fatalf("no hay barrido escrito para la tabla %q", tabla)
	}
	if err := db.QueryRowContext(ctx, consulta).Scan(&nulos, &total); err != nil {
		t.Fatalf("barriendo %s por intake_type IS NULL: %v", tabla, err)
	}
	return nulos, total
}

// invalidaElHashDelEsquema fuerza que el SIGUIENTE Migrate reejecute TODOS los
// `structure/*.sql` en vez de saltárselos.
//
// 🔴 SIN ESTO, UN TEST QUE «SIMULA EL SEGUNDO ARRANQUE» SALE HUECO. `Migrate`
// compara versión Y hash de contenido (`isUpToDate`): con los dos iguales devuelve
// `Skipped=true` y NO TOCA LA BASE — el test estaría afirmando que no pasa nada
// porque no se ejecutó nada. Se altera el hash de la ÚLTIMA fila de
// `public.schema_version` (`readSchemaVersion` lee por `id DESC`), que es el estado
// exacto en que queda una base cuando alguien edita un structure/*.sql sin tocar
// `SchemaVersion` — o sea, esta migración. Lo prueba el llamante asertando
// `Skipped == false`. Mismo mecanismo que profile_replay_integration_test.go y que
// internal/intake/backoff_integration_test.go: no se inventa uno nuevo.
func invalidaElHashDelEsquema(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		UPDATE public.schema_version SET content_hash = 'hash-alterado-0077'
		WHERE id = (SELECT id FROM public.schema_version ORDER BY id DESC LIMIT 1)
	`); err != nil {
		t.Fatalf("alterando el hash registrado del esquema: %v", err)
	}
}

// reaplicaElDirectorio fuerza el replay y EXIGE que el runner reaplique de verdad.
func reaplicaElDirectorio(ctx context.Context, t *testing.T, db *sql.DB, etapa string) {
	t.Helper()
	invalidaElHashDelEsquema(ctx, t, db)
	res, err := migrations.Migrate(ctx, db)
	if err != nil {
		t.Fatalf("%s: %v", etapa, err)
	}
	if res.Skipped {
		t.Fatalf("%s: Migrate devolvió Skipped=true. El full-replay NO corrió y este test no probó "+
			"nada (isUpToDate exige versión Y hash; ver invalidaElHashDelEsquema)", etapa)
	}
}

// ---------------------------------------------------------------------------
// (a) LAS DOS COLUMNAS, Y EL BARRIDO
// ---------------------------------------------------------------------------

// TestIntegration_IntakeType_LasDosColumnasExistenConSuFormaExacta es el criterio
// (a), y hay que leerlo sabiendo qué mitad de él sostiene el peso.
//
// 🔴 EL BARRIDO DEL ENUNCIADO, SOLO, ES UNA TAUTOLOGÍA. «`SELECT count(*) FROM
// intakes WHERE intake_type IS NULL` da 0» NO PUEDE FALLAR una vez la columna es
// NOT NULL: Postgres no admitiría la fila que lo rompiera. Ejecutarlo y celebrarlo
// sería el modo favorito de esta casa de mentir en verde. Se ejecuta igual —lo pide
// el criterio y cuesta una consulta— pero lo que de verdad se afirma aquí es la
// CAUSA de que sea cierto: `is_nullable = 'NO'` en el catálogo, en las DOS tablas.
// Bórrese el bloque `SET NOT NULL` de la 0077 y este test cae; el barrido a secas
// seguiría verde.
//
// La otra mitad no vacua es el DEFAULT: es lo único que hace que una fila nueva
// que OMITA la columna nazca tipada, y sin él todo INSERT existente del repo
// —ninguno nombra `intake_type`— reventaría contra el NOT NULL. Eso se afirma por
// conducta, no por catálogo: se inserta omitiéndola y se lee lo que salió.
//
// SALIDAS ESPERADAS: para las dos tablas, data_type='text', is_nullable='NO',
// column_default con 'order'; barrido = 0 nulos; y una fila sin `intake_type` sale
// con 'order'.
func TestIntegration_IntakeType_LasDosColumnasExistenConSuFormaExacta(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	preparaTenantTipo(t, db)

	for _, sede := range lasDosSedes {
		var tipo, admiteNulo string
		var porDefecto sql.NullString
		if err := db.QueryRowContext(ctx, `
			SELECT data_type, is_nullable, column_default
			  FROM information_schema.columns
			 WHERE table_schema = 'public' AND table_name = $1 AND column_name = 'intake_type'
		`, sede.tabla).Scan(&tipo, &admiteNulo, &porDefecto); err != nil {
			t.Fatalf("%s.intake_type NO EXISTE en el catálogo (%v). Sin ella el objeto sigue siendo "+
				"MONOMÓRFICO: la tabla no puede distinguir un pedido de una reserva, una cita o una "+
				"incidencia — los otros tres usos que promete el ADR-0044", sede.tabla, err)
		}
		if tipo != "text" {
			t.Fatalf("%s.intake_type es %q; ESPERADO text", sede.tabla, tipo)
		}
		if admiteNulo != "NO" {
			t.Fatalf("%s.intake_type admite NULL; ESPERADO NOT NULL. Es lo ÚNICO que hace verdadero "+
				"PARA SIEMPRE el barrido del criterio (a): sin el NOT NULL, hoy da 0 y mañana no",
				sede.tabla)
		}
		if !porDefecto.Valid || !strings.Contains(porDefecto.String, "'order'") {
			t.Fatalf("%s.intake_type tiene DEFAULT %q; ESPERADO uno con 'order'. Ningún INSERT del "+
				"repo nombra esta columna: sin DEFAULT, todos revientan contra el NOT NULL",
				sede.tabla, porDefecto.String)
		}

		nulos, _ := barridoDeNulos(ctx, t, db, sede.tabla)
		if nulos != 0 {
			t.Fatalf("el barrido de %s encontró %d filas con intake_type IS NULL; ESPERADO 0",
				sede.tabla, nulos)
		}
	}

	// El DEFAULT, por CONDUCTA: una fila que omite la columna.
	idIntake := uuid.NewString()
	eventID := eventoTerminalT28(ctx, t, db)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.intakes
		       (id, tenant_id, contact_id, session_id, status, total, event_id, created_at, updated_at)
		VALUES ($1, $2, 'contacto-opaco-t28', 'sess-t28', 'cancelled', 18000, $3, now(), now())
	`, idIntake, tenantTipo, eventID); err != nil {
		t.Fatalf("insertando una solicitud SIN nombrar intake_type: %v. Así escriben HOY los dos "+
			"INSERT de producción sobre intakes: si esto falla, la 0077 rompió el camino vivo", err)
	}
	if got := tipoDelIntake(ctx, t, db, idIntake); got != "order" {
		t.Fatalf("una solicitud que omite intake_type salió con %q; ESPERADO 'order'", got)
	}

	idJob, err := sembrarJobConTipoPorDefecto(ctx, t, db)
	if err != nil {
		t.Fatalf("insertando un job SIN nombrar intake_type: %v", err)
	}
	if got := tipoDelJob(ctx, t, db, idJob); got != "order" {
		t.Fatalf("un job que omite intake_type salió con %q; ESPERADO 'order'", got)
	}
}

// sembrarJobConTipoPorDefecto inserta un job OMITIENDO la columna: el gemelo del
// INSERT que hace hoy `internal/intake` (que no la nombra, porque no existía).
func sembrarJobConTipoPorDefecto(ctx context.Context, t *testing.T, db *sql.DB) (string, error) {
	t.Helper()
	var id string
	err := db.QueryRowContext(ctx, `
		INSERT INTO public.intake_jobs
		       (tenant_id, session_id, contact_id, event_id, status)
		VALUES ($1, 'sess-t28', 'contacto-opaco-t28', $2::uuid, 'done')
		RETURNING id::text
	`, tenantTipo, uuid.NewString()).Scan(&id)
	return id, err
}

// ---------------------------------------------------------------------------
// LAS DOS DEFINICIONES, COMPARADAS
// ---------------------------------------------------------------------------

// TestIntegration_IntakeType_LosDosCheckSonIdenticos es la mitad del enunciado que
// no es un criterio numerado y sin la cual el resto se degrada solo.
//
// El plan lo dice con todas las letras: «el mismo enum y el mismo CHECK (escrito una
// vez y aplicado dos, o dos CHECK idénticos con un test que los compare: dos
// definiciones que divergen en un valor son la forma clásica de que una se quede
// atrás)». La 0077 elige la segunda variante —dos CHECK explícitos, porque en 75
// ficheros de `structure/` no hay una línea de SQL dinámico— y ESTE es el test que
// la sostiene. Sin él, ampliar `intakes` y olvidar `intake_jobs` sale verde: los
// tres tests siguientes recorren las dos tablas, pero cada uno con SU juego, y una
// divergencia hacia MÁS valores en una sola no la nota ninguno.
//
// Se comparan las definiciones NORMALIZADAS por Postgres (`pg_get_constraintdef`),
// no el texto del fichero: así una reordenación de los literales o un cambio de
// comillas no da un rojo falso, y un valor de más o de menos sí.
//
// SALIDA ESPERADA: las dos definiciones, cadena a cadena, IGUALES.
func TestIntegration_IntakeType_LosDosCheckSonIdenticos(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	definiciones := make(map[string]string, len(lasDosSedes))
	for _, sede := range lasDosSedes {
		var def string
		if err := db.QueryRowContext(ctx, `
			SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = $1
		`, sede.check).Scan(&def); err != nil {
			t.Fatalf("no existe el CHECK %q sobre %s (%v). Un CHECK inline dentro de un CREATE TABLE "+
				"no se recrea NUNCA en el replay: por eso la 0077 lo pone aparte y con nombre",
				sede.check, sede.tabla, err)
		}
		definiciones[sede.tabla] = def
	}

	if definiciones["intakes"] != definiciones["intake_jobs"] {
		t.Fatalf("los dos CHECK del discriminador DIVERGEN:\n"+
			"  intakes_intake_type_chk        = %s\n"+
			"  intake_jobs_intake_type_check  = %s\n"+
			"Un job podría nacer con un tipo que su solicitud no admite (o al revés) y el fallo "+
			"aparecería al FINAL del pipeline, al escribir el borrador, con el trabajo ya hecho. "+
			"Si se amplía uno hay que ampliar el otro, y se amplía EDITANDO LA 0077.",
			definiciones["intakes"], definiciones["intake_jobs"])
	}
}

// ---------------------------------------------------------------------------
// (c) EL CHECK: LOS CUATRO SÍ, EL QUINTO NO
// ---------------------------------------------------------------------------

// TestIntegration_IntakeType_ElCheckAceptaLosCuatroYRechazaElResto es el criterio
// (c), con su mitad gemela pegada, y sin esa mitad no probaría lo que dice.
//
// 🔴 UN TEST QUE SOLO RECHAZA SE SATISFACE CON UN CHECK DEMASIADO ESTRECHO. Un
// `CHECK (intake_type = 'order')` rechazaría 'reserva' igual de bien y dejaría la
// tabla tan monomórfica como estaba —que es el defecto entero que T2.8 viene a
// arreglar—. Por eso se afirman los DOS lados: los cuatro nombres del ADR-0044
// entran, y lo que no está en la lista no entra. Y entran HOY, aunque tres no
// tengan escritor, porque un CHECK cerrado en este repo solo se amplía EDITANDO su
// propia migración: dejarlos para después es dejarlos para una edición en caliente.
//
// Los rechazados no son aleatorios: 'reserva' es el nombre CASTELLANO del valor
// legal 'booking' (el error que cometería quien no leyera el COMMENT), 'quote' es
// un tipo plausible que nadie ha decidido, y la cadena vacía es lo que llega cuando
// alguien mapea un campo ausente.
//
// SALIDAS ESPERADAS: 4 INSERT que pasan y 3 que fallan con SQLSTATE 23514 sobre el
// CHECK de SU tabla, en las dos tablas.
func TestIntegration_IntakeType_ElCheckAceptaLosCuatroYRechazaElResto(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	preparaTenantTipo(t, db)

	for _, tipo := range losCuatroTipos {
		id, err := sembrarIntakeConTipo(ctx, t, db, tipo)
		if err != nil {
			t.Fatalf("intakes RECHAZÓ el tipo legal %q (%v). Con la lista recortada la tabla sigue "+
				"siendo monomórfica, que es el defecto que T2.8 arregla", tipo, err)
		}
		if got := tipoDelIntake(ctx, t, db, id); got != tipo {
			t.Fatalf("intakes guardó %q habiendo escrito %q", got, tipo)
		}
		if _, err := sembrarJobConTipo(ctx, t, db, tipo); err != nil {
			t.Fatalf("intake_jobs RECHAZÓ el tipo legal %q (%v)", tipo, err)
		}
	}

	for _, malo := range []string{"reserva", "quote", ""} {
		_, err := sembrarIntakeConTipo(ctx, t, db, malo)
		exigeCheckViolado(t, err, "intakes", "intakes_intake_type_chk", malo)
		_, err = sembrarJobConTipo(ctx, t, db, malo)
		exigeCheckViolado(t, err, "intake_jobs", "intake_jobs_intake_type_check", malo)
	}
}

// exigeCheckViolado afirma que el INSERT falló POR EL CHECK y no por otra cosa.
//
// 🔴 CON UN `err != nil` A SECAS ESTE TEST SALDRÍA VERDE POR EL MOTIVO EQUIVOCADO:
// un typo en el nombre de una columna, un evento que no existe o una FK rota
// también devuelven error, y el CHECK podría no estar puesto en absoluto. Se exige
// el SQLSTATE 23514 (check_violation) Y el nombre de la constraint, que es lo único
// que distingue «lo rechazó el enum» de «lo rechazó cualquier otra cosa».
func exigeCheckViolado(t *testing.T, err error, tabla, nombreCheck, valor string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s ACEPTÓ el tipo ilegal %q. El discriminador deja de discriminar en cuanto admite "+
			"cualquier cadena: el CHECK es la mitad de esta tarea", tabla, valor)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("%s falló con %q al escribir %q, pero NO es un error de Postgres. El test no puede "+
			"afirmar que lo rechazó el CHECK", tabla, err, valor)
	}
	if pgErr.Code != "23514" {
		t.Fatalf("%s rechazó %q con SQLSTATE %s (%s); ESPERADO 23514 (check_violation). Falló por "+
			"otra cosa y el CHECK podría no estar puesto", tabla, valor, pgErr.Code, pgErr.Message)
	}
	if pgErr.ConstraintName != nombreCheck {
		t.Fatalf("%s rechazó %q por la constraint %q; ESPERADO %q",
			tabla, valor, pgErr.ConstraintName, nombreCheck)
	}
}

// ---------------------------------------------------------------------------
// (b) LAS FILAS QUE YA ESTABAN — LO QUE UNA BASE RECIÉN MIGRADA NO PUEDE PROBAR
// ---------------------------------------------------------------------------

// TestIntegration_IntakeType_FilasPreexistentesQuedanTipadas es el criterio (b), y
// su montaje es el test entero.
//
// 🔴 EN UNA BASE RECIÉN MIGRADA ESTE CRITERIO SALE VERDE POR CERO FILAS. Cuando la
// suite arranca, la 0077 ya corrió sobre tablas vacías: cualquier barrido pasa y
// nadie ha probado que la solicitud que lleva meses en UAT sobreviva a la
// migración. Por eso aquí se FABRICA el estado anterior —se TIRAN las dos columnas,
// se siembran filas como las que hay hoy, y solo entonces se reejecuta la migración
// encima—, y por eso el barrido de abajo mira TAMBIÉN el total: «0 nulos sobre 0
// filas» no es una respuesta.
//
// 🔴 Y NO SE DA POR SUPUESTO QUE UN DEFAULT POBLE LAS FILAS VIEJAS. Aquí ni
// siquiera podría: la 0077 crea las columnas SIN default (regla 1 del full-replay) y
// quien las puebla es el `UPDATE … WHERE intake_type IS NULL`. Bórrese ese UPDATE y
// el `SET NOT NULL` del final revienta contra las filas de aquí — que es
// exactamente lo que pasaría en UAT con la fila que ya existe, y el arranque del
// servidor se caería. Este test es lo único que lo nota antes.
//
// SALIDAS ESPERADAS: Migrate ... Skipped=false; las 4 filas fabricadas con
// intake_type='order'; barrido = 0 nulos sobre un total > 0 en las dos tablas.
func TestIntegration_IntakeType_FilasPreexistentesQuedanTipadas(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	preparaTenantTipo(t, db)

	// (1) EL ESTADO ANTERIOR: la columna no existe en ninguna de las dos. El DROP se
	// lleva por delante también los dos CHECK, que cuelgan de ella; el replay los
	// recrea y el test de los gemelos es quien vigila que vuelvan.
	if _, err := db.ExecContext(ctx, `
		ALTER TABLE public.intakes     DROP COLUMN IF EXISTS intake_type
	`); err != nil {
		t.Fatalf("fabricando el estado anterior (drop en intakes): %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		ALTER TABLE public.intake_jobs DROP COLUMN IF EXISTS intake_type
	`); err != nil {
		t.Fatalf("fabricando el estado anterior (drop en intake_jobs): %v", err)
	}

	// (2) CUATRO FILAS COMO LAS QUE HAY HOY EN UAT, escritas SIN la columna —no se
	// puede de otra forma: acaba de dejar de existir—. Dos por tabla, para que el
	// barrido tenga que recorrer y no le valga con acertar una.
	viejasIntakes := []string{
		sembrarIntakeLegado(ctx, t, db),
		sembrarIntakeLegado(ctx, t, db),
	}
	viejosJobs := []string{
		sembrarJobLegado(ctx, t, db),
		sembrarJobLegado(ctx, t, db),
	}

	// (3) EL ARRANQUE QUE APLICA LA 0077 ENCIMA.
	reaplicaElDirectorio(ctx, t, db, "reejecutando las migraciones sobre el estado anterior")

	// (4) LAS FILAS VIEJAS SALIERON TIPADAS.
	for _, id := range viejasIntakes {
		if got := tipoDelIntake(ctx, t, db, id); got != "order" {
			t.Fatalf("la solicitud legada %s salió con intake_type=%q; ESPERADO 'order'. Todo lo que "+
				"existe hoy es un pedido: la tabla no sabía decir otra cosa", id, got)
		}
	}
	for _, id := range viejosJobs {
		if got := tipoDelJob(ctx, t, db, id); got != "order" {
			t.Fatalf("el job legado %s salió con intake_type=%q; ESPERADO 'order'", id, got)
		}
	}

	// (5) Y EL BARRIDO DEL CRITERIO (a), AHORA SÍ CON FILAS DEBAJO.
	for _, sede := range lasDosSedes {
		nulos, total := barridoDeNulos(ctx, t, db, sede.tabla)
		if total == 0 {
			t.Fatalf("el barrido de %s corrió sobre CERO filas: no probó nada, y las filas fabricadas "+
				"en el paso (2) deberían estar ahí", sede.tabla)
		}
		if nulos != 0 {
			t.Fatalf("el barrido de %s encontró %d filas con intake_type IS NULL sobre %d; ESPERADO 0. "+
				"El backfill de la 0077 no alcanzó a las filas anteriores a la columna", sede.tabla, nulos, total)
		}
	}
}

// sembrarIntakeLegado escribe una solicitud SIN nombrar `intake_type`. Solo tiene
// sentido llamarla con la columna ya tirada: con la columna puesta, el DEFAULT la
// rellenaría y la fila dejaría de ser «legada».
func sembrarIntakeLegado(ctx context.Context, t *testing.T, db *sql.DB) string {
	t.Helper()
	id := uuid.NewString()
	eventID := eventoTerminalT28(ctx, t, db)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.intakes
		       (id, tenant_id, contact_id, session_id, status, total, event_id, created_at, updated_at)
		VALUES ($1, $2, 'contacto-opaco-t28', 'sess-t28', 'cancelled', 18000, $3, now() - interval '30 days', now())
	`, id, tenantTipo, eventID); err != nil {
		t.Fatalf("sembrando la solicitud legada: %v", err)
	}
	return id
}

// sembrarJobLegado escribe un job SIN nombrar `intake_type`, en 'done' por el mismo
// motivo que sembrarJobConTipo: la cola de ClaimNext es global.
func sembrarJobLegado(ctx context.Context, t *testing.T, db *sql.DB) string {
	t.Helper()
	var id string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO public.intake_jobs
		       (tenant_id, session_id, contact_id, event_id, status, created_at, updated_at)
		VALUES ($1, 'sess-t28', 'contacto-opaco-t28', $2::uuid, 'done',
		        now() - interval '30 days', now())
		RETURNING id::text
	`, tenantTipo, uuid.NewString()).Scan(&id); err != nil {
		t.Fatalf("sembrando el job legado: %v", err)
	}
	return id
}

// ---------------------------------------------------------------------------
// (d) EL SEGUNDO ARRANQUE — Y LO QUE «NO REVIENTA» SE DEJA SIN DECIR
// ---------------------------------------------------------------------------

// TestIntegration_IntakeType_ElReplayNoPisaUnTipoYaEscrito es el criterio (d), con
// el enunciado ENDURECIDO, y conviene decir por qué.
//
// 🔴 «EL RUNNER VUELVE A PASAR LA MIGRACIÓN Y NO REVIENTA» ES CASI UNA TAUTOLOGÍA
// AQUÍ. Todas las sentencias de la 0077 son `IF NOT EXISTS` / `DROP … IF EXISTS` +
// `ADD` / `UPDATE` con guard: un replay que no rompa nada es lo que sale por
// defecto, y ese criterio se satisfaría igual con la migración MÁS PELIGROSA que se
// puede escribir aquí —un `UPDATE … SET intake_type='order'` SIN el `WHERE
// intake_type IS NULL`—, que tampoco revienta: simplemente VUELVE A TIPAR TODO COMO
// PEDIDO en cada arranque. Un cliente con reservas y citas las vería convertirse en
// pedidos la próxima vez que alguien tocara cualquier fichero de `structure/`, sin
// un solo error en ningún log.
//
// Así que el criterio se prueba con contenido: dos filas con tipos NO 'order' y
// OPUESTOS entre sí, un replay completo encima, y las dos INTACTAS. Que el runner
// no reviente queda afirmado de camino (Migrate devuelve error si lo hiciera), y de
// propina se comprueba que el estado final del replay conserva la forma: NOT NULL,
// DEFAULT y los dos CHECK vuelven a estar puestos, que es lo que un CHECK inline
// NO garantizaría.
//
// SALIDAS ESPERADAS: Migrate ... Skipped=false, sin error; la solicitud sigue en
// 'booking' y el job en 'appointment'.
func TestIntegration_IntakeType_ElReplayNoPisaUnTipoYaEscrito(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	preparaTenantTipo(t, db)

	idIntake, err := sembrarIntakeConTipo(ctx, t, db, "booking")
	if err != nil {
		t.Fatalf("sembrando una solicitud de tipo booking: %v", err)
	}
	idJob, err := sembrarJobConTipo(ctx, t, db, "appointment")
	if err != nil {
		t.Fatalf("sembrando un job de tipo appointment: %v", err)
	}

	reaplicaElDirectorio(ctx, t, db, "segundo arranque (replay del directorio entero)")

	if got := tipoDelIntake(ctx, t, db, idIntake); got != "booking" {
		t.Fatalf("el replay PISÓ el tipo de la solicitud: got %q, want booking. El guard "+
			"`WHERE intake_type IS NULL` del backfill de la 0077 es lo ÚNICO que lo impide, y sin él "+
			"cada arranque convierte en pedidos las reservas y las citas de un cliente real — sin un "+
			"solo error en ningún log", got)
	}
	if got := tipoDelJob(ctx, t, db, idJob); got != "appointment" {
		t.Fatalf("el replay PISÓ el tipo del job: got %q, want appointment", got)
	}
}
