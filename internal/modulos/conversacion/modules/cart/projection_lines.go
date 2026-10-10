// Porta internal/flujos/modules/cart/projection.go @ 9d5a4b6 (trozo «líneas»:
// ensureOpenIntake, projectOpenLines y la lectura de la foto del payload; el viejo es
// un solo fichero y aquí nace partido, 05 E-13)

// projection_lines.go es la mitad del proyector que mantiene AL DÍA las líneas de una
// solicitud abierta (item_added y note_added): asegura la solicitud —por identidad de
// negocio primero y por evento después (D-044.46)— y reemplaza sus intake_items por la
// foto del carrito que trae el efecto. Aquí vive también la ÚNICA traducción del
// payload de líneas a filas, que el cierre (projection_close.go) reusa.
//
// No exporta nada: lo que promete se ve por Projector.Project (projection.go).

package cart

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// ensureOpenIntake garantiza UN contenido durable para el turno al primer
// item_added (design.md §3.4). Idempotente por identidad de negocio: si ya hay
// abierta NO crea otra, y la "toca" tal cual está.
//
// 🔄 Desde D-044.46 (T4.0) pregunta DOS veces antes de crear, y el orden importa:
// primero por identidad de negocio (tenant, contacto) y después POR EVENTO, sin
// filtro de estado. La segunda existe porque este proyector ya no es el único
// productor de filas en `intakes`: la etapa `draft` del pipeline del Plan 044
// escribe la suya sobre el mismo evento y en `pending_approval`, un estado que la
// primera pregunta no ve. Ver el bloque de la segunda pregunta, más abajo.
//
// Hasta T4.7 esto fijaba y REFRESCABA un vencimiento (expires_at = now +
// tenant_settings.order_ttl) en CADA item_added. D-041.16 lo derogó: ninguna
// solicitud vence por tiempo, así que la solicitud nace con expires_at ZERO (NULL
// en la tabla) y la fila que ya existía se reescribe con el valor que tuviera —las
// históricas conservan el suyo tal cual, que es justo lo que promete el COMMENT de
// la columna.
// La solicitud DECLARA A SU PADRE al nacer (D-043.21, T4.5.3): event_id =
// meta.EventID, el dato que este método ya tenía en la mano y descartaba. Tres
// reglas, en orden:
//
//   - Al CREAR, la fila nace con el event_id de la meta. Si la meta llega VACÍA
//     (efecto fuera de evento — no debería pasar en cart), NO se inventa nada: va
//     NULL, se loguea, y el CHECK de la 0054 hace su trabajo en la base.
//   - Al REUSAR una "open" con event_id NULL (legado pre-0054), se ESTAMPA: es la
//     única reparación legítima de un huérfano — el evento vivo del turno es, por
//     E-2 (uno vivo por tipo), el mismo del que esa solicitud colgaba.
//   - Al REUSAR una que YA declara OTRO evento, NO se pisa: se loguea y se sigue.
//     No debería pasar (un vivo por tipo + índice único parcial), así que si se ve
//     en un log es una carrera o un dato roto que hay que mirar, no maquillar. El
//     COALESCE del UpsertIntake garantiza además EN EL SQL que ninguna escritura
//     des-declara un padre.
func (p *Projector) ensureOpenIntake(ctx context.Context, meta modules.EffectMeta) (string, error) {
	existing, found, err := p.store.GetOpenIntake(ctx, meta.TenantID, meta.ContactID)
	if err != nil {
		return "", err
	}
	if found {
		switch {
		case existing.EventID == "" && meta.EventID != "":
			existing.EventID = meta.EventID
		case meta.EventID != "" && existing.EventID != meta.EventID:
			slog.Warn("cart: la solicitud abierta ya declara OTRO evento; no se pisa (D-043.21)",
				"intake_id", existing.ID, "event_id_solicitud", existing.EventID, "event_id_meta", meta.EventID)
		}
		return existing.ID, p.store.UpsertIntake(ctx, existing)
	}
	// SEGUNDA PREGUNTA, y la que cierra el hallazgo #24 por el lado del carrito
	// (D-044.46, T4.0). La de arriba resuelve por identidad de NEGOCIO y solo ve las
	// `open`; desde el Plan 044 la etapa `draft` del pipeline para contenido durable
	// sobre el MISMO evento y lo deja en `pending_approval`, que a la de arriba le
	// resulta invisible. Sin esta pregunta el carrito creaba una fila nueva con un id
	// sorteado y chocaba contra `intakes_event_id_uidx` — medido en UAT (00:11:08 del
	// 27-08): el pedido de ese turno se perdía.
	//
	// 🔴 SE REUSA LA FILA Y NO SE LA TOCA LA CABECERA. No hay UpsertIntake aquí a
	// propósito: la fila ya declara este evento (por eso la encontramos) y su `status`
	// es de quien lo puso — un borrador esperando al dueño no vuelve a `open` porque
	// el cliente siga agregando. Lo que sí sigue su curso son las LÍNEAS: el llamante
	// (projectOpenLines) escribe sobre esta solicitud la foto del carrito, que es
	// exactamente lo que pide D-044.46 («el carrito reusando la fila»).
	if meta.EventID != "" {
		foreign, hit, err := p.store.GetIntakeByEvent(ctx, meta.TenantID, meta.EventID)
		if err != nil {
			return "", err
		}
		if hit {
			slog.Info("cart: el evento YA tenía contenido durable; se reusa en vez de crear otro (D-044.46)",
				"tenant_id", meta.TenantID, "contact_id", meta.ContactID, "session_id", meta.SessionID,
				"event_id", meta.EventID, "intake_id", foreign.ID, "status", foreign.Status)
			return foreign.ID, nil
		}
	}
	if meta.EventID == "" {
		slog.Warn("cart: item_added sin evento en la meta; la solicitud nace sin padre y el CHECK de la 0054 decidirá",
			"tenant_id", meta.TenantID, "session_id", meta.SessionID)
	}
	intake := store.Intake{
		ID:        uuid.NewString(),
		TenantID:  meta.TenantID,
		ContactID: meta.ContactID,
		SessionID: meta.SessionID,
		Status:    intakeStatusOpen,
		EventID:   meta.EventID,
	}
	if err := p.store.UpsertIntake(ctx, intake); err != nil {
		if postgres.IsUniqueViolation(err) {
			// Defensa en profundidad del hallazgo #24 (Plan 043 · Ola 6): con el cierre
			// natural del carrito arreglado (cart.go, Step) este choque YA NO DEBERÍA
			// ocurrir —un evento cerrado no vuelve a reusarse—, pero un INSERT que
			// choca contra el único parcial `intakes_event_id_uidx` (migración 0054,
			// "un evento tiene A LO SUMO un contenido durable, para siempre") es
			// exactamente el síntoma medido del pedido que se pierde en silencio.
			//
			// ACTUALIZADO 2026-08-13 (Plan 054 · D-054.4 · T3): la parte de este
			// comentario que decía que el dispatcher es best-effort "y esto NO lo
			// cambia" YA NO ES CIERTA, y la "decisión abierta" del hallazgo #24 quedó
			// CERRADA. Como cart.ProducesDurableContent() == true, Runtime.dispatch
			// deja de tragarse el fallo: corta el turno con aviso explícito al cliente
			// en vez de despedirlo creyendo que compró. Este error concreto NO se
			// reintenta —y eso sigue siendo lo correcto—: un choque contra el único
			// parcial es SQLSTATE clase 23 (violación de integridad), que no cede
			// reintentando.
			//
			// El log específico se conserva: sin él el único rastro sería "sink de
			// efecto falló" sin tenant/contacto/evento, insuficiente para diagnosticar
			// CUÁL pedido se perdió.
			slog.Error("cart: el evento ya tenía un intake (intakes_event_id_uidx); el pedido de este turno NO se pudo guardar (hallazgo #24)",
				"tenant_id", meta.TenantID, "contact_id", meta.ContactID, "session_id", meta.SessionID,
				"event_id", meta.EventID, "intake_id_intentado", intake.ID, "error", err)
		}
		return "", err
	}
	return intake.ID, nil
}

