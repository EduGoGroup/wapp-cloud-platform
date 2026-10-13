//go:build integracion

package procesos

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Los fixtures de este fichero son SQL directo, y solo existen donde el producto no ofrece una
// puerta HTTP para dejar ese estado. El comentario de cada uno dice cuál es el hueco. Todo lo que SÍ
// tiene puerta HTTP (crear una empresa, canjear, invitar, configurar) se hace por ella, no aquí.

const (
	// tenantPlataformaID es la empresa operadora de wApp, sembrada por la migración 0059: la única
	// ante la que los handlers de plataforma (EnforcePlatformCaller) aceptan al llamante.
	tenantPlataformaID = "55550000-0000-0000-0000-000000000055"
	// rolPlataformaID es la plantilla global del rol platform_admin, sembrada por la 0059; sus
	// permisos (tenants.read.any, tenants.create.any, tenants.revoke.any…) los dan la 0059 y la 0060.
	rolPlataformaID = "10000000-0000-0000-0000-000000000004"

	// topeFixture acota una sentencia de fixture: son sentencias sobre una fila.
	topeFixture = 15 * time.Second
)

// columnasEnvejecibles es la lista blanca de envejecer: cada tabla admitida, con las columnas de
// instante que se retrasan para que el registro entero parezca más viejo. Una tabla solo entra si
// tiene `id` y estas columnas existen. Una columna que admite NULL (deposit_due_at) sigue en NULL
// mientras lo esté.
var columnasEnvejecibles = map[string][]string{
	// tenants: created_at y updated_at.
	"tenants": {"created_at", "updated_at"},
	// intakes: el plazo del presupuesto (intakes.QuoteDeadline) se mide desde updated_at, y created_at
	// es su suplente; deposit_due_at es el plazo de la seña.
	"intakes": {"created_at", "updated_at", "deposit_due_at"},
}

// uuidAleatorio devuelve un UUID v4 aleatorio en minúsculas, para usuarios y entidades de prueba
// que no necesitan un valor concreto: cada corrida y cada test tiene los suyos, así que no chocan.
// Falla el test (t.Fatalf) si el sistema no da bytes aleatorios.
func uuidAleatorio(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("uuidAleatorio: %v", err)
	}
	b[6] = b[6]&0x0f | 0x40 // versión 4
	b[8] = b[8]&0x3f | 0x80 // variante RFC 4122
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// exigirUUID devuelve un error si valor no tiene forma de UUID. Los fixtures lo piden antes de
// tocar la base para que un identificador mal escrito falle con su nombre y no como un error de
// Postgres a media sentencia.
func exigirUUID(nombre, valor string) error {
	if !identidadUUID.MatchString(valor) {
		return fmt.Errorf("%s %q no es un UUID", nombre, valor)
	}
	return nil
}

// altaStaffPlataforma deja a usuario (un UUID) como staff de plataforma: miembro de la empresa
// operadora (tenant_members en 55550000-…-0055) y con el rol platform_admin (iam_user_roles con
// 10000000-…-0004, con ámbito GLOBAL —tenant_id NULL—, que es el único ámbito global que el IAM
// admite y el de la alta de staff del runbook). Después de esto, el canje de su Identity Token da
// un Context Token con esa empresa y ese rol, que abre las rutas /admin/tenants y las demás del plano
// de plataforma. Es idempotente. Recibe el test, la base del servidor y el UUID del usuario; falla
// (t.Fatalf) si el UUID no lo es o el SQL falla.
//
// 🔧 POR QUÉ NO HAY PUERTA HTTP: la única es la aprobación de una solicitud de acceso
// (POST /admin/access-requests/{id}/approve), y esa llama a identity-core para acreditar al usuario
// (cliente M2M); el arnés no levanta identity-core, solo el JWKS. Y el primer staff de cualquier
// despliegue se da de alta así, a mano: no puede aprobarse a sí mismo.
func altaStaffPlataforma(t *testing.T, db *sql.DB, usuario string) {
	t.Helper()
	ctx, cancelar := context.WithTimeout(t.Context(), topeFixture)
	defer cancelar()
	if err := darAltaStaff(ctx, db, usuario); err != nil {
		t.Fatalf("altaStaffPlataforma: %v", err)
	}
}

