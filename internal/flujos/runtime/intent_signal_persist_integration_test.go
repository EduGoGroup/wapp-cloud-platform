// intent_signal_persist_integration_test.go cubre el criterio (d) de T4.3 (Plan 046 ·
// Ola 4, REQ-18), que es el único que mira donde de verdad estaba el problema: EL DISCO.
//
// 🔴 POR QUÉ NO BASTA CON LOS TESTS DEL ENGINE. Los de enter_primed_strip_test.go
// afirman que EnterPrimed devuelve unas Vars limpias, y eso es una afirmación sobre un
// STRUCT EN MEMORIA. La fuga que REQ-18 cierra no es esa: es que el texto extraído del
// mensaje del cliente acabe escrito en el JSONB de public.flow_state y se quede ahí
// para siempre, porque nada lo borra después. Entre el struct y el disco está el Save
// del runtime (start.go), y solo un SELECT contra Postgres real responde por él.
package runtime_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// textoDelCliente es lo que de verdad se persigue: no la CLAVE intent_params, sino el
// contenido que el clasificador extrajo de lo que escribió una persona. Se busca por
// separado a propósito — un barrido que dejara el valor bajo otra clave pasaría un test
// que solo mirase el nombre de la clave.
const textoDelCliente = "empanada de pino"

// TestIntegration_ArranqueSinConsumidor_NoPersisteLaSenalDeIntencion reproduce el
// camino de start.go: se siembra la señal en Vars igual que seedIntentParams, se
// arranca con EnterPrimed sobre un flujo cuyo nodo inicial NO consume la señal (un
// menú), se guarda con el repositorio REAL y se lee la columna cruda.
//
// 💥 MUTACIÓN: quitar `st.Vars = modules.StripIntentSignal(st.Vars)` de EnterPrimed ⇒
// ROJO aquí, con el texto del cliente visible en la salida del fallo.
func TestIntegration_ArranqueSinConsumidor_NoPersisteLaSenalDeIntencion(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	repo := store.NewPostgresRepository(db)

	sessionID := "sesion-" + uuid.NewString()
	contactID := uuid.NewString()
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.flow_state WHERE tenant_id = $1 AND session_id = $2`,
			tenantID, sessionID); err != nil {
			t.Logf("limpiando flow_state: %v", err)
		}
	})

	reg := modules.NewRegistry()
	reg.Register(menu.New())
	e := engine.New(reg)

	def := model.Flow{
		FlowID:  "menu-sin-primer",
		Version: 1,
		Initial: "root",
		Nodes: map[string]model.Node{
			"root": {Type: model.NodeTypeMenu, Prompt: "Hola\n1) A", Options: map[string]string{"1": "a"}},
			"a":    {Type: model.NodeTypeMessage, Text: "A"},
		},
	}

	// Espejo de seedIntentParams (start.go): las dos claves, sembradas ANTES del
	// EnterPrimed. Es el estado exacto con el que arranca un flujo por decisión llm.
	st := model.Conversation{
		TenantID:  tenantID,
		SessionID: sessionID,
		ContactID: contactID,
		Vars: map[string]any{
			modules.VarIntentParams: map[string]any{"producto": textoDelCliente},
			modules.VarIntentName:   "comprar",
		},
	}

	st, _, _, err := e.EnterPrimed(ctx, def, st)
	if err != nil {
		t.Fatalf("EnterPrimed: %v", err)
	}
	if err := repo.Save(ctx, st); err != nil {
		t.Fatalf("guardar estado inicial (espejo de start.go): %v", err)
	}

	var varsCrudo string
	if err := db.QueryRowContext(ctx,
		`SELECT vars::text FROM public.flow_state
		  WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3`,
		tenantID, sessionID, contactID).Scan(&varsCrudo); err != nil {
		t.Fatalf("leer vars de flow_state: %v", err)
	}

	if strings.Contains(varsCrudo, modules.VarIntentParams) {
		t.Fatalf("la clave %q quedó PERSISTIDA en public.flow_state.vars: %s\n"+
			"⇒ nadie consumió la señal y nadie la limpió, así que el texto del cliente "+
			"se queda en la base para siempre (REQ-18). El barrido va en la rama "+
			"no-handled de EnterPrimed", modules.VarIntentParams, varsCrudo)
	}
	if strings.Contains(varsCrudo, modules.VarIntentName) {
		t.Fatalf("la clave %q quedó PERSISTIDA en public.flow_state.vars: %s",
			modules.VarIntentName, varsCrudo)
	}
	if strings.Contains(varsCrudo, textoDelCliente) {
		t.Fatalf("🔴 EL TEXTO DEL CLIENTE está en la base aunque las claves no: %s\n"+
			"⇒ alguien barrió los nombres y dejó el contenido bajo otra clave", varsCrudo)
	}
}