// projectOpenLines asegura la solicitud "open" y deja sus intake_items IGUALES a la
// foto del carrito que trae el efecto (Plan 043 · Ola 3). Es lo que cierra el hueco
// que destapó REQ-26b: hasta ahora una solicitud "open" no tenía NINGUNA línea —se
// materializaban solo al cerrar—, de modo que rescatarla, o mostrarla en el CRM,
// prometía unas líneas que no existían.
//
// Por qué REEMPLAZAR y no ir añadiendo la línea que el efecto publica:
//
//   - IDEMPOTENCIA. El mismo efecto reentregado escribe el mismo conjunto, así que
//     no duplica ni pierde nada. Añadiendo no habría forma de conseguirlo: dos
//     item_added del MISMO artículo son dos líneas legítimas del pedido (se agrega
//     dos veces, con indicaciones distintas), así que no existe clave natural con la
//     que reconocer un reenvío.
//   - FIDELIDAD. El carrito cambia sus líneas por más sitios que el "agregar": la
//     indicación de una línea y el split de D-041.20 las reescriben. Con la foto, la
//     tabla dice lo que dice el carrito; acumulando, diría solo lo que se agregó.
//
// SIN foto en el payload no se toca ninguna línea, y eso NO es lo mismo que una foto
// vacía: es un flow_event HISTÓRICO reejecutado (los hay, y el proyector los soporta
// a propósito, ver EffectCartExpired). Un efecto viejo no sabe qué había en el
// carrito, así que borrarle las líneas al pedido sería inventarse que estaba vacío.
func (p *Projector) projectOpenLines(ctx context.Context, meta modules.EffectMeta, eff modules.Effect) error {
	intakeID, err := p.ensureOpenIntake(ctx, meta)
	if err != nil {
		return err
	}
	items, ok := cartLineSnapshot(eff.Payload)
	if !ok {
		return nil
	}
	return p.store.ReplaceIntakeItems(ctx, intakeID, items)
}

