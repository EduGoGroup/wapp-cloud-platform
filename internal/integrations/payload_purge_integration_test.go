// payload_purge_integration_test.go verifica contra Postgres real las dos mitades
// de la política del 2026-08-08: una entrega que llega a `delivered` deja de tener
// payload, y las filas que YA estaban entregadas cuando esto se decidió se limpian
// con la migración 0050.
//
// Va contra Postgres y no contra el fake por una razón que este repo ya aprendió
// dos veces: la mitad del arreglo vive en SQL —la sentencia de MarkWebhookDelivered
// y el UPDATE de la migración—, y un doble en memoria no puede acreditar SQL. Es el
// mismo criterio de buyerdata_integration_test.go (T4.5 del Plan 041): "cifrado en
// reposo" y "vaciado en reposo" solo se demuestran mirando la fila.
package integrations_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// notaEnLaCola es el literal que se busca como subcadena. Es una dirección: si
// sobrevive a la entrega, sobrevive en claro y para siempre (esta tabla no se poda).
const notaEnLaCola = "dejarlo en porteria calle Mayor 14 qqz"

// payloadDeCortesía es una plantilla verosímil CON la indicación del cliente
// dentro. Representa lo que las bases que corren desde las Olas 1-4 tienen hoy
// guardado: la nota viajaba congelada en el payload hasta la Ola 5.
func payloadDeCortesía() []byte {
	return []byte(`{"contract_version":"1","verb":"intake.push","tenant":"t-purga",` +
		`"contact":"c-opaco","intake_id":"i-1","lifecycle_status":"confirmed","revision_no":1,` +
		`"customer_note":"` + notaEnLaCola + `","items":[],"total":10}`)
}

func payloadDeLaFila(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var payload string
	if err := db.QueryRowContext(context.Background(),
		`SELECT payload::text FROM public.webhook_outbox WHERE id = $1`, id).Scan(&payload); err != nil {
		t.Fatalf("leer payload de la entrega %d: %v", id, err)
	}
	return payload
}

// TestMarkWebhookDelivered_VacíaElPayload: al cerrar la entrega en 2xx, la fila
// SOBREVIVE (es el recibo) pero su payload queda vacío. Las dos afirmaciones van
// juntas a propósito: vaciar no puede convertirse en borrar, porque entonces se
// perdería la traza de que la entrega ocurrió.
func TestMarkWebhookDelivered_VacíaElPayload(t *testing.T) {
	db := openTestDB(t)
	wipeWebhookTables(t, db)
	store := integrations.NewPostgres(db, cipherDePrueba(t))
	ctx := context.Background()

	id, err := store.EnqueueWebhook(ctx, "t-purga", "intake.push", payloadDeCortesía())
	if err != nil {
		t.Fatalf("encolar: %v", err)
	}
	if antes := payloadDeLaFila(t, db, id); !strings.Contains(antes, notaEnLaCola) {
		t.Fatalf("la fila recién encolada no trae el literal: el test no probaría nada:\n%s", antes)
	}

	batch, err := store.ClaimWebhookBatch(ctx, 10)
	if err != nil || len(batch) != 1 {
		t.Fatalf("ClaimWebhookBatch: len=%d err=%v", len(batch), err)
	}
	if err := store.MarkWebhookDelivered(ctx, batch[0]); err != nil {
		t.Fatalf("MarkWebhookDelivered: %v", err)
	}

	tras := payloadDeLaFila(t, db, id)
	if strings.Contains(tras, notaEnLaCola) {
		t.Fatalf("FUGA: la entrega quedó en delivered CON su payload intacto. Esta tabla no se "+
			"poda nunca, así que ese texto se queda en claro para siempre:\n%s", tras)
	}
	if tras != "{}" {
		t.Fatalf("payload tras entregar = %s, quiero {} (vacío, no un residuo parcial)", tras)
	}

	// El recibo sigue ahí: id, estado, intentos y momento del encolado.
	var status string
	var attempts int
	if err := db.QueryRowContext(ctx,
		`SELECT status, attempts FROM public.webhook_outbox WHERE id = $1`, id).Scan(&status, &attempts); err != nil {
		t.Fatalf("vaciar el payload NO puede borrar la fila (se perdería la traza): %v", err)
	}
	if status != integrations.StatusDelivered {
		t.Fatalf("status=%q, quiero delivered", status)
	}
}

