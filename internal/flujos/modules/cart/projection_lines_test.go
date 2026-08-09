// projection_lines_test.go cubre las LÍNEAS DURABLES del pedido abierto (Plan 043 ·
// Ola 3): que una solicitud "open" tenga sus intake_items al día en todo momento, y
// no solo a partir del cierre.
//
// Los efectos NO se escriben a mano: se conducen desde el módulo real
// (Module.Step), porque lo que hay que probar es que la foto que el carrito emite y
// lo que el proyector escribe son la misma cosa. Un payload escrito a mano en el
// test probaría el proyector contra una foto que nadie emite.
package cart

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// lineProjector arma un proyector sobre un repositorio en memoria y lo devuelve
// junto a él (es lo que se interroga después).
func lineProjector() (*Projector, *store.MemoryRepository) {
	repo := store.NewMemoryRepository()
	return NewProjector(repo, intakes.NewMemoryStore(), &envíoEspía{}, intakes.NewMemoryStore()), repo
}

// project despacha por el proyector TODOS los efectos de un Step, igual que hace el
// PersistSink (que solo llama a los que Handles reconoce).
func project(t *testing.T, p *Projector, effs []modules.Effect) {
	t.Helper()
	for _, e := range effs {
		if !p.Handles(e.Name) {
			continue
		}
		if err := p.Project(context.Background(), projectorMeta(), e); err != nil {
			t.Fatalf("Project(%s): %v", e.Name, err)
		}
	}
}

// soloSolicitud exige que haya UNA solicitud y devuelve la fila.
func soloSolicitud(t *testing.T, repo *store.MemoryRepository) store.Intake {
	t.Helper()
	todas := repo.Intakes()
	if len(todas) != 1 {
		t.Fatalf("solicitudes: got %d, want 1 (%+v)", len(todas), todas)
	}
	return todas[0]
}

// espejo exige que las líneas persistidas sean EXACTAMENTE las del carrito, en el
// mismo orden y con los mismos datos copiados (label y precio incluidos: son verdad
// durable de la línea, no se recalculan del catálogo).
//
// Compara contra el estado del MÓDULO y no contra una lista literal a propósito: la
// propiedad que se quiere es "la tabla dice lo que dice el carrito", y un literal
// dejaría de mirarla en cuanto el recorrido cambiara.
func espejo(t *testing.T, repo *store.MemoryRepository, intakeID string, quiero []cartLine) {
	t.Helper()
	tengo := repo.IntakeItems(intakeID)
	if len(tengo) != len(quiero) {
		t.Fatalf("intake_items: got %d líneas, want %d\n  got:  %+v\n  want: %+v",
			len(tengo), len(quiero), tengo, quiero)
	}
	for i, l := range quiero {
		got := tengo[i]
		if got.SKU != l.SKU || got.Label != l.Label || got.Qty != l.Qty ||
			got.UnitPrice != l.UnitPrice || got.Customization != l.Customization {
			t.Errorf("línea %d: got %+v, want sku=%q label=%q qty=%d precio=%v personalización=%q",
				i, got, l.SKU, l.Label, l.Qty, l.UnitPrice, l.Customization)
		}
		if got.IntakeID != intakeID {
			t.Errorf("línea %d colgada de la solicitud %q, no de %q", i, got.IntakeID, intakeID)
		}
	}
}

// agrega2Cafés conduce el recorrido real hasta agregar 2 × Café y despacha los
// efectos por el proyector. Devuelve las Vars para seguir la conversación.
func agrega2Cafés(t *testing.T, p *Projector) map[string]any {
	t.Helper()
	m := New()
	vars := seededVars()
	for _, in := range []string{"1", "1", "2"} { // categoría → artículo → agregar
		_, effs, next := driveE(t, m, vars, in)
		project(t, p, effs)
		vars = next
	}
	_, effs, vars := driveE(t, m, vars, "2") // cantidad 2 ⇒ item_added
	project(t, p, effs)
	return vars
}