// cartItems extrae las líneas del payload de cart_closed a []store.IntakeItem. Tolera
// ambas formas de la lista: el camino en-proceso ([]map[string]any que construye el
// módulo) y el round-trip JSON ([]any de map[string]any). Ítems mal formados se
// omiten sin panica. El IntakeID lo fija store.CloseIntake.
func cartItems(payload map[string]any) []store.IntakeItem {
	var out []store.IntakeItem
	switch items := payload[snapshotKey].(type) {
	case []map[string]any:
		for _, m := range items {
			out = append(out, intakeItemFromMap(m))
		}
	case []any:
		for _, e := range items {
			if m, ok := e.(map[string]any); ok {
				out = append(out, intakeItemFromMap(m))
			}
		}
	}
	return out
}

// cartLineSnapshot devuelve las líneas de la foto del carrito y si el efecto TRAÍA
// foto. Los dos casos son distintos y confundirlos borra pedidos: una foto vacía
// dice «el carrito no tiene líneas», y la AUSENCIA de foto dice «este efecto no sabe
// qué líneas hay» —un flow_event escrito antes de que la foto existiera, reejecutado
// hoy—. Por eso la presencia se mira sobre la CLAVE y no sobre el resultado de
// cartItems, que devuelve nil para las dos.
func cartLineSnapshot(payload map[string]any) ([]store.IntakeItem, bool) {
	if _, ok := payload[snapshotKey]; !ok {
		return nil, false
	}
	return cartItems(payload), true
}

// intakeItemFromMap traduce UNA línea del payload del cierre a la fila que se
// persiste. `customization` (D-041.17) se lee como cualquier otra clave y su
// AUSENCIA da la cadena vacía: un blob de carrito escrito antes de que el campo
// existiera —o por un productor que no lo escribe— cierra exactamente igual, sin
// rama especial ni versión de payload.
func intakeItemFromMap(m map[string]any) store.IntakeItem {
	return store.IntakeItem{
		SKU:           modules.AsString(m["sku"]),
		Label:         modules.AsString(m["label"]),
		Customization: modules.AsString(m["customization"]),
		Qty:           modules.AsInt(m["qty"]),
		UnitPrice:     modules.AsFloat(m["unit_price"]),
	}
}
