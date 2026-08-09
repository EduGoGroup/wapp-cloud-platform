// outbox_stats_integration_test.go verifica contra Postgres real lo único que un
// doble en memoria no puede demostrar de CountOutbox: que los agregados
// condicionales (COUNT(*) FILTER / MIN(created_at) FILTER) cuentan lo que se cree,
// que el WHERE tenant_id aísla de verdad (INV-8 en el SQL, no solo en el handler),
// y que un tenant sin ninguna fila devuelve ceros en vez de sql.ErrNoRows.
//
// Esa última es la que más fácil se rompería en silencio: si la consulta llevara
// un GROUP BY, un tenant con la cola vacía dejaría de devolver fila y la pantalla
// pasaría de decir «todo al día» a decir «no se pudo leer».
package integrations_test

import (
	"context"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations"
)

// TestCountOutbox_CuentaPorEstadoYAíslaPorTenant recorre la cola por los estados
// REALES —usando las transiciones del store, no UPDATEs a mano— para que lo que se
// cuenta sea exactamente lo que el worker deja escrito.
func TestCountOutbox_CuentaPorEstadoYAíslaPorTenant(t *testing.T) {
	db := openTestDB(t)
	wipeWebhookTables(t, db)
	store := integrations.NewPostgres(db, cipherDePrueba(t))
	ctx := context.Background()

	const (
		tenantA = "t-cola-a"
		tenantB = "t-cola-b"
	)
	// A: tres entregas; B: dos. Se encolan intercaladas a propósito, para que el
	// aislamiento no pueda pasar por casualidad de orden de ids.
	for _, tenant := range []string{tenantA, tenantB, tenantA, tenantB, tenantA} {
		if _, err := store.EnqueueWebhook(ctx, tenant, "intake.push", []byte(`{}`)); err != nil {
			t.Fatalf("encolar de %s: %v", tenant, err)
		}
	}

	// Se reclama TODO (el claim no filtra por tenant: es del worker, que sirve a todos) y se separan
	// las filas por dueño antes de resolverlas.
	claims, err := store.ClaimWebhookBatch(ctx, 10)
	if err != nil {
		t.Fatalf("reclamar: %v", err)
	}
	deA, deB := partirPorTenant(claims, tenantA)
	if len(deA) != 3 || len(deB) != 2 {
		t.Fatalf("reclamadas: A=%d B=%d, quiero 3 y 2", len(deA), len(deB))
	}

	// A: una entregada, una muerta tras agotar reintentos, y una que falló y VUELVE a la cola —esa
	// cuenta como pendiente, y su created_at (no su next_attempt_at, que queda en el futuro) es el
	// que tiene que asomar como antigüedad.
	exigeSinError(t, "A entregada", store.MarkWebhookDelivered(ctx, deA[0]))
	exigeSinError(t, "A muerta", store.MarkWebhookDead(ctx, deA[1], "502 del puente"))
	exigeSinError(t, "A fallida", store.MarkWebhookFailed(ctx, deA[2], time.Now().Add(time.Hour), "timeout"))

	// B: una entregada y otra que se queda EN VUELO (nadie la cierra).
	exigeSinError(t, "B entregada", store.MarkWebhookDelivered(ctx, deB[0]))

	colaA, err := store.CountOutbox(ctx, tenantA)
	if err != nil {
		t.Fatalf("contar A: %v", err)
	}
	exigeContadores(t, "A", colaA, integrations.OutboxCounts{Pending: 1, Delivered: 1, Dead: 1})
	if colaA.OldestPendingAt.IsZero() {
		t.Fatal("A tiene una entrega en cola: su antigüedad no puede venir en cero")
	}

	colaB, err := store.CountOutbox(ctx, tenantB)
	if err != nil {
		t.Fatalf("contar B: %v", err)
	}
	exigeContadores(t, "B", colaB, integrations.OutboxCounts{Delivering: 1, Delivered: 1})
	// Sin nada pendiente NO hay «la más vieja»: el MIN filtrado es NULL y tiene que
	// llegar como cero, que es lo que el DTO traduce a «no publiques el campo».
	if !colaB.OldestPendingAt.IsZero() {
		t.Fatalf("B no tiene nada en cola, pero la antigüedad vino a %s", colaB.OldestPendingAt)
	}
}

