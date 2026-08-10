package intakes_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// El tenant de estos tests es propio para no pisarse con los fixtures de listado,
// que limpian por tenant.
const tenantSeña = "dddddddd-dddd-dddd-dddd-dddddddddddd"

// filaDeSeña siembra UNA solicitud con sus marcas de seña tal como quedan en la
// tabla. `dueAt` y `remindedAt` nil ⇒ NULL (la mayoría de las solicitudes reales).
// Limpia el tenant entero al terminar.
func filaDeSeña(t *testing.T, db *sql.DB, id, contactID, status string, dueAt, remindedAt any) {
	t.Helper()
	// Cadena tenant→evento→solicitud de la 0054 (ver seedPG): sin padre declarado,
	// el INSERT —y cualquier transición posterior de estas filas— revienta contra
	// el CHECK intakes_event_id_required_chk.
	ensureTenantPG(t, db, tenantSeña)
	eventID := seedEventoPG(t, db, tenantSeña, "cancelled")
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO public.intakes
			(id, tenant_id, contact_id, session_id, status, total, event_id, created_at, updated_at,
			 deposit_due_at, deposit_reminded_at)
		VALUES ($1, $2, $3, 'sess-negocio', $4, 18000, $5, now(), now(), $6, $7)
	`, id, tenantSeña, contactID, status, eventID, dueAt, remindedAt); err != nil {
		t.Fatalf("sembrando la solicitud %s: %v", id, err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.intakes WHERE tenant_id = $1`, tenantSeña); err != nil {
			t.Logf("limpiando solicitudes: %v", err)
		}
	})
}

// TestSeñaEnBD_PedirLaSeñaFijaElPlazoEnLaMismaEscritura: la transición a
// deposit_requested deja deposit_due_at = now() + deposit_due_days SIN un segundo
// paso. Es lo único de T4.4 que no puede probar un doble: que make_interval y el
// now() de la transacción hagan lo que se cree, contra las columnas de la 0045.
func TestSeñaEnBD_PedirLaSeñaFijaElPlazoEnLaMismaEscritura(t *testing.T) {
	db := openTestDB(t)
	id := "44444444-0000-0000-0000-000000000001"
	filaDeSeña(t, db, id, "contacto-opaco-1", intakes.StatusConfirmed, nil, nil)
	señaEnBD(t, db, tenantSeña, plantillaDeSeña, 5)

	st := intakes.NewPostgres(db)
	updated, err := st.UpdateStatus(context.Background(), tenantSeña, id,
		intakes.StatusDepositRequested, intakes.StoredVariants(intakes.StatusConfirmed))
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	if updated.DepositDueAt.IsZero() {
		t.Fatal("deposit_due_at quedó NULL: el plazo tiene que fijarse en la MISMA escritura del estado")
	}
	plazo := updated.DepositDueAt.Sub(time.Now().UTC())
	if plazo < 4*24*time.Hour || plazo > 5*24*time.Hour+time.Minute {
		t.Fatalf("plazo = %v, quiero ~5 días (deposit_due_days del tenant)", plazo)
	}
	if !updated.DepositRemindedAt.IsZero() {
		t.Fatal("una seña recién pedida no puede venir con recordatorio marcado")
	}
}

// TestSeñaEnBD_SinFilaDeConfigElPlazoEsElPorDefecto: un tenant que nunca configuró
// nada no es un error (mismo criterio que NotifySettings) — se le fija el plazo por
// defecto. Sin esto, pedir seña reventaría justo en el tenant recién nacido.
func TestSeñaEnBD_SinFilaDeConfigElPlazoEsElPorDefecto(t *testing.T) {
	db := openTestDB(t)
	id := "44444444-0000-0000-0000-000000000002"
	filaDeSeña(t, db, id, "contacto-opaco-1", intakes.StatusConfirmed, nil, nil)

	updated, err := intakes.NewPostgres(db).UpdateStatus(context.Background(), tenantSeña, id,
		intakes.StatusDepositRequested, intakes.StoredVariants(intakes.StatusConfirmed))
	if err != nil {
		t.Fatalf("UpdateStatus sin fila de config: %v", err)
	}
	plazo := updated.DepositDueAt.Sub(time.Now().UTC())
	quiero := time.Duration(intakes.DefaultDepositDueDays) * 24 * time.Hour
	if plazo < quiero-time.Hour || plazo > quiero+time.Minute {
		t.Fatalf("plazo = %v, quiero ~%v (el default)", plazo, quiero)
	}
}

// TestSeñaEnBD_ElCASRepartUnSoloRecordatorio: la garantía de «un solo recordatorio»
// contra el SQL real. La primera llamada gana y escribe la marca; la segunda no gana
// y no escribe. Entre las dos no hay ningún `if` de Go que pueda saltarse.
func TestSeñaEnBD_ElCASRepartUnSoloRecordatorio(t *testing.T) {
	db := openTestDB(t)
	id := "44444444-0000-0000-0000-000000000003"
	vencida := time.Now().UTC().Add(-2 * time.Hour)
	filaDeSeña(t, db, id, "contacto-opaco-1", intakes.StatusDepositRequested, vencida, nil)

	st := intakes.NewPostgres(db)
	ahora := time.Now().UTC()

	primera, ganó, err := st.MarkDepositReminded(context.Background(), tenantSeña, id, ahora)
	if err != nil {
		t.Fatalf("primer MarkDepositReminded: %v", err)
	}
	if !ganó {
		t.Fatal("la primera llamada tiene que ganar: la seña está vencida y sin recordar")
	}
	if primera.DepositRemindedAt.IsZero() {
		t.Fatal("la fila devuelta por el CAS tiene que traer la marca puesta")
	}

	_, ganó, err = st.MarkDepositReminded(context.Background(), tenantSeña, id, ahora)
	if err != nil {
		t.Fatalf("segundo MarkDepositReminded: %v", err)
	}
	if ganó {
		t.Fatal("la segunda llamada NO puede ganar: sería un segundo WhatsApp al mismo cliente")
	}
}

