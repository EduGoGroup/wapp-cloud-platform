// buyer.go es el paso de DATOS DEL COMPRADOR del carrito (Plan 041 · T4.5,
// D-041.13 / ADR-0031 §5): el checklist que el dueño configura —RUT, dirección de
// entrega, referencia— y que el cliente rellena entre el resumen y el cierre, un
// campo por mensaje.
//
// LA REGLA QUE GOBIERNA ESTE ARCHIVO. El valor que teclea el cliente es DATO
// PERSONAL, y su único destino es la fila cifrada de public.intake_buyer_data. No
// entra en public.flow_events (outbox append-only en claro), no entra en
// public.flow_state.vars (el estado conversacional, también en claro), no entra en
// el summary.json y no se loguea. La forma de garantizarlo no es la disciplina de
// quien edite esto mañana: es que el valor no se guarda en ningún sitio de este
// paquete. Se lee del mensaje, se sanea, se mete en un efecto modules.KindPrivate
// —el Kind que el PersistSink NO escribe en flow_events— y se suelta. Lo único que
// sobrevive en el estado es cartState.BuyerIdx, un contador.
//
// El módulo sigue PURO: aquí no hay I/O. El checklist llega sembrado en Vars por la
// ResumePolicy (igual que el page_size del tenant) y el cifrado y la escritura los
// hace el Projector, que es el adaptador impuro.
package cart

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// VarBuyerFields es la clave de Conversation.Vars bajo la que el RUNTIME siembra el
// checklist del comprador del tenant (tenant_settings.buyer_fields), igual que
// siembra el catálogo y el page_size. Ausente o vacía ⇒ no se pregunta nada.
//
// Lo que viaja por aquí es CONFIGURACIÓN (claves y etiquetas), nunca respuestas:
// esta clave sí acaba en el JSONB del estado, y ahí no puede haber PII.
const VarBuyerFields = "cart_buyer_fields"

// loadBuyerFields lee el checklist sembrado en Vars tolerando el round-trip JSONB
// (el estado se guarda y se relee entre mensajes, así que lo que se sembró como
// []store.BuyerField vuelve como []any de map[string]any). Mismo mecanismo que
// loadState: un marshal/unmarshal en vez de un type-switch por forma.
//
// Devuelve SOLO los campos que el carrito va a preguntar —los `required` con clave
// no vacía, en el orden configurado— porque es la única lista con la que trabaja el
// paso: quien pregunta y quien cuenta cuántas van tienen que ver exactamente lo
// mismo, o el contador señalaría a otro campo.
func loadBuyerFields(vars map[string]any) []store.BuyerField {
	raw, ok := vars[VarBuyerFields]
	if !ok || raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var all []store.BuyerField
	if err := json.Unmarshal(b, &all); err != nil {
		return nil
	}
	out := make([]store.BuyerField, 0, len(all))
	for _, f := range all {
		if f.Required && f.Key != "" {
			out = append(out, f)
		}
	}
	return out
}

// pendingBuyerFields son los campos del checklist que TODAVÍA no se han capturado
// en esta conversación. Con el contador por encima de la lista (el dueño recortó su
// checklist a media conversación) devuelve vacío: lo que no se puede preguntar no
// puede bloquear un cierre.
func pendingBuyerFields(fields []store.BuyerField, idx int) []store.BuyerField {
	if idx < 0 {
		idx = 0
	}
	if idx >= len(fields) {
		return nil
	}
	return fields[idx:]
}

