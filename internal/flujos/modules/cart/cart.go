// Package cart es el módulo del carrito conversacional (Plan 016, design.md §4):
// una sub-máquina jerárquica que navega un catálogo de dos niveles
// (categorías → artículos), acumula líneas de pedido y cierra con un resumen.
// Es el tercer módulo del Motor de Flujos tras Menú (Plan 006) y Encuesta
// (Plan 014).
//
// PUREZA (invariante, design.md §2/§4.1): el módulo NO hace I/O. Recibe el
// catálogo ya resuelto y navega en memoria; todo su estado vive en
// Conversation.Vars (JSONB) y lo persiste el engine. En T1 no emite efectos ni
// persiste solicitudes (Result.Effects vacío); los efectos llegan en T2 y la
// persistencia/TTL en T2/T3.
//
// UN SOLO NODO (design.md §9.A): la definición del flujo tiene un único nodo
// {"type":"cart"}; los niveles (categorías/artículos/artículo/cantidad/
// continuar/resumen) y el "volver" se manejan internamente con el estado en
// Vars, sin multiplicar nodos en flow_definitions.
//
// CATÁLOGO EN Step (nota de ejecución, design.md §9): la interfaz Module entrega
// el content resuelto SOLO a Render, no a Step (registry.go). Para que Step
// navegue sin romper la pureza, el catálogo viaja como snapshot en
// Vars["cart_catalog"] (misma forma que model.Content.Raw): en T1 lo siembran
// los tests; en T2 lo siembra el runtime al resolver el content del nodo. Así el
// engine, menú y encuesta NO se tocan y el módulo sigue siendo I/O-free.
package cart

