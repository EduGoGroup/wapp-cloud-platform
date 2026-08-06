package intakes_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// solicitudConLínea siembra una solicitud del tenant en el estado dado, con UNA
// línea de catálogo de 1 × 18000, y devuelve el store y el id. Es el punto de
// partida de todos los tests de envío: un pedido normal al que aún no le han
// puesto el reparto.
func solicitudConLínea(t *testing.T, db *sql.DB, tenant, status string) (*intakes.Postgres, string) {
	t.Helper()
	id := uuid.NewString()
	seedPG(t, db, tenant, []fixture{{id, status, "sess-a", 1}})
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO public.intake_items (intake_id, sku, label, qty, unit_price)
		VALUES ($1, 'torta-v1', 'Torta 10-12 porciones', 1, 18000)
	`, id); err != nil {
		t.Fatalf("sembrando la línea de catálogo: %v", err)
	}
	return intakes.NewPostgres(db), id
}

// zonasEnBD escribe tenant_settings.shipping_zones tal cual lo haría el tenant.
// Limpia su fila al terminar: tenant_settings NO la borra seedPG.
func zonasEnBD(t *testing.T, db *sql.DB, tenant, zonasJSON string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO public.tenant_settings (tenant_id, shipping_zones)
		VALUES ($1, $2::jsonb)
		ON CONFLICT (tenant_id) DO UPDATE SET shipping_zones = EXCLUDED.shipping_zones
	`, tenant, zonasJSON); err != nil {
		t.Fatalf("sembrando zonas de envío: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenant_settings WHERE tenant_id = $1`, tenant); err != nil {
			t.Logf("limpiando tenant_settings: %v", err)
		}
	})
}

