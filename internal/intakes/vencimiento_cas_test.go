package intakes_test

// vencimiento_cas_test.go — LA MISMA TABLA DE CASOS CONTRA LAS DOS IMPLEMENTACIONES
// DEL COMPARE-AND-SWAP (Plan 044 · Ola 4 · T4.5).
//
// El problema que resuelve: `MarkExpiryReminded` está escrito DOS veces —una en SQL
// (postgres.go) y otra en Go (memory.go)— y las dos tienen que decir exactamente lo
// mismo, porque los tests de conducta corren sobre la de memoria y quien atiende al
// dueño en producción es la otra. Una divergencia entre ellas es invisible: los tests
// siguen verdes describiendo un comportamiento que la base no tiene.
//
// La tabla se declara UNA vez y la recorren los dos runners. Añadir un caso los
// cubre a los dos; que uno de los dos deje de cumplirlo es, literalmente, la
// divergencia.
//
// ⚠️ HONESTIDAD SOBRE EL ALCANCE, PORQUE ES LA MITAD DE LA DECISIÓN. El runner de
// Postgres **se SALTA sin WAPP_TEST_DB_DSN**, así que en la corrida normal esta tabla
// solo prueba el store en memoria. NO es el candado de la sentencia: ése es
// vencimiento_sql_test.go, que corre siempre y afirma sobre el texto del SQL. Éste
// es su complemento —lo que ese candado no puede ver es la SEMÁNTICA: que Postgres
// entienda `<=` sobre timestamptz como el Go entiende su comparación—, y solo aporta
// donde hay base: en local con WAPP_TEST_DB_DSN y en el CI que la levanta.

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// tenantPlazo es propio de estos tests: los fixtures limpian por tenant y pisarse
// con los del listado o los de la seña dejaría filas de otro test decidiendo aquí.
const tenantPlazo = "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"

// casoCAS es una fila del mundo y lo que el compare-and-swap tiene que contestar
// sobre ella. Los desplazamientos van CONTRA el instante del toque para que el mismo
// caso valga en los dos stores sin sembrar fechas absolutas.
type casoCAS struct {
	nombre string
	estado string
	// esperaDesde es cuánto lleva la solicitud sin tocarse, en negativo respecto del
	// toque: -25h = updated_at hace 25 horas.
	esperaDesde time.Duration
	// yaAvisado pone expiry_reminded_at con valor (≠ NULL) antes del CAS.
	yaAvisado   bool
	quiereGanar bool
}

// casosDelCAS es la tabla, y su caso más importante es el CUARTO.
//
// 🔴 «vencido pero YA avisado ⇒ no gana» es exactamente la mutación que se coló:
// quitar `AND expiry_reminded_at IS NULL` del SQL de producción dejaba la suite
// entera en verde. Contra Postgres, este caso es el único que la caza.
func casosDelCAS() []casoCAS {
	return []casoCAS{
		{"vencido de sobra y sin avisar", intakes.StatusPendingApproval, -3 * intakes.QuoteDeadline, false, true},
		{"justo en el borde del plazo", intakes.StatusPendingApproval, -intakes.QuoteDeadline, false, true},
		{"todavía en plazo", intakes.StatusPendingApproval, -intakes.QuoteDeadline + time.Hour, false, false},
		{"🔴 vencido pero YA avisado", intakes.StatusPendingApproval, -3 * intakes.QuoteDeadline, true, false},
		{"el dueño ya aprobó", intakes.StatusConfirmed, -3 * intakes.QuoteDeadline, false, false},
		{"el dueño ya rechazó", intakes.StatusRejected, -3 * intakes.QuoteDeadline, false, false},
		{"el dueño pidió información", intakes.StatusNeedsInfo, -3 * intakes.QuoteDeadline, false, false},
		{"cancelado", intakes.StatusCancelled, -3 * intakes.QuoteDeadline, false, false},
		{"abierto: ni siquiera es un presupuesto", intakes.StatusOpen, -3 * intakes.QuoteDeadline, false, false},
	}
}

