package store_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// itemsRenderModule es un módulo de test que expone en su Render TODO el
// model.Content resuelto: el Prompt y, además, el Label de cada Item del
// catálogo. Se registra bajo NodeTypeMenu (tipo válido para model.Validate y
// para el que WaitsForInput es true, de modo que el engine delega el render).
//
// Existe para PROBAR EL VALOR del adapter json: la lista de items del catálogo
// (que el nodo estático NO puede aportar, porque no lleva esos items en su
// definición) llega intacta al Render vía tenant_content. El módulo menu real
// solo emite content.Prompt; este fake evidencia también content.Items.
type itemsRenderModule struct{}

func (itemsRenderModule) Type() string        { return model.NodeTypeMenu }
func (itemsRenderModule) WaitsForInput() bool { return true }

func (itemsRenderModule) Render(_ model.Node, c model.Content) []string {
	outs := make([]string, 0, 1+len(c.Items))
	outs = append(outs, c.Prompt)
	for _, it := range c.Items {
		outs = append(outs, it.Label)
	}
	return outs
}

func (itemsRenderModule) Step(_ model.Node, _ model.Conversation, _ string) modules.Result {
	return modules.Result{}
}

// TestIntegration_EngineJSONAdapterRendersFromTenantContent es la evidencia
// automatizada del criterio T4 del Plan 015: siembra un tenant_content y un nodo
// con content:{source:"json", ref} y verifica que json.Resolve (vía Router)
// alimenta el MISMO Module.Render con el contrato leído de Postgres, sin que el
// engine ni el módulo sepan de dónde vino. El mismo engine (router) renderiza un
// nodo estático desde su propio node.Prompt: static y json CONVIVEN.
//
// Gated por WAPP_TEST_DB_DSN (SKIP limpio sin DSN, patrón openTestDB).
func TestIntegration_EngineJSONAdapterRendersFromTenantContent(t *testing.T) {
	db := openTestDB(t) // migra incl. 0010_tenant_content; SKIP limpio sin DSN
	ctx := context.Background()
	repo := store.NewPostgresRepository(db)

	// tenant_id/ref únicos por corrida: tenant_content es TEXT sin FK y aislamos
	// para no colisionar con otras corridas / tests.
	tenantID := fmt.Sprintf("tenant-json-render-%d", time.Now().UnixNano())
	ref := "bebidas"

	// El BLOB del contrato: prompt e items del catálogo que el nodo NO lleva. El
	// prompt es DISTINTO del node.Prompt para poder distinguir la fuente.
	const blobPrompt = "Elige una bebida"
	blob := `{
		"prompt":"Elige una bebida",
		"options":{"1":"agua","2":"cola"},
		"items":[
			{"code":"1","sku":"AGUA","label":"Agua","price":1.5},
			{"code":"2","sku":"COLA","label":"Cola","price":2.0}
		]
	}`

	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_content (tenant_id, ref, content)
		VALUES ($1, $2, $3::jsonb)
	`, tenantID, ref, blob); err != nil {
		t.Fatalf("sembrar tenant_content: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), `
			DELETE FROM public.tenant_content WHERE tenant_id = $1
		`, tenantID); err != nil {
			t.Logf("limpiando tenant_content: %v", err)
		}
	})

	// Engine REAL cableado como en producción: el Router compone Static (PURO) +
	// JSON (respaldado por el PostgresRepository, que satisface content.Store por
	// structural typing). El engine solo ve el puerto content.Source.
	reg := modules.NewRegistry()
	reg.Register(itemsRenderModule{})
	src := content.NewRouter(content.NewStatic(), content.NewJSON(repo))
	e := engine.New(reg, engine.WithContentSource(src))

	// node.Prompt DISTINTO del blob: si el render lo usara, el assert lo cazaría.
	const nodePrompt = "PROMPT-DEL-NODO-NO-DEBE-VERSE"

	// ---- Caso json: el contenido lo resuelve json.Resolve desde tenant_content.
	jsonFlow := model.Flow{
		FlowID:  "carta-json",
		Version: 1,
		Initial: "root",
		Nodes: map[string]model.Node{
			"root": {
				Type:    model.NodeTypeMenu,
				Prompt:  nodePrompt,
				Options: map[string]string{"1": "root", "2": "root"},
				Content: &model.ContentRef{Source: "json", Ref: ref},
			},
		},
	}

	_, outs, err := e.Enter(ctx, jsonFlow, model.Conversation{TenantID: tenantID})
	if err != nil {
		t.Fatalf("Enter (json): %v", err)
	}
	got := texts(outs)

	// ASSERT 1: el render vino del BLOB (prompt + labels de los items), NO del
	// node.Prompt. La lista de items es la prueba fuerte: static no podría darla
	// (el nodo no lleva esos items), así que su presencia demuestra que
	// json.Resolve leyó el contrato de tenant_content y lo pasó al Render.
	want := []string{blobPrompt, "Agua", "Cola"}
	if !slices.Equal(got, want) {
		t.Fatalf("render json = %q, quiero %q (del blob de tenant_content)", got, want)
	}
	// ASSERT explícito de que NO se usó el node.Prompt.
	if slices.Contains(got, nodePrompt) {
		t.Fatalf("el render usó node.Prompt %q en vez del contrato del blob: %q", nodePrompt, got)
	}

	// ---- Caso static (contraste, no-regresión): MISMO engine (router), nodo SIN
	// Content ⇒ el Router elige el adapter Static (PURO) y el render sale del
	// propio node.Prompt, byte-a-byte. Prueba que static y json conviven.
	const staticPrompt = "PROMPT-ESTATICO-DEL-NODO"
	staticFlow := model.Flow{
		FlowID:  "carta-static",
		Version: 1,
		Initial: "root",
		Nodes: map[string]model.Node{
			"root": {
				Type:    model.NodeTypeMenu,
				Prompt:  staticPrompt,
				Options: map[string]string{"1": "root"},
			},
		},
	}

	_, sOuts, err := e.Enter(ctx, staticFlow, model.Conversation{TenantID: tenantID})
	if err != nil {
		t.Fatalf("Enter (static): %v", err)
	}
	if sGot := texts(sOuts); !slices.Equal(sGot, []string{staticPrompt}) {
		t.Fatalf("render static = %q, quiero %q byte-a-byte (rama static del router)", sGot, []string{staticPrompt})
	}
}

// texts extrae los textos de las salidas del engine (helper local del test).
func texts(outs []engine.Output) []string {
	got := make([]string, len(outs))
	for i, o := range outs {
		got[i] = o.Text
	}
	return got
}
