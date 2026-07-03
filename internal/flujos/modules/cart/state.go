// state.go define el estado de la sub-máquina del carrito (design.md §3.2): el
// nivel de navegación en curso, la categoría/artículo en foco, la página de
// paginación, las líneas acumuladas del pedido y el id de la orden. Todo el
// estado vive serializado en Conversation.Vars["cart"] (JSONB), de modo que el
// engine lo persiste tras cada Step sin que el módulo toque BD (PURO).
//
// El módulo es un ÚNICO nodo type:cart (design.md §9.A): no hay un nodo por
// nivel en flow_definitions; el nivel actual es cartState.Level y las
// transiciones "volver"/paginación/agregar son deterministas por la topología
// fija (categories ↔ articles ↔ article ↔ quantity/continue/summary).
package cart

import "encoding/json"

// Niveles de la sub-máquina (design.md §3.2/§4.2). Level es el "nodo" interno
// del carrito. Los dos últimos (closed/cancelled) son terminales: la
// conversación queda en la pantalla final y la entrada se ignora (en T1 no hay
// transición al centinela del flujo; eso lo cablea el flujo real en T2/T5).
const (
	LevelCategories = "categories" // L1 · lista de categorías (raíz, sin "volver")
	LevelArticles   = "articles"   // L2 · artículos de la categoría en foco
	LevelArticle    = "article"    // L3 · menú del artículo (ver desc./agregar/volver)
	LevelQuantity   = "quantity"   // L4 · cantidad libre (qty>=1)
	LevelContinue   = "continue"   // L5 · agregar más / finalizar / cancelar / volver
	LevelSummary    = "summary"    // L6 · resumen + confirmar / seguir / cancelar
	LevelClosed     = "closed"     // terminal · pedido confirmado
	LevelCancelled  = "cancelled"  // terminal · pedido cancelado
)

// Códigos de control fijos (design.md §4.2). "volver" es 0 en cada nivel (L1 es
// la raíz y no lo ofrece); "cancelar" es 9 en los niveles de decisión (L5/L6).
// El código de "Más ▾" es DINÁMICO (moreCode, cart.go): el siguiente entero
// fuera del rango de códigos del nivel, para no colisionar con ningún ítem.
const (
	codeVolver   = "0"
	codeCancelar = "9"
)

// NodeTypeCart es el tipo de nodo que maneja este módulo. Se mantiene local al
// paquete (no se añade a model.NodeType* ni a model.Validate) para no tocar el
// contrato compartido en T1; el cableado del flujo real y su validación llegan
// con el seed del catálogo (T5).
const NodeTypeCart = "cart"

// DefaultPageSize es el tamaño de página por defecto de los niveles de lista
// (L1 categorías, L2 artículos) cuando no se configura otro (design.md §9.E).
// En T1 el tamaño es PURO (constante o WithPageSize); el cableado real desde
// tenant_settings llega en T3.
const DefaultPageSize = 5

// stateVarKey es la clave de Conversation.Vars bajo la que se serializa el
// cartState. catalogVarKey es la clave bajo la que el RUNTIME (T2) siembra el
// snapshot crudo del catálogo (map[string]any, la misma forma que
// model.Content.Raw) para que Step —que NO recibe el content resuelto, a
// diferencia de Render— pueda navegar sin hacer I/O (design.md §3.2/§4.1).
const (
	stateVarKey   = "cart"
	catalogVarKey = "cart_catalog"
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
	OrderID string     `json:"order_id,omitempty"` // uuid de la orden open (se asigna en T2)
}

// cartLine es una línea del pedido (design.md §3.2). SKU/Label son códigos de
// negocio (cero PII); UnitPrice es el precio al momento de agregar.
type cartLine struct {
	SKU       string  `json:"sku"`
	Label     string  `json:"label"`
	Qty       int     `json:"qty"`
	UnitPrice float64 `json:"unit_price"`
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

// cloneLines copia las líneas para no mutar el estado de entrada (pureza).
func cloneLines(in []cartLine) []cartLine {
	if len(in) == 0 {
		return nil
	}
	out := make([]cartLine, len(in))
	copy(out, in)
	return out
}
