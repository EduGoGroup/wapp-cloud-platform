// conversationevents_list_e2e_integration_test.go es el criterio HTTP de T4.5.4
// (Plan 043 · Ola 4.5, D-043.21/22): el filtro `content` de REQ-28 deja de mentir.
//
// Lo que hace e2e a este test: la petición entra por el mux montado con
// publicapi.Register (ruta, scope intakes.read y gate de features REALES), el
// lister es *events.Store DE VERDAD sobre Postgres real, y el contenido se
// resuelve por el join con la vista public.event_content — no hay una sola
// ligadura sembrada en el padre, porque el padre YA NO TIENE columna de hijo:
// es el intake quien declara su evento (intakes.event_id, FK invertida).
//
// La foto sembrada, una fila por cada respuesta posible de la vista:
//
//	evVivo      → cart  con intake 'open'      ⇒ content_state=alive, content_ref=el intake
//	evMuerto    → cart  con intake 'abandoned' ⇒ contenido 'discarded': NO SE LISTA (INV-17)
//	evSinNada   → survey sin intake            ⇒ SIN claves content_* (la ausencia es el dato)
//
// Corre contra WAPP_TEST_DB_DSN (se omite sin ella; WAPP_TEST_REQUIRE_DB la
// exige), igual que los otros e2e del paquete. Datos con prefijo t45p-.
package publicapi_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

const (
	t45pSesión = "t45p-sess-listado"
	t45pFlujo  = "t45p-flujo-listado"
	t45pKey    = "key-t45p-duena" // credencial de la dueña, con intakes.read

	// Tres contactos DISTINTOS a propósito: dos de los eventos son cart y los dos
	// están open, y el índice único parcial one_alive_per_kind (E-2) es por
	// (tenant, session, contact, kind) — con el mismo contacto el segundo cart no
	// podría nacer. Son UUID porque la columna lo es.
	t45pContactoVivo   = "45450001-4545-4545-8545-454545450001"
	t45pContactoMuerto = "45450002-4545-4545-8545-454545450002"
	t45pContactoSolo   = "45450003-4545-4545-8545-454545450003"
)