// stepBuyerData captura UN campo del checklist y avanza (D-041.13). Tres salidas:
//
//   - `0` vuelve al resumen SIN perder lo ya capturado (el contador se conserva:
//     lo que el cliente ya escribió ya está guardado y cifrado, y volvérselo a
//     preguntar sería pedirle dos veces su RUT).
//   - Un valor válido se emite en su efecto privado y pasa al campo siguiente; si
//     era el último, cierra el pedido en el MISMO turno.
//   - Cualquier otra cosa —vacío tras sanear, o pasado de largo— repregunta el
//     MISMO campo. No hay forma de saltárselo: `required` significa eso.
//
// wApp NO valida la semántica (REQ-25): no sabe si «12.345.678-5» es un RUT. Lo
// único que se aplica es el saneo de la puerta (SanitizeNote), que es de higiene
// —controles, invisibles, saltos de línea, largo— y no de significado.
func stepBuyerData(st cartState, in string, fields []store.BuyerField) (cartState, []string, []modules.Effect) {
	pending := pendingBuyerFields(fields, st.BuyerIdx)
	if len(pending) == 0 {
		// Nada que preguntar (checklist vacío o ya completo): se cierra, que es lo
		// que el cliente pidió al confirmar. Nunca se queda atrapado en este nivel.
		return closeCart(st)
	}
	field := pending[0]
	if in == codeVolver {
		st.Level = LevelSummary
		return st, []string{screenSummary(st.Lines, st.Note)}, nil
	}

	value, err := SanitizeNote(in)
	if err != nil {
		var tooLong NoteTooLongError
		if errors.As(err, &tooLong) {
			return st, []string{noteTooLongMsg(tooLong) + "\n\n" + screenBuyerData(field, st.BuyerIdx, len(fields))}, nil
		}
		return st, []string{screenBuyerData(field, st.BuyerIdx, len(fields))}, nil
	}
	if value == "" {
		// Vacío tras sanear en un campo obligatorio: se repregunta. Aquí NO vale la
		// equivalencia "vacío ≡ 0" de las indicaciones (REQ-33e): allí el dato es
		// opcional y aquí el dueño dijo que sin él no hay pedido.
		return st, []string{buyerRequiredMsg + "\n\n" + screenBuyerData(field, st.BuyerIdx, len(fields))}, nil
	}

	// El valor SALE del módulo aquí y no se guarda en ninguna parte del estado.
	eff := buyerDataEffect(field.Key, value)
	st.BuyerIdx++
	if remaining := pendingBuyerFields(fields, st.BuyerIdx); len(remaining) > 0 {
		return st, []string{buyerAck(field) + "\n" + screenBuyerData(remaining[0], st.BuyerIdx, len(fields))},
			[]modules.Effect{eff}
	}

	// Último campo: se cierra en el mismo turno. El ORDEN de los dos efectos es
	// parte del contrato y no una casualidad de escritura — ver closeCart.
	closed, outs, closeEffects := closeCart(st)
	return closed, outs, append([]modules.Effect{eff}, closeEffects...)
}

// closeCart lleva la sub-máquina al cierre confirmado. Se extrajo para que el
// resumen (confirmar sin checklist) y el checklist (confirmar tras el último campo)
// cierren por el MISMO camino: dos cierres con dos criterios acabarían divergiendo.
//
// ORDEN DE LOS EFECTOS EN EL CIERRE CON CHECKLIST. El efecto del comprador va
// SIEMPRE antes que cart_closed, y el runtime despacha los efectos en orden. El
// motivo es de escritura: el proyector localiza la solicitud por la que está
// ABIERTA para (tenant, contacto), y cart_closed es justo lo que la cierra. Al
// revés, el último dato del comprador no encontraría solicitud abierta donde
// colgarse y se perdería en silencio.
func closeCart(st cartState) (cartState, []string, []modules.Effect) {
	st.Level = LevelClosed
	return st, []string{screenClosed(total(st.Lines))}, []modules.Effect{closedEffect(st.Lines, st.Note)}
}

// buyerRequiredMsg es el aviso de campo obligatorio vacío. Dice qué hacer, no qué
// se hizo mal: el cliente no tiene por qué saber qué es "sanear".
const buyerRequiredMsg = "Necesitamos ese dato para completar tu pedido."

// screenBuyerData pide UN campo del checklist. Lleva el progreso ("1 de 2") porque
// es lo único que le dice al cliente cuántas preguntas le quedan antes de tener su
// pedido: sin él, un checklist de tres campos parece que no se acaba nunca.
//
// La pantalla declara para qué se pide el dato (ADR-0034 §Decisión 1: una UI que
// captura un campo dice su propósito) y que se guarda protegido — no es marketing:
// es la diferencia entre pedirle la dirección a alguien y que la escriba, o que la
// escriba en la ranura equivocada, que es la de indicaciones y va EN CLARO.
func screenBuyerData(f store.BuyerField, done, totalFields int) string {
	label := f.Label
	if label == "" {
		label = f.Key
	}
	return "📋 Falta un dato para completar tu pedido (" +
		strconv.Itoa(done+1) + " de " + strconv.Itoa(totalFields) + "):" +
		"\nEscribe tu " + label + "." +
		"\nSe guarda protegido y solo lo ve el negocio." +
		"\n" + codeVolver + ") ← Volver al resumen"
}

// buyerAck acusa el campo capturado SIN repetir lo que el cliente escribió. Es la
// diferencia con noteAck, que sí repite la indicación: allí el texto es dato de
// producción y verlo es la confirmación de que se entendió; aquí es dato personal y
// devolverlo por WhatsApp lo deja escrito una segunda vez, en un sitio que wApp no
// controla — el historial del teléfono del cliente.
func buyerAck(f store.BuyerField) string {
	label := f.Label
	if label == "" {
		label = f.Key
	}
	return "Anotado: " + label + " ✅"
}