// envíosEnBD CUENTA las líneas de envío de la solicitud contra la tabla real y
// devuelve, además, la etiqueta y el precio de la primera y el total de la
// cabecera. Contar es el punto: «hay línea» no es el criterio; «hay UNA» sí.
func envíosEnBD(t *testing.T, db *sql.DB, intakeID string) (int, string, float64, float64) {
	t.Helper()
	ctx := context.Background()

	var n int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM public.intake_items WHERE intake_id = $1 AND sku = $2
	`, intakeID, intakes.ShippingSKU).Scan(&n); err != nil {
		t.Fatalf("contando líneas de envío: %v", err)
	}

	var total float64
	if err := db.QueryRowContext(ctx,
		`SELECT total FROM public.intakes WHERE id = $1`, intakeID).Scan(&total); err != nil {
		t.Fatalf("leyendo el total: %v", err)
	}
	if n == 0 {
		return 0, "", 0, total
	}

	var (
		label string
		price float64
	)
	if err := db.QueryRowContext(ctx, `
		SELECT label, unit_price FROM public.intake_items
		WHERE intake_id = $1 AND sku = $2 ORDER BY id LIMIT 1
	`, intakeID, intakes.ShippingSKU).Scan(&label, &price); err != nil {
		t.Fatalf("leyendo la línea de envío: %v", err)
	}
	return n, label, price, total
}

// TestPostgres_EnsureShippingLine_IdempotenteContraLaTablaReal: el criterio de
// T4.3 demostrado donde importa. Se aplica TRES veces sobre la misma solicitud y
// se cuentan las filas de la tabla — no se lee el código.
func TestPostgres_EnsureShippingLine_IdempotenteContraLaTablaReal(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	store, id := solicitudConLínea(t, db, tenant, intakes.StatusOpen)
	zonasEnBD(t, db, tenant, `[{"code":"z1","label":"Providencia","price":3000}]`)
	ctx := context.Background()

	for i := range 3 {
		if err := store.EnsureShippingLine(ctx, tenant, id, intakes.ShippingAlways); err != nil {
			t.Fatalf("EnsureShippingLine (pasada %d): %v", i+1, err)
		}
	}

	n, label, price, total := envíosEnBD(t, db, id)
	if n != 1 {
		t.Fatalf("filas `_shipping` tras 3 pasadas: got %d, want 1", n)
	}
	if label != "Envío — Providencia" || price != 3000 {
		t.Errorf("línea=(%q, %v); quiero la de la zona configurada", label, price)
	}
	if total != 21000 {
		t.Fatalf("total=%v; quiero 18000 + 3000 UNA sola vez", total)
	}
}

// TestPostgres_SetStatus_PendingApprovalPoneElEnvío recorre el camino del
// endpoint: el servicio transiciona la solicitud a `pending_approval` y la línea
// tiene que estar puesta al volver, con el total ya coherente en la respuesta —la
// consola pinta ESE total sin releer.
func TestPostgres_SetStatus_PendingApprovalPoneElEnvío(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	store, id := solicitudConLínea(t, db, tenant, intakes.StatusOpen)
	zonasEnBD(t, db, tenant, `[{"code":"z1","label":"Providencia","price":3000}]`)
	svc := intakes.NewService(store)
	ctx := context.Background()

	updated, err := svc.SetStatus(ctx, tenant, id, intakes.StatusPendingApproval)
	if err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if updated.Status != intakes.StatusPendingApproval || updated.Total != 21000 {
		t.Fatalf("la transición devolvió status=%q total=%v; quiero pending_approval con 21000",
			updated.Status, updated.Total)
	}

	n, _, _, total := envíosEnBD(t, db, id)
	if n != 1 || total != 21000 {
		t.Fatalf("tras la transición: %d líneas de envío, total=%v", n, total)
	}

	// Y volver a asegurarla no la duplica ni mueve el total.
	if err := svc.EnsureShippingLine(ctx, tenant, id, intakes.ShippingAlways); err != nil {
		t.Fatalf("EnsureShippingLine tras la transición: %v", err)
	}
	if n, _, _, total := envíosEnBD(t, db, id); n != 1 || total != 21000 {
		t.Fatalf("tras re-asegurar: %d líneas, total=%v", n, total)
	}
}

// TestPostgres_EnsureShippingLine_CambioDeZona: el caso feo contra la tabla real.
// La línea ya existe cuando el tenant cambia de zona; lo que no puede quedar es un
// pedido con dos envíos sumados ni con el reparto viejo.
func TestPostgres_EnsureShippingLine_CambioDeZona(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	store, id := solicitudConLínea(t, db, tenant, intakes.StatusOpen)
	zonasEnBD(t, db, tenant, `[{"code":"z1","label":"Providencia","price":3000}]`)
	ctx := context.Background()

	if err := store.EnsureShippingLine(ctx, tenant, id, intakes.ShippingAlways); err != nil {
		t.Fatalf("EnsureShippingLine (zona vieja): %v", err)
	}
	zonasEnBD(t, db, tenant, `[{"code":"z2","label":"Puente Alto","price":5000}]`)
	if err := store.EnsureShippingLine(ctx, tenant, id, intakes.ShippingAlways); err != nil {
		t.Fatalf("EnsureShippingLine (zona nueva): %v", err)
	}

	n, label, price, total := envíosEnBD(t, db, id)
	if n != 1 {
		t.Fatalf("filas `_shipping` tras el cambio de zona: got %d, want 1", n)
	}
	if label != "Envío — Puente Alto" || price != 5000 {
		t.Errorf("línea=(%q, %v); el envío viejo no puede sobrevivir al cambio", label, price)
	}
	if total != 23000 {
		t.Fatalf("total=%v; quiero 18000 + 5000, sin rastro de los 3000 viejos", total)
	}
}

// TestPostgres_EnsureShippingLine_SinZonasYSinPolítica: un tenant sin zonas ni
// fila en tenant_settings. Con ShippingOnlyIfZones (cierre del carrito) no se toca
// nada; con ShippingAlways (presupuesto) sale la línea marcada a 0, que no mueve el
// total. Van juntos porque lo que se prueba es el contraste entre las dos
// políticas sobre el MISMO tenant sin configurar.
func TestPostgres_EnsureShippingLine_SinZonasYSinPolítica(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	store, id := solicitudConLínea(t, db, tenant, intakes.StatusOpen)
	ctx := context.Background()

	if err := store.EnsureShippingLine(ctx, tenant, id, intakes.ShippingOnlyIfZones); err != nil {
		t.Fatalf("EnsureShippingLine(OnlyIfZones): %v", err)
	}
	if n, _, _, total := envíosEnBD(t, db, id); n != 0 || total != 18000 {
		t.Fatalf("sin zonas el cierre del carrito no toca nada: n=%d total=%v", n, total)
	}

	if err := store.EnsureShippingLine(ctx, tenant, id, intakes.ShippingAlways); err != nil {
		t.Fatalf("EnsureShippingLine(Always): %v", err)
	}
	n, label, price, total := envíosEnBD(t, db, id)
	if n != 1 || label != intakes.ShippingPendingLabel || price != 0 {
		t.Fatalf("n=%d línea=(%q, %v); quiero UNA «%s» a 0", n, label, price, intakes.ShippingPendingLabel)
	}
	if total != 18000 {
		t.Fatalf("total=%v; una línea a 0 no mueve el dinero", total)
	}
}

// TestPostgres_EnsureShippingLine_NoPisaElPrecioDelDueño: sin zona configurada, el
// precio lo pone el dueño (v1, D-041.11). Re-presupuestar —confirmed →
// pending_approval, D-041.26— es una transición de rutina y NO puede devolver la
// línea a 0.
func TestPostgres_EnsureShippingLine_NoPisaElPrecioDelDueño(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	store, id := solicitudConLínea(t, db, tenant, intakes.StatusClosedLegacy)
	ctx := context.Background()

	if err := store.EnsureShippingLine(ctx, tenant, id, intakes.ShippingAlways); err != nil {
		t.Fatalf("EnsureShippingLine: %v", err)
	}
	// El dueño precifica a mano lo que salió «por confirmar». Se escribe la línea Y
	// el total de la cabecera porque eso es lo que hace una edición manual: quien
	// toca una línea cuadra el total (T4.10). Sembrar solo la línea probaría otra
	// cosa —si el ensure repara totales ajenos—, y no es lo que aquí se afirma.
	if _, err := db.ExecContext(ctx, `
		UPDATE public.intake_items SET unit_price = 2500 WHERE intake_id = $1 AND sku = $2
	`, id, intakes.ShippingSKU); err != nil {
		t.Fatalf("precificando a mano: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE public.intakes SET total = 20500 WHERE id = $1`, id); err != nil {
		t.Fatalf("cuadrando el total tras precificar: %v", err)
	}

	// La vuelta a pending_approval vuelve a asegurar la línea, en la misma
	// transacción de la transición.
	if _, err := intakes.NewService(store).SetStatus(ctx, tenant, id, intakes.StatusPendingApproval); err != nil {
		t.Fatalf("SetStatus(pending_approval): %v", err)
	}

	n, _, price, total := envíosEnBD(t, db, id)
	if n != 1 || price != 2500 {
		t.Fatalf("n=%d precio=%v; el precio del dueño manda mientras no haya zona", n, price)
	}
	if total != 20500 {
		t.Fatalf("total=%v; la transición no puede devolver el envío a 0", total)
	}
}

