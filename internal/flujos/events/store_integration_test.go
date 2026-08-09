package events_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// dsnEnv habilita los tests de integración con BD real (igual que en flujos/store).
const dsnEnv = "WAPP_TEST_DB_DSN"

// openTestDB abre la conexión de test o salta si no hay BD configurada.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD: la integración DEBE correr", dsnEnv)
		}
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible en %s (%v) pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv, err)
		}
		t.Skipf("BD no disponible en %s (%v): se omiten los tests de integración", dsnEnv, err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD de test: %v", cerr)
		}
	})
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migrando BD de test: %v", err)
	}
	return db
}

// seedTenant crea un tenant con slug único y devuelve su UUID (FK del evento).
func seedTenant(t *testing.T, db *sql.DB) string {
	t.Helper()
	repo := postgres.NewTenantRepository(db)
	slug := fmt.Sprintf("tenant-events-%d", time.Now().UnixNano())
	ten, err := repo.Create(context.Background(), slug, "Events Store Test")
	if err != nil {
		t.Fatalf("crear tenant: %v", err)
	}
	return ten.ID
}

// ── Reloj de laboratorio ──────────────────────────────────────────────────────

// relojFijo devuelve un reloj inyectable y el puntero con el que moverlo. Que el
// reloj sea MENTIRA a propósito es el punto: un instante inventado que ninguna
// máquina real produciría (2031) hace imposible que un test pase porque el código
// llamó a time.Now() por su cuenta.
type relojFijo struct{ t time.Time }

func (r *relojFijo) now() time.Time          { return r.t }
func (r *relojFijo) avanzar(d time.Duration) { r.t = r.t.Add(d) }

// kek32 es una KEK de 32 bytes en base64, todos iguales, para el keyring de test.
func kek32(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

// cipherDeTest construye el FieldCipher con un keyring explícito de laboratorio.
func cipherDeTest(t *testing.T) *crypto.FieldCipher {
	t.Helper()
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		KeyringB64: "k1:" + kek32(0x11),
		CurrentID:  "k1",
		IndexB64:   kek32(0x44),
	})
	if err != nil {
		t.Fatalf("NewEnvKeyProvider: %v", err)
	}
	return crypto.NewFieldCipher(kp)
}

// nuevoStore arma el store con reloj fijo en el instante dado.
func nuevoStore(t *testing.T, db *sql.DB, arranque time.Time) (*events.Store, *relojFijo) {
	t.Helper()
	reloj := &relojFijo{t: arranque}
	return events.NewStore(db, cipherDeTest(t), events.WithClock(reloj.now)), reloj
}

// nuevoEvento son los datos de un evento de laboratorio para la conversación dada.
func nuevoEvento(tenantID, sessionID, contactID, kind string) events.NewEvent {
	return events.NewEvent{
		TenantID: tenantID, SessionID: sessionID, ContactID: contactID,
		Kind: kind, FlowID: "flujo-lab", FlowVersion: 3,
	}
}

// ── Lectura CRUDA: lo que la BD guardó de verdad, sin pasar por el store ──────

// filaCruda es la fila leída con SQL directo. Los tests afirman sobre ESTO, no
// sobre lo que el store devolvió: un store puede devolver lo que quiera.
type filaCruda struct {
	historyID      string
	status         string
	createdAt      time.Time
	lastActivityAt time.Time
	closedAt       sql.NullTime
}

func leerCruda(ctx context.Context, t *testing.T, db *sql.DB, id string) filaCruda {
	t.Helper()
	var f filaCruda
	err := db.QueryRowContext(ctx,
		`SELECT history_id, status, created_at, last_activity_at, closed_at
		   FROM public.conversation_events WHERE id = $1`, id).
		Scan(&f.historyID, &f.status, &f.createdAt, &f.lastActivityAt, &f.closedAt)
	if err != nil {
		t.Fatalf("leer fila cruda del evento %s: %v", id, err)
	}
	return f
}

