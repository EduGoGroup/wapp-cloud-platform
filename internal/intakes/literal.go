package intakes

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// literal.go — EL MATERIAL DE NIVEL 2 DE UNA REVISIÓN (Plan 044 · Ola 3 · T3.5).
//
// D-044.13 (doc 14 D-13, ADR-0034 §Decisión 2): dentro del payload de una revisión
// hay DOS clases de dato y se tratan distinto.
//
//	NIVEL 2 — se cifra: `source_text` (el texto que el cliente escribió) y las
//	          `evidence` de cada línea (las frases suyas que sostienen lo
//	          interpretado). Es texto libre y puede arrastrar identidad: «hola
//	          Herminia», «deposítamelo a la cuenta XYZ».
//
//	NIVEL 1 — se queda EN CLARO: todo lo demás. Skus, cantidades, rangos, precios,
//	          fechas, variantes, avisos y —esto es lo que más se malinterpreta— las
//	          PERSONALIZACIONES («sin sal», «sin cebolla»). Son dato de negocio
//	          cuantificable: «cuántos clientes piden sin sal» es una estadística que
//	          la dueña quiere, y cifrarla destruiría su valor sin proteger a nadie.
//
// Este fichero es el ÚNICO sitio del repo que sabe DÓNDE viven esos dos campos
// dentro del contrato §7.4. Partirlo entre el escritor y el lector habría creado dos
// listas de claves que divergen en cuanto alguien renombre un campo — y el modo de
// fallo de esa divergencia no es un error: es literal del cliente que se queda en
// claro sin que nadie se entere, que es exactamente el precedente MP-06
// (`vars.intent_params`) que D-044.13 nombra para no repetirlo.
//
// 🔴 LO QUE ESTE FICHERO NO DECIDE: si el sobre se cifra o no. Aquí solo se PARTE y
// se FUNDE el JSON. El cifrado lo hace el store con el FieldCipher (Planes 011/012),
// que es quien tiene la KEK, y la política de retención vive en retencion().

// TTLLiteralPorDefecto es el default de PLATAFORMA de
// `tenant_settings.intake_literal_ttl_seconds` (12 meses, D-044.13) y espeja el
// DEFAULT de la migración 0079. Vale para el tenant SIN fila en `tenant_settings`;
// un tenant CON fila manda siempre, incluido su 0 explícito, que aquí significa
// RETENCIÓN INDEFINIDA (sin poda) — igual que el 0 de `event_history_ttl_seconds`.
//
// 365 días y no «un año de calendario»: un TTL es una duración, no una fecha, y
// `time.Duration` no sabe de años bisiestos. La diferencia con un año real es de un
// día sobre 365 y no cambia ninguna decisión.
const TTLLiteralPorDefecto = 365 * 24 * time.Hour

// Las tres claves del contrato §7.4 que este fichero mueve. Están aquí y no
// dispersas en literales porque el test de simetría de `stages` las compara contra
// las etiquetas JSON REALES de PayloadRevision y Linea: si alguien renombra un campo
// del contrato y no toca esto, ese test se pone rojo. Sin él, el renombre dejaría el
// literal en claro en silencio.
const (
	ClavePayloadSourceText = "source_text"
	ClavePayloadLines      = "lines"
	ClaveLineaEvidence     = "evidence"
)

// LiteralRevision es el material de nivel 2 EXTRAÍDO de un payload: lo que va dentro
// del sobre cifrado y lo único que la poda destruye.
//
// `Evidence` va indexado por la POSICIÓN de la línea en `lines` (como texto, porque
// es una clave JSON), no por sku ni por label: una línea `unmatched` no tiene sku, y
// dos líneas pueden compartir label. La posición es lo único que identifica una
// línea dentro de su propia revisión, y el orden de `lines` es contrato (§7.5).
type LiteralRevision struct {
	// SourceText es el texto ORIGINAL del cliente, ya compuesto con sus
	// delimitadores (`### MENSAJES …###`), tal como lo interpretaron P2–P4.
	SourceText string `json:"source_text,omitempty"`
	// Evidence son las frases literales del cliente que sostienen cada línea,
	// indexadas por su posición en `lines`.
	Evidence map[string]string `json:"evidence,omitempty"`
}

// Vacio dice si no hay nada de nivel 2 que proteger. Un literal vacío NO se cifra:
// un sobre de una cadena vacía ocuparía sitio, entraría en la rotación de KEK y no
// protegería nada.
func (l LiteralRevision) Vacio() bool {
	return l.SourceText == "" && len(l.Evidence) == 0
}

// SobreLiteral son las TRES piezas del envelope, tal como salen de
// crypto.FieldCipher.Encrypt y tal como viven en las columnas `literal_enc`,
// `literal_dek` y `literal_kek_id` de la migración 0079.
//
// Es el mismo trío que `intake.SourceText` (Ola 1, `intake_jobs`) y que el de
// `contacts`, `intake_buyer_data`, `tenant_integrations`, `fleet_sessions` y
// `tenant_llm`: seis sobres con la misma forma, porque es la forma que el
// FieldCipher devuelve.
type SobreLiteral struct {
	Enc   []byte
	DEK   []byte
	KEKID string
}

