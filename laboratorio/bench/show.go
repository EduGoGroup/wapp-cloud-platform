//go:build laboratorio

package bench

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

// Turn manda un entrante y narra el turno entero: qué entra, qué contesta el Sender doble y qué
// avisos dejó el runtime en el log. Devuelve las respuestas.
func (b *Bench) Turn(n *common.Narrator, phone, waID, text string) []string {
	n.Show("entra ("+waID+")", quote(text))
	replies, err := b.SayFrom(phone, waID, text)
	if err != nil {
		n.Fail(err, "HandleIncoming devolvió error")
	}
	if len(replies) == 0 {
		n.Note("El Sender doble NO recibió ningún texto: el Edge no habría mandado nada.")
	}
	for i, r := range replies {
		n.Show("sale "+strconv.Itoa(i+1), r)
	}
	for _, line := range b.Log.Take(false) {
		n.Note("log del runtime → %s", line)
	}
	return replies
}

// ShowState narra el estado guardado de la conversación de un teléfono (flow_state).
func (b *Bench) ShowState(n *common.Narrator, phone string) (model.Conversation, bool) {
	st, ok, err := b.State(phone)
	if err != nil {
		n.Fail(err, "leer flow_state")
		return st, false
	}
	if !ok {
		n.Note("flow_state: SIN fila para este contacto (no hay conversación viva).")
		return st, false
	}
	if st.FlowID == "" {
		n.Note("flow_state: fila SIN flujo: guarda lo que el despachador ofreció, a la espera de que el cliente elija.")
		n.Show("   vars", varsJSON(st.Vars))
		return st, true
	}
	node := st.CurrentNode
	if node == model.NodeTerminal {
		node += " (el centinela de fin de flujo)"
	}
	event := "sin evento (limbo)"
	switch {
	case st.EventID != "" && st.EventID == st.OwnerEventID:
		event = "con evento " + short(st.EventID) + " (activo y dueño)"
	case st.EventID != "" || st.OwnerEventID != "":
		event = "punteros distintos: activo " + short(st.EventID) + ", dueño " + short(st.OwnerEventID)
	}
	n.Note("flow_state: flujo %s v%d · nodo %s · %s · último wa_message_id %q",
		st.FlowID, st.FlowVersion, node, event, st.LastWaMessageID)
	if len(st.Vars) > 0 {
		n.Show("   vars", varsJSON(st.Vars))
	}
	return st, true
}

// quote pone comillas a un texto para que se vea el espacio de los extremos.
func quote(s string) string { return "«" + s + "»" }

// short recorta un UUID a sus ocho primeros caracteres, para leer.
func short(id string) string {
	if len(id) > 8 {
		return id[:8] + "…"
	}
	return id
}

// varsJSON serializa las vars con las claves en orden.
func varsJSON(v map[string]any) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make([]string, 0, len(keys))
	for _, k := range keys {
		if k == "cart_catalog" {
			// El carrito CONGELA el catálogo en las vars al empezar: se resume para leer.
			ordered = append(ordered, `"cart_catalog":"(el catálogo entero, congelado al empezar el carrito)"`)
			continue
		}
		raw, err := json.Marshal(v[k])
		if err != nil {
			raw = []byte(`"?"`)
		}
		ordered = append(ordered, `"`+k+`":`+string(raw))
	}
	return "{" + strings.Join(ordered, ",") + "}"
}