// insertarEventoCrudo mete una fila con SQL directo, saltándose el store por
// completo: así lo que se prueba en T1.2 es la RESTRICCIÓN DE LA BD, no el código.
func insertarEventoCrudo(ctx context.Context, db *sql.DB, tenantID, sessionID, contactID, kind, status string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO public.conversation_events
		        (tenant_id, session_id, contact_id, kind, history_id, status, flow_id, flow_version)
		 VALUES ($1, $2, $3, $4, $5, $6, 'flujo-lab', 1)`,
		tenantID, sessionID, contactID, kind, kind+"-2026-08-09-1200", status)
	return err
}

const (
	contactoA = "aaaaaaaa-0000-4000-8000-000000000001"
	contactoB = "bbbbbbbb-0000-4000-8000-000000000002"
)

// ── Ayudas «must»: el fallo de montaje aborta, y así el cuerpo del test solo
// contiene las aserciones que de verdad se están probando ─────────────────────

func mustCrear(ctx context.Context, t *testing.T, st *events.Store, in events.NewEvent) events.Event {
	t.Helper()
	ev, err := st.CreateEvent(ctx, in)
	if err != nil {
		t.Fatalf("CreateEvent(%s): %v", in.Kind, err)
	}
	return ev
}

func mustTransitar(ctx context.Context, t *testing.T, st *events.Store, id string, to events.Status) {
	t.Helper()
	if err := st.TransitionEvent(ctx, id, to); err != nil {
		t.Fatalf("TransitionEvent(%s → %s): %v", id, to, err)
	}
}

func mustTocar(ctx context.Context, t *testing.T, st *events.Store, id string) {
	t.Helper()
	if err := st.Touch(ctx, id); err != nil {
		t.Fatalf("Touch(%s): %v", id, err)
	}
}

func mustListar(ctx context.Context, t *testing.T, etiqueta string,
	listar func(context.Context, string, string, string) ([]events.Event, error),
	tenantID, sesion, contacto string) []events.Event {
	t.Helper()
	evs, err := listar(ctx, tenantID, sesion, contacto)
	if err != nil {
		t.Fatalf("%s: %v", etiqueta, err)
	}
	return evs
}

// quieroVivo comprueba de una vez las tres respuestas de GetAliveByKind: error,
// presencia e identidad. Sin él, cada caso serían tres condiciones sueltas.
func quieroVivo(ctx context.Context, t *testing.T, st *events.Store, tenantID, sesion, contacto, kind, quieroID string) {
	t.Helper()
	got, hay, err := st.GetAliveByKind(ctx, tenantID, sesion, contacto, kind)
	if err != nil {
		t.Fatalf("GetAliveByKind(%s): %v", kind, err)
	}
	if quieroID == "" {
		if hay {
			t.Fatalf("GetAliveByKind(%s) encontró %s; no debería haber ninguno vivo", kind, got.ID)
		}
		return
	}
	if !hay {
		t.Fatalf("GetAliveByKind(%s) no encontró nada; quiero %s", kind, quieroID)
	}
	if got.ID != quieroID {
		t.Fatalf("GetAliveByKind(%s) devolvió %s; quiero %s", kind, got.ID, quieroID)
	}
	if !got.Alive() {
		t.Fatalf("GetAliveByKind devolvió un evento con status %q; debería estar vivo", got.Status)
	}
}

// ── T1.2 · El único parcial lo impone la BD ──────────────────────────────────

// TestIntegration_UnicoVivoPorTipoLoImponeLaBD es la verificación de E-2: la regla
// «uno vivo por tipo y conversación» no es una comprobación del código que un
// camino nuevo pueda olvidar, es un índice único parcial. Por eso este test
// INSERTA CON SQL CRUDO: si la regla viviera en Go, este test pasaría igual y
// estaría mintiendo.
func TestIntegration_UnicoVivoPorTipoLoImponeLaBD(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	const sesion = "sess-unico"

	// 1) El primer carrito vivo entra.
	if err := insertarEventoCrudo(ctx, db, tenantID, sesion, contactoA, "cart", "open"); err != nil {
		t.Fatalf("el primer carrito vivo debería entrar: %v", err)
	}

	// 2) El segundo carrito vivo de la MISMA conversación choca contra el índice.
	err := insertarEventoCrudo(ctx, db, tenantID, sesion, contactoA, "cart", "open")
	if err == nil {
		t.Fatalf("el segundo carrito vivo NO debería haber entrado: el índice único parcial (E-2) no está imponiendo nada")
	}
	if !postgres.IsUniqueViolation(err) {
		t.Fatalf("el segundo carrito falló, pero no por violación de índice único: %v", err)
	}

	// 3) Un carrito vivo y una encuesta viva CONVIVEN: el único parcial es por
	//    TIPO, no por conversación. Es lo que permite saltar entre eventos.
	if err := insertarEventoCrudo(ctx, db, tenantID, sesion, contactoA, "survey", "open"); err != nil {
		t.Fatalf("una encuesta viva debería convivir con el carrito vivo: %v", err)
	}

	// 4) El MISMO contacto en OTRA sesión es otra conversación: no colisiona.
	if err := insertarEventoCrudo(ctx, db, tenantID, "sess-otra", contactoA, "cart", "open"); err != nil {
		t.Fatalf("el mismo contacto en otra sesión no debería colisionar: %v", err)
	}

	// 5) Cerrado el primero, el tipo queda libre y el segundo YA entra. Los
	//    terminales no ocupan sitio: por eso nada se borra (INV-09).
	if _, err := db.ExecContext(ctx,
		`UPDATE public.conversation_events SET status='closed', closed_at=now()
		  WHERE tenant_id=$1 AND session_id=$2 AND contact_id=$3 AND kind='cart' AND status='open'`,
		tenantID, sesion, contactoA); err != nil {
		t.Fatalf("cerrar el primer carrito: %v", err)
	}
	if err := insertarEventoCrudo(ctx, db, tenantID, sesion, contactoA, "cart", "open"); err != nil {
		t.Fatalf("cerrado el primero, el segundo carrito debería entrar: %v", err)
	}
}

// ── T1.4 · history_id sale del reloj INYECTADO ───────────────────────────────

// TestIntegration_CreateEventHistoryIDDelRelojInyectado es el test que más fácil
// se escribe en falso. Dos precauciones deliberadas:
//
//   - El reloj miente y miente lejos: 2031. Si el código llamara a time.Now() en
//     vez de al reloj inyectado, el id diría 2026 y esto reventaría.
//   - El instante viene en UTC+5 y a las 23:30, así que su UTC es de las 18:30 del
//     día ANTERIOR en la esfera del reloj. Un .UTC() olvidado cambia hora y día.
//
// Se afirma sobre la fila CRUDA, no sobre el struct devuelto.
func TestIntegration_CreateEventHistoryIDDelRelojInyectado(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)

	// 2031-03-04 23:30:45 en UTC+5  ==  2031-03-04 18:30:45 UTC.
	instante := time.Date(2031, 3, 4, 23, 30, 45, 0, time.FixedZone("UTC+5", 5*3600))
	store, _ := nuevoStore(t, db, instante)

	ev, err := store.CreateEvent(ctx, nuevoEvento(tenantID, "sess-hid", contactoA, "cart"))
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	const quiero = "cart-2031-03-04-1830"
	if ev.HistoryID != quiero {
		t.Fatalf("HistoryID devuelto = %q; quiero %q", ev.HistoryID, quiero)
	}

	cruda := leerCruda(ctx, t, db, ev.ID)
	if cruda.historyID != quiero {
		t.Fatalf("history_id PERSISTIDO = %q; quiero %q", cruda.historyID, quiero)
	}
	if cruda.status != string(events.StatusOpen) {
		t.Fatalf("el evento nace en %q; quiero open", cruda.status)
	}
	if cruda.closedAt.Valid {
		t.Fatalf("un evento recién nacido no puede tener closed_at: %v", cruda.closedAt.Time)
	}

	// El nacimiento y el reloj arrancan en el MISMO instante, y ese instante es el
	// inyectado — no el now() de la BD, que sería de este año.
	if !cruda.createdAt.Equal(instante) {
		t.Fatalf("created_at = %s; quiero el instante inyectado %s", cruda.createdAt.UTC(), instante.UTC())
	}
	if !cruda.lastActivityAt.Equal(instante) {
		t.Fatalf("last_activity_at = %s; quiero el instante inyectado %s", cruda.lastActivityAt.UTC(), instante.UTC())
	}
}

// TestIntegration_CreateEventSegundoVivoDelMismoTipo comprueba que el store
// traduce el choque con el índice a ErrAliveExists, y que cerrar libera el tipo.
func TestIntegration_CreateEventSegundoVivoDelMismoTipo(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	store, reloj := nuevoStore(t, db, time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC))

	primero, err := store.CreateEvent(ctx, nuevoEvento(tenantID, "sess-dup", contactoA, "cart"))
	if err != nil {
		t.Fatalf("CreateEvent del primero: %v", err)
	}

	reloj.avanzar(time.Hour) // otro minuto ⇒ otro history_id: el choque no es por el id
	if _, err := store.CreateEvent(ctx, nuevoEvento(tenantID, "sess-dup", contactoA, "cart")); !errors.Is(err, events.ErrAliveExists) {
		t.Fatalf("CreateEvent del segundo vivo = %v; quiero ErrAliveExists", err)
	}

	if err := store.TransitionEvent(ctx, primero.ID, events.StatusClosed); err != nil {
		t.Fatalf("cerrar el primero: %v", err)
	}
	if _, err := store.CreateEvent(ctx, nuevoEvento(tenantID, "sess-dup", contactoA, "cart")); err != nil {
		t.Fatalf("cerrado el primero, el segundo debería nacer: %v", err)
	}
}

// ── T1.4 · Los guards de la transición ───────────────────────────────────────

// TestIntegration_TransitionGuards recorre las dos imposibilidades del diseño.
// Cada caso comprueba DOS cosas: que la llamada devuelve el error esperado y que
// la fila NO se movió. La segunda es la que importa — un guard que devuelve error
// pero ya escribió no es un guard.
func TestIntegration_TransitionGuards(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	muerte := time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC)
	store, reloj := nuevoStore(t, db, muerte)

	cerrado, err := store.CreateEvent(ctx, nuevoEvento(tenantID, "sess-guard", contactoA, "cart"))
	if err != nil {
		t.Fatalf("CreateEvent cerrado: %v", err)
	}
	cancelado, err := store.CreateEvent(ctx, nuevoEvento(tenantID, "sess-guard", contactoA, "survey"))
	if err != nil {
		t.Fatalf("CreateEvent cancelado: %v", err)
	}
	if err := store.TransitionEvent(ctx, cerrado.ID, events.StatusClosed); err != nil {
		t.Fatalf("cerrar: %v", err)
	}
	if err := store.TransitionEvent(ctx, cancelado.ID, events.StatusCancelled); err != nil {
		t.Fatalf("cancelar: %v", err)
	}

	// El sellado de la muerte sale del reloj inyectado, igual que el nacimiento.
	if f := leerCruda(ctx, t, db, cerrado.ID); !f.closedAt.Valid || !f.closedAt.Time.Equal(muerte) {
		t.Fatalf("closed_at = %v (válido=%v); quiero el instante inyectado %s", f.closedAt.Time, f.closedAt.Valid, muerte)
	}

	// A partir de aquí el reloj avanza: si algún guard dejara pasar la escritura,
	// closed_at se movería y lo veríamos.
	reloj.avanzar(24 * time.Hour)

	casos := []struct {
		nombre       string
		id           string
		destino      events.Status
		quieroErr    error
		quieroStatus string
	}{
		{"closed→open es imposible", cerrado.ID, events.StatusOpen, events.ErrNotTerminal, "closed"},
		{"cancelled→closed es imposible", cancelado.ID, events.StatusClosed, events.ErrNotOpen, "cancelled"},
		{"closed→cancelled tampoco: terminal es terminal", cerrado.ID, events.StatusCancelled, events.ErrNotOpen, "closed"},
		{"cancelled→open es imposible", cancelado.ID, events.StatusOpen, events.ErrNotTerminal, "cancelled"},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			antes := leerCruda(ctx, t, db, c.id)
			if err := store.TransitionEvent(ctx, c.id, c.destino); !errors.Is(err, c.quieroErr) {
				t.Fatalf("TransitionEvent(→%s) = %v; quiero %v", c.destino, err, c.quieroErr)
			}
			despues := leerCruda(ctx, t, db, c.id)
			if despues.status != c.quieroStatus {
				t.Fatalf("status tras el intento = %q; quiero %q (el guard rechazó pero la fila se movió)", despues.status, c.quieroStatus)
			}
			if !despues.closedAt.Time.Equal(antes.closedAt.Time) {
				t.Fatalf("closed_at se movió de %v a %v pese al rechazo", antes.closedAt.Time, despues.closedAt.Time)
			}
		})
	}
}

// ── T1.4 · Touch mueve el reloj y NADA más ───────────────────────────────────

// TestIntegration_TouchMueveElRelojYNoElStatus es el test que más fácil pasa en
// verde sin comprobar nada, así que aquí se comprueba lo contrario de lo obvio:
// se toca un evento CERRADO. Si Touch llevara un `WHERE status='open'` el toque no
// afectaría a ninguna fila y devolvería ErrEventMissing; si llevara un
// `SET status=...` el estado cambiaría. Con el evento vivo, ninguna de las dos
// roturas se vería.
func TestIntegration_TouchMueveElRelojYNoElStatus(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	nacimiento := time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC)
	store, reloj := nuevoStore(t, db, nacimiento)

	vivo, err := store.CreateEvent(ctx, nuevoEvento(tenantID, "sess-touch", contactoA, "cart"))
	if err != nil {
		t.Fatalf("CreateEvent vivo: %v", err)
	}
	cerrado, err := store.CreateEvent(ctx, nuevoEvento(tenantID, "sess-touch", contactoA, "survey"))
	if err != nil {
		t.Fatalf("CreateEvent cerrado: %v", err)
	}
	if err := store.TransitionEvent(ctx, cerrado.ID, events.StatusClosed); err != nil {
		t.Fatalf("cerrar: %v", err)
	}

	reloj.avanzar(90 * time.Minute)
	toque := reloj.now()

	for _, c := range []struct {
		nombre       string
		id           string
		quieroStatus string
	}{
		{"un evento vivo", vivo.ID, "open"},
		{"un evento cerrado: Touch no discrimina por estado ni lo cambia", cerrado.ID, "closed"},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			antes := leerCruda(ctx, t, db, c.id)
			if err := store.Touch(ctx, c.id); err != nil {
				t.Fatalf("Touch: %v", err)
			}
			despues := leerCruda(ctx, t, db, c.id)

			if !despues.lastActivityAt.Equal(toque) {
				t.Fatalf("last_activity_at = %s; quiero el instante del toque %s", despues.lastActivityAt.UTC(), toque)
			}
			if despues.lastActivityAt.Equal(antes.lastActivityAt) {
				t.Fatalf("last_activity_at no se movió: sigue en %s", antes.lastActivityAt.UTC())
			}
			if despues.status != c.quieroStatus {
				t.Fatalf("Touch cambió el status de %q a %q; no debe tocarlo", antes.status, despues.status)
			}
			if !despues.createdAt.Equal(antes.createdAt) {
				t.Fatalf("Touch movió created_at de %s a %s", antes.createdAt.UTC(), despues.createdAt.UTC())
			}
			if despues.closedAt.Valid != antes.closedAt.Valid || !despues.closedAt.Time.Equal(antes.closedAt.Time) {
				t.Fatalf("Touch movió closed_at de %v a %v", antes.closedAt.Time, despues.closedAt.Time)
			}
		})
	}

	if err := store.Touch(ctx, "99999999-9999-4999-8999-999999999999"); !errors.Is(err, events.ErrEventMissing) {
		t.Fatalf("Touch de un id inexistente = %v; quiero ErrEventMissing", err)
	}
}

// ── T1.4 · IsSuspended no escribe nada ───────────────────────────────────────

// TestIntegration_IsSuspendedNoEscribeNada cierra la otra mitad de «es pura»: con
// una BD delante, se fotografía la fila, se llama muchas veces con el reloj muy
// adelantado (el caso en que un diseño perezoso «aprovecharía» para marcar algo) y
// se compara la foto. Y de paso: con ttl=0 sigue devolviendo false por muy vencido
// que esté.
func TestIntegration_IsSuspendedNoEscribeNada(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	store, reloj := nuevoStore(t, db, time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC))

	ev, err := store.CreateEvent(ctx, nuevoEvento(tenantID, "sess-susp", contactoA, "cart"))
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	antes := leerCruda(ctx, t, db, ev.ID)

	reloj.avanzar(30 * 24 * time.Hour) // un mes sin hablar

	if !store.IsSuspended(ev, 2*time.Hour) {
		t.Fatalf("tras un mes de inactividad con ttl de 2 h debería estar suspendido")
	}
	if store.IsSuspended(ev, 0) {
		t.Fatalf("con ttl=0 (override «sin vencimiento») NUNCA está suspendido, ni tras un mes")
	}
	for i := 0; i < 20; i++ {
		_ = store.IsSuspended(ev, time.Second)
	}

	despues := leerCruda(ctx, t, db, ev.ID)
	if despues != antes {
		t.Fatalf("IsSuspended tocó la fila: antes %+v, después %+v", antes, despues)
	}
	if n := contarEntradas(ctx, t, db, ev.ID); n != 0 {
		t.Fatalf("IsSuspended escribió %d entradas en el historial; no debe escribir ninguna", n)
	}
}

// ── T1.4 · Listados ──────────────────────────────────────────────────────────

// TestIntegration_ListAliveYListRescuable comprueba que ambos listan solo los
// VIVOS y que el orden es el que cada consumidor necesita: nacimiento para el
// despachador, última actividad descendente para el automensaje de rescate.
func TestIntegration_ListAliveYListRescuable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	store, reloj := nuevoStore(t, db, time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC))
	const sesion = "sess-list"

	carrito := mustCrear(ctx, t, store, nuevoEvento(tenantID, sesion, contactoA, "cart"))
	reloj.avanzar(time.Hour)
	encuesta := mustCrear(ctx, t, store, nuevoEvento(tenantID, sesion, contactoA, "survey"))
	reloj.avanzar(time.Hour)
	menu := mustCrear(ctx, t, store, nuevoEvento(tenantID, sesion, contactoA, "menu"))

	// Ruido que NO debe salir de los listados: otro contacto de la misma sesión
	// (otra conversación) y un evento terminal (que ya no es vivo).
	mustCrear(ctx, t, store, nuevoEvento(tenantID, sesion, contactoB, "cart"))
	mustTransitar(ctx, t, store, menu.ID, events.StatusCancelled)

	// El carrito (el más VIEJO) vuelve a hablar: pasa a ser el último tocado, así
	// que los dos órdenes divergen y el test puede distinguirlos.
	reloj.avanzar(time.Hour)
	mustTocar(ctx, t, store, carrito.ID)

	nacimiento := carrito.ID + "," + encuesta.ID
	if got := ids(mustListar(ctx, t, "ListAlive", store.ListAlive, tenantID, sesion, contactoA)); got != nacimiento {
		t.Fatalf("ListAlive = %s; quiero orden de nacimiento %s", got, nacimiento)
	}
	if got := ids(mustListar(ctx, t, "ListRescuable", store.ListRescuable, tenantID, sesion, contactoA)); got != nacimiento {
		t.Fatalf("ListRescuable = %s; quiero el último tocado primero, %s", got, nacimiento)
	}

	// Y ahora habla la encuesta: el orden de RESCATE se da la vuelta y el de
	// nacimiento no se mueve. Si ambos listados compartieran ORDER BY, uno de los
	// dos fallaría aquí.
	reloj.avanzar(time.Hour)
	mustTocar(ctx, t, store, encuesta.ID)

	rescate := encuesta.ID + "," + carrito.ID
	if got := ids(mustListar(ctx, t, "ListRescuable tras el segundo toque", store.ListRescuable, tenantID, sesion, contactoA)); got != rescate {
		t.Fatalf("ListRescuable = %s; quiero %s", got, rescate)
	}
	if got := ids(mustListar(ctx, t, "ListAlive tras el segundo toque", store.ListAlive, tenantID, sesion, contactoA)); got != nacimiento {
		t.Fatalf("ListAlive = %s; el orden de nacimiento no cambia al hablar (quiero %s)", got, nacimiento)
	}
}

// TestIntegration_GetAliveByKind comprueba que resuelve el vivo de ESE tipo y que
// «no hay» no es un error.
func TestIntegration_GetAliveByKind(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	store, _ := nuevoStore(t, db, time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC))
	const sesion = "sess-bykind"

	// Sin eventos: no hay, y eso NO es un error (E-6, el saludo no crea evento).
	quieroVivo(ctx, t, store, tenantID, sesion, contactoA, "cart", "")

	carrito := mustCrear(ctx, t, store, nuevoEvento(tenantID, sesion, contactoA, "cart"))

	quieroVivo(ctx, t, store, tenantID, sesion, contactoA, "cart", carrito.ID)
	quieroVivo(ctx, t, store, tenantID, sesion, contactoA, "survey", "") // otro tipo
	quieroVivo(ctx, t, store, tenantID, sesion, contactoB, "cart", "")   // otro contacto
	quieroVivo(ctx, t, store, tenantID, "sess-ajena", contactoA, "cart", "")

	// Cerrado deja de ser vivo: el mismo tipo, la misma conversación, y ya no sale.
	mustTransitar(ctx, t, store, carrito.ID, events.StatusClosed)
	quieroVivo(ctx, t, store, tenantID, sesion, contactoA, "cart", "")
}

// ── T1.4 · El historial: seq sin huecos y el grado del ADR-0034 ──────────────

// TestIntegration_AppendHistorialSeqSinHuecos comprueba la numeración y, en la
// misma pasada, que cada puerta escribe en el nivel que le toca: el resumen en
// claro y estructurado, el mensaje CIFRADO y sin payload.
func TestIntegration_AppendHistorialSeqSinHuecos(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	store, _ := nuevoStore(t, db, time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC))

	ev, err := store.CreateEvent(ctx, nuevoEvento(tenantID, "sess-hist", contactoA, "cart"))
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	for i, quiero := range []int{1, 2, 3} {
		seq, aErr := store.AppendSummary(ctx, ev.ID, []byte(fmt.Sprintf(`{"lines":[{"sku":"h%d","qty":1}]}`, i)))
		if aErr != nil {
			t.Fatalf("AppendSummary #%d: %v", i, aErr)
		}
		if seq != quiero {
			t.Fatalf("AppendSummary #%d devolvió seq=%d; quiero %d", i, seq, quiero)
		}
	}
	seq, err := store.AppendMessage(ctx, ev.ID, events.RoleClient, "quiero una hamburguesa sin sal")
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if seq != 4 {
		t.Fatalf("AppendMessage devolvió seq=%d; quiero 4 (la numeración es del EVENTO, no del tipo de entrada)", seq)
	}

	// La numeración leída de la BD: 1..N contiguos, sin huecos ni repeticiones.
	seqs := leerSeqs(ctx, t, db, ev.ID)
	quiero := []int{1, 2, 3, 4}
	if len(seqs) != len(quiero) {
		t.Fatalf("el historial tiene %d entradas (%v); quiero %v", len(seqs), seqs, quiero)
	}
	for i := range quiero {
		if seqs[i] != quiero[i] {
			t.Fatalf("seq del historial = %v; quiero %v (hay un hueco o un salto)", seqs, quiero)
		}
	}

	// Un evento distinto numera desde 1: seq es del evento, no global.
	otro, err := store.CreateEvent(ctx, nuevoEvento(tenantID, "sess-hist", contactoA, "survey"))
	if err != nil {
		t.Fatalf("CreateEvent del otro evento: %v", err)
	}
	if s, aErr := store.AppendSummary(ctx, otro.ID, []byte(`{"answers":[]}`)); aErr != nil || s != 1 {
		t.Fatalf("AppendSummary del otro evento = (%d, %v); quiero (1, nil)", s, aErr)
	}
}

// TestIntegration_AppendSummaryEsNivel1EnClaro comprueba la mitad «resumen» del
// ADR-0034 §Decisión 1 tal como la impone conversation_event_messages_grade_chk:
// estructura EN CLARO, role='system' FIJO y NINGÚN cuerpo cifrado.
func TestIntegration_AppendSummaryEsNivel1EnClaro(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	store, _ := nuevoStore(t, db, time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC))

	ev := mustCrear(ctx, t, store, nuevoEvento(tenantID, "sess-grado-res", contactoA, "cart"))
	if _, err := store.AppendSummary(ctx, ev.ID, []byte(`{"lines":[{"sku":"burger","qty":1}]}`)); err != nil {
		t.Fatalf("AppendSummary: %v", err)
	}

	res := leerEntrada(ctx, t, db, ev.ID, 1)
	if res.entryKind != "summary" {
		t.Fatalf("entry_kind = %q; quiero summary", res.entryKind)
	}
	// El rol es FIJO, no un parámetro: toda fila emitida por nosotros va marcada
	// como nuestra, que es lo que evita la doble contabilidad (INV-11).
	if res.role != "system" {
		t.Fatalf("role = %q; quiero system", res.role)
	}
	if !res.payload.Valid {
		t.Fatalf("el resumen debe llevar payload en claro (nivel 1); llegó NULL")
	}
	if res.bodyEnc != nil || res.bodyDEK != nil || res.bodyKEKID.Valid {
		t.Fatalf("un resumen JAMÁS lleva cuerpo cifrado (CHECK de grado): %+v", res)
	}
}

// TestIntegration_AppendMessageEsNivel2Cifrado comprueba la mitad «mensaje»: sin
// payload en claro, con el envelope puesto y —lo que de verdad importa— con el
// literal ausente de la fila y recuperable solo con la KEK.
func TestIntegration_AppendMessageEsNivel2Cifrado(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	cipher := cipherDeTest(t)
	store := events.NewStore(db, cipher, events.WithClock(func() time.Time {
		return time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC)
	}))

	ev := mustCrear(ctx, t, store, nuevoEvento(tenantID, "sess-grado-msg", contactoA, "cart"))
	const literal = "deposítamelo a la cuenta XYZ, soy Herminia"
	if _, err := store.AppendMessage(ctx, ev.ID, events.RoleClient, literal); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	msg := leerEntrada(ctx, t, db, ev.ID, 1)
	if msg.entryKind != "message" || msg.role != "client" {
		t.Fatalf("el mensaje quedó como (%s, %s); quiero (message, client)", msg.entryKind, msg.role)
	}
	if msg.payload.Valid {
		t.Fatalf("un mensaje JAMÁS lleva payload en claro (CHECK de grado): %q", msg.payload.String)
	}
	if len(msg.bodyEnc) == 0 || len(msg.bodyDEK) == 0 || !msg.bodyKEKID.Valid {
		t.Fatalf("el mensaje debe traer body_enc/body_dek/body_kek_id: %+v", msg)
	}
	// La aserción que caza un «cifrado» que no cifra: el literal, tal cual, dentro
	// del blob.
	if bytes.Contains(msg.bodyEnc, []byte(literal)) {
		t.Fatalf("el literal está EN CLARO dentro de body_enc: no se cifró nada")
	}
	// Y la simétrica, que caza el ruido: lo guardado se abre y ES el literal.
	claro, err := cipher.Decrypt(msg.bodyEnc, msg.bodyDEK, msg.bodyKEKID.String)
	if err != nil {
		t.Fatalf("Decrypt del cuerpo: %v", err)
	}
	if claro != literal {
		t.Fatalf("descifrado = %q; quiero %q", claro, literal)
	}
}

// TestIntegration_AppendSummaryRechazaProsa comprueba que la puerta del nivel 1 no
// acepta texto libre: si no es JSON, no entra. Es lo que impide que alguien
// «resuma» pegando la frase del cliente en la columna EN CLARO.
func TestIntegration_AppendSummaryRechazaProsa(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	store, _ := nuevoStore(t, db, time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC))

	ev, err := store.CreateEvent(ctx, nuevoEvento(tenantID, "sess-prosa", contactoA, "cart"))
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	if _, err := store.AppendSummary(ctx, ev.ID, []byte("hola Herminia, quería una hamburguesa")); !errors.Is(err, events.ErrSummaryNotJSON) {
		t.Fatalf("AppendSummary con prosa = %v; quiero ErrSummaryNotJSON", err)
	}
	if n := contarEntradas(ctx, t, db, ev.ID); n != 0 {
		t.Fatalf("el rechazo escribió %d entradas; no debe escribir ninguna", n)
	}
}

// TestIntegration_AppendMessageSinCipherNoDegrada comprueba que la ausencia de
// cifrado CIERRA la puerta en vez de abrir una en claro.
func TestIntegration_AppendMessageSinCipherNoDegrada(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	store := events.NewStore(db, nil, events.WithClock(func() time.Time {
		return time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC)
	}))

	ev, err := store.CreateEvent(ctx, nuevoEvento(tenantID, "sess-nocipher", contactoA, "cart"))
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	if _, err := store.AppendMessage(ctx, ev.ID, events.RoleClient, "texto que no debe persistirse"); !errors.Is(err, events.ErrNoCipher) {
		t.Fatalf("AppendMessage sin cipher = %v; quiero ErrNoCipher", err)
	}
	if n := contarEntradas(ctx, t, db, ev.ID); n != 0 {
		t.Fatalf("sin cipher se escribieron %d entradas; no debe escribirse ninguna", n)
	}
	if _, err := store.AppendMessage(ctx, ev.ID, "operador", "rol inventado"); !errors.Is(err, events.ErrInvalidRole) {
		t.Fatalf("AppendMessage con rol desconocido = %v; quiero ErrInvalidRole", err)
	}
}

// ── T1.4 · La frontera del grado: la impone la BD, no el store ───────────────

// insertarEntradaCruda mete una entrada del historial con SQL directo,
// saltándose el store: es la única forma de intentar las combinaciones que la API
// de Go no deja expresar. Lo que se prueba aquí es el CHECK, no mi validación.
func insertarEntradaCruda(ctx context.Context, db *sql.DB, eventID string, seq int,
	role, entryKind string, payload any, bodyEnc, bodyDEK []byte) error {
	var kekID any
	if len(bodyEnc) > 0 {
		kekID = "k1"
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO public.conversation_event_messages
		        (event_id, seq, role, entry_kind, payload, body_enc, body_dek, body_kek_id)
		 VALUES ($1::uuid, $2, $3, $4, $5::jsonb, $6, $7, $8)`,
		eventID, seq, role, entryKind, payload, bodyEnc, bodyDEK, kekID)
	return err
}

