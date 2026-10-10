// Porta internal/flujos/modules/cart/state.go @ 9d5a4b6

// state.go define el estado de la sub-máquina del carrito (design.md §3.2): el
// nivel de navegación en curso, la categoría/artículo en foco, la página de
// paginación, las líneas acumuladas del pedido y los contadores. Todo el estado
// vive serializado en Conversation.Vars["cart"] (JSONB), de modo que el engine lo
// persiste tras cada Step sin que el módulo toque BD (PURO).
//
// El módulo es un ÚNICO nodo type:cart (design.md §9.A): no hay un nodo por nivel
// en flow_definitions; el nivel actual es el campo `level` del estado y las
// transiciones "volver"/paginación/agregar son deterministas por la topología fija
// (categories ↔ articles ↔ article ↔ quantity/continue/summary).
//
// ════════════════════════════════════════════════════════════════════════════
// LA FORMA DE Vars["cart"] ES CONTRATO (lo leen el runtime, el resumen del evento
// abandonado y las filas ya guardadas en public.flow_state)
// ════════════════════════════════════════════════════════════════════════════
//
// Step deja bajo la clave "cart" un map[string]any —nunca un struct: es el
// round-trip JSON el que lo hace homogéneo con la relectura— con estas claves, EN
// ESTE ORDEN de serialización, todas `omitempty` menos `level`:
//
//	level              nivel actual (una de las constantes Level*)
//	cat_code           código de la categoría en foco (L2+)
//	sku                SKU del artículo en foco (L3/L4): el identificador de negocio
//	                   estable, NO el código de pantalla
//	page               página del nivel de lista actual
//	lines              líneas acumuladas; cada una {sku, label, qty, unit_price} y
//	                   `customization` solo si la línea lleva indicación (D-041.19)
//	variant_code       variante elegida del artículo en foco (D-041.4); TRANSITORIA:
//	                   vive entre el nivel de variante y el de cantidad
//	started            ya se declaró cart_started (EXACTAMENTE una vez por carrito)
//	note               indicación del cliente para TODO el pedido (D-041.19)
//	note_split         la indicación en curso va a UNA unidad de una línea ×N
//	                   (D-041.20); TRANSITORIA: muere al salir del nivel de nota
//	buyer_idx          CUÁNTOS campos del checklist del comprador van capturados
//	                   (D-041.13). Es un CONTADOR: las respuestas NO viven en el
//	                   estado, ni aquí ni en ninguna otra clave (son dato personal y
//	                   flow_state.vars es JSONB en claro)
//	reprompts          inválidos CONSECUTIVOS del nivel actual; solo cuenta DENTRO de
//	                   un evento (Plan 043 · T5.2)
//	reprompts_event_id SELLO del contador: el evento en que se cometieron
//
// Un carrito que no usa una capacidad no escribe su clave: el JSONB de un tenant v1
// sin indicaciones, sin variantes y sin checklist es byte a byte el de antes de que
// esas claves existieran, y un estado guardado antes de ellas carga sin rama
// especial. No existe clave `intake_id` ni `order_id` (retirada el 2026-08-12: nadie
// le asignó nunca un valor): el uuid de la solicitud lo lleva la proyección.
//
// LECTURA TOLERANTE. La clave ausente, nil o ilegible (un valor que no se puede
// leer como el estado) es el arranque: nivel LevelCategories, sin líneas. Un estado
// legible con `level` vacío también arranca en LevelCategories, conservando lo
// demás. Se tolera tanto el tipo nativo como el round-trip JSONB (números float64,
// objetos map[string]any).

package cart

// Niveles de la sub-máquina (design.md §3.2/§4.2). El nivel es el "nodo" interno
// del carrito. LevelClosed y LevelCancelled son terminales: el Step que los alcanza
// declara el FIN del flujo (ver Module.Step) y, si una fila legada llegara con uno
// de ellos sin el centinela puesto, la entrada se ignora y se re-muestra la pantalla
// final.
const (
	LevelCategories = "categories" // L1 · lista de categorías (raíz, sin "volver")
	LevelArticles   = "articles"   // L2 · artículos de la categoría en foco
	LevelArticle    = "article"    // L3 · menú del artículo (ver desc./agregar/volver)
	LevelVariant    = "variant"    // L3b · variante del artículo (SOLO si tiene; Plan 041 · D-041.4)
	LevelQuantity   = "quantity"   // L4 · cantidad libre (qty>=1)
	LevelContinue   = "continue"   // L5 · agregar más / finalizar / cancelar / volver
	LevelSummary    = "summary"    // L6 · resumen + confirmar / seguir / cancelar
	LevelClosed     = "closed"     // terminal · pedido confirmado
	LevelCancelled  = "cancelled"  // terminal · pedido cancelado

	// Los tres niveles de INDICACIÓN (Plan 041 · T4.1c, D-041.19 + D-041.20). NO
	// son pasos del recorrido: son ramas OPCIONALES colgadas de dos menús que ya
	// se imprimían (L5 y L6), y se vuelve al nivel de origen en cuanto se resuelven.
	// Quien no pulsa 3 no los pisa jamás (INV-15).
	LevelItemNoteScope = "item_note_scope" // alcance de la indicación de línea cuando qty > 1 (D-041.20)
	LevelItemNote      = "item_note"       // texto de la indicación de la ÚLTIMA línea agregada
	LevelOrderNote     = "order_note"      // texto de la indicación de TODO el pedido

	// LevelBuyerData es el checklist de DATOS DEL COMPRADOR (Plan 041 · T4.5,
	// D-041.13): un campo `required` por mensaje, entre el resumen y el cierre. Solo
	// existe para los tenants que configuraron buyer_fields; con la lista vacía —el
	// default de la columna y el caso de todos los tenants de hoy— el 1 del resumen
	// cierra el pedido exactamente igual que antes de esta tarea (INV-15).
	LevelBuyerData = "buyer_data"
)

// NodeTypeCart es el tipo de nodo que maneja este módulo. Se mantiene local al
// paquete (no se añade a model.NodeType*): el modelo NO conoce los tipos de los
// módulos, para no acoplarse a ellos. La validación del alta admin sí lo acepta,
// pero por INYECCIÓN: el módulo se registra en el Registry (modules.Register) y el
// handler pasa Registry.Types() a model.ParseAndValidate, que acepta laxo cualquier
// tipo de módulo. Así un flujo con nodo "cart" pasa POST /admin/flows sin tocar
// `model` (follow-up del Plan 016).
const NodeTypeCart = "cart"

// DefaultPageSize es el tamaño de página por defecto de los niveles de lista
// (L1 categorías, L2 artículos) cuando no se configura otro (design.md §9.E): el
// que usa Render, y el que usa Step si el runtime no sembró VarPageSize.
const DefaultPageSize = 5

// VarPageSize es la clave de Conversation.Vars bajo la que el RUNTIME siembra el
// tamaño de página REAL del tenant (tenant_settings.page_size) antes del Step,
// igual que el engine siembra el catálogo. Así la paginación usa la config del
// tenant SIN que el módulo haga I/O (design.md §9.E): el módulo sigue PURO y el
// cableado a tenant_settings vive en el runtime (ResumePolicy.Seed).
//
// Step lo lee tolerando el tipo nativo (int, int64) y el round-trip JSONB
// (float64, truncado). Ausente, de otro tipo o <= 0 ⇒ el tamaño del Module
// (DefaultPageSize, o el de WithPageSize).
const VarPageSize = "cart_page_size"