// TestPlazoCAS_ElStoreEnMemoriaCumpleLaTabla corre la tabla contra MemoryStore. Es el
// que se ejecuta SIEMPRE, y el que sostiene que los 24 tests de conducta del plazo
// describen algo coherente.
func TestPlazoCAS_ElStoreEnMemoriaCumpleLaTabla(t *testing.T) {
	for _, c := range casosDelCAS() {
		t.Run(c.nombre, func(t *testing.T) {
			const id = "77777777-0000-4000-8000-000000000001"
			st := intakes.NewMemoryStore()
			st.SetClock(func() time.Time { return ahora })
			in := intakes.Intake{
				ID: id, ContactID: "contacto-opaco-1", SessionID: "sess-negocio",
				Status: c.estado, Total: 21000,
				CreatedAt: ahora.Add(c.esperaDesde), UpdatedAt: ahora.Add(c.esperaDesde),
			}
			if c.yaAvisado {
				in.ExpiryRemindedAt = ahora.Add(-time.Hour)
			}
			st.Add(tenantPlazo, in)

			comprobarCAS(t, st, tenantPlazo, id, c)

			// Y la fila no se movió: ni de estado ni de fecha.
			d, err := st.Get(context.Background(), tenantPlazo, id)
			if err != nil {
				t.Fatalf("releer: %v", err)
			}
			exigirFilaQuieta(t, d.Intake, c)
		})
	}
}

// TestVencimientoEnBD_ElPostgresCumpleLaMISMATabla es el runner contra Postgres. Se
// SALTA sin WAPP_TEST_DB_DSN (openTestDB), y por eso NO es el candado de la sentencia —
// ver la nota de alcance en la cabecera de este fichero.
//
// Lo que aporta y el otro runner no puede: que el `<=` sobre timestamptz, el
// `status = ANY($4)` con las variantes legadas y el `IS NULL` de la columna nueva de
// la 0081 se comporten DE VERDAD como el store en memoria finge que se comportan.
func TestVencimientoEnBD_ElPostgresCumpleLaMISMATabla(t *testing.T) {
	db := openTestDB(t)
	st := intakes.NewPostgres(db)

	for i, c := range casosDelCAS() {
		t.Run(c.nombre, func(t *testing.T) {
			id := idDePruebaPlazo(i)
			var avisado any
			if c.yaAvisado {
				avisado = ahora.Add(-time.Hour)
			}
			filaDePresupuesto(t, db, id, c.estado, ahora.Add(c.esperaDesde), avisado)

			comprobarCAS(t, st, tenantPlazo, id, c)

			d, err := st.Get(context.Background(), tenantPlazo, id)
			if err != nil {
				t.Fatalf("releer de la BD: %v", err)
			}
			exigirFilaQuieta(t, d.Intake, c)
		})
	}
}

// TestVencimientoEnBD_DosVecesSeguidasSoloGanaLaPrimera es lo que la tabla no puede
// afirmar por sí sola: la idempotencia como SECUENCIA sobre la MISMA fila, contra la
// base de verdad. La tabla siembra el `ya avisado`; esto lo produce.
//
// Es el complemento exacto del candado de la sentencia: aquél comprueba que la
// cláusula está escrita, éste que hace lo que dice.
func TestVencimientoEnBD_DosVecesSeguidasSoloGanaLaPrimera(t *testing.T) {
	db := openTestDB(t)
	st := intakes.NewPostgres(db)
	id := idDePruebaPlazo(90)
	filaDePresupuesto(t, db, id, intakes.StatusPendingApproval, ahora.Add(-3*intakes.QuoteDeadline), nil)

	marcada, ganó, err := st.MarkExpiryReminded(context.Background(), tenantPlazo, id, ahora)
	if err != nil {
		t.Fatalf("primer CAS: %v", err)
	}
	if !ganó {
		t.Fatal("el PRIMER compare-and-swap no ganó sobre un presupuesto vencido y sin avisar")
	}
	if marcada.ExpiryRemindedAt.IsZero() {
		t.Fatal("el CAS ganador devolvió la fila SIN la marca puesta: el llamante no sabría con qué avisar")
	}

	_, ganóOtraVez, err := st.MarkExpiryReminded(context.Background(), tenantPlazo, id, ahora.Add(time.Hour))
	if err != nil {
		t.Fatalf("segundo CAS: %v", err)
	}
	if ganóOtraVez {
		t.Fatal("🔴 el SEGUNDO compare-and-swap también ganó. Falta `AND expiry_reminded_at IS NULL` " +
			"en la sentencia: cada lectura del dueño volvería a avisar y el recordatorio se " +
			"convierte en un goteo")
	}
}

