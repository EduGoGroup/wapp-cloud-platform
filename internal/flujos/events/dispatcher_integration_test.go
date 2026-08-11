package events_test

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// planesCon devuelve los planes que traen una feature, leídos de plan_features.
func planesCon(t *testing.T, db *sql.DB, feature string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT plan_id FROM public.plan_features WHERE feature = $1 ORDER BY plan_id`, feature)
	if err != nil {
		t.Fatalf("leer plan_features de %q: %v", feature, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Logf("cerrando filas: %v", cerr)
		}
	}()

	planes := make([]string, 0, 5)
	for rows.Next() {
		var plan string
		if err := rows.Scan(&plan); err != nil {
			t.Fatalf("leer plan: %v", err)
		}
		planes = append(planes, plan)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("recorrer planes: %v", err)
	}
	return planes
}

// TestIntegration_LasFeaturesDeTipoCasanConLaSiembra ata el literal Go a lo que
// las migraciones sembraron DE VERDAD.
//
// Existe por un hallazgo concreto: romper a mano la constante FeatureSurvey a
// "surveys" no ponía nada rojo, porque hasta el despachador NADIE las consumía.
// Una clave que existe en la BD y no en el código no es un gate a medias: es
// ningún gate. Y al revés —una constante Go que no casa con ninguna fila— apaga
// un tipo del menú EN SILENCIO, sin error, sin log y sin nada que un operador
// pueda mirar.
//
// Por eso el assert es el conjunto EXACTO de planes y no «aparece en alguno»:
// que `media` naciera en commerce y `survey` en basic es una decisión de
// producto (migración 0053), y un cambio de reparto tiene que verse aquí.
func TestIntegration_LasFeaturesDeTipoCasanConLaSiembra(t *testing.T) {
	db := openTestDB(t)

	losCinco := []string{"advisor_ai", "advisor_ai_pro", "basic", "commerce", "pro"}
	sinBasic := []string{"advisor_ai", "advisor_ai_pro", "commerce", "pro"}

	quiero := []struct {
		feature string
		planes  []string
	}{
		{entitlements.FeatureMenu, losCinco},      // 0039: los cinco planes
		{entitlements.FeatureCartBasic, losCinco}, // 0039: los cinco planes
		{entitlements.FeatureSurvey, losCinco},    // 0053: nace en basic ⇒ los cinco
		{entitlements.FeatureMedia, sinBasic},     // 0053: nace en commerce ⇒ basic NO
	}

	for _, c := range quiero {
		got := planesCon(t, db, c.feature)
		if !slices.Equal(got, c.planes) {
			t.Fatalf("la feature %q está sembrada en %v; el reparto dice %v "+
				"(o la constante Go no casa con el SQL, o el reparto cambió sin actualizar esto)",
				c.feature, got, c.planes)
		}
	}
}

// seedTenantConPlan crea un tenant y le fija el plan.
func seedTenantConPlan(t *testing.T, db *sql.DB, plan string) string {
	t.Helper()
	tid := seedTenant(t, db)
	if _, err := db.ExecContext(context.Background(),
		`UPDATE public.tenants SET plan_id = $2 WHERE id = $1`, tid, plan); err != nil {
		t.Fatalf("fijar el plan %q del tenant: %v", plan, err)
	}
	return tid
}

// TestIntegration_ElMenuDeUnTenantBasicNoOfreceDocumentos recorre el camino
// COMPLETO con las piezas reales: reglas event_start en flow_triggers, derechos
// resueltos por el Resolver Postgres contra plan_features y el menú armado con
// ellos.
//
// Es el test que caza el typo por el lado del consumidor: si FeatureSurvey no
// casara con la clave sembrada, este tenant `basic` —que SÍ tiene encuesta—
// dejaría de verla y el menú saldría con una opción menos. Y `media`, que nace
// en `commerce`, no puede aparecer aquí ni aunque el tenant tenga la regla dada
// de alta: tener la palabra configurada no es tener el plan.
func TestIntegration_ElMenuDeUnTenantBasicNoOfreceDocumentos(t *testing.T) {
	db := openTestDB(t)
	tid := seedTenantConPlan(t, db, "basic")

	reglas := trigger.NewPostgresStore(db)
	for _, r := range []trigger.Rule{
		reglaEventStart(tid, "carrito", "cart"),
		reglaEventStart(tid, "encuesta", "survey"),
		reglaEventStart(tid, "documentos", "media"),
	} {
		if _, err := reglas.Insert(context.Background(), r); err != nil {
			t.Fatalf("sembrar regla %q: %v", r.Keyword, err)
		}
	}

	d := events.NewDispatcher(
		events.NewStore(db, nil),
		events.NewTriggerKindOffer(reglas),
		entitlements.NewPostgres(db),
	)
	m, err := d.Build(context.Background(), events.ConversationRef{
		TenantID:  tid,
		SessionID: "sesion-A",
		ContactID: "44444444-4444-4444-4444-444444444444",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if m.Unfiltered {
		t.Fatalf("un tenant con plan tiene derechos: el menú NO puede salir sin filtrar")
	}
	if got := kindsDe(m); len(got) != 2 || got[0] != "start:cart" || got[1] != "start:survey" {
		t.Fatalf("un tenant basic ofrece carrito y encuesta, no documentos; got %v", got)
	}
	if texto := m.Render(); !strings.Contains(texto, "encuesta") || strings.Contains(texto, "documentos") {
		t.Fatalf("el texto debe ofrecer la encuesta y no los documentos; texto:\n%s", texto)
	}
}

// TestIntegration_ElTenantSinTaxonomiaListaSinFiltro es la otra rama del gate
// contra BD real: un tenant cuyo plan no trae NINGUNA feature (aquí, un plan
// vacío sembrado a mano) lista sin filtro en vez de quedarse mudo, y lo reporta.
// Es la excepción que el plan pide para una instalación donde el Plan 040 nunca
// sembró taxonomía.
func TestIntegration_ElTenantSinTaxonomiaListaSinFiltro(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO public.plans (id, name) VALUES ('t23_sin_features', 'Sin features')
		 ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("sembrar plan sin features: %v", err)
	}
	tid := seedTenantConPlan(t, db, "t23_sin_features")

	reglas := trigger.NewPostgresStore(db)
	for _, r := range []trigger.Rule{
		reglaEventStart(tid, "carrito", "cart"),
		reglaEventStart(tid, "documentos", "media"),
	} {
		if _, err := reglas.Insert(ctx, r); err != nil {
			t.Fatalf("sembrar regla %q: %v", r.Keyword, err)
		}
	}

	d := events.NewDispatcher(
		events.NewStore(db, nil),
		events.NewTriggerKindOffer(reglas),
		entitlements.NewPostgres(db),
	)
	m, err := d.Build(ctx, events.ConversationRef{
		TenantID: tid, SessionID: "sesion-A", ContactID: "44444444-4444-4444-4444-444444444444",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !m.Unfiltered {
		t.Fatalf("sin taxonomía el menú debe reportar que se armó sin filtro")
	}
	if got := kindsDe(m); len(got) != 2 {
		t.Fatalf("sin taxonomía se listan los dos tipos ofrecidos; got %v", got)
	}
}

// TestIntegration_ElMenuNoMencionaUnPedidoDescartado es la escena de Marta contra
// BD real y con las piezas de verdad: el mismo contacto, el mismo evento y lo único
// que cambia entre las dos mitades es el estado de su SOLICITUD.
//
// Con la solicitud abierta, el menú ofrece las dos cosas: pedir y retomar. En cuanto
// el dueño descarta el pedido —lo que en la tabla es `abandoned`—, la opción de
// retomar desaparece aunque el evento siga `open`, que es el estado en el que el
// descarte lo deja durante un instante (y en el que se quedaría para siempre si el
// UPDATE del otro plan fallara). INV-17: no se lista, no se rescata y no se
// menciona.
func TestIntegration_ElMenuNoMencionaUnPedidoDescartado(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tid := seedTenantConPlan(t, db, "basic")
	const sesion = "sesion-marta-menu"

	reglas := trigger.NewPostgresStore(db)
	if _, err := reglas.Insert(ctx, reglaEventStart(tid, "carrito", "cart")); err != nil {
		t.Fatalf("sembrar regla: %v", err)
	}

	store := events.NewStore(db, nil)
	elPedido := mustCrear(ctx, t, store, nuevoEvento(tid, sesion, contactoA, "cart"))
	intakeID := insertarIntake(ctx, t, db, tid, sesion, contactoA, "open", elPedido.ID)

	d := events.NewDispatcher(store, events.NewTriggerKindOffer(reglas), entitlements.NewPostgres(db))
	ref := events.ConversationRef{TenantID: tid, SessionID: sesion, ContactID: contactoA}
	armar := func() events.Menu {
		t.Helper()
		m, err := d.Build(ctx, ref)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		return m
	}

	if got := kindsDe(armar()); len(got) != 2 || got[0] != "start:cart" || got[1] != "resume:cart" {
		t.Fatalf("con la solicitud abierta se ofrece pedir Y retomar; got %v", got)
	}

	// El dueño descarta el pedido. El evento sigue `open` a propósito: lo que tapa la
	// opción tiene que ser el estado de la solicitud, no el del evento.
	ponerStatusIntake(ctx, t, db, intakeID, "abandoned")
	if leerCruda(ctx, t, db, elPedido.ID).status != "open" {
		t.Fatalf("el escenario exige que el evento siga open")
	}

	m := armar()
	if got := kindsDe(m); len(got) != 1 || got[0] != "start:cart" {
		t.Fatalf("descartado el pedido, el menú ya no lo menciona (INV-17); got %v", got)
	}
	if texto := m.Render(); strings.Contains(texto, "dejaste a medias") {
		t.Fatalf("el menú no puede ofrecer retomar un pedido descartado; texto:\n%s", texto)
	}
}
