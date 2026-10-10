// Porta internal/flujos/modules/cart/buyer.go @ 9d5a4b6

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
// sobrevive en el estado es `buyer_idx`, un contador (state.go).
//
// El módulo sigue PURO: aquí no hay I/O. El checklist llega sembrado en Vars por la
// ResumePolicy (igual que el page_size del tenant) y el cifrado y la escritura los
// hace el Projector, que es el adaptador impuro.
//
// ════════════════════════════════════════════════════════════════════════════
// LO QUE PROMETE EL PASO, VISTO POR Module.Step
// ════════════════════════════════════════════════════════════════════════════
//
// El checklist se intercala en UN sitio: entre el «1) Confirmar» del resumen y el
// pedido cerrado (nivel LevelBuyerData). Con la lista vacía —el default de la
// columna y el caso de todos los tenants de hoy— el 1 del resumen cierra el pedido
// con la misma pantalla y el mismo efecto de siempre (INV-15).
//
// Se preguntan SOLO los campos `required` con clave no vacía, en el orden
// configurado; quien pregunta y quien cuenta ven la misma lista, o el contador
// señalaría a otro campo. Cada mensaje del cliente en ese nivel tiene tres salidas:
//
//   - `0` vuelve al resumen SIN perder lo ya capturado: el contador se conserva (lo
//     que el cliente ya escribió ya está guardado y cifrado, y volvérselo a
//     preguntar sería pedirle dos veces su RUT). Confirmar otra vez RETOMA donde
//     quedó.
//   - Un valor válido sale en SU efecto buyer_data_captured {key, value} y pasa al
//     campo siguiente con el acuse «Anotado: <etiqueta> ✅» —que NO repite lo que el
//     cliente escribió: devolverlo por WhatsApp lo dejaría escrito una segunda vez,
//     en el historial de un teléfono que wApp no controla—. Si era el último, cierra
//     el pedido EN EL MISMO turno, y el efecto del comprador va SIEMPRE antes que
//     cart_closed: el proyector localiza la solicitud por la que está ABIERTA, y
//     cart_closed es justo lo que la cierra.
//   - Cualquier otra cosa repregunta el MISMO campo: vacío tras sanear, con el aviso
//     «Necesitamos ese dato para completar tu pedido.» (aquí NO vale la equivalencia
//     "vacío ≡ 0" de las indicaciones: `required` significa eso); pasado de largo,
//     con el aviso de largo y sin truncar.
//
// wApp NO valida la semántica (REQ-25): no sabe si «12.345.678-5» es un RUT. Lo
// único que se aplica es el saneo de la puerta (intakes.SanitizeNote), que es de
// higiene —controles, invisibles, saltos de línea, largo— y no de significado.
//
// La pantalla de cada campo dice el progreso y el propósito (ADR-0034 §Decisión 1):
//
//	📋 Falta un dato para completar tu pedido (1 de 2):
//	Escribe tu <etiqueta>.
//	Se guarda protegido y solo lo ve el negocio.
//	0) ← Volver al resumen
//
// con la clave del campo en el sitio de la etiqueta si la etiqueta viene vacía.
//
// Si el dueño recorta su checklist a media conversación y el contador queda por
// encima de la lista, no queda nada que preguntar: lo que no se puede preguntar no
// puede bloquear un cierre, y el cliente nunca se queda atrapado en este nivel.

package cart

// VarBuyerFields es la clave de Conversation.Vars bajo la que el RUNTIME siembra el
// checklist del comprador del tenant (tenant_settings.buyer_fields), igual que
// siembra el catálogo y el page_size. Ausente, nil, vacía o ilegible ⇒ no se
// pregunta nada.
//
// Step la lee tolerando el round-trip JSONB: lo que se sembró como
// []store.BuyerField vuelve, tras guardarse el estado, como []any de
// map[string]any con las claves `key`, `label` y `required`.
//
// Lo que viaja por aquí es CONFIGURACIÓN (claves y etiquetas), nunca respuestas:
// esta clave sí acaba en el JSONB del estado, y ahí no puede haber PII.
const VarBuyerFields = "cart_buyer_fields"