// darAltaStaff es el cuerpo de altaStaffPlataforma: las dos inserciones en una transacción, para que
// no quede un usuario miembro sin rol o con rol sin empresa. Devuelve el error si el UUID no lo es
// o el SQL falla.
func darAltaStaff(ctx context.Context, db *sql.DB, usuario string) error {
	if err := exigirUUID("el usuario", usuario); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("abrir la transacción: %w", err)
	}
	defer func() {
		if errCierre := tx.Rollback(); errCierre != nil && !errors.Is(errCierre, sql.ErrTxDone) {
			fmt.Fprintf(os.Stderr, "procesos: rollback del alta de staff: %v\n", errCierre)
		}
	}()
	// ON CONFLICT sin objetivo: la clave de tenant_members y los dos índices únicos de
	// iam_user_roles (con y sin tenant_id) quedan cubiertos, y repetir la alta no falla.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO public.tenant_members (user_id, tenant_id) VALUES ($1::uuid, $2::uuid)
		ON CONFLICT DO NOTHING`, usuario, tenantPlataformaID); err != nil {
		return fmt.Errorf("insertar en tenant_members: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO public.iam_user_roles (user_id, role_id, tenant_id) VALUES ($1::uuid, $2::uuid, NULL)
		ON CONFLICT DO NOTHING`, usuario, rolPlataformaID); err != nil {
		return fmt.Errorf("insertar en iam_user_roles: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("confirmar el alta de staff: %w", err)
	}
	return nil
}

// ventanaInmediata deja la ventana de captación de la empresa en 0 s de silencio y 0 s de techo
// (tenant_settings.aggregation_window_seconds y aggregation_max_seconds, migraciones 0072 y 0076),
// de modo que el barrido del agregador (cada 5 s) la cierra en la primera pasada en vez de esperar
// los 45 s / 120 s por defecto. Es un upsert: crea la fila de la empresa si no existe y deja el
// resto de sus ajustes como están. Recibe el test, la base del servidor y el id (UUID) de una
// empresa que ya exista; falla (t.Fatalf) si no es un UUID, si la empresa no existe o si el SQL
// falla. Hay que llamarla antes de que la empresa reciba el mensaje que abre la ventana.
//
// 🔧 POR QUÉ NO HAY PUERTA HTTP: ninguna ruta de /api/v1 ni de /admin escribe estos dos plazos; son
// ajustes de plataforma que hoy solo cambia el operador por SQL.
func ventanaInmediata(t *testing.T, db *sql.DB, tenant string) {
	t.Helper()
	ctx, cancelar := context.WithTimeout(t.Context(), topeFixture)
	defer cancelar()
	if err := cerrarVentana(ctx, db, tenant); err != nil {
		t.Fatalf("ventanaInmediata: %v", err)
	}
}

// cerrarVentana es el cuerpo de ventanaInmediata. Devuelve el error si el id no es un UUID, la
// empresa no existe o el SQL falla.
func cerrarVentana(ctx context.Context, db *sql.DB, tenant string) error {
	if err := exigirUUID("la empresa", tenant); err != nil {
		return err
	}
	var existe bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM public.tenants WHERE id = $1::uuid)`, tenant).Scan(&existe); err != nil {
		return fmt.Errorf("buscar la empresa %s: %w", tenant, err)
	}
	if !existe {
		return fmt.Errorf("la empresa %s no existe en tenants", tenant)
	}
	// tenant_settings.tenant_id es TEXT (la PK de 0013), no UUID.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_settings (tenant_id, aggregation_window_seconds, aggregation_max_seconds)
		VALUES ($1, 0, 0)
		ON CONFLICT (tenant_id) DO UPDATE
		   SET aggregation_window_seconds = 0, aggregation_max_seconds = 0`, tenant); err != nil {
		return fmt.Errorf("escribir tenant_settings de %s: %w", tenant, err)
	}
	return nil
}