// Completo dice si el sobre está entero. Uno a medias NO se escribe: deja una fila
// INDESCIFRABLE y eso no se arregla leyendo, porque no hay copia de la DEK en
// ningún otro sitio (mismo criterio que intake.SourceText.Complete).
func (s SobreLiteral) Completo() bool {
	return len(s.Enc) > 0 && len(s.DEK) > 0 && s.KEKID != ""
}

// Vacio dice si no hay sobre. Las tres piezas en cero es el estado normal de la
// mayoría de las revisiones (las del carrito numérico no tienen literal) y también
// el estado de una revisión ya podada — a esas las distingue `PrunedAt`.
func (s SobreLiteral) Vacio() bool {
	return len(s.Enc) == 0 && len(s.DEK) == 0 && s.KEKID == ""
}

// PartirLiteral saca del payload lo que es de nivel 2 y devuelve el payload SIN ello
// más el literal extraído. Es lo que se llama ANTES de persistir.
//
// Lo que NO hace, y es la mitad del valor de esta función: no reinterpreta el resto.
// Trabaja sobre `json.RawMessage` a cada nivel —no sobre `map[string]any`—, así que
// los números del payload (precios, cantidades) llegan a la BD con los MISMOS BYTES
// con los que llegaron aquí. Un round-trip por `any` los pasaría por `float64` y
// convertiría `2500` en `2500` casi siempre… y en `2.5e+07` o en un entero grande
// mordido de vez en cuando, sin error y sin forma de notarlo hasta que un total no
// cuadra.
//
// Un payload que no sea un objeto JSON (o que no traiga ninguna de las dos claves)
// vuelve TAL CUAL con un literal vacío, sin error: el payload de una revisión `cart`
// es exactamente ese caso y es el más frecuente de la tabla.
func PartirLiteral(payload json.RawMessage) (limpio json.RawMessage, lit LiteralRevision, err error) {
	raiz, ok := comoObjeto(payload)
	if !ok {
		return payload, LiteralRevision{}, nil
	}

	tocado := false

	if crudo, hay := raiz[ClavePayloadSourceText]; hay {
		var texto string
		if uerr := json.Unmarshal(crudo, &texto); uerr != nil {
			// La clave existe pero no es una cadena: el payload no es del contrato
			// §7.4. Se falla en vez de dejarlo pasar — dejarlo pasar significaría
			// persistir en claro algo que se llama `source_text`.
			return nil, LiteralRevision{}, fmt.Errorf("intakes: %s del payload no es una cadena: %w", ClavePayloadSourceText, uerr)
		}
		delete(raiz, ClavePayloadSourceText)
		tocado = true
		lit.SourceText = texto
	}

	lineas, hayLineas := comoLista(raiz[ClavePayloadLines])
	for i, cruda := range lineas {
		linea, esObjeto := comoObjeto(cruda)
		if !esObjeto {
			continue
		}
		crudo, hay := linea[ClaveLineaEvidence]
		if !hay {
			continue
		}
		var frase string
		if uerr := json.Unmarshal(crudo, &frase); uerr != nil {
			return nil, LiteralRevision{}, fmt.Errorf("intakes: %s de la línea %d no es una cadena: %w", ClaveLineaEvidence, i, uerr)
		}
		delete(linea, ClaveLineaEvidence)
		nueva, merr := json.Marshal(linea)
		if merr != nil {
			return nil, LiteralRevision{}, fmt.Errorf("intakes: reserializar la línea %d sin %s: %w", i, ClaveLineaEvidence, merr)
		}
		lineas[i] = nueva
		tocado = true
		if frase == "" {
			// La clave estaba pero venía vacía: se quita del payload igual (para que
			// el lector no vea un campo que el escritor considera ausente) y NO se
			// mete en el sobre, que no tiene nada que guardar.
			continue
		}
		if lit.Evidence == nil {
			lit.Evidence = make(map[string]string, len(lineas))
		}
		lit.Evidence[strconv.Itoa(i)] = frase
	}

	if !tocado {
		return payload, LiteralRevision{}, nil
	}

	if hayLineas {
		relista, merr := json.Marshal(lineas)
		if merr != nil {
			return nil, LiteralRevision{}, fmt.Errorf("intakes: reserializar %s: %w", ClavePayloadLines, merr)
		}
		raiz[ClavePayloadLines] = relista
	}

	limpio, err = json.Marshal(raiz)
	if err != nil {
		return nil, LiteralRevision{}, fmt.Errorf("intakes: reserializar el payload sin el literal: %w", err)
	}
	return limpio, lit, nil
}

