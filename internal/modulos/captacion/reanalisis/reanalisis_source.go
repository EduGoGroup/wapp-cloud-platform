// Porta internal/reanalisis/reanalisis.go @ 56097aa (E-13: el material,
// `reanalisis.go:657-772` del viejo).

package reanalisis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
)

// reanalisis_source.go — EL MATERIAL de Reanalyze: si hay algo que analizar (escalón
// 9) y la escritura del texto pegado (primer paso del 10). Sin exportados: su contrato
// es el de Reanalyze.

// sourceOfMaterial (antes `origenDelMaterial`) decide `analysis.source` Y, de paso, si
// hay algo que analizar. Son la MISMA pregunta contestada una vez: «¿qué material
// hay?». Partirla en dos funciones dejaría dos criterios sobre lo mismo, y el día que
// divergieran el endpoint aceptaría una petición que después no tiene qué componer.
//
// Devuelve SourceUnavailableError con su razón cuando no hay material por ningún lado.
func (s *Service) sourceOfMaterial(ctx context.Context, eventID, pasted string) (string, error) {
	// El MISMO límite que el compositor, y por eso entra por el constructor: la
	// pregunta «¿hay material?» tiene que mirar exactamente las entradas que se van a
	// componer.
	entries, err := s.thread.ListThread(ctx, eventID, s.threadLimit)
	if err != nil {
		return "", fmt.Errorf("reanalisis: leer el hilo del evento %s: %w", eventID, err)
	}
	messages, withText := countMessages(entries)

	switch {
	case withText > 0 && pasted != "":
		return stages.SourceBoth, nil
	case withText > 0:
		return stages.SourceEventThread, nil
	case pasted != "":
		// Sin hilo pero CON transcripción: hay material, y es solo el del dueño. Es
		// el único camino por el que `pasted_text` se escribe, y por eso no es un
		// valor decorativo del contrato §7.4.
		return stages.SourcePastedText, nil
	case messages > 0:
		// Quedan filas `message` y ninguna conserva cuerpo ⇒ el literal EXISTIÓ y se
		// vació. Ver ReasonPurged para por qué esto no puede ocurrir todavía en campo.
		return "", SourceUnavailableError{Reason: ReasonPurged}
	default:
		return "", SourceUnavailableError{Reason: ReasonNeverStored}
	}
}

// countMessages (antes `contarMensajes`) reparte el hilo en los dos números que
// deciden la razón del 422. Es PURA para poder afirmar la frontera entre `purged` y
// `never_stored` sin base de datos.
//
// 🔴 SOLO CUENTA `entry_kind='message'`. Los `summary` y los `message_out_of_turn`
// son CONTEXTO (REQ-10b, D-044.24) y un `source_text` hecho solo de contexto es
// exactamente el accidente que D-044.24 describe: productos que listamos NOSOTROS y
// ninguna frase del cliente que los contradiga.
//
// `withText` cuenta las que conservan cuerpo. Una fila `message` con `Text` vacío es
// una fila cuyo `body_enc` está a NULL: eso es lo que dejaría la poda del hilo el día
// que exista, y lo que separa `purged` de `never_stored`.
func countMessages(entries []events.ThreadEntry) (messages, withText int) {
	for _, e := range entries {
		if e.Kind != events.KindMessage {
			continue
		}
		messages++
		if e.Text != "" {
			withText++
		}
	}
	return messages, withText
}

// persistPasted (antes `persistirPegado`) guarda la transcripción del dueño como UNA
// fila más del hilo, salvo que ya esté (D-044.17, cierre de MD-044.2).
//
// # EL DEDUPE SE HACE EN MEMORIA, Y NO ES UN ATAJO
//
// El criterio pide deduplicar por `(event_id, origin, hash del texto saneado)`, y ese
// hash NO CABE EN NINGUNA COLUMNA: el CHECK `conversation_event_messages_grade_chk`
// obliga a `payload IS NULL` en toda fila `message`, así que la única sede sería una
// columna nueva. Se descartó por una razón más dura que el coste de migrar: el cuerpo
// va cifrado con DEK fresca y nonce por fila, de modo que dos filas con el MISMO texto
// tienen `body_enc` distintos y no hay forma de compararlas en SQL. Así que se leen
// las filas `owner_pasted` de ese evento —que son unidades, no cientos—, se descifran
// y se compara el hash en memoria.
//
// 🔴 EL HASH ES DEL TEXTO SANEADO EN LOS DOS LADOS. Lo que se guardó ya pasó por el
// saneo, y lo entrante también (ver `sanitize`), así que dos pegadas que solo difieran
// en espacios repetidos o en un salto de línea SON la misma y no duplican.
func (s *Service) persistPasted(ctx context.Context, eventID, pasted string) error {
	previous, err := s.thread.ListPastedByOwner(ctx, eventID)
	if err != nil {
		return fmt.Errorf("reanalisis: leer las transcripciones ya pegadas del evento %s: %w", eventID, err)
	}
	incoming := fingerprint(pasted)
	for _, p := range previous {
		if fingerprint(p) == incoming {
			// Ya está. NO es un error y NO cambia el desenlace de la petición: el
			// re-análisis sigue adelante y volverá a leer esa misma fila como parte del
			// origen, que es literalmente el criterio («el segundo re-análisis, sin
			// `text`, la vuelve a leer»).
			s.log.Debug("reanalisis: la transcripción pegada ya estaba en el hilo; no se duplica",
				"event_id", eventID, "runas", len([]rune(pasted)))
			return nil
		}
	}
	seq, err := s.thread.AppendPastedMessage(ctx, eventID, pasted)
	if err != nil {
		return fmt.Errorf("reanalisis: guardar la transcripción pegada en el hilo del evento %s: %w", eventID, err)
	}
	s.log.Debug("reanalisis: transcripción del dueño añadida al hilo",
		"event_id", eventID, "seq", seq, "runas", len([]rune(pasted)))
	return nil
}

// fingerprint (antes `huella`) es el hash del texto saneado con el que se deduplica.
// SHA-256 en hex.
//
// No se compara el texto tal cual —que con 280 runas sería igual de barato— por dos
// razones: el criterio de T4.6 pide el hash con esas palabras, y una huella no se
// puede leer por accidente en un log si algún día alguien la registra, mientras que
// el texto sí es contenido del cliente (ADR-0034).
func fingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