// envejecer retrasa en `intervalo` los instantes del registro `id` de la tabla `tabla`, como si
// se hubiera creado (y tocado) hace `intervalo` más: created_at, updated_at y, si los tiene,
// deposit_due_at (ver columnasEnvejecibles). Es la forma de probar un vencimiento sin esperarlo.
// Recibe el test, la base del servidor, la tabla (solo las de la lista blanca: tenants e intakes),
// el id del registro (el `id` de la tabla, en texto) y un intervalo positivo. Solo sirve para
// tablas con columna `id` y con esas columnas de instante. Falla (t.Fatalf) si la tabla no está
// en la lista, el intervalo no es positivo, el id está vacío, el SQL falla o no existe exactamente
// un registro con ese id.
//
// 🔧 POR QUÉ NO HAY PUERTA HTTP: los vencimientos del producto se miden contra el reloj de Postgres
// (24 h el presupuesto, días la seña) y no hay ruta que mueva el reloj ni que reescriba fechas; el
// único modo de verlos sin esperar un día es mover la fecha del registro hacia atrás.
func envejecer(t *testing.T, db *sql.DB, tabla, id string, intervalo time.Duration) {
	t.Helper()
	ctx, cancelar := context.WithTimeout(t.Context(), topeFixture)
	defer cancelar()
	if err := envejecerFila(ctx, db, tabla, id, intervalo); err != nil {
		t.Fatalf("envejecer(%s, %s, %s): %v", tabla, id, intervalo, err)
	}
}

// sentenciaEnvejecer valida la entrada de envejecer y arma el UPDATE. La tabla se valida contra la
// lista blanca y tanto ella como las columnas se entrecomillan con pgx.Identifier: ninguna entrada
// llega al SQL como texto. Devuelve la sentencia, con $1 = id (texto) y $2 = segundos
// (double precision), o el error si la tabla no está en la lista, el id está vacío o el intervalo
// no es positivo.
func sentenciaEnvejecer(tabla, id string, intervalo time.Duration) (string, error) {
	columnas, ok := columnasEnvejecibles[tabla]
	if !ok {
		admitidas := make([]string, 0, len(columnasEnvejecibles))
		for nombre := range columnasEnvejecibles {
			admitidas = append(admitidas, nombre)
		}
		slices.Sort(admitidas)
		return "", fmt.Errorf("la tabla %q no está en la lista blanca (%s)", tabla, strings.Join(admitidas, ", "))
	}
	if id == "" {
		return "", errors.New("el id está vacío")
	}
	if intervalo <= 0 {
		return "", fmt.Errorf("el intervalo %s no es positivo: envejecer solo retrasa", intervalo)
	}
	asignaciones := make([]string, len(columnas))
	for i, c := range columnas {
		col := pgx.Identifier{c}.Sanitize()
		asignaciones[i] = col + " = " + col + " - $2::double precision * interval '1 second'"
	}
	return "UPDATE " + pgx.Identifier{"public", tabla}.Sanitize() +
		" SET " + strings.Join(asignaciones, ", ") + " WHERE id::text = $1", nil
}

// envejecerFila es el cuerpo de envejecer: valida con sentenciaEnvejecer, ejecuta y exige que se
// haya tocado exactamente un registro. Devuelve el error si la entrada no vale, el SQL falla o el
// id no existe.
func envejecerFila(ctx context.Context, db *sql.DB, tabla, id string, intervalo time.Duration) error {
	sentencia, err := sentenciaEnvejecer(tabla, id, intervalo)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, sentencia, id, intervalo.Seconds())
	if err != nil {
		return fmt.Errorf("actualizar %s: %w", tabla, err)
	}
	tocados, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("contar los registros tocados de %s: %w", tabla, err)
	}
	if tocados != 1 {
		return fmt.Errorf("se tocaron %d registros de %s con id %s, quería exactamente 1", tocados, tabla, id)
	}
	return nil
}

// ---------------------------------------------------------------------------------------------
// Tests propios del arnés
// ---------------------------------------------------------------------------------------------