// t45pSeed siembra la foto de arriba y devuelve los ids generados. El orden es
// el que la FK invertida obliga: primero nace el padre (conversation_events),
// después el hijo que lo declara (intakes.event_id NOT NULL para toda fila
// nueva por el CHECK de la 0054; ÚNICO por evento por el índice parcial).
func t45pSeed(t *testing.T, db *sql.DB) (tenantID, evVivo, evMuerto, evSinNada, intakeVivo, intakeMuerto string) {
	t.Helper()
	ctx := context.Background()

	if err := db.QueryRowContext(ctx,
		`INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id::text`,
		"t45p-listado-content", "T45P listado content e2e").Scan(&tenantID); err != nil {
		t.Fatalf("sembrando tenant: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		// Los intakes primero (su FK a conversation_events no cascada); el tenant
		// cascada después los eventos.
		for _, id := range []string{intakeVivo, intakeMuerto} {
			if id == "" {
				continue
			}
			if _, err := db.ExecContext(ctx, `DELETE FROM public.intakes WHERE id = $1::uuid`, id); err != nil {
				t.Logf("limpiando intake %s: %v", id, err)
			}
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM public.tenants WHERE id = $1`, tenantID); err != nil {
			t.Logf("limpiando tenant: %v", err)
		}
	})

	sembrarEvento := func(kind, contacto, historia string) string {
		var id string
		if err := db.QueryRowContext(ctx, `
			INSERT INTO public.conversation_events
				(tenant_id, session_id, contact_id, kind, history_id, status, flow_id, flow_version)
			VALUES ($1, $2, $3, $4, $5, 'open', $6, 1)
			RETURNING id::text
		`, tenantID, t45pSesión, contacto, kind, historia, t45pFlujo).Scan(&id); err != nil {
			t.Fatalf("sembrando evento %s: %v", historia, err)
		}
		return id
	}
	sembrarIntake := func(status, contacto, eventID string) string {
		var id string
		if err := db.QueryRowContext(ctx, `
			INSERT INTO public.intakes
				(id, tenant_id, contact_id, session_id, status, total, created_at, updated_at, event_id)
			VALUES (gen_random_uuid(), $1, $2, $3, $4, 100, now(), now(), $5::uuid)
			RETURNING id::text
		`, tenantID, contacto, t45pSesión, status, eventID).Scan(&id); err != nil {
			t.Fatalf("sembrando intake %s del evento %s: %v", status, eventID, err)
		}
		return id
	}

	evVivo = sembrarEvento("cart", t45pContactoVivo, "cart-2026-08-10-1000")
	evMuerto = sembrarEvento("cart", t45pContactoMuerto, "cart-2026-08-10-1001")
	evSinNada = sembrarEvento("survey", t45pContactoSolo, "survey-2026-08-10-1002")
	intakeVivo = sembrarIntake("open", t45pContactoVivo, evVivo)
	intakeMuerto = sembrarIntake("abandoned", t45pContactoMuerto, evMuerto)
	return tenantID, evVivo, evMuerto, evSinNada, intakeVivo, intakeMuerto
}

// t45pAPI monta la API real sobre el store real, con las features de tipo
// encendidas para el tenant generado y una credencial con intakes.read.
func t45pAPI(db *sql.DB, tenantID string) *testAPI {
	feats := entitlements.NewFake()
	for _, f := range events.KindFeatures() {
		feats.Enable(tenantID, f)
	}
	keys := intakesKeys()
	keys[t45pKey] = testIdentity{TenantID: tenantID, Subject: "t45p-duena", Grants: []string{"intakes.read"}}
	return newAPI(publicapi.Deps{
		ConversationEvents: events.NewStore(db, nil),
		Entitlements:       feats,
	}, keys)
}

// t45pListar hace el GET con el filtro dado y devuelve la página decodificada.
func t45pListar(t *testing.T, api *testAPI, query string) conversationEventListDTO {
	t.Helper()
	rec := call(api, t45pKey, http.MethodGet, "/api/v1/conversation-events"+query, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: code=%d, quiero 200; body=%s", query, rec.Code, rec.Body.String())
	}
	return decodeEventos(t, rec.Body.Bytes())
}

// t45pSinContenidoCrudo busca en el JSON crudo del listado la fila del evento
// `id` y afirma que NO publica ninguna clave de contenido (sinClavesDeContenido,
// el helper compartido con el test del wire).
func t45pSinContenidoCrudo(t *testing.T, api *testAPI, id string) {
	t.Helper()
	rec := call(api, t45pKey, http.MethodGet, "/api/v1/conversation-events?content=any", "")
	for _, fila := range filasCrudas(t, rec.Body.Bytes()) {
		var filaID string
		if err := json.Unmarshal(fila["id"], &filaID); err != nil {
			t.Fatalf("unmarshal del id crudo: %v", err)
		}
		if filaID == id {
			sinClavesDeContenido(t, fila)
			return
		}
	}
	t.Fatalf("el evento %s no aparece en el listado crudo", id)
}

// TestE2E_ConversationEvents_ContentTresFiltros recorre los TRES valores del
// vocabulario público (any|none|alive) contra la vista real, y de paso fija el
// DTO nuevo: content_state/content_ref derivados del join, omitidos sin fila.
func TestE2E_ConversationEvents_ContentTresFiltros(t *testing.T) {
	db := e2eOpenDB(t)
	tenantID, evVivo, evMuerto, evSinNada, intakeVivo, _ := t45pSeed(t, db)
	api := t45pAPI(db, tenantID)

	// ── content=any: las DOS MITADES del vocabulario (none ∪ alive), que NO es
	// «todo»: el evento cuyo contenido murió (intake abandoned ⇒ discarded en la
	// vista) NO SE LISTA — INV-17, la escena de Marta: un contenido que ya no está
	// vivo no se enseña ni se ofrece, con ningún filtro. Antes de la 0054 esta
	// exclusión era mentira (la columna que la decidía no la escribía nadie).
	todos := t45pListar(t, api, "?content=any")
	if todos.Total != 2 || len(todos.Events) != 2 {
		t.Fatalf("content=any devolvió %d filas / total=%d; quiero exactamente 2 (vivo y sin-nada): %+v",
			len(todos.Events), todos.Total, todos.Events)
	}
	porID := map[string]int{}
	for i, ev := range todos.Events {
		porID[ev.ID] = i
	}
	if _, está := porID[evMuerto]; está {
		t.Fatalf("content=any enseña el evento con contenido discarded (%s); INV-17 lo excluye", evMuerto)
	}
	for _, id := range []string{evVivo, evSinNada} {
		if _, está := porID[id]; !está {
			t.Fatalf("content=any no trae el evento %s; filas=%+v", id, todos.Events)
		}
	}

	// El del intake open trae los DERIVADOS del join: alive y el id del intake —
	// el hilo evento→solicitud que antes fingía intake_id (que nadie escribía).
	vivo := todos.Events[porID[evVivo]]
	if vivo.ContentState != "alive" || vivo.ContentRef != intakeVivo {
		t.Fatalf("evento con intake open: content_state=%q content_ref=%q; quiero alive/%s",
			vivo.ContentState, vivo.ContentRef, intakeVivo)
	}
	// Y el survey sin intake no lleva LAS CLAVES (omitempty): sin fila en la
	// vista no hay contenido que describir. Se mira el JSON crudo (helper
	// compartido con el test del wire) porque el struct del decode no distingue
	// «clave ausente» de «cadena vacía».
	t45pSinContenidoCrudo(t, api, evSinNada)

	// ── content=alive: SOLO el del intake open. El abandonado queda fuera — ni
	// vivo (alive) ni virgen (none) — que es exactamente la mentira que la
	// columna muerta contaba y la vista ya no puede contar.
	vivos := t45pListar(t, api, "?content=alive")
	if len(vivos.Events) != 1 || vivos.Events[0].ID != evVivo {
		t.Fatalf("content=alive devolvió %+v; quiero exactamente %s", vivos.Events, evVivo)
	}

	// ── content=none: SOLO el que no produjo nada (REQ-15 del 045 vive de esto:
	// conversaciones que no parieron pedido). El abandonado NO es none: su fila
	// en la vista existe.
	vacíos := t45pListar(t, api, "?content=none")
	if len(vacíos.Events) != 1 || vacíos.Events[0].ID != evSinNada {
		t.Fatalf("content=none devolvió %+v; quiero exactamente %s", vacíos.Events, evSinNada)
	}
}
