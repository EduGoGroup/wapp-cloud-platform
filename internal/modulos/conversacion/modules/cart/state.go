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

import (
	"encoding/json"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

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

// variantSKUSuffix separa el sku del artículo del code de la variante en el sku
// de la LÍNEA ("TORTA-CHOC#V2", D-041.4): el pedido dice qué presentación se
// vendió sin necesidad de un sku distinto por variante en el catálogo.
const variantSKUSuffix = "#"

// variantLabelSep une la etiqueta del artículo con la de la variante en la línea
// ("Torta de chocolate — 25-30 porciones", D-041.4).
const variantLabelSep = " — "

// Códigos de control fijos (design.md §4.2). "volver" es 0 en cada nivel (L1 es
// la raíz y no lo ofrece); "cancelar" es 9 en los niveles de decisión (L5/L6).
// El código de "Más ▾" es DINÁMICO (moreCode, cart.go): el siguiente entero
// fuera del rango de códigos del nivel, para no colisionar con ningún ítem.
// codeNote es la tecla de las DOS ranuras de indicación (D-041.19). Es 3 y
// no 7 ni 8 por una razón verificable en este mismo archivo: L5 y L6 son los
// únicos niveles de decisión con conjunto FIJO y corto de opciones (1, 2, 9 y en
// L5 además 0) y JAMÁS paginan, así que "Más ▾" —cuyo código es dinámico
// (moreCode)— no aparece ahí y no puede colisionar sea cual sea el page_size del
// tenant. Una tecla para las dos ranuras: el alcance lo da el nivel desde el que
// se pulsa, no una tecla distinta que el cliente tendría que aprender.
const (
	codeBack   = "0"
	codeCancel = "9"
	codeNote   = "3"
)

// stateVarKey es la clave de Conversation.Vars bajo la que se serializa el
// cartState. catalogVarKey es la clave bajo la que el RUNTIME (T2) siembra el
// snapshot crudo del catálogo (map[string]any, la misma forma que
// model.Content.Raw) para que Step —que NO recibe el content resuelto, a
// diferencia de Render— pueda navegar sin hacer I/O (design.md §3.2/§4.1).
const (
	stateVarKey = "cart"
	// catalogVarKey reusa la clave del contrato engine↔módulos (modules.VarContentRaw):
	// el engine siembra ahí el blob crudo del catálogo (model.Content.Raw) antes del
	// Step (design.md §4.1). No es una clave inventada por el módulo.
	catalogVarKey = modules.VarContentRaw
)

// cartState es el estado serializable de la sub-máquina (design.md §3.2). Forma
// EXACTA del contrato para T2/T3. Las etiquetas json garantizan el round-trip
// JSONB (números como float64, structs como map[string]any) sin bytes nulos.
type cartState struct {
	Level   string     `json:"level"`              // nivel actual de la sub-máquina
	CatCode string     `json:"cat_code,omitempty"` // código de la categoría en foco (L2+)
	SKU     string     `json:"sku,omitempty"`      // SKU del artículo en foco (L3/L4)
	Page    int        `json:"page,omitempty"`     // página del nivel de lista actual
	Lines   []cartLine `json:"lines,omitempty"`    // líneas acumuladas del pedido
	// El campo IntakeID (tag `intake_id`, antes `order_id`) se RETIRÓ el 2026-08-12:
	// era un hueco del contrato, no un dato. Nadie le asignó nunca un valor —su único
	// uso era copiarse a sí mismo al reencauzar a L1— y con `omitempty` jamás llegó a
	// escribirse en el JSONB, así que no hay una sola fila que migrar ni un solo
	// `order_id`/`intake_id` guardado que quede huérfano (verificado antes de borrar).
	// El uuid de la solicitud open lo lleva la proyección (projection.go), que lo
	// obtiene de ensureOpenIntake y lo publica en el payload del efecto.
	//
	// VariantCode es la variante elegida del artículo en foco (Plan 041 ·
	// D-041.4). Es TRANSITORIO: vive entre el nivel de variante y el de cantidad,
	// y se limpia al agregar la línea o al salir del artículo. Un artículo sin
	// variantes nunca lo escribe (omitempty ⇒ el JSONB de los tenants v1 no
	// cambia ni un byte).
	VariantCode string `json:"variant_code,omitempty"`
	// Started marca que ya se emitió el efecto cart_started. La pureza del módulo
	// y el contrato de efectos (solo Step declara Effects, no Render) impiden
	// emitirlo en el Enter/Render; se emite EXACTAMENTE UNA vez en el primer Step
	// (design.md §3.3: cart_started al arranque — aquí, primera interacción).
	Started bool `json:"started,omitempty"`
	// Note es la indicación del cliente para TODO el pedido (D-041.19): "dejarlo
	// en portería". Se captura en LevelOrderNote y viaja al cierre, donde acaba en
	// intakes.customer_note. NO es facturable y jamás toca el total (INV-13).
	Note string `json:"note,omitempty"`
	// NoteSplit marca que la indicación que se está escribiendo va a UNA sola
	// unidad de una línea con qty > 1, y que por tanto hay que partirla al guardar
	// (D-041.20). Es TRANSITORIO —vive entre el nivel de alcance y el de texto— y
	// se limpia siempre al salir del nivel de nota, se guarde o no.
	//
	// Existe porque el alcance se elige en una pantalla y el texto en la siguiente:
	// sin él, quien guarda el texto no sabría si el cliente dijo "para las 2" o
	// "solo para 1". Con `omitempty` un carrito que nunca comenta no escribe ni un
	// byte más en el JSONB.
	NoteSplit bool `json:"note_split,omitempty"`
	// BuyerIdx es CUÁNTOS campos del checklist del comprador ya se capturaron
	// (D-041.13, T4.5). Es un CONTADOR, y lo que NO es —ni aquí ni en ningún otro
	// campo de este struct— es el sitio donde vivan las respuestas.
	//
	// Esa ausencia es la tarea entera. cartState se serializa a
	// public.flow_state.vars, JSONB EN CLARO: guardar aquí el RUT o la dirección
	// del cliente sería exactamente el dato personal en claro que la fila cifrada de
	// intake_buyer_data existe para evitar, y además duraría lo que dure la
	// conversación. Por eso cada respuesta sale del módulo EN EL MISMO Step en que
	// se teclea —dentro de un efecto modules.KindPrivate que el sink no persiste— y
	// lo único que queda en el estado es este entero: cuántas van.
	BuyerIdx int `json:"buyer_idx,omitempty"`
	// Reprompts cuenta los inválidos CONSECUTIVOS del nivel actual (Plan 043 · T5.2).
	// Es el primer contador acotado del carrito: hasta esta ola repromptaba sin techo.
	// Solo cuenta DENTRO de un evento (ver inEvent): fuera, el carrito se comporta
	// exactamente como siempre (regresión cero).
	Reprompts int `json:"reprompts,omitempty"`
	// RepromptsEvent es el SELLO de Reprompts: el evento en que se cometieron esos
	// inválidos. Sin él el contador cruzaba el cambio de contexto —hay caminos del
	// runtime que cambian el evento activo conservando Vars, y el cartState viaja
	// dentro de Vars—, y un carrito con dos fallos acumulados armaba el menú de salida
	// al PRIMER fallo del evento siguiente. Module.Step lo compara con conv.EventID y
	// pone el contador a cero si no casan (misma regla que modules.RepromptEventKey
	// aplica a los contadores que sí viven sueltos en Vars).
	//
	// Va aquí y no en Vars porque el sello pertenece al lado del dato que sella; con
	// `omitempty`, un carrito sin inválidos no escribe ni un byte más en el JSONB.
	RepromptsEvent string `json:"reprompts_event_id,omitempty"`
	// inEvent y exitScreen son TRANSITORIOS (sin etiqueta json y NO exportados ⇒
	// json.Marshal los ignora, así que jamás tocan el JSONB de flow_state.vars).
	// Module.Step los estampa y los consume EN EL MISMO turno.
	inEvent    bool
	exitScreen string
}

// cartLine es una línea del pedido (design.md §3.2). SKU/Label son códigos de
// negocio (cero PII); UnitPrice es el precio al momento de agregar.
// Customization es la indicación del cliente para ESA línea (D-041.19): el «sin
// cebolla» que quien prepara tiene que leer. Va con `omitempty` a propósito: un
// carrito sin indicaciones serializa EXACTAMENTE el mismo JSONB que antes de
// T4.1c, y un estado guardado antes de que el campo existiera carga con "" sin
// rama especial (round-trip JSONB, cero regresión).
type cartLine struct {
	SKU           string  `json:"sku"`
	Label         string  `json:"label"`
	Qty           int     `json:"qty"`
	UnitPrice     float64 `json:"unit_price"`
	Customization string  `json:"customization,omitempty"`
}

// loadState reconstruye el cartState desde Vars tolerando el round-trip JSONB
// (map[string]any) o el tipo nativo. Ausente/nil ⇒ estado inicial en L1
// categorías (arranque del carrito).
func loadState(vars map[string]any) cartState {
	st := cartState{Level: LevelCategories}
	raw, ok := vars[stateVarKey]
	if !ok || raw == nil {
		return st
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return cartState{Level: LevelCategories}
	}
	var loaded cartState
	if err := json.Unmarshal(b, &loaded); err != nil {
		return cartState{Level: LevelCategories}
	}
	if loaded.Level == "" {
		loaded.Level = LevelCategories
	}
	return loaded
}

// storeState serializa el cartState de vuelta a Vars como map[string]any (round
// trip por JSON) para que el engine lo persista en JSONB de forma homogénea con
// la relectura (loadState). PURO: solo muta el mapa recibido.
func storeState(vars map[string]any, st cartState) {
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return
	}
	vars[stateVarKey] = m
}