// TestMarkWebhookDead_ConservaElPayload documenta la otra mitad de la decisión, la
// que NO es simétrica: una fila `dead` es la que el puente nunca recibió, y su
// payload es el único sitio donde queda qué se iba a entregar — lo que un operador
// necesita para diagnosticar o re-empujar a mano. En `delivered` la copia es
// redundante; en `dead` es el original.
//
// 🔴 ESTO YA NO ES CONDICIONAL, ES DEFINITIVO. Aquí decía «si el Plan 046 decide lo
// contrario al fijar la retención, este test es el que hay que cambiar»: ese plan
// DESCARTÓ la retención el 2026-08-20 (D-046.16, ADR-0043 — wApp no es sistema de
// registro contable ni fiscal), así que no hay decisión pendiente que pueda invalidar
// esta aserción. Las filas `dead` conservan su payload A PROPÓSITO: es lo único que le
// dice al operador qué no se entregó. Que nadie lo «arregle» después.
func TestMarkWebhookDead_ConservaElPayload(t *testing.T) {
	db := openTestDB(t)
	wipeWebhookTables(t, db)
	store := integrations.NewPostgres(db, cipherDePrueba(t))
	ctx := context.Background()

	id, err := store.EnqueueWebhook(ctx, "t-purga", "intake.push", payloadDeCortesía())
	if err != nil {
		t.Fatalf("encolar: %v", err)
	}
	batch, err := store.ClaimWebhookBatch(ctx, 10)
	if err != nil || len(batch) != 1 {
		t.Fatalf("ClaimWebhookBatch: len=%d err=%v", len(batch), err)
	}
	if err := store.MarkWebhookDead(ctx, batch[0], "el puente devolvió 500 diez veces"); err != nil {
		t.Fatalf("MarkWebhookDead: %v", err)
	}

	if tras := payloadDeLaFila(t, db, id); tras == "{}" {
		t.Fatal("una entrega DEAD se quedó sin payload: es lo único que dice qué no se entregó, " +
			"y sin ello el operador no puede ni diagnosticar ni re-empujar")
	}
}

// TestMigración0050_LimpiaLasFilasYaEntregadas cubre la mitad retroactiva: las
// bases que llevan corriendo desde las Olas 1-4 tienen filas `delivered` con su
// payload dentro (hay datos reales en Neon), y arreglar solo la transición las
// dejaría ahí para siempre.
//
// Fuerza el FULL-REPLAY del runner ensuciando el content_hash registrado: es el
// mismo mecanismo que dispara el replay en producción cuando cambia un
// structure/*.sql, y sin forzarlo Migrate no reejecutaría nada (isUpToDate exige
// versión Y hash, y openTestDB ya migró).
func TestMigración0050_LimpiaLasFilasYaEntregadas(t *testing.T) {
	db := openTestDB(t)
	wipeWebhookTables(t, db)
	ctx := context.Background()

	// Una fila ENTREGADA por el código viejo: delivered, con payload.
	var entregada int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO public.webhook_outbox (tenant_id, kind, payload, status)
		VALUES ('t-purga', 'intake.push', $1::jsonb, 'delivered')
		RETURNING id
	`, string(payloadDeCortesía())).Scan(&entregada); err != nil {
		t.Fatalf("sembrar fila delivered legada: %v", err)
	}
	// Y una PENDIENTE, que la migración no puede tocar: ahí el payload es lo que
	// todavía hay que entregar.
	var pendiente int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO public.webhook_outbox (tenant_id, kind, payload, status)
		VALUES ('t-purga', 'intake.push', $1::jsonb, 'pending')
		RETURNING id
	`, string(payloadDeCortesía())).Scan(&pendiente); err != nil {
		t.Fatalf("sembrar fila pending: %v", err)
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE public.schema_version SET content_hash = 'forzar-replay'
		WHERE id = (SELECT max(id) FROM public.schema_version)
	`); err != nil {
		t.Fatalf("ensuciar el content_hash para forzar el replay: %v", err)
	}
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("replay de las migraciones: %v", err)
	}

	if tras := payloadDeLaFila(t, db, entregada); strings.Contains(tras, notaEnLaCola) {
		t.Fatalf("FUGA HEREDADA: la 0050 no limpió las filas que ya estaban en delivered:\n%s", tras)
	}
	if tras := payloadDeLaFila(t, db, pendiente); !strings.Contains(tras, notaEnLaCola) {
		t.Fatalf("la 0050 vació una entrega PENDIENTE: se quedó sin lo que hay que entregar:\n%s", tras)
	}
}