// TestIntegration_ElGradoLoImponeLaBD fija la frontera del ADR-0034 §Decisión 1
// donde de verdad vive: en conversation_event_messages_grade_chk.
//
// Los tests del store comprueban que MIS métodos escriben en el nivel correcto;
// este comprueba que aunque alguien escriba por fuera —otro paquete, una ola
// futura, un script— la BD sigue diciendo que no. Es la invariante que se rompe
// sola dentro de seis meses si nadie la fija: basta con que una tarea posterior
// añada un AppendDecision descuidado.
func TestIntegration_ElGradoLoImponeLaBD(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	store, _ := nuevoStore(t, db, time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC))
	ev := mustCrear(ctx, t, store, nuevoEvento(tenantID, "sess-check", contactoA, "cart"))

	cifrado := []byte("nonce||ciphertext||tag de mentira")
	estructura := `{"lines":[{"sku":"burger","qty":1}]}`

	casos := []struct {
		nombre      string
		seq         int
		role        string
		entryKind   string
		payload     any
		bodyEnc     []byte
		quiereFallo bool
	}{
		// Prohibido: el nivel 1 no lleva cuerpo cifrado. Un resumen o una decisión
		// no son texto libre, así que no hay nada que cifrar — y si lo hay, es que
		// se está colando prosa por la puerta equivocada.
		{"un summary con cuerpo cifrado", 1, "system", "summary", nil, cifrado, true},
		{"una decision con cuerpo cifrado", 2, "client", "decision", nil, cifrado, true},
		{"un summary con payload Y cuerpo cifrado", 3, "system", "summary", estructura, cifrado, true},
		// Prohibido: el nivel 2 no lleva payload en claro. El literal jamás se cuela
		// en la columna consultable por SQL.
		{"un message con payload en claro", 4, "client", "message", estructura, nil, true},
		{"un message con payload Y cuerpo cifrado", 5, "client", "message", estructura, cifrado, true},
		// Permitido: cada grado en su nivel.
		{"un summary con solo payload", 6, "system", "summary", estructura, nil, false},
		{"una decision con solo payload", 7, "client", "decision", estructura, nil, false},
		{"un message con solo cuerpo cifrado", 8, "client", "message", nil, cifrado, false},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			err := insertarEntradaCruda(ctx, db, ev.ID, c.seq, c.role, c.entryKind, c.payload, c.bodyEnc, c.bodyEnc)
			if !c.quiereFallo {
				if err != nil {
					t.Fatalf("debería entrar y la BD lo rechazó: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("la BD ACEPTÓ una fila que viola el grado del ADR-0034: el CHECK no está imponiendo nada")
			}
			// No basta con que falle: tiene que fallar por ESTE motivo. Un NOT NULL
			// o un FK cualquiera también darían error y el test pasaría en falso.
			if !strings.Contains(err.Error(), "conversation_event_messages_grade_chk") {
				t.Fatalf("falló, pero no por el CHECK de grado: %v", err)
			}
		})
	}
}