// TestPostgres_ÍndiceÚnicoDeEnvío: la garantía ESTRUCTURAL de la migración 0045.
// El código serializa por la cabecera y no puede duplicar la línea, pero si algún
// día un camino nuevo lo intentara, la BD lo rechaza en vez de dejar al cliente
// pagando el reparto dos veces.
func TestPostgres_ÍndiceÚnicoDeEnvío(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	store, id := solicitudConLínea(t, db, tenant, intakes.StatusOpen)
	ctx := context.Background()

	if err := store.EnsureShippingLine(ctx, tenant, id, intakes.ShippingAlways); err != nil {
		t.Fatalf("EnsureShippingLine: %v", err)
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO public.intake_items (intake_id, sku, label, qty, unit_price)
		VALUES ($1, $2, 'Envío colado por la puerta de atrás', 1, 9999)
	`, id, intakes.ShippingSKU)
	if !postgres.IsUniqueViolation(err) {
		t.Fatalf("insertar un segundo `_shipping` dio err=%v; quiero violación de unicidad", err)
	}

	// Y la línea legítima sigue intacta: el rechazo no dejó nada a medias.
	if n, _, _, _ := envíosEnBD(t, db, id); n != 1 {
		t.Fatalf("filas `_shipping`: got %d, want 1", n)
	}

	// El índice es PARCIAL: dos líneas del MISMO artículo de catálogo siguen
	// siendo legales (D-041.20 parte líneas justo para eso).
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.intake_items (intake_id, sku, label, qty, unit_price)
		VALUES ($1, 'torta-v1', 'Torta 10-12 porciones', 1, 18000)
	`, id); err != nil {
		t.Fatalf("el índice no puede morder a las líneas de catálogo: %v", err)
	}
}