// TestProyección_ItemAdded_LaSolicitudAbiertaYaTieneSuLínea es el escenario que
// motiva la tarea: el cliente agregó 2 unidades y NO ha cerrado nada. Hasta ahora la
// solicitud "open" existía sin una sola fila en intake_items, así que rescatarla
// (REQ-26b) o enseñarla en el CRM prometía unas líneas que no estaban.
func TestProyección_ItemAdded_LaSolicitudAbiertaYaTieneSuLínea(t *testing.T) {
	p, repo := lineProjector()
	agrega2Cafés(t, p)

	solicitud := soloSolicitud(t, repo)
	if solicitud.Status != intakeStatusOpen {
		t.Fatalf("la solicitud debe seguir ABIERTA: %+v", solicitud)
	}
	espejo(t, repo, solicitud.ID, []cartLine{
		{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5},
	})
}

// TestProyección_ItemAdded_ReflejaElCarritoSinDuplicar: se agrega una segunda línea
// y se cambia la cantidad de la primera. La tabla tiene que ser el ESPEJO del
// carrito —no un registro de lo que se fue agregando—, y por eso la proyección
// REEMPLAZA el conjunto en vez de acumularlo.
//
// La segunda línea se conduce por el módulo; el cambio de cantidad de la PRIMERA no
// tiene hoy tecla en el recorrido numérico, así que se emite la foto que lo
// declararía. Es la única parte escrita a mano del archivo y es deliberada: prueba
// que el proyector obedece a la foto, que es lo que el pipeline LLM del Plan 044 y
// el CRM van a mover.
func TestProyección_ItemAdded_ReflejaElCarritoSinDuplicar(t *testing.T) {
	p, repo := lineProjector()
	m := New()
	vars := agrega2Cafés(t, p)

	// Segunda línea real: agregar más → Té → agregar → cantidad 3.
	for _, in := range []string{"1", "2", "2"} {
		_, effs, next := driveE(t, m, vars, in)
		project(t, p, effs)
		vars = next
	}
	_, effs, _ := driveE(t, m, vars, "3")
	project(t, p, effs)

	solicitud := soloSolicitud(t, repo)
	espejo(t, repo, solicitud.ID, []cartLine{
		{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5},
		{SKU: "TE", Label: "Té", Qty: 3, UnitPrice: 2.0},
	})

	// Ahora la cantidad de la PRIMERA línea cambia a 5: la foto siguiente lo dice y
	// la tabla tiene que quedar igual que la foto, con 2 líneas y no con 3.
	repriced := []cartLine{
		{SKU: "CAFE", Label: "Café", Qty: 5, UnitPrice: 2.5},
		{SKU: "TE", Label: "Té", Qty: 3, UnitPrice: 2.0},
	}
	project(t, p, []modules.Effect{withLineSnapshot(
		event(EffectItemAdded, map[string]any{"sku": "CAFE", "label": "Café", "qty": 5, "unit_price": 2.5}),
		repriced,
	)})
	espejo(t, repo, solicitud.ID, repriced)
}

// TestProyección_CierreDespuésDeProyectar_NoDuplicaNiUnaLínea protege el riesgo
// CRÍTICO de esta tarea: CloseIntake escribía TODAS las líneas al cerrar, así que
// con las líneas ya materializadas el cierre las habría duplicado una a una.
func TestProyección_CierreDespuésDeProyectar_NoDuplicaNiUnaLínea(t *testing.T) {
	p, repo := lineProjector()
	m := New()
	vars := agrega2Cafés(t, p)

	abierta := soloSolicitud(t, repo)
	espejo(t, repo, abierta.ID, []cartLine{{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5}})

	// Finalizar → resumen → confirmar ⇒ cart_closed sobre la MISMA solicitud.
	_, effs, vars := driveE(t, m, vars, "2")
	project(t, p, effs)
	_, effs, _ = driveE(t, m, vars, "1")
	project(t, p, effs)

	cerrada := soloSolicitud(t, repo)
	if cerrada.ID != abierta.ID || cerrada.Status != intakeStatusClosed {
		t.Fatalf("el cierre debe caer sobre la solicitud abierta y cerrarla: %+v", cerrada)
	}
	espejo(t, repo, cerrada.ID, []cartLine{{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5}})
}

