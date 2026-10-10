// Porta internal/flujos/modules/cart/cart.go @ 9d5a4b6 (trozo «indicaciones»:
// toItemNote, stepItemNoteScope, stepItemNote, stepOrderNote y backToContinue; el
// viejo es un solo fichero y aquí nace partido, 05 E-13)

// cart_notes.go son los tres niveles de INDICACIÓN del cliente (Plan 041 · T4.1c,
// D-041.19 + D-041.20): la de una línea («sin cebolla») y la de todo el pedido
// («dejarlo en portería»). NO son pasos del recorrido: son ramas OPCIONALES colgadas
// de la tecla «3» de dos menús que ya se imprimían (L5 y L6), y se vuelve al nivel
// de origen en cuanto se resuelven. Quien no pulsa «3» teclea exactamente lo mismo
// que antes de que existieran (INV-15). En cualquier otro nivel, «3» sigue siendo lo
// que era: un código de catálogo, una cantidad o un inválido.
//
// No exporta nada: lo que promete se ve por Module.Step. Una indicación JAMÁS toca
// el dinero: ni el total ni el precio de una línea (INV-13).
//
// «3» EN L5 · indicación de la línea. La línea comentada es SIEMPRE la última: a L5
// solo se llega tras un item_added, así que «este artículo» no es ambiguo nunca, ni
// con dos líneas del mismo producto.
//   - Con UNA unidad va directo al texto (LevelItemNote): el caso corriente no gana
//     una pantalla.
//   - Con MÁS de una pregunta antes el ALCANCE (LevelItemNoteScope), porque la
//     indicación es siempre de línea entera y sin preguntar anotaría las N unidades.
//   - Sin líneas —un estado que el recorrido no produce— es un inválido de opción
//     de L5.
//
// ALCANCE (LevelItemNoteScope): «1» ⇒ para las N; «2» ⇒ solo para 1; «0» ⇒ vuelve a
// L5 sin indicación; lo demás, inválido. Elegir el alcance NO parte nada todavía:
// solo decide qué se hará al guardar un texto válido.
//
// TEXTO DE LÍNEA (LevelItemNote)
//   - «0» ⇒ vuelve a L5 sin tocar nada; si la línea ya tenía indicación, se queda
//     como estaba.
//   - El texto pasa por el saneo de la puerta (intakes.SanitizeNote). Vacío tras
//     sanear ≡ «0»: sin indicación y sin error (REQ-33e). Pasado del límite
//     (intakes.MaxNoteRunes, medido DESPUÉS de sanear) se RECHAZA en el mismo paso
//     —«Esa indicación es muy larga (N de 280 caracteres). Escríbela más corta.», una
//     línea en blanco y la misma pantalla— y jamás se trunca; el carrito no cambia.
//   - Un texto válido se guarda como `customization` de la última línea
//     (reemplazando la que hubiera), declara note_added y vuelve a L5 con el acuse
//     —`Anotado: "<texto>" ✅`— delante de la pantalla.
//   - «Solo para 1» sobre una línea ×N la PARTE al guardar: ×(N-1), que CONSERVA la
//     indicación que la línea ya tuviera, y ×1 con la nueva, EN ESE ORDEN (la
//     comentada queda la última, que es la que el siguiente «3» volvería a tomar).
//     Mismo sku, misma etiqueta, mismo precio y misma suma de cantidades: es una
//     re-agrupación, no una edición, y el total no se mueve (INV-16). Que el resto
//     herede la indicación es la decisión D-044.18: quien pidió «con cebolla» para
//     las dos y luego «sin sal» para una NO se queda sin cebolla en la otra. El
//     acuse es `Anotado para 1 de las N: "<texto>" ✅` y el efecto lleva
//     split_from_qty. El split ocurre AQUÍ y no al elegir el alcance: si el cliente
//     desiste o se pasa del límite, la línea no se ha partido.
//   - El alcance en curso muere SIEMPRE al salir del nivel, se haya guardado o no.
//
// TEXTO DE PEDIDO (LevelOrderNote): «0» o vacío tras sanear ⇒ vuelve al resumen sin
// tocar la nota que hubiera; demasiado largo ⇒ rechazo en el mismo paso; un texto
// válido se guarda como la nota del pedido (reemplazando la anterior), declara
// note_added con ámbito "order" y vuelve al resumen —que ya la enseña bajo el total—
// con el acuse `Anotado para todo el pedido: "<texto>" ✅` delante.
//
// Los tres niveles de texto son TEXTO LIBRE: ni cascada ni consulta los miran
// (preresolutor.go, consulta.go), y un rechazo por largo no es un inválido de opción.

package cart

import (
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

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
func toItemNote(cat catalogo.Catalog, st cartState, size int) (cartState, []string, []modules.Effect) {
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
func stepItemNoteScope(cat catalogo.Catalog, st cartState, in string) (cartState, []string, []modules.Effect) {
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
	case codeBack:
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
func stepItemNote(cat catalogo.Catalog, st cartState, in string) (cartState, []string, []modules.Effect) {
	if len(st.Lines) == 0 {
		return backToContinue(cat, st, "")
	}
	idx := len(st.Lines) - 1
	line := st.Lines[idx]
	if in == codeBack {
		// Sin indicación; y si ya había una, se deja como estaba.
		return backToContinue(cat, st, "")
	}
	note, err := intakes.SanitizeNote(in)
	if err != nil {
		var tooLong intakes.NoteTooLongError
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
		// La línea ×N se sustituye por ×(N-1) —que CONSERVA la indicación que ya
		// tuviera— y ×1 con la nueva, EN ESE ORDEN: la comentada queda la última, que
		// es la que el siguiente "3" volvería a tomar. Mismo sku, mismo label, mismo
		// unit_price y misma suma de qty: es una re-agrupación, no una edición del
		// pedido, y el total no se mueve (INV-16).
		//
		// Que el resto herede la indicación es la decisión de producto D-044.18 (cierra
		// A4/MD-041.12 del 041): quien pidió «con cebolla» para las dos y luego «sin
		// sal» para una NO se queda sin cebolla en la otra. Partir re-agrupa unidades;
		// no borra lo que el cliente ya había pedido. Por eso `rest` copia la línea
		// entera —Customization incluida— y solo baja la cantidad.
		scope = scopeItemSplit
		splitFrom = line.Qty
		rest := line
		rest.Qty = line.Qty - 1
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
	if in == codeBack {
		st.Level = LevelSummary
		return st, []string{screenSummary(st.Lines, st.Note)}, nil
	}
	note, err := intakes.SanitizeNote(in)
	if err != nil {
		var tooLong intakes.NoteTooLongError
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
func backToContinue(cat catalogo.Catalog, st cartState, ack string) (cartState, []string, []modules.Effect) {
	category, hasCat := findCategory(cat, st.CatCode)
	st.Level = LevelContinue
	st.NoteSplit = false
	screen := screenContinue(mustCategory(category, hasCat))
	if ack != "" {
		screen = ack + "\n" + screen
	}
	return st, []string{screen}, nil
}
