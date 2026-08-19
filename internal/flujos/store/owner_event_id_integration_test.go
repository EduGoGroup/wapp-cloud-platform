// owner_event_id_integration_test.go — la persistencia del puntero al evento DUEÑO
// (flow_state.owner_event_id, Plan 053 · T1.4) contra Postgres REAL. Cierra REQ-053.5.
//
// Va contra la BD y no contra el gemelo en memoria por dos razones que un mapa Go no
// puede reproducir: la columna es UUID y el dominio la lleva como cadena —el choque de
// tipos solo existe en SQL—, y el fallo que estos tests cazan es una propiedad de LA
// SENTENCIA, no del modelo: una columna ausente del `ON CONFLICT ... DO UPDATE` no da
// error, simplemente conserva el valor viejo para siempre. El MemoryRepository sustituye
// la fila entera en cada Save, así que APROBARÍA un upsert roto.
//
// Sin build tag, como el resto del paquete: el fichero acaba en _integration_test.go y
// openTestDB salta solo si no hay WAPP_TEST_DB_DSN. Corren bajo `make test-integration`.
package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// baseConversaciónDeDueño arma el estado mínimo válido para la clave dada, sin dueño:
// cada caso decide qué OwnerEventID le pone encima.
func baseConversaciónDeDueño(key store.Key) model.Conversation {
	return model.Conversation{
		TenantID:    key.TenantID,
		SessionID:   key.SessionID,
		ContactID:   key.ContactID,
		FlowID:      "pedido",
		FlowVersion: 1,
		CurrentNode: "root",
		Vars:        map[string]any{},
	}
}

// leerOwnerEventIDCrudo lee la columna SIN pasar por el repositorio, igual que
// leerEventIDCrudo hace con event_id: es lo único que distingue «el modelo dice ""» de
// «la columna es NULL». Devuelve el IS NULL calculado por Postgres y el valor, para que
// el mensaje de fallo pueda decir con qué se quedó pegada la fila.
func leerOwnerEventIDCrudo(t *testing.T, db *sql.DB, key store.Key) (esNULL bool, valor sql.NullString) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(), `
		SELECT owner_event_id IS NULL, owner_event_id::text
		FROM public.flow_state
		WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3
	`, key.TenantID, key.SessionID, key.ContactID).Scan(&esNULL, &valor); err != nil {
		t.Fatalf("leyendo owner_event_id crudo: %v", err)
	}
	return esNULL, valor
}

// exigirOwnerEventID carga por el repositorio y comprueba el dueño que trae. Extraído
// para que los tests no repitan el trío Load/found/err en cada etapa.
func exigirOwnerEventID(t *testing.T, repo *store.PostgresRepository, key store.Key, esperado, etapa string) {
	t.Helper()
	got, found, err := repo.Load(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("%s: Load found=%v err=%v", etapa, found, err)
	}
	if got.OwnerEventID != esperado {
		t.Fatalf("%s: OwnerEventID = %q, esperaba %q", etapa, got.OwnerEventID, esperado)
	}
}

