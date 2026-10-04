// Nuevo (D-F3-3): no tiene fichero viejo. El comentario de paquete porta el de
// internal/ingest/dedupe.go @ 8896f13, y la firma de Deduper es la de runtime.IngestDeduper
// (internal/flujos/runtime/runtime.go:116 @ 8896f13).

// Package ingest deduplica los mensajes ENTRANTES (Edge→Cloud) ante la semántica
// at-least-once del OUTBOX DURABLE del Edge (Plan 028 · T6, ADR-0003).
//
// El outbox del Edge (Plan 027 Ola 3) reenvía los frames no confirmados tras una
// reconexión con los MISMOS bytes, de modo que la ingesta de la nube puede recibir
// el MISMO mensaje de WhatsApp dos veces (o intercalado con otros). La idempotencia
// previa vivía en flow_state.last_wa_message_id (runtime) pero es CONSECUTIVA: solo
// corta la RE-ENTREGA INMEDIATA. Este paquete añade una memoria PERSISTENTE e
// independiente del estado del flujo, cerrada por la clave (session_id,
// wa_message_id).
//
// REGLA DURA (INV-5, zero-knowledge): CERO PII. session_id y wa_message_id son
// metadatos OPACOS del transporte (whatsmeow/CloudLink), NUNCA el número/JID del
// contacto ni el contenido del mensaje. NUNCA la DEK/lease del Edge.
//
// El doble en memoria no vive aquí (D-F3-1): es ingesthelpertest.Memoria, junto a la
// suite ingesthelpertest.ContratoDeduper.
package ingest

import "context"

// Deduper es el dedupe de entrantes por la clave (session_id, wa_message_id). Lo
// implementan PostgresDeduper y, en los tests, ingesthelpertest.Memoria. El motor de
// flujos lo consume estructuralmente (su runtime declara una interfaz con esta misma
// firma): la firma de Seen no se cambia.
type Deduper interface {
	// Seen registra la clave de forma IDEMPOTENTE y dice si YA se había visto:
	//
	//   - false = primer avistamiento: el entrante es nuevo, procésalo;
	//   - true = duplicado: el outbox del Edge lo reenvió, ignóralo;
	//   - ante un error devuelve false junto al error.
	//
	// La clave son LOS DOS: el mismo wa_message_id en otra sesión es otro mensaje.
	//
	// ⚠️ El consumidor es fail-open A PROPÓSITO: si Seen falla, el runtime procesa el
	// entrante (prefiere un duplicado a perder un mensaje). Por eso el error viaja con
	// false, nunca con true. Es una decisión del consumidor, que aquí solo se dice.
	Seen(ctx context.Context, sessionID, waMessageID string) (bool, error)
}