// TestArnes_Fixtures prueba los tres fixtures contra una base clonada, sin servidor: el alta de staff
// deja sus filas y es idempotente; ventanaInmediata crea o corrige la fila de ajustes y exige que la
// empresa exista; envejecer retrasa exactamente lo pedido en tenants e intakes y rechaza lo que no
// está en la lista blanca. Necesita Docker.
func TestArnes_Fixtures(t *testing.T) {
	t.Parallel()
	base := nuevaBase(t, "clientes_fixtures")
	db := base.Abrir(t)
	var tenant string
	if err := db.QueryRowContext(t.Context(),
		`INSERT INTO public.tenants (slug, display_name) VALUES ('fixtures', 'Fixtures') RETURNING id::text`).Scan(&tenant); err != nil {
		t.Fatalf("sembrar la empresa de la prueba: %v", err)
	}

	t.Run("altaStaffPlataforma", func(t *testing.T) { probarAltaStaff(t, db) })
	t.Run("ventanaInmediata", func(t *testing.T) { probarVentanaInmediata(t, db, tenant) })
	t.Run("envejecer una empresa", func(t *testing.T) { probarEnvejecerTenant(t, db, tenant) })
	t.Run("envejecer una solicitud", func(t *testing.T) { probarEnvejecerIntake(t, db, tenant) })
	t.Run("envejecer rechaza lo que no debe", func(t *testing.T) { probarEnvejecerRechazos(t, db, tenant) })
}

// consultaEntero recibe una consulta de una fila y una columna entera (un count(*), un page_size)
// con sus argumentos y devuelve el valor; falla el test si la consulta falla.
func consultaEntero(t *testing.T, db *sql.DB, consulta string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), consulta, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", consulta, err)
	}
	return n
}

// probarAltaStaff comprueba que el alta deja una fila de tenant_members en la empresa de plataforma
// y una de iam_user_roles con el rol platform_admin y ámbito global, que repetirla no duplica nada,
// que no toca a otro usuario y que un UUID mal escrito se rechaza antes de tocar la base.
func probarAltaStaff(t *testing.T, db *sql.DB) {
	t.Helper()
	staff, otro := uuidAleatorio(t), uuidAleatorio(t)
	altaStaffPlataforma(t, db, staff)
	altaStaffPlataforma(t, db, staff) // idempotente

	if n := consultaEntero(t, db, `SELECT count(*) FROM public.tenant_members WHERE user_id = $1::uuid AND tenant_id = $2::uuid`, staff, tenantPlataformaID); n != 1 {
		t.Errorf("filas en tenant_members del staff = %d, quería 1", n)
	}
	if n := consultaEntero(t, db, `SELECT count(*) FROM public.iam_user_roles WHERE user_id = $1::uuid AND role_id = $2::uuid AND tenant_id IS NULL`, staff, rolPlataformaID); n != 1 {
		t.Errorf("filas en iam_user_roles del staff = %d, quería 1 (platform_admin, ámbito global)", n)
	}
	if n := consultaEntero(t, db, `SELECT count(*) FROM public.tenant_members WHERE user_id = $1::uuid`, otro); n != 0 {
		t.Errorf("el alta tocó a otro usuario: %d filas", n)
	}
	var nombreRol string
	if err := db.QueryRowContext(t.Context(), `SELECT name FROM public.iam_roles WHERE id = $1::uuid`, rolPlataformaID).Scan(&nombreRol); err != nil || nombreRol != "platform_admin" {
		t.Errorf("el rol %s se llama %q (error %v), quería platform_admin", rolPlataformaID, nombreRol, err)
	}
	if err := darAltaStaff(t.Context(), db, "no-es-un-uuid"); err == nil {
		t.Errorf("un usuario que no es UUID debía rechazarse")
	}
}