// TestCountOutbox_TenantSinFilas_DevuelveCeros: el agregado sin GROUP BY siempre
// trae una fila. «Nunca encoló nada» y «ya se entregó todo» responden lo mismo, y
// ninguno de los dos es un error.
func TestCountOutbox_TenantSinFilas_DevuelveCeros(t *testing.T) {
	db := openTestDB(t)
	wipeWebhookTables(t, db)
	store := integrations.NewPostgres(db, cipherDePrueba(t))

	cola, err := store.CountOutbox(context.Background(), "t-que-no-existe")
	if err != nil {
		t.Fatalf("contar un tenant sin filas devolvió error: %v", err)
	}
	if cola != (integrations.OutboxCounts{}) {
		t.Fatalf("cola=%+v, quiero todo a cero", cola)
	}
}

// TestCountOutbox_LaAntigüedadEsLaDeLaMásVieja: con varias en cola, la marca que
// sale es la de la PRIMERA que se encoló. Es lo que hace útil el campo — mide el
// retraso acumulado, no el de la última que entró.
func TestCountOutbox_LaAntigüedadEsLaDeLaMásVieja(t *testing.T) {
	db := openTestDB(t)
	wipeWebhookTables(t, db)
	store := integrations.NewPostgres(db, cipherDePrueba(t))
	ctx := context.Background()

	const tenant = "t-antiguedad"
	primera, err := store.EnqueueWebhook(ctx, tenant, "intake.push", []byte(`{}`))
	if err != nil {
		t.Fatalf("encolar la primera: %v", err)
	}
	// La primera se envejece a mano: es la única forma de tener dos created_at
	// distinguibles sin dormir el test.
	const hace6h = "6 hours"
	if _, err := db.ExecContext(ctx,
		`UPDATE public.webhook_outbox SET created_at = now() - $2::interval WHERE id = $1`,
		primera, hace6h); err != nil {
		t.Fatalf("envejecer la primera: %v", err)
	}
	if _, err := store.EnqueueWebhook(ctx, tenant, "intake.push", []byte(`{}`)); err != nil {
		t.Fatalf("encolar la segunda: %v", err)
	}

	cola, err := store.CountOutbox(ctx, tenant)
	if err != nil {
		t.Fatalf("contar: %v", err)
	}
	if cola.Pending != 2 {
		t.Fatalf("pending=%d, quiero 2", cola.Pending)
	}
	if espera := time.Since(cola.OldestPendingAt); espera < 5*time.Hour {
		t.Fatalf("la antigüedad es de %s: se tomó la de la entrega NUEVA, no la de la más vieja", espera)
	}
}

// exigeContadores compara los cuatro contadores (la antigüedad se comprueba
// aparte: es una marca de tiempo viva y no se puede fijar en la tabla esperada).
func exigeContadores(t *testing.T, quién string, got, want integrations.OutboxCounts) {
	t.Helper()
	got.OldestPendingAt = time.Time{}
	if got != want {
		t.Fatalf("cola de %s:\n got %+v\nwant %+v", quién, got, want)
	}
}

// partirPorTenant separa las filas reclamadas en las del tenant dado y las demás.
func partirPorTenant(filas []integrations.WebhookOutbox, tenant string) (propias, ajenas []integrations.WebhookOutbox) {
	for _, f := range filas {
		if f.TenantID == tenant {
			propias = append(propias, f)
			continue
		}
		ajenas = append(ajenas, f)
	}
	return propias, ajenas
}

// exigeSinError corta el test nombrando la transición que falló.
func exigeSinError(t *testing.T, qué string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", qué, err)
	}
}