// TestSeñaEnBD_ElCASNoCasaLoQueNoToca barre las tres formas de "no procede" contra
// el SQL: la seña ya resuelta (el cliente pagó), la que aún no vence y la que no
// tiene fecha. Ninguna es un error: son `false` sin error.
func TestSeñaEnBD_ElCASNoCasaLoQueNoToca(t *testing.T) {
	db := openTestDB(t)
	ahora := time.Now().UTC()
	casos := []struct {
		nombre string
		id     string
		status string
		dueAt  any
	}{
		{"ya pagó la seña", "44444444-0000-0000-0000-000000000004", intakes.StatusDepositPaid, ahora.Add(-2 * time.Hour)},
		{"todavía no vence", "44444444-0000-0000-0000-000000000005", intakes.StatusDepositRequested, ahora.Add(48 * time.Hour)},
		{"sin fecha límite", "44444444-0000-0000-0000-000000000006", intakes.StatusDepositRequested, nil},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			filaDeSeña(t, db, c.id, "contacto-opaco-1", c.status, c.dueAt, nil)

			_, ganó, err := intakes.NewPostgres(db).MarkDepositReminded(
				context.Background(), tenantSeña, c.id, ahora)
			if err != nil {
				t.Fatalf("MarkDepositReminded: %v", err)
			}
			if ganó {
				t.Fatal("no procedía recordar y el CAS lo dio por bueno")
			}
		})
	}
}

// TestSeñaEnBD_PendientesDelContacto: la consulta del toque del mensaje entrante,
// que es la que corre en el camino caliente del motor. Tiene que traer SOLO lo del
// contacto que habló, solo lo vencido y sin recordar, y respetar la cota.
func TestSeñaEnBD_PendientesDelContacto(t *testing.T) {
	db := openTestDB(t)
	ahora := time.Now().UTC()
	vieja := "44444444-0000-0000-0000-000000000007"
	nueva := "44444444-0000-0000-0000-000000000008"

	filaDeSeña(t, db, vieja, "contacto-que-habla", intakes.StatusDepositRequested, ahora.Add(-72*time.Hour), nil)
	filaDeSeña(t, db, nueva, "contacto-que-habla", intakes.StatusDepositRequested, ahora.Add(-1*time.Hour), nil)
	// Ruido que NO puede salir: de otro contacto, ya recordada, y sin vencer.
	filaDeSeña(t, db, "44444444-0000-0000-0000-000000000009", "otro-contacto", intakes.StatusDepositRequested, ahora.Add(-5*time.Hour), nil)
	filaDeSeña(t, db, "44444444-0000-0000-0000-00000000000a", "contacto-que-habla", intakes.StatusDepositRequested, ahora.Add(-5*time.Hour), ahora)
	filaDeSeña(t, db, "44444444-0000-0000-0000-00000000000b", "contacto-que-habla", intakes.StatusDepositRequested, ahora.Add(5*time.Hour), nil)

	st := intakes.NewPostgres(db)
	pend, err := st.PendingDepositReminders(context.Background(), tenantSeña, "contacto-que-habla", ahora, 10)
	if err != nil {
		t.Fatalf("PendingDepositReminders: %v", err)
	}
	if len(pend) != 2 {
		t.Fatalf("pendientes = %d, quiero 2 (las otras tres son ruido)", len(pend))
	}
	// Lo más vencido primero: con la cota de un recordatorio por toque, a quien lleva
	// más tiempo esperando se le avisa antes.
	if pend[0].ID != vieja || pend[1].ID != nueva {
		t.Fatalf("orden = [%s %s], quiero lo más vencido primero", pend[0].ID, pend[1].ID)
	}

	acotadas, err := st.PendingDepositReminders(context.Background(), tenantSeña, "contacto-que-habla", ahora, 1)
	if err != nil {
		t.Fatalf("PendingDepositReminders acotada: %v", err)
	}
	if len(acotadas) != 1 || acotadas[0].ID != vieja {
		t.Fatalf("con limit=1 quiero solo la más vencida, tengo %d", len(acotadas))
	}
}

// TestSeñaEnBD_ElÍndiceParcialExiste: el toque del entrante corre en el camino
// caliente de CADA mensaje, así que su índice no es un lujo. Se comprueba que la
// 0045 lo dejó puesto — un `CREATE INDEX` que nadie verifica es un índice que
// desaparece en el próximo replay sin que nadie lo note.
func TestSeñaEnBD_ElÍndiceParcialExiste(t *testing.T) {
	db := openTestDB(t)

	var def string
	if err := db.QueryRowContext(context.Background(),
		`SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND indexname = 'idx_intakes_deposit_pending'`,
	).Scan(&def); err != nil {
		t.Fatalf("el índice del recordatorio no está en la base: %v", err)
	}
	if !strings.Contains(def, "deposit_reminded_at IS NULL") {
		t.Fatalf("el índice existe pero no es el PARCIAL que se quería: %s", def)
	}
}