// TestProyección_ItemAdded_ReentregaNoCambiaNada: el MISMO efecto reentregado (un
// reproceso, un replay del outbox) deja la tabla igual. Es la propiedad por la que
// la proyección reemplaza en vez de añadir: con una línea suelta no habría clave
// natural para reconocer el reenvío —dos item_added del mismo artículo son dos
// líneas legítimas del pedido—.
func TestProyección_ItemAdded_ReentregaNoCambiaNada(t *testing.T) {
	p, repo := lineProjector()
	m := New()
	vars := seededVars()
	for _, in := range []string{"1", "1", "2"} {
		_, effs, next := driveE(t, m, vars, in)
		project(t, p, effs)
		vars = next
	}
	_, effs, _ := driveE(t, m, vars, "2")
	project(t, p, effs)

	solicitud := soloSolicitud(t, repo)
	antes := repo.IntakeItems(solicitud.ID)

	project(t, p, effs) // reentrega literal del mismo efecto
	project(t, p, effs) // y otra vez

	if len(repo.Intakes()) != 1 {
		t.Fatalf("la reentrega no debe abrir otra solicitud: %+v", repo.Intakes())
	}
	después := repo.IntakeItems(solicitud.ID)
	if len(después) != len(antes) {
		t.Fatalf("la reentrega cambió las líneas: %d → %d\n  %+v", len(antes), len(después), después)
	}
	espejo(t, repo, solicitud.ID, []cartLine{{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5}})
}

// TestProyección_ItemAdded_SinFotoNoBorraLasLíneas: un flow_event HISTÓRICO —escrito
// antes de que el efecto llevara la foto— reejecutado hoy no sabe qué había en el
// carrito. Tratarlo como "carrito vacío" le borraría las líneas al pedido, que es la
// forma silenciosa en que este cambio podría destruir datos.
func TestProyección_ItemAdded_SinFotoNoBorraLasLíneas(t *testing.T) {
	p, repo := lineProjector()
	agrega2Cafés(t, p)
	solicitud := soloSolicitud(t, repo)

	viejo := modules.Effect{Kind: kindEvent, Name: EffectItemAdded, Payload: map[string]any{
		"sku": "CAFE", "label": "Café", "qty": 2, "unit_price": 2.5,
	}}
	project(t, p, []modules.Effect{viejo})

	espejo(t, repo, solicitud.ID, []cartLine{{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5}})
}

// TestProyección_NoteAdded_LaIndicaciónYElSplitLleganEnElMomento: la tecla 3 es el
// OTRO camino por el que cambian las líneas —le pone la indicación a la última y,
// con qty > 1, parte la línea ×N en ×(N-1) + ×1 (D-041.20)—. Sin proyectarlo, el
// pedido abierto enseñaría el conjunto anterior hasta el cierre.
func TestProyección_NoteAdded_LaIndicaciónYElSplitLleganEnElMomento(t *testing.T) {
	p, repo := lineProjector()
	m := New()
	vars := agrega2Cafés(t, p)
	solicitud := soloSolicitud(t, repo)

	// 3 → alcance (hay 2 unidades) → 2 = "solo para 1" → texto ⇒ split al guardar.
	for _, in := range []string{"3", "2"} {
		_, effs, next := driveE(t, m, vars, in)
		project(t, p, effs)
		vars = next
	}
	st, effs, _ := driveE(t, m, vars, "sin azúcar")
	project(t, p, effs)

	if len(st.Lines) != 2 {
		t.Fatalf("el split debe dejar 2 líneas en el carrito: %+v", st.Lines)
	}
	espejo(t, repo, solicitud.ID, st.Lines)
}