// pageSizeFromVars lee el page_size que el runtime sembró en Vars[VarPageSize]
// (tenant_settings.page_size); si está ausente o es <= 0, cae al default recibido
// (el del Module). Tolera el round-trip JSONB (float64) y el tipo nativo int.
func pageSizeFromVars(vars map[string]any, def int) int {
	switch n := vars[VarPageSize].(type) {
	case int:
		if n > 0 {
			return n
		}
	case int64:
		if n > 0 {
			return int(n)
		}
	case float64:
		if int(n) > 0 {
			return int(n)
		}
	}
	return def
}

// cloneLines copia las líneas para no mutar el estado de entrada (pureza).
func cloneLines(in []cartLine) []cartLine {
	if len(in) == 0 {
		return nil
	}
	out := make([]cartLine, len(in))
	copy(out, in)
	return out
}

// loadCatalog reconstruye el catálogo desde el snapshot que vive en
// Vars["cart_catalog"] (misma forma que model.Content.Raw). Es la vía por la que
// Step —que NO recibe el content resuelto, a diferencia de Render— accede al
// catálogo sin hacer I/O (design.md §4.1, nota de ejecución). Lo siembra el
// runtime. Un snapshot ausente/mal formado deriva en el mismo error que
// ParseCatalog (envuelto sobre model.ErrInvalidFlow).
func loadCatalog(vars map[string]any) (catalogo.Catalog, error) {
	raw, ok := vars[catalogVarKey].(map[string]any)
	if !ok {
		// Ausente o de otro tipo: se trata igual que Raw nil (ParseCatalog
		// devuelve el error de "catálogo ausente").
		raw = nil
	}
	return catalogo.ParseCatalog(model.Content{Raw: raw})
}