// TestFlowStateRoundTrip_OwnerEventID recorre la vida del puntero al dueño contra la
// columna real: se enciende con un evento, CAMBIA a otro y se apaga.
//
// Los dos eventos son REALES (seedTenantEventoPG / seedEventoDePG) y no UUID inventados
// porque owner_event_id sí lleva FK hacia conversation_events (a diferencia de event_id,
// que la 0052 dejó suelta a propósito): con un UUID de mentira el INSERT reventaría con
// un 23503 y el test estaría midiendo la FK en vez del upsert.
func TestFlowStateRoundTrip_OwnerEventID(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := store.NewPostgresRepository(db)
	tenantID, dueñoA := seedTenantEventoPG(t, db, "open")
	dueñoB := seedEventoDePG(t, db, tenantID, "open")
	key := store.Key{TenantID: tenantID, SessionID: "sess-dueño", ContactID: uuid.NewString()}

	// 1. Alta: el dueño viaja a la columna y vuelve intacto por el camino del INSERT.
	st := baseConversaciónDeDueño(key)
	st.OwnerEventID = dueñoA
	if err := repo.Save(ctx, st); err != nil {
		t.Fatalf("Save con dueño A: %v", err)
	}
	exigirOwnerEventID(t, repo, key, dueñoA, "alta")

	// 2. EL CORAZÓN DEL TEST. El segundo Save por la MISMA clave ya no pasa por el
	// INSERT sino por el ON CONFLICT ... DO UPDATE, y ahí es donde se pierde una
	// columna olvidada: la fila se quedaría con el dueño A sin que nadie dé un error.
	// La etapa 1, sola, aprobaría ese upsert roto — por eso este assert no es opcional.
	//
	// El relevo no es un caso de laboratorio: es lo que ocurre cuando el flujo del
	// carrito acaba y el contacto abre otro evento sobre la misma conversación.
	st.OwnerEventID = dueñoB
	if err := repo.Save(ctx, st); err != nil {
		t.Fatalf("Save con dueño B: %v", err)
	}
	exigirOwnerEventID(t, repo, key, dueñoB, "relevo de dueño")

	// 3. Apagado: guardar un estado sin dueño deja la columna en NULL, no pegada a B.
	// Se comprueba contra la columna cruda y no solo por el round-trip porque un Save
	// que ignorara la columna dejaría el modelo contento y la fila mintiendo.
	st.OwnerEventID = ""
	if err := repo.Save(ctx, st); err != nil {
		t.Fatalf("Save apagando el dueño: %v", err)
	}
	exigirOwnerEventID(t, repo, key, "", "apagado")
	esNULL, valor := leerOwnerEventIDCrudo(t, db, key)
	if !esNULL {
		t.Fatalf("owner_event_id debería ser NULL tras apagar el dueño, y vale %q", valor.String)
	}
}

// TestFlowStateLoad_FilaSinOwnerEventID_CargaVaciaSinPanico es el test de la fila
// LEGADA: toda conversación viva hoy en producción tiene esa columna en NULL, y el día
// del despliegue el motor la va a leer antes de que nadie la escriba. Debe cargar como
// "" —el cero de Go, el mismo trato que "no hay dueño"— sin error y sin panic.
//
// Es también la red del cambio de forma del SELECT: si la columna nueva se hubiera
// colado en un sitio del SELECT y en otro del Scan, esta fila se leería torcida (el
// flow_id apareciendo en current_node, o un error de tipos), y el último assert lo dice.
func TestFlowStateLoad_FilaSinOwnerEventID_CargaVaciaSinPanico(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := store.NewPostgresRepository(db)
	tenantID := seedTenant(t, db)
	key := store.Key{TenantID: tenantID, SessionID: "sess-legada", ContactID: uuid.NewString()}

	// La fila se siembra con SQL crudo NOMBRANDO SOLO las columnas anteriores a esta
	// ola: owner_event_id se queda en NULL sin que nadie la mencione, que es exactamente
	// como nacieron las filas que ya existen.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.flow_state
			(tenant_id, session_id, contact_id, flow_id, flow_version, current_node, vars, last_wa_message_id, updated_at)
		VALUES ($1, $2, $3, 'pedido', 1, 'root', '{}'::jsonb, NULL, now())
	`, key.TenantID, key.SessionID, key.ContactID); err != nil {
		t.Fatalf("sembrando fila legada: %v", err)
	}
	// Sin esta comprobación el test podría estar pasando por un DEFAULT inesperado en la
	// columna en vez de por el NULL que dice medir.
	if esNULL, valor := leerOwnerEventIDCrudo(t, db, key); !esNULL {
		t.Fatalf("la fila legada nació con owner_event_id = %q: el seed no prueba nada", valor.String)
	}

	got, found, err := repo.Load(ctx, key)
	if err != nil || !found {
		t.Fatalf("Load de fila legada: found=%v err=%v", found, err)
	}
	if got.OwnerEventID != "" {
		t.Fatalf("la fila legada trajo OwnerEventID %q: un NULL debe leerse como cadena vacía", got.OwnerEventID)
	}
	if got.FlowID != "pedido" || got.CurrentNode != "root" || got.FlowVersion != 1 {
		t.Fatalf("la fila legada se leyó torcida (¿orden del SELECT vs orden del Scan?): %+v", got)
	}
}