// ── Ayudas de lectura del historial ──────────────────────────────────────────

type entradaCruda struct {
	role      string
	entryKind string
	payload   sql.NullString
	bodyEnc   []byte
	bodyDEK   []byte
	bodyKEKID sql.NullString
}

func leerEntrada(ctx context.Context, t *testing.T, db *sql.DB, eventID string, seq int) entradaCruda {
	t.Helper()
	var e entradaCruda
	err := db.QueryRowContext(ctx,
		`SELECT role, entry_kind, payload::text, body_enc, body_dek, body_kek_id
		   FROM public.conversation_event_messages WHERE event_id = $1 AND seq = $2`,
		eventID, seq).Scan(&e.role, &e.entryKind, &e.payload, &e.bodyEnc, &e.bodyDEK, &e.bodyKEKID)
	if err != nil {
		t.Fatalf("leer entrada seq=%d del evento %s: %v", seq, eventID, err)
	}
	return e
}

func leerSeqs(ctx context.Context, t *testing.T, db *sql.DB, eventID string) []int {
	t.Helper()
	rows, err := db.QueryContext(ctx,
		`SELECT seq FROM public.conversation_event_messages WHERE event_id = $1 ORDER BY seq`, eventID)
	if err != nil {
		t.Fatalf("listar seqs: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Logf("cerrar filas de seqs: %v", cerr)
		}
	}()
	var out []int
	for rows.Next() {
		var s int
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("escanear seq: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("recorrer seqs: %v", err)
	}
	return out
}

func contarEntradas(ctx context.Context, t *testing.T, db *sql.DB, eventID string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.conversation_event_messages WHERE event_id = $1`, eventID).Scan(&n); err != nil {
		t.Fatalf("contar entradas: %v", err)
	}
	return n
}

// ids concatena los ids en orden para comparar listados de un vistazo.
func ids(evs []events.Event) string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.ID)
	}
	return joinComa(out)
}

func joinComa(ss []string) string {
	var b bytes.Buffer
	for i, s := range ss {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s)
	}
	return b.String()
}