// probarVentanaInmediata comprueba que la ventana queda en 0/0 tanto si la empresa no tenía fila de
// ajustes como si la tenía con otros valores, que no duplica la fila, y que una empresa que no
// existe o un id que no es UUID se rechazan.
func probarVentanaInmediata(t *testing.T, db *sql.DB, tenant string) {
	t.Helper()
	const consulta = `SELECT aggregation_window_seconds || '/' || aggregation_max_seconds FROM public.tenant_settings WHERE tenant_id = $1`
	leer := func() string {
		var plazos string
		if err := db.QueryRowContext(t.Context(), consulta, tenant).Scan(&plazos); err != nil {
			t.Fatalf("leer tenant_settings de %s: %v", tenant, err)
		}
		return plazos
	}

	ventanaInmediata(t, db, tenant) // sin fila previa: la crea
	if plazos := leer(); plazos != "0/0" {
		t.Errorf("tras ventanaInmediata sin fila previa, ventana/techo = %s, quería 0/0", plazos)
	}
	if _, err := db.ExecContext(t.Context(),
		`UPDATE public.tenant_settings SET aggregation_window_seconds = 45, aggregation_max_seconds = 120, page_size = 9 WHERE tenant_id = $1`, tenant); err != nil {
		t.Fatalf("devolver los plazos a los de fábrica: %v", err)
	}
	ventanaInmediata(t, db, tenant) // con fila previa: la corrige
	if plazos := leer(); plazos != "0/0" {
		t.Errorf("tras ventanaInmediata con fila previa, ventana/techo = %s, quería 0/0", plazos)
	}
	if n := consultaEntero(t, db, `SELECT count(*) FROM public.tenant_settings WHERE tenant_id = $1`, tenant); n != 1 {
		t.Errorf("filas de ajustes de la empresa = %d, quería 1", n)
	}
	if pagina := consultaEntero(t, db, `SELECT page_size FROM public.tenant_settings WHERE tenant_id = $1`, tenant); pagina != 9 {
		t.Errorf("ventanaInmediata pisó otro ajuste: page_size = %d, quería 9", pagina)
	}

	if err := cerrarVentana(t.Context(), db, uuidAleatorio(t)); err == nil {
		t.Errorf("una empresa que no existe debía rechazarse")
	}
	if err := cerrarVentana(t.Context(), db, "no-es-un-uuid"); err == nil {
		t.Errorf("un id que no es UUID debía rechazarse")
	}
}

// instantes lee created_at y updated_at del registro id de la tabla (tenants o intakes) y, de
// intakes, también deposit_due_at (en tenants esa columna sale siempre NULL). Falla el test si la
// tabla no es una de las dos o no puede leerlos.
func instantes(t *testing.T, db *sql.DB, tabla, id string) (creado, tocado time.Time, plazoSena sql.NullTime) {
	t.Helper()
	var consulta string
	switch tabla {
	case "tenants":
		consulta = `SELECT created_at, updated_at, NULL::timestamptz FROM public.tenants WHERE id::text = $1`
	case "intakes":
		consulta = `SELECT created_at, updated_at, deposit_due_at FROM public.intakes WHERE id::text = $1`
	default:
		t.Fatalf("instantes: la tabla %q no se sabe leer", tabla)
	}
	if err := db.QueryRowContext(t.Context(), consulta, id).Scan(&creado, &tocado, &plazoSena); err != nil {
		t.Fatalf("leer los instantes de %s %s: %v", tabla, id, err)
	}
	return creado, tocado, plazoSena
}

// probarEnvejecerTenant comprueba que envejecer retrasa created_at y updated_at de una empresa
// exactamente lo pedido y no toca otra empresa.
func probarEnvejecerTenant(t *testing.T, db *sql.DB, tenant string) {
	t.Helper()
	var vecina string
	if err := db.QueryRowContext(t.Context(),
		`INSERT INTO public.tenants (slug, display_name) VALUES ('vecina', 'Vecina') RETURNING id::text`).Scan(&vecina); err != nil {
		t.Fatalf("sembrar la empresa vecina: %v", err)
	}
	creado0, tocado0, _ := instantes(t, db, "tenants", tenant)
	vecinaCreado0, _, _ := instantes(t, db, "tenants", vecina)

	const retraso = 36*time.Hour + 30*time.Minute
	envejecer(t, db, "tenants", tenant, retraso)

	creado1, tocado1, _ := instantes(t, db, "tenants", tenant)
	if d := creado0.Sub(creado1); d != retraso {
		t.Errorf("created_at se retrasó %s, quería %s", d, retraso)
	}
	if d := tocado0.Sub(tocado1); d != retraso {
		t.Errorf("updated_at se retrasó %s, quería %s", d, retraso)
	}
	if vecinaCreado1, _, _ := instantes(t, db, "tenants", vecina); !vecinaCreado1.Equal(vecinaCreado0) {
		t.Errorf("envejecer tocó otra empresa: created_at %s → %s", vecinaCreado0, vecinaCreado1)
	}
}