import (
	"errors"
	"strconv"
	"strings"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// catalogUnavailable es la pantalla que se muestra cuando no hay catálogo con el
// que navegar (Raw ausente / snapshot no sembrado). No debería ocurrir en el
// camino real: el runtime siembra el catálogo antes del primer Step (T2).
const catalogUnavailable = "El catálogo no está disponible en este momento. Intenta más tarde."

// Module implementa modules.Module para el tipo de nodo "cart".
type Module struct {
	pageSize int
	log      logger.Logger
}

// Option configura el Module al construirlo (patrón functional-options).
type Option func(*Module)

// WithLogger le da al módulo un logger para AVISAR de los campos del catálogo v2
// que el parseo tolerante descartó (Plan 041 · T2.2). Es OPCIONAL y solo
// observabilidad: sin él, el módulo se comporta exactamente igual —los avisos
// siguen en Catalog.Warnings— y la pureza no se toca, porque un aviso no lee ni
// escribe nada del mundo.
func WithLogger(l logger.Logger) Option {
	return func(m *Module) {
		if l != nil {
			m.log = l
		}
	}
}

// WithPageSize fija el tamaño de página de los niveles de lista. Un valor <= 0
// se ignora (se mantiene el default). En T1 es la vía PURA para probar la
// paginación; el cableado desde tenant_settings llega en T3.
func WithPageSize(n int) Option {
	return func(m *Module) {
		if n > 0 {
			m.pageSize = n
		}
	}
}

// New crea el módulo Carrito con el tamaño de página por defecto (design.md §9.E).
func New(opts ...Option) Module {
	m := Module{pageSize: DefaultPageSize}
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

// Type devuelve el identificador del tipo de nodo manejado.
func (Module) Type() string { return NodeTypeCart }

// WaitsForInput indica que el carrito es interactivo: se renderiza y detiene el
// flujo esperando la entrada del usuario (igual que menú/encuesta).
func (Module) WaitsForInput() bool { return true }

// Render produce la pantalla de ARRANQUE del carrito: la lista de categorías
// (L1, página 0). Recibe el catálogo ya resuelto por el engine (model.Content.Raw
// vía ParseCatalog). El resto de pantallas (tras cada Step) las produce Step en
// Result.Outputs, porque el carrito permanece en el MISMO nodo (Next==nil) y el
// engine no vuelve a llamar Render dentro de la sub-máquina.
func (m Module) Render(_ model.Node, content model.Content) []string {
	cat, err := ParseCatalog(content)
	if err != nil {
		return []string{catalogUnavailable}
	}
	m.warnCatalog(cat)
	return []string{screenCategories(cat, cartState{Level: LevelCategories}, m.pageSize)}
}

// warnCatalog vuelca por el log los campos v2 que el parseo tolerante descartó
// (Plan 041 · T2.2). Se llama SOLO donde el catálogo se resuelve al ENTRAR al
// nodo (Render y Prime), no en Step: Step re-parsea el snapshot en cada mensaje
// del cliente y avisar ahí repetiría el mismo defecto una vez por tecleo, hasta
// enterrar el log del resto de la flota. Un aviso por conversación basta para
// que el dueño se entere de que su catálogo tiene una parte ilegible.
func (m Module) warnCatalog(cat Catalog) {
	if m.log == nil {
		return
	}
	for _, w := range cat.Warnings {
		m.log.Warn("cart: campo del catálogo v2 descartado",
			"categoria", w.Category, "sku", w.SKU, "campo", w.Field, "motivo", w.Reason)
	}
}

// Step procesa la entrada del usuario sobre el nodo carrito: carga el catálogo
// (snapshot en Vars) y el cartState, aplica la transición de la sub-máquina
// (advance), guarda el nuevo estado en Vars y DECLARA los efectos de negocio
// (design.md §3.3). El carrito permanece en el mismo nodo (Next==nil) durante
// toda la navegación; devuelve la pantalla del nuevo nivel en Outputs.
//
// cart_started se emite EXACTAMENTE UNA vez, en el primer Step (flag Started): la
// pureza del módulo y el contrato de efectos impiden emitirlo en el Enter/Render
// (Render no declara Effects). Es lo más cercano a "al arranque" (design.md §3.3)
// sin tocar el contrato del engine.
func (m Module) Step(_ model.Node, conv model.Conversation, input string) modules.Result {
	vars := cloneVars(conv.Vars)
	cat, err := loadCatalog(vars)
	if err != nil {
		return modules.Result{Vars: vars, Outputs: []string{catalogUnavailable}}
	}
	// page_size REAL del tenant: el runtime lo siembra en Vars[VarPageSize]
	// (tenant_settings.page_size); sin sembrar cae al default del Module (design.md
	// §9.E). Toda la paginación de la sub-máquina (que ocurre en Step) lo respeta;
	// Render (una sola pantalla, al arranque) usa el default del Module.
	size := pageSizeFromVars(vars, m.pageSize)
	// Checklist del comprador REAL del tenant (tenant_settings.buyer_fields, T4.5),
	// sembrado por la misma vía que el page_size. Ausente ⇒ lista vacía ⇒ el carrito
	// no pregunta nada y el recorrido es el de siempre (INV-15).
	fields := loadBuyerFields(vars)
	st := loadState(vars)
	st.inEvent = modules.InEvent(conv)
	// Los inválidos son del EVENTO en que se cometieron: si el estado viene sellado con
	// otro (la conversación cambió de contexto por un camino que conserva Vars), el
	// contador arranca de cero aquí. Sin esto, dos fallos en el carrito armaban el menú
	// de salida al primer fallo del evento siguiente (ver RepromptsEvent).
	if st.RepromptsEvent != conv.EventID {
		st.Reprompts = 0
		st.RepromptsEvent = ""
	}
	var effects []modules.Effect
	if !st.Started {
		st.Started = true
		effects = append(effects, event(EffectCartStarted, map[string]any{}))
	}
	newSt, outs, stepEffects := advance(cat, st, input, size, fields)
	effects = append(effects, stepEffects...)
	// Contador: si advance NO pasó por reprompt(), el contador no se movió ⇒ la entrada
	// fue válida ⇒ se reinicia. Evita tocar los 8 sitios de reprompt para reiniciarlo.
	if newSt.Reprompts == st.Reprompts {
		newSt.Reprompts = 0
	}
	// El contador que sobrevive al turno se sella con el evento en que se contó; el que
	// murió (entrada válida, o el tercer inválido que armó el menú) suelta su sello.
	newSt.RepromptsEvent = ""
	if newSt.Reprompts > 0 {
		newSt.RepromptsEvent = conv.EventID
	}
	if newSt.exitScreen != "" {
		outs = modules.ArmExitMenu(vars, conv, newSt.exitScreen).Outputs
		newSt.exitScreen = ""
	}
	storeState(vars, newSt)
	return modules.Result{Vars: vars, Outputs: outs, Effects: effects}
}

// advance es el CORAZÓN PURO de la sub-máquina: dada la topología fija, el
// estado actual y la entrada, produce el nuevo estado, la pantalla a emitir y los
// efectos declarados por la transición (design.md §4.2). No toca Vars ni BD; el
// Module la envuelve.
func advance(cat Catalog, st cartState, input string, size int, fields []store.BuyerField) (cartState, []string, []modules.Effect) {
	in := strings.TrimSpace(input)
	switch st.Level {
	case LevelCategories:
		return stepCategories(cat, st, in, size)
	case LevelArticles:
		return stepArticles(cat, st, in, size)
	case LevelArticle:
		return stepArticle(cat, st, in, size)
	case LevelVariant:
		return stepVariant(cat, st, in, size)
	case LevelQuantity:
		return stepQuantity(cat, st, in, size)
	case LevelContinue:
		return stepContinue(cat, st, in, size)
	case LevelItemNoteScope:
		return stepItemNoteScope(cat, st, in)
	case LevelItemNote:
		return stepItemNote(cat, st, in)
	case LevelSummary:
		return stepSummary(cat, st, in, size, fields)
	case LevelOrderNote:
		return stepOrderNote(st, in)
	case LevelBuyerData:
		return stepBuyerData(st, in, fields)
	case LevelClosed, LevelCancelled:
		// Terminal: la entrada se ignora, se re-muestra la pantalla final.
		return st, []string{terminalScreen(st)}, nil
	default:
		// Estado inconsistente: reencauzar a la raíz (preservando Started). La nota
		// del pedido se conserva por la misma razón que las líneas: es algo que el
		// cliente ya dijo, y un nivel que no existe no es motivo para olvidarlo.
		// BuyerIdx viaja con ellas por una razón más dura: los campos que cuenta YA
		// están escritos y cifrados, y reiniciarlo volvería a pedirle al cliente un
		// dato personal que ya dio.
		st = cartState{Level: LevelCategories, Lines: st.Lines, IntakeID: st.IntakeID,
			Started: st.Started, Note: st.Note, BuyerIdx: st.BuyerIdx}
		return st, []string{screenCategories(cat, st, size)}, nil
	}
}

// --- L1 · Categorías -------------------------------------------------------

func stepCategories(cat Catalog, st cartState, in string, size int) (cartState, []string, []modules.Effect) {
	codes := categoryCodes(cat)
	if in == moreCode(codes) && hasMore(len(cat.Categories), st.Page, size) {
		st.Page++
		return st, []string{screenCategories(cat, st, size)}, nil
	}
	if c, ok := findCategory(cat, in); ok {
		st.Level = LevelArticles
		st.CatCode = c.Code
		st.Page = 0
		eff := event(EffectCategorySelected, map[string]any{"category_code": c.Code})
		return st, []string{screenArticles(c, st, size)}, []modules.Effect{eff}
	}
	st, outs := reprompt(st, screenCategories(cat, st, size))
	return st, outs, nil
}

// --- L2 · Artículos de la categoría ---------------------------------------

func stepArticles(cat Catalog, st cartState, in string, size int) (cartState, []string, []modules.Effect) {
	category, ok := findCategory(cat, st.CatCode)
	if !ok {
		st, outs := toCategories(cat, st, size)
		return st, outs, nil
	}
	if in == codeVolver {
		st, outs := toCategories(cat, st, size)
		return st, outs, nil
	}
	codes := articleCodes(category)
	if in == moreCode(codes) && hasMore(len(category.Items), st.Page, size) {
		st.Page++
		return st, []string{screenArticles(category, st, size)}, nil
	}
	if a, ok := findArticle(category, in); ok {
		st.Level = LevelArticle
		st.SKU = a.SKU
		st.Page = 0
		return st, []string{screenArticle(a, false)}, nil
	}
	st, outs := reprompt(st, screenArticles(category, st, size))
	return st, outs, nil
}

// --- L3 · Menú del artículo ------------------------------------------------

func stepArticle(cat Catalog, st cartState, in string, size int) (cartState, []string, []modules.Effect) {
	category, a, ok := locate(cat, st.CatCode, st.SKU)
	if !ok {
		st, outs := toArticles(cat, st, size)
		return st, outs, nil
	}
	switch in {
	case "1": // Ver descripción → item_viewed{sku}; re-muestra L3 con la descripción.
		eff := event(EffectItemViewed, map[string]any{"sku": a.SKU})
		return st, []string{screenArticle(a, true)}, []modules.Effect{eff}
	case "2": // Agregar al pedido → L3b variante (si tiene) o L4 cantidad.
		st, screen := toAdd(st, a)
		return st, []string{screen}, nil
	case codeVolver:
		st, outs := toArticlesOf(category, st, size)
		return st, outs, nil
	default:
		st, outs := reprompt(st, screenArticle(a, false))
		return st, outs, nil
	}
}

// --- L3b · Variante del artículo (Plan 041 · D-041.4) ----------------------

// stepVariant resuelve la elección de variante: la lista va numerada POR
// POSICIÓN (1..N), "0" vuelve a la ficha del artículo y cualquier otra cosa
// reprompt. Este nivel SOLO existe para artículos con variantes; uno sin ellas
// nunca lo pisa (REQ-08: el flujo v1 no gana un paso).
func stepVariant(cat Catalog, st cartState, in string, size int) (cartState, []string, []modules.Effect) {
	_, a, ok := locate(cat, st.CatCode, st.SKU)
	if !ok {
		st, outs := toArticles(cat, st, size)
		return st, outs, nil
	}
	if !a.HasVariants() {
		// El catálogo cambió bajo los pies (el artículo perdió sus variantes):
		// se sigue por el camino sin variante en vez de quedar atrapado.
		st.Level = LevelQuantity
		st.VariantCode = ""
		return st, []string{screenQuantity(a)}, nil
	}
	if in == codeVolver {
		st.Level = LevelArticle
		st.VariantCode = ""
		return st, []string{screenArticle(a, false)}, nil
	}
	if v, ok := variantByPosition(a, in); ok {
		st.Level = LevelQuantity
		st.VariantCode = v.Code
		return st, []string{screenQuantityOf(lineLabel(a, v, true))}, nil
	}
	st, outs := reprompt(st, screenVariants(a))
	return st, outs, nil
}

// --- L4 · Cantidad ---------------------------------------------------------

func stepQuantity(cat Catalog, st cartState, in string, size int) (cartState, []string, []modules.Effect) {
	category, a, ok := locate(cat, st.CatCode, st.SKU)
	if !ok {
		st, outs := toArticles(cat, st, size)
		return st, outs, nil
	}
	v, hasVariant := selectedVariant(a, st)
	if in == codeVolver {
		st, screen := backFromQuantity(st, a)
		return st, []string{screen}, nil
	}
	if a.HasVariants() && !hasVariant {
		// Estado incoherente (la variante elegida ya no existe en el catálogo):
		// se vuelve a preguntar en vez de cobrar el precio de referencia, que es
		// justo lo que el contrato v2 prohíbe.
		st.Level = LevelVariant
		st.VariantCode = ""
		return st, []string{screenVariants(a)}, nil
	}
	qty, err := strconv.Atoi(in)
	if err != nil || qty < 1 {
		// Entrada no numérica o < 1 → reprompt del MISMO paso (design.md §9.D).
		return st, []string{"Escribe una cantidad válida (un número mayor o igual a 1).\n\n" + screenQuantityOf(lineLabel(a, v, hasVariant))}, nil
	}
	// item_added: agrega la línea y pasa a L5 continuar. El runtime, al recibir
	// este efecto, ASEGURA una solicitud "open" por (tenant, contact) (design.md §3.4).
	// Con variante la línea lleva SU precio y SU etiqueta y el sku compuesto; el
	// combo (components) es UNA línea al precio del artículo, sin nada especial:
	// sus componentes son información del dueño, no artículos que se cobren.
	line := newLine(a, v, hasVariant, qty)
	st.Lines = append(cloneLines(st.Lines), line)
	st.Level = LevelContinue
	st.VariantCode = ""
	eff := withLineSnapshot(event(EffectItemAdded, map[string]any{
		"sku":        line.SKU,
		"label":      line.Label,
		"qty":        line.Qty,
		"unit_price": line.UnitPrice,
	}), st.Lines)
	return st, []string{screenContinue(category)}, []modules.Effect{eff}
}

// --- L5 · Continuar --------------------------------------------------------

func stepContinue(cat Catalog, st cartState, in string, size int) (cartState, []string, []modules.Effect) {
	category, hasCat := findCategory(cat, st.CatCode)
	switch in {
	case "1": // Agregar más de la MISMA categoría → L2 (CatCode intacto, design.md §9.C).
		if !hasCat {
			st, outs := toCategories(cat, st, size)
			return st, outs, nil
		}
		st, outs := toArticlesOf(category, st, size)
		return st, outs, nil
	case "2": // Finalizar → L6 resumen.
		st.Level = LevelSummary
		return st, []string{screenSummary(st.Lines, st.Note)}, nil
	case codeIndicacion: // Indicación para la línea recién agregada (D-041.19).
		return toItemNote(cat, st, size)
	case codeCancelar: // Cancelar pedido completo (design.md §1.2) → cart_cancelled.
		st.Level = LevelCancelled
		return st, []string{screenCancelled()}, []modules.Effect{event(EffectCartCancelled, map[string]any{})}
	case codeVolver: // Volver al artículo en foco (L3).
		if _, a, ok := locate(cat, st.CatCode, st.SKU); ok {
			st.Level = LevelArticle
			return st, []string{screenArticle(a, false)}, nil
		}
		st, outs := toArticles(cat, st, size)
		return st, outs, nil
	default:
		category = mustCategory(category, hasCat)
		st, outs := reprompt(st, screenContinue(category))
		return st, outs, nil
	}
}

// --- L6 · Resumen y confirmar ---------------------------------------------

func stepSummary(cat Catalog, st cartState, in string, size int, fields []store.BuyerField) (cartState, []string, []modules.Effect) {
	switch in {
	case "1": // Confirmar → checklist del comprador si lo hay, y si no cierra.
		// El checklist (D-041.13) se intercala AQUÍ y en ningún otro sitio: entre
		// "confirmo" y el pedido cerrado. Con buyer_fields vacío —el default de la
		// columna y el caso de todos los tenants de hoy— esta rama es literalmente el
		// cierre de siempre, misma pantalla y mismo efecto (INV-15).
		if pending := pendingBuyerFields(fields, st.BuyerIdx); len(pending) > 0 {
			st.Level = LevelBuyerData
			return st, []string{screenBuyerData(pending[0], st.BuyerIdx, len(fields))}, nil
		}
		return closeCart(st)
	case "2": // Seguir agregando → L2 misma categoría, o L1 si no hay categoría en foco.
		if category, ok := findCategory(cat, st.CatCode); ok {
			st, outs := toArticlesOf(category, st, size)
			return st, outs, nil
		}
		st, outs := toCategories(cat, st, size)
		return st, outs, nil
	case codeIndicacion: // Indicación para TODO el pedido (D-041.19).
		st.Level = LevelOrderNote
		return st, []string{screenOrderNote(st.Note)}, nil
	case codeCancelar:
		st.Level = LevelCancelled
		return st, []string{screenCancelled()}, []modules.Effect{event(EffectCartCancelled, map[string]any{})}
	default:
		st, outs := reprompt(st, screenSummary(st.Lines, st.Note))
		return st, outs, nil
	}
}

// --- Indicaciones del cliente (D-041.19 / D-041.20) ------------------------

// toItemNote resuelve la tecla 3 desde L5: con UNA unidad va directo al texto (el
// caso corriente, cero teclas nuevas respecto de D-041.19) y con más de una
// pregunta antes el alcance, porque la indicación es siempre de línea entera y sin
// preguntar anotaría las N unidades (D-041.20).
//
// La línea comentada es SIEMPRE la última: a L5 solo se llega desde stepQuantity
// tras un item_added, así que "este artículo" no es ambiguo nunca —ni con dos
// líneas del mismo producto—. Sin líneas no hay nada que comentar: es un estado
// que el recorrido no produce, y se responde con el reprompt del propio nivel en
// vez de con una pantalla de indicación sobre la nada.
func toItemNote(cat Catalog, st cartState, size int) (cartState, []string, []modules.Effect) {
	if len(st.Lines) == 0 {
		category, hasCat := findCategory(cat, st.CatCode)
		st, outs := reprompt(st, screenContinue(mustCategory(category, hasCat)))
		return st, outs, nil
	}
	line := st.Lines[len(st.Lines)-1]
	if line.Qty > 1 {
		st.Level = LevelItemNoteScope
		return st, []string{screenItemNoteScope(line)}, nil
	}
	st.Level = LevelItemNote
	st.NoteSplit = false
	return st, []string{screenItemNote(line, false)}, nil
}

// stepItemNoteScope resuelve "¿para las N o solo para 1?" (D-041.20). Aquí NO se
// parte nada todavía: elegir el alcance solo decide qué se hará al guardar un
// texto válido. Si el cliente desiste después, el carrito queda como estaba.
func stepItemNoteScope(cat Catalog, st cartState, in string) (cartState, []string, []modules.Effect) {
	if len(st.Lines) == 0 {
		return backToContinue(cat, st, "")
	}
	line := st.Lines[len(st.Lines)-1]
	switch in {
	case "1": // Para las N: una sola línea ×N con su indicación.
		st.Level = LevelItemNote
		st.NoteSplit = false
		return st, []string{screenItemNote(line, false)}, nil
	case "2": // Solo para 1: la línea se partirá AL GUARDAR el texto.
		st.Level = LevelItemNote
		st.NoteSplit = true
		return st, []string{screenItemNote(line, true)}, nil
	case codeVolver:
		return backToContinue(cat, st, "")
	default:
		st, outs := reprompt(st, screenItemNoteScope(line))
		return st, outs, nil
	}
}

// stepItemNote captura el texto de la indicación de línea y vuelve a L5.
//
// El split ocurre AQUÍ, al guardar un texto válido, y no al elegir el alcance
// (D-041.20): si el cliente desiste con 0 o se pasa del límite y abandona, la
// línea no se ha partido y el carrito queda exactamente como estaba.
func stepItemNote(cat Catalog, st cartState, in string) (cartState, []string, []modules.Effect) {
	if len(st.Lines) == 0 {
		return backToContinue(cat, st, "")
	}
	idx := len(st.Lines) - 1
	line := st.Lines[idx]
	if in == codeVolver {
		// Sin indicación; y si ya había una, se deja como estaba.
		return backToContinue(cat, st, "")
	}
	note, err := SanitizeNote(in)
	if err != nil {
		var tooLong NoteTooLongError
		if errors.As(err, &tooLong) {
			// Reprompt del MISMO paso: se rechaza y se repregunta, jamás se trunca.
			return st, []string{noteTooLongMsg(tooLong) + "\n\n" + screenItemNote(line, st.NoteSplit)}, nil
		}
		return st, []string{screenItemNote(line, st.NoteSplit)}, nil
	}
	if note == "" {
		// Vacío tras sanear ≡ 0: sin indicación, sin error (REQ-33e).
		return backToContinue(cat, st, "")
	}

	scope := scopeItem
	splitFrom := 0
	lines := cloneLines(st.Lines)
	if st.NoteSplit && line.Qty > 1 {
		// La línea ×N se sustituye por ×(N-1) sin indicación y ×1 con ella, EN ESE
		// ORDEN: la comentada queda la última, que es la que el siguiente "3" volvería
		// a tomar. Mismo sku, mismo label, mismo unit_price y misma suma de qty: es
		// una re-agrupación, no una edición del pedido, y el total no se mueve (INV-16).
		scope = scopeItemSplit
		splitFrom = line.Qty
		rest := line
		rest.Qty = line.Qty - 1
		rest.Customization = ""
		commented := line
		commented.Qty = 1
		commented.Customization = note
		lines = append(lines[:idx], rest, commented)
	} else {
		lines[idx].Customization = note
	}
	st.Lines = lines
	st.NoteSplit = false

	eff := noteAddedEffect(scope, line.SKU, note, splitFrom, st.Lines)
	newSt, outs, _ := backToContinue(cat, st, noteAck(note, scope, line.Qty))
	return newSt, outs, []modules.Effect{eff}
}

// stepOrderNote captura el texto de la indicación de TODO el pedido y vuelve a L6.
// No toca las líneas ni el total: la nota vive en la cabecera del pedido y acaba
// en intakes.customer_note.
func stepOrderNote(st cartState, in string) (cartState, []string, []modules.Effect) {
	if in == codeVolver {
		st.Level = LevelSummary
		return st, []string{screenSummary(st.Lines, st.Note)}, nil
	}
	note, err := SanitizeNote(in)
	if err != nil {
		var tooLong NoteTooLongError
		if errors.As(err, &tooLong) {
			return st, []string{noteTooLongMsg(tooLong) + "\n\n" + screenOrderNote(st.Note)}, nil
		}
		return st, []string{screenOrderNote(st.Note)}, nil
	}
	if note == "" {
		st.Level = LevelSummary
		return st, []string{screenSummary(st.Lines, st.Note)}, nil
	}
	st.Note = note
	st.Level = LevelSummary
	return st, []string{noteAck(note, scopeOrder, 0) + "\n" + screenSummary(st.Lines, st.Note)},
		[]modules.Effect{noteAddedEffect(scopeOrder, "", note, 0, st.Lines)}
}

// backToContinue devuelve la sub-máquina a L5 desde cualquiera de los niveles de
// indicación, con un acuse opcional antepuesto (patrón del reprompt: el aviso
// encabeza la pantalla del nivel). Limpia SIEMPRE el alcance en curso: el split
// pendiente muere con la salida del nivel, se haya guardado o no.
func backToContinue(cat Catalog, st cartState, ack string) (cartState, []string, []modules.Effect) {
	category, hasCat := findCategory(cat, st.CatCode)
	st.Level = LevelContinue
	st.NoteSplit = false
	screen := screenContinue(mustCategory(category, hasCat))
	if ack != "" {
		screen = ack + "\n" + screen
	}
	return st, []string{screen}, nil
}

// --- transiciones auxiliares ----------------------------------------------

// toCategories reencauza a L1 conservando las líneas y la solicitud (design.md §9.C:
// "volver" desde artículos sube a categorías con el carrito intacto).
func toCategories(cat Catalog, st cartState, size int) (cartState, []string) {
	st.Level = LevelCategories
	st.CatCode = ""
	st.SKU = ""
	st.Page = 0
	st.VariantCode = "" // sin artículo en foco no hay variante en curso
	return st, []string{screenCategories(cat, st, size)}
}

// toArticles reencauza a L2 de la categoría en foco; si ya no existe, cae a L1.
func toArticles(cat Catalog, st cartState, size int) (cartState, []string) {
	if category, ok := findCategory(cat, st.CatCode); ok {
		return toArticlesOf(category, st, size)
	}
	return toCategories(cat, st, size)
}

// toArticlesOf reencauza a L2 de una categoría concreta (misma categoría).
func toArticlesOf(category Category, st cartState, size int) (cartState, []string) {
	st.Level = LevelArticles
	st.CatCode = category.Code
	st.SKU = ""
	st.Page = 0
	st.VariantCode = "" // se suelta el artículo en foco: la variante muere con él
	return st, []string{screenArticles(category, st, size)}
}

// mustCategory devuelve la categoría si es válida, o una vacía si no (para el
// reprompt de L5 cuando el catálogo cambió: se re-muestra un continuar neutro).
func mustCategory(category Category, ok bool) Category {
	if ok {
		return category
	}
	return Category{}
}

// reprompt re-muestra el nivel actual precedido de un aviso, sin avanzar. DENTRO de
// un evento cuenta los inválidos y, al tercero, ARMA el menú de salida (D-043.10) en
// vez de repreguntar por enésima vez; fuera de un evento repromptea como siempre.
func reprompt(st cartState, screen string) (cartState, []string) {
	if st.inEvent {
		st.Reprompts++
		if st.Reprompts >= modules.MaxReprompts {
			st.Reprompts = 0
			st.exitScreen = screen
			return st, nil // Module.Step traduce exitScreen a la salida.
		}
	}
	return st, []string{"Opción no válida. Responde con el número de una de las opciones.\n\n" + screen}
}

// --- localización en el catálogo ------------------------------------------

func findCategory(cat Catalog, code string) (Category, bool) {
	if code == "" {
		return Category{}, false
	}
	for _, c := range cat.Categories {
		if c.Code == code {
			return c, true
		}
	}
	return Category{}, false
}

func findArticle(category Category, code string) (Article, bool) {
	if code == "" {
		return Article{}, false
	}
	for _, a := range category.Items {
		if a.Code == code {
			return a, true
		}
	}
	return Article{}, false
}

// locate resuelve la categoría (por código) y el artículo en foco (por SKU) del
// estado. El artículo se guarda por SKU (identificador de negocio estable),
// no por Code, para no depender de la posición en el catálogo.
func locate(cat Catalog, catCode, sku string) (Category, Article, bool) {
	category, ok := findCategory(cat, catCode)
	if !ok {
		return Category{}, Article{}, false
	}
	for _, a := range category.Items {
		if a.SKU == sku {
			return category, a, true
		}
	}
	return category, Article{}, false
}

// cloneVars copia el mapa de variables para mantener la pureza (no mutar el
// estado de entrada). nil → mapa nuevo. Mismo patrón que menu/survey.
func cloneVars(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}