// TestProyección_EfectosSinFoto_CierranExactamenteComoSiempre replica el escenario
// de runtime/cart_persist_sink_test.go (dos item_added SIN foto —los que escribe a
// mano ese test, y los que hay guardados en flow_events desde antes de esta tarea— y
// un cart_closed con dos líneas) para acreditar la no-regresión aquí: aquel test vive
// en un paquete que hoy no compila por trabajo en curso ajeno, y la propiedad que
// comprueba es de este código.
func TestProyección_EfectosSinFoto_CierranExactamenteComoSiempre(t *testing.T) {
	p, repo := lineProjector()
	ctx := context.Background()
	meta := projectorMeta()

	viejo := func(sku, label string, qty int, unit float64) modules.Effect {
		return modules.Effect{Kind: kindEvent, Name: EffectItemAdded, Payload: map[string]any{
			"sku": sku, "label": label, "qty": qty, "unit_price": unit,
		}}
	}
	for _, e := range []modules.Effect{viejo("CAFE", "Café", 2, 2.5), viejo("FLAN", "Flan", 1, 3.0)} {
		if err := p.Project(ctx, meta, e); err != nil {
			t.Fatalf("Project(item_added viejo): %v", err)
		}
	}
	abierta := soloSolicitud(t, repo)
	if abierta.Status != intakeStatusOpen {
		t.Fatalf("dos item_added abren UNA solicitud open: %+v", abierta)
	}

	cierre := modules.Effect{Kind: kindPersist, Name: EffectCartClosed, Payload: map[string]any{
		"total": 8.0,
		snapshotKey: []map[string]any{
			{"sku": "CAFE", "label": "Café", "qty": 2, "unit_price": 2.5},
			{"sku": "FLAN", "label": "Flan", "qty": 1, "unit_price": 3.0},
		},
	}}
	if err := p.Project(ctx, meta, cierre); err != nil {
		t.Fatalf("Project(cart_closed): %v", err)
	}

	cerrada := soloSolicitud(t, repo)
	if cerrada.Status != intakeStatusClosed || cerrada.Total != 8.0 {
		t.Fatalf("la solicitud debe cerrar con total 8.0: %+v", cerrada)
	}
	// La aserción literal de cart_persist_sink_test.go:73-76: DOS líneas, ni una más.
	espejo(t, repo, cerrada.ID, []cartLine{
		{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5},
		{SKU: "FLAN", Label: "Flan", Qty: 1, UnitPrice: 3.0},
	})
}

// TestItemAdded_LaFotoDeLineasNoEntraEnFlowEvents: la foto es para el proyector, NO
// para el outbox. public.flow_events es append-only y sin poda, y publicar el
// carrito entero en cada item_added lo reescribiría N veces por pedido además de
// cambiar el payload que hoy leen la telemetría y el puente del CRM.
//
// Lo sujeta este test y no el golden de la transcripción: el golden compara el
// payload PÚBLICO, que es justo del que la foto tiene que estar ausente.
func TestItemAdded_LaFotoDeLineasNoEntraEnFlowEvents(t *testing.T) {
	m := New()
	vars := seededVars()
	for _, in := range []string{"1", "1", "2"} {
		_, _, vars = driveE(t, m, vars, in)
	}
	_, effs, _ := driveE(t, m, vars, "2")
	e := effByName(t, effs, EffectItemAdded)

	if _, hay := e.Payload[snapshotKey]; !hay {
		t.Fatalf("el proyector necesita la foto en el payload: %+v", e.Payload)
	}
	if _, hay := e.PublicPayload()[snapshotKey]; hay {
		t.Fatalf("la foto NO puede llegar a flow_events: %+v", e.PublicPayload())
	}
	if len(e.PublicPayload()) != 4 {
		t.Fatalf("el payload público de item_added tiene que seguir siendo el de siempre: %+v", e.PublicPayload())
	}
}