// FundirLiteral es el INVERSO exacto de PartirLiteral: devuelve el literal a su
// sitio dentro del payload. Es lo que se llama al LEER, para que quien consuma la
// revisión vea el contrato §7.4 entero y no tenga que saber que el texto viajó
// aparte.
//
// Un literal vacío devuelve el payload tal cual: es el caso de una revisión sin
// texto y el de una revisión PODADA, y las dos tienen que devolver la interpretación
// completa y nada más.
func FundirLiteral(payload json.RawMessage, lit LiteralRevision) (json.RawMessage, error) {
	if lit.Vacio() {
		return payload, nil
	}
	raiz, ok := comoObjeto(payload)
	if !ok {
		// No hay dónde devolverlo. Se falla en vez de tirar el texto en silencio:
		// perder el literal de una revisión es perder la única defensa del dueño
		// contra una clasificación mala (ADR-0034 §Decisión 2).
		return nil, fmt.Errorf("intakes: el payload de la revisión no es un objeto JSON: no hay dónde devolver el literal")
	}

	if lit.SourceText != "" {
		crudo, err := json.Marshal(lit.SourceText)
		if err != nil {
			return nil, fmt.Errorf("intakes: serializar %s: %w", ClavePayloadSourceText, err)
		}
		raiz[ClavePayloadSourceText] = crudo
	}

	if len(lit.Evidence) > 0 {
		lineas, _ := comoLista(raiz[ClavePayloadLines])
		for pos, frase := range lit.Evidence {
			i, cerr := strconv.Atoi(pos)
			if cerr != nil || i < 0 || i >= len(lineas) {
				// La revisión cambió de forma bajo el sobre (o el sobre es de otra).
				// Se falla: devolver la evidencia a la línea EQUIVOCADA sería peor
				// que no devolverla — el dueño leería como prueba de una línea una
				// frase que sostiene otra.
				return nil, fmt.Errorf("intakes: la evidencia %q no corresponde a ninguna de las %d líneas de la revisión", pos, len(lineas))
			}
			linea, esObjeto := comoObjeto(lineas[i])
			if !esObjeto {
				return nil, fmt.Errorf("intakes: la línea %d de la revisión no es un objeto JSON", i)
			}
			crudo, merr := json.Marshal(frase)
			if merr != nil {
				return nil, fmt.Errorf("intakes: serializar la %s de la línea %d: %w", ClaveLineaEvidence, i, merr)
			}
			linea[ClaveLineaEvidence] = crudo
			nueva, merr := json.Marshal(linea)
			if merr != nil {
				return nil, fmt.Errorf("intakes: reserializar la línea %d con su %s: %w", i, ClaveLineaEvidence, merr)
			}
			lineas[i] = nueva
		}
		relista, merr := json.Marshal(lineas)
		if merr != nil {
			return nil, fmt.Errorf("intakes: reserializar %s con las evidencias: %w", ClavePayloadLines, merr)
		}
		raiz[ClavePayloadLines] = relista
	}

	out, err := json.Marshal(raiz)
	if err != nil {
		return nil, fmt.Errorf("intakes: reserializar el payload con el literal: %w", err)
	}
	return out, nil
}

// comoObjeto decodifica un JSON a objeto SIN interpretar sus valores (quedan en
// json.RawMessage). Devuelve false —y no error— cuando no es un objeto: los
// llamantes de aquí tratan «no es de esta forma» como un caso normal, no como un
// fallo.
func comoObjeto(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

// comoLista es comoObjeto para arrays. El bool distingue «no hay lista» de «hay una
// lista vacía», que es la diferencia entre no tocar la clave y reescribirla con [].
func comoLista(raw json.RawMessage) ([]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var lista []json.RawMessage
	if err := json.Unmarshal(raw, &lista); err != nil || lista == nil {
		return nil, false
	}
	return lista, true
}

// LiteralVencido dice si un literal escrito en `createdAt` ya pasó su plazo de
// retención, medido con `ahora`.
//
// 🔴 LOS DOS INSTANTES TIENEN QUE VENIR DEL MISMO RELOJ, y por eso esta función NO
// llama a time.Now(): quien la use decide de dónde salen. En el store Postgres los
// dos salen de Postgres (la edad se calcula en SQL, `now() - created_at`), porque
// `created_at` lo pone la BD y compararlo contra el reloj de Go es comparar dos
// relojes — un incidente con ficha propia en esta casa. En el MemoryStore los dos
// salen del reloj inyectado, que en los tests es falso a propósito.
//
// ttl == 0 significa RETENCIÓN INDEFINIDA (sin poda), no «vencido siempre»: es el
// mismo 0 de `event_history_ttl_seconds` y la lectura contraria destruiría el
// literal de todo tenant que dejase la clave a cero.
func LiteralVencido(edad, ttl time.Duration) bool {
	if ttl <= 0 {
		return false
	}
	return edad >= ttl
}