// probarEnvejecerIntake siembra una solicitud con plazo de seña y comprueba que envejecer retrasa
// sus tres instantes exactamente lo pedido y que una solicitud sin plazo de seña sigue sin él.
func probarEnvejecerIntake(t *testing.T, db *sql.DB, tenant string) {
	t.Helper()
	sembrar := func(plazoSena sql.NullTime) string {
		var id string
		// intakes.event_id es obligatorio (0054/0055): cada solicitud cuelga de un evento de conversación.
		if err := db.QueryRowContext(t.Context(), `
			WITH evento AS (
				INSERT INTO public.conversation_events (tenant_id, session_id, contact_id, kind, history_id, flow_id, flow_version)
				VALUES ($1::uuid, 'sesion', gen_random_uuid(), 'cart', 'cart-2026-01-01-0000', 'flujo', 1)
				RETURNING id
			)
			INSERT INTO public.intakes (id, tenant_id, contact_id, session_id, status, event_id, deposit_due_at)
			SELECT gen_random_uuid(), $2::text, 'contacto-opaco', 'sesion', 'pending_approval', evento.id, $3::timestamptz FROM evento
			RETURNING id::text`, tenant, tenant, plazoSena).Scan(&id); err != nil {
			t.Fatalf("sembrar la solicitud: %v", err)
		}
		return id
	}
	conSena := sembrar(sql.NullTime{Time: time.Now().Add(72 * time.Hour), Valid: true})
	sinSena := sembrar(sql.NullTime{})
	creado0, tocado0, sena0 := instantes(t, db, "intakes", conSena)

	const retraso = 25 * time.Hour
	envejecer(t, db, "intakes", conSena, retraso)
	envejecer(t, db, "intakes", sinSena, retraso)

	creado1, tocado1, sena1 := instantes(t, db, "intakes", conSena)
	if d := creado0.Sub(creado1); d != retraso {
		t.Errorf("created_at se retrasó %s, quería %s", d, retraso)
	}
	if d := tocado0.Sub(tocado1); d != retraso {
		t.Errorf("updated_at se retrasó %s, quería %s", d, retraso)
	}
	if !sena0.Valid || !sena1.Valid || sena0.Time.Sub(sena1.Time) != retraso {
		t.Errorf("deposit_due_at pasó de %v a %v, quería %s menos", sena0, sena1, retraso)
	}
	if _, _, sena := instantes(t, db, "intakes", sinSena); sena.Valid {
		t.Errorf("deposit_due_at NULL dejó de serlo: %v", sena)
	}
}

// probarEnvejecerRechazos comprueba lo que envejecer no admite: una tabla fuera de la lista blanca
// (incluida una con intento de inyección), un intervalo que no es positivo, un id vacío y un id que
// no existe. En ningún caso se toca la base.
func probarEnvejecerRechazos(t *testing.T, db *sql.DB, tenant string) {
	t.Helper()
	creado0, _, _ := instantes(t, db, "tenants", tenant)
	casos := []struct {
		nombre    string
		tabla, id string
		intervalo time.Duration
	}{
		{"tabla fuera de la lista", "tenant_members", tenant, time.Hour},
		{"inyección en la tabla", `tenants; DROP TABLE public.tenants; --`, tenant, time.Hour},
		{"tabla con otro esquema", "public.tenants", tenant, time.Hour},
		{"tabla vacía", "", tenant, time.Hour},
		{"intervalo cero", "tenants", tenant, 0},
		{"intervalo negativo", "tenants", tenant, -time.Hour},
		{"id vacío", "tenants", "", time.Hour},
		{"id que no existe", "tenants", uuidAleatorio(t), time.Hour},
		{"id que no es un uuid", "tenants", "no-es-un-uuid", time.Hour},
	}
	for _, c := range casos {
		if err := envejecerFila(t.Context(), db, c.tabla, c.id, c.intervalo); err == nil {
			t.Errorf("%s: envejecerFila(%q, %q, %s) no devolvió error", c.nombre, c.tabla, c.id, c.intervalo)
		}
	}
	if creado1, _, _ := instantes(t, db, "tenants", tenant); !creado1.Equal(creado0) {
		t.Errorf("un rechazo tocó la base: created_at %s → %s", creado0, creado1)
	}
	if n := consultaEntero(t, db, `SELECT count(*) FROM public.tenants`); n < 1 {
		t.Errorf("tenants quedó vacía")
	}
}