// comprobarCAS ejecuta el compare-and-swap y contrasta con lo que la tabla espera.
// Es lo COMPARTIDO por los dos runners: si esta función se duplicara, los dos podrían
// exigir cosas distintas y la tabla dejaría de significar nada.
func comprobarCAS(t *testing.T, st intakes.ExpiryStore, tenantID, id string, c casoCAS) {
	t.Helper()
	marcada, ganó, err := st.MarkExpiryReminded(context.Background(), tenantID, id, ahora)
	if err != nil {
		t.Fatalf("MarkExpiryReminded: %v", err)
	}
	if ganó != c.quiereGanar {
		t.Fatalf("ganó = %v, quiero %v (estado=%s, esperando=%v, ya avisado=%v).\n\n"+
			"Las DOS implementaciones del compare-and-swap —el SQL de postgres.go y el Go de "+
			"memory.go— tienen que contestar lo mismo a esta tabla. Si solo una falla, acaban de "+
			"divergir, y la que atiende al dueño en producción es la de SQL",
			ganó, c.quiereGanar, c.estado, c.esperaDesde, c.yaAvisado)
	}
	if ganó && marcada.ExpiryRemindedAt.IsZero() {
		t.Fatal("el CAS dijo que ganó pero devolvió la fila sin marca: el aviso saldría sobre una " +
			"versión de la solicitud que no es la que quedó escrita")
	}
	if !ganó && !marcada.ExpiryRemindedAt.IsZero() {
		t.Fatal("el CAS dijo que NO ganó pero devolvió una fila poblada: `false` significa que no " +
			"le tocaba, y su Intake no significa nada")
	}
}

// exigirFilaQuieta es la otra mitad, y vale para los dos runners: ganar el CAS no
// puede mover el estado ni la fecha de la solicitud.
//
// 🔴 Lo de `UpdatedAt` no es higiene: es la BASE del plazo. Si el CAS la tocara, la
// marca «vencido» de la bandeja se apagaría en el mismo instante del aviso.
func exigirFilaQuieta(t *testing.T, in intakes.Intake, c casoCAS) {
	t.Helper()
	if in.Status != intakes.NormalizeStatus(c.estado) {
		t.Errorf("status tras el CAS = %q, quiero %q: un recordatorio no transiciona nada "+
			"(D-041.16, nada muere por tiempo)", in.Status, intakes.NormalizeStatus(c.estado))
	}
	quiero := ahora.Add(c.esperaDesde)
	if !in.UpdatedAt.Equal(quiero) {
		t.Errorf("updated_at tras el CAS = %v, quiero %v (intacto).\n\n"+
			"Es la BASE del plazo: tocarla aquí reinicia el plazo que este mismo UPDATE acaba de "+
			"constatar como vencido, y la marca de la bandeja se apaga justo al encenderse",
			in.UpdatedAt.UTC(), quiero.UTC())
	}
}

// idDePruebaPlazo fabrica un UUID distinto por caso: la tabla siembra una fila por
// subtest y dos casos con el mismo id se pisarían. El último grupo de un UUID son 12
// dígitos, y por eso el %012d — uno corto lo rechaza uuid.Parse y el CAS devolvería
// ErrNotFound sobre todos los casos a la vez.
func idDePruebaPlazo(n int) string {
	return fmt.Sprintf("77777777-0000-4000-8000-%012d", n)
}

// filaDePresupuesto siembra en la BD UNA solicitud con su updated_at y su marca de
// aviso tal como quedan en la tabla. Limpia el tenant al terminar.
//
// La cadena tenant→evento→solicitud de la 0054 es obligatoria: sin padre declarado el
// INSERT revienta contra intakes_event_id_required_chk. El evento va TERMINAL
// (`cancelled`) por lo mismo que en el fixture de la seña — aquí no se prueba nada
// sobre eventos vivos.
//
// ⚠️ created_at y updated_at se escriben con el MISMO valor y a mano, no con now():
// el plazo se mide contra updated_at y un `now()` haría que todos los casos nacieran
// en plazo.
func filaDePresupuesto(t *testing.T, db *sql.DB, id, status string, updatedAt time.Time, expiryRemindedAt any) {
	t.Helper()
	ensureTenantPG(t, db, tenantPlazo)
	eventID := seedEventoPG(t, db, tenantPlazo, "cancelled")
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO public.intakes
			(id, tenant_id, contact_id, session_id, status, total, event_id, created_at, updated_at,
			 expiry_reminded_at)
		VALUES ($1, $2, 'contacto-opaco-1', 'sess-negocio', $3, 21000, $4, $5, $5, $6)
	`, id, tenantPlazo, status, eventID, updatedAt, expiryRemindedAt); err != nil {
		t.Fatalf("sembrando el presupuesto %s: %v", id, err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.intakes WHERE tenant_id = $1`, tenantPlazo); err != nil {
			t.Logf("limpiando presupuestos: %v", err)
		}
	})
}
