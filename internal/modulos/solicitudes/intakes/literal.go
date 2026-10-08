// Porta internal/intakes/literal.go @ 64c181a

package intakes

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// literal.go — EL MATERIAL DE NIVEL 2 DE UNA REVISIÓN (Plan 044 · Ola 3 · T3.5).
//
// D-044.13 (doc 14 D-13, ADR-0034 §Decisión 2; regla R-10): dentro del payload de
// una revisión hay DOS clases de dato y se tratan distinto.
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
// que es quien tiene la KEK, y la política de retención vive en el store. Este
// fichero es PURO: sin BD, sin reloj y sin cifra.

// DefaultLiteralTTL es el default de PLATAFORMA de
// `tenant_settings.intake_literal_ttl_seconds` (12 meses, D-044.13) y espeja el
// DEFAULT de la migración 0079: 365 días exactos (8760 h). Vale para el tenant SIN
// fila en `tenant_settings`; un tenant CON fila manda siempre, incluido su 0
// explícito, que aquí significa RETENCIÓN INDEFINIDA (sin poda) — igual que el 0 de
// `event_history_ttl_seconds`.
//
// 365 días y no «un año de calendario»: un TTL es una duración, no una fecha, y
// `time.Duration` no sabe de años bisiestos. La diferencia con un año real es de un
// día sobre 365 y no cambia ninguna decisión.
//
// Era `TTLLiteralPorDefecto` en el paquete viejo.
const DefaultLiteralTTL = 365 * 24 * time.Hour

// Las tres claves del contrato §7.4 que este fichero mueve: "source_text" y "lines"
// en la raíz del payload, y "evidence" en cada línea. Están aquí y no dispersas en
// literales porque el test de simetría de `stages` las compara contra las etiquetas
// JSON REALES de PayloadRevision y Linea: si alguien renombra un campo del contrato
// y no toca esto, ese test se pone rojo. Sin él, el renombre dejaría el literal en
// claro en silencio.
//
// Eran `ClavePayloadSourceText`, `ClavePayloadLines` y `ClaveLineaEvidence` en el
// paquete viejo; sus valores no cambian.
const (
	PayloadKeySourceText = "source_text"
	PayloadKeyLines      = "lines"
	LineKeyEvidence      = "evidence"
)

// LiteralRevision es el material de nivel 2 EXTRAÍDO de un payload: lo que va dentro
// del sobre cifrado y lo único que la poda destruye.
//
// `Evidence` va indexado por la POSICIÓN de la línea en `lines` (como texto decimal
// —"0", "1", "10"—, porque es una clave JSON), no por sku ni por label: una línea
// `unmatched` no tiene sku, y dos líneas pueden compartir label. La posición es lo
// único que identifica una línea dentro de su propia revisión, y el orden de `lines`
// es contrato (§7.5).
//
// Sus etiquetas JSON son la forma del TEXTO CLARO del sobre y no cambian:
// `{"source_text":"…","evidence":{"0":"…"}}`, las dos con omitempty (un literal
// vacío serializa `{}`).
type LiteralRevision struct {
	// SourceText es el texto ORIGINAL del cliente, ya compuesto con sus
	// delimitadores (`### MENSAJES …###`), tal como lo interpretaron P2–P4.
	SourceText string `json:"source_text,omitempty"`
	// Evidence son las frases literales del cliente que sostienen cada línea,
	// indexadas por su posición en `lines`.
	Evidence map[string]string `json:"evidence,omitempty"`
}

// Empty dice si no hay nada de nivel 2 que proteger: SourceText vacío Y ninguna
// entrada en Evidence (un mapa nil y uno vacío cuentan igual). Una entrada cuyo
// valor es la cadena vacía SÍ cuenta como contenido, y un SourceText de solo
// espacios también: aquí no se recorta nada.
//
// Un literal vacío NO se cifra: un sobre de una cadena vacía ocuparía sitio,
// entraría en la rotación de KEK y no protegería nada.
//
// Era `Vacio` en el paquete viejo.
func (l LiteralRevision) Empty() bool {
	return l.SourceText == "" && len(l.Evidence) == 0
}

// LiteralEnvelope son las TRES piezas del envelope, tal como salen de
// crypto.FieldCipher.Encrypt y tal como viven en las columnas `literal_enc`,
// `literal_dek` y `literal_kek_id` de la migración 0079.
//
// Es el mismo trío que `intake.SourceText` (Ola 1, `intake_jobs`) y que el de
// `contacts`, `intake_buyer_data`, `tenant_integrations`, `fleet_sessions` y
// `tenant_llm`: seis sobres con la misma forma, porque es la forma que el
// FieldCipher devuelve.
//
// ⚠️ Homónimo (T-1): este `DEK` es la llave de datos del sobre de un dato de
// NEGOCIO, envuelta por la KEK que custodia esta pieza. No es la DEK del ADR-0007.
//
// Era `SobreLiteral` en el paquete viejo.
type LiteralEnvelope struct {
	Enc   []byte
	DEK   []byte
	KEKID string
}

// Complete dice si el sobre está entero: las TRES piezas no vacías. Uno a medias NO
// se escribe: deja una fila INDESCIFRABLE y eso no se arregla leyendo, porque no hay
// copia de la DEK en ningún otro sitio (mismo criterio que
// intake.SourceText.Complete).
//
// Era `Completo` en el paquete viejo.
func (s LiteralEnvelope) Complete() bool {
	return len(s.Enc) > 0 && len(s.DEK) > 0 && s.KEKID != ""
}

// Empty dice si no hay sobre: las TRES piezas vacías (un slice nil y uno de largo
// cero cuentan igual). Es el estado normal de la mayoría de las revisiones (las del
// carrito numérico no tienen literal) y también el estado de una revisión ya podada
// — a esas las distingue Revision.LiteralPrunedAt.
//
// Un sobre a medias no es ni Complete ni Empty.
//
// Era `Vacio` en el paquete viejo.
func (s LiteralEnvelope) Empty() bool {
	return len(s.Enc) == 0 && len(s.DEK) == 0 && s.KEKID == ""
}

// SplitLiteral saca del payload lo que es de nivel 2 y devuelve el payload SIN ello
// más el literal extraído. Es lo que se llama ANTES de persistir.
//
// QUÉ SACA, exactamente:
//
//   - la clave `source_text` de la RAÍZ (no la de un objeto anidado);
//   - la clave `evidence` de cada elemento de `lines` que sea un OBJETO (no la de un
//     objeto anidado dentro de la línea). Va a Evidence bajo la posición decimal de
//     la línea en `lines`, contando también los elementos que no son objetos.
//
// Las claves se comparan como las entrega el decodificador de JSON: exactas en
// mayúsculas (`Source_Text` y `EVIDENCE` NO son literal y se quedan en claro), con
// los escapes ya resueltos (`"source\u005ftext"` SÍ lo es) y, si vienen repetidas,
// gana la última.
//
// Una `evidence` que es la cadena vacía o `null`, y un `source_text` que es `""` o
// `null`, se QUITAN del payload (para que el lector no vea un campo que el escritor
// considera ausente) y NO entran en el literal, que no tiene nada que guardar. Una
// evidencia de solo espacios sí entra, tal cual.
//
// Lo que NO hace, y es la mitad del valor de esta función: no reinterpreta el resto.
// Trabaja sobre `json.RawMessage` a cada nivel —no sobre `map[string]any`—, así que
// los números del payload (precios, cantidades) llegan a la BD con los MISMOS BYTES
// con los que llegaron aquí (`9007199254740993`, `2500.00`, `1e2`, `-0`). Un
// round-trip por `any` los pasaría por `float64` y convertiría `2500` en `2500` casi
// siempre… y en `2.5e+07` o en un entero grande mordido de vez en cuando, sin error
// y sin forma de notarlo hasta que un total no cuadra.
//
// Cuando SÍ toca el payload, lo reserializa con encoding/json: las claves de la raíz
// y las de cada línea tocada salen en orden ALFABÉTICO, sin espacios, y con `<`,
// `>`, `&` y U+2028 escapados en los textos que se quedan. Las líneas que no tenían
// `evidence` conservan su orden de claves.
//
// Un payload que no sea un objeto JSON (vacío, nil, `null`, un array, un escalar o
// JSON roto), o que no traiga ninguna de las dos claves, vuelve TAL CUAL —los mismos
// bytes, espacios incluidos— con un literal vacío y sin error: el payload de una
// revisión `cart` es exactamente ese caso y es el más frecuente de la tabla. Un
// `lines` que no es un array se ignora y se deja como está.
//
// Errores (payload nil y literal vacío en los dos):
//
//   - `source_text` existe y no es una cadena ni null: "intakes: source_text del
//     payload no es una cadena: " envolviendo (%w) el error de encoding/json. Se
//     falla en vez de dejarlo pasar — dejarlo pasar significaría persistir en claro
//     algo que se llama `source_text`.
//   - la `evidence` de la línea i no es una cadena ni null: "intakes: evidence de la
//     línea <i> no es una cadena: " envolviendo (%w) el de encoding/json.
//
// Era `PartirLiteral` en el paquete viejo.
func SplitLiteral(payload json.RawMessage) (clean json.RawMessage, lit LiteralRevision, err error) {
	root, ok := asObject(payload)
	if !ok {
		return payload, LiteralRevision{}, nil
	}

	touched := false

	if raw, present := root[PayloadKeySourceText]; present {
		var text string
		if uerr := json.Unmarshal(raw, &text); uerr != nil {
			// La clave existe pero no es una cadena: el payload no es del contrato
			// §7.4. Se falla en vez de dejarlo pasar — dejarlo pasar significaría
			// persistir en claro algo que se llama `source_text`.
			return nil, LiteralRevision{}, fmt.Errorf("intakes: %s del payload no es una cadena: %w", PayloadKeySourceText, uerr)
		}
		delete(root, PayloadKeySourceText)
		touched = true
		lit.SourceText = text
	}

	lines, hasLines := asList(root[PayloadKeyLines])
	for i, rawLine := range lines {
		line, isObject := asObject(rawLine)
		if !isObject {
			continue
		}
		raw, present := line[LineKeyEvidence]
		if !present {
			continue
		}
		var phrase string
		if uerr := json.Unmarshal(raw, &phrase); uerr != nil {
			return nil, LiteralRevision{}, fmt.Errorf("intakes: %s de la línea %d no es una cadena: %w", LineKeyEvidence, i, uerr)
		}
		delete(line, LineKeyEvidence)
		rebuilt, merr := json.Marshal(line)
		if merr != nil {
			return nil, LiteralRevision{}, fmt.Errorf("intakes: reserializar la línea %d sin %s: %w", i, LineKeyEvidence, merr)
		}
		lines[i] = rebuilt
		touched = true
		if phrase == "" {
			// La clave estaba pero venía vacía: se quita del payload igual (para que
			// el lector no vea un campo que el escritor considera ausente) y NO se
			// mete en el sobre, que no tiene nada que guardar.
			continue
		}
		if lit.Evidence == nil {
			lit.Evidence = make(map[string]string, len(lines))
		}
		lit.Evidence[strconv.Itoa(i)] = phrase
	}

	if !touched {
		return payload, LiteralRevision{}, nil
	}

	if hasLines {
		relisted, merr := json.Marshal(lines)
		if merr != nil {
			return nil, LiteralRevision{}, fmt.Errorf("intakes: reserializar %s: %w", PayloadKeyLines, merr)
		}
		root[PayloadKeyLines] = relisted
	}

	clean, err = json.Marshal(root)
	if err != nil {
		return nil, LiteralRevision{}, fmt.Errorf("intakes: reserializar el payload sin el literal: %w", err)
	}
	return clean, lit, nil
}

// MergeLiteral es el INVERSO de SplitLiteral: devuelve el literal a su sitio dentro
// del payload. Es lo que se llama al LEER, para que quien consuma la revisión vea el
// contrato §7.4 entero y no tenga que saber que el texto viajó aparte. El resultado
// equivale al payload original como JSON, no byte a byte: sale con las claves en
// orden alfabético y sin espacios.
//
// Un literal vacío (LiteralRevision.Empty) devuelve el payload TAL CUAL, sea lo que
// sea —aunque no sea un objeto—: es el caso de una revisión sin texto y el de una
// revisión PODADA, y las dos tienen que devolver la interpretación completa y nada
// más.
//
// Con literal: SourceText no vacío se escribe en `source_text` de la raíz (pisando
// el que hubiera) y cada entrada de Evidence, en la `evidence` de la línea de esa
// posición (pisando la que hubiera; un valor vacío se escribe como `""`). Los
// números del payload conservan sus bytes.
//
// La posición se lee como entero decimal ASCII: "+0", "-0" y "01" se aceptan como
// 0, 0 y 1; un dígito no ASCII ("٠", "０"), un espacio (" 0"), un decimal ("0.0") o
// la cadena vacía NO son posiciones.
//
// Errores (payload nil en todos):
//
//   - el payload no es un objeto JSON: "intakes: el payload de la revisión no es un
//     objeto JSON: no hay dónde devolver el literal". Se falla en vez de tirar el
//     texto en silencio: perder el literal de una revisión es perder la única
//     defensa del dueño contra una clasificación mala (ADR-0034 §Decisión 2).
//   - una posición que no es un número, es negativa o es ≥ el número de líneas (un
//     `lines` ausente, null o que no es un array cuenta como 0 líneas): "intakes: la
//     evidencia \"<pos>\" no corresponde a ninguna de las <n> líneas de la
//     revisión", con la posición entre comillas (%q). La revisión cambió de forma
//     bajo el sobre (o el sobre es de otra): devolver la evidencia a la línea
//     EQUIVOCADA sería peor que no devolverla — el dueño leería como prueba de una
//     línea una frase que sostiene otra.
//   - la línea de esa posición no es un objeto: "intakes: la línea <i> de la
//     revisión no es un objeto JSON".
//
// Era `FundirLiteral` en el paquete viejo.
func MergeLiteral(payload json.RawMessage, lit LiteralRevision) (json.RawMessage, error) {
	if lit.Empty() {
		return payload, nil
	}
	root, ok := asObject(payload)
	if !ok {
		// No hay dónde devolverlo. Se falla en vez de tirar el texto en silencio:
		// perder el literal de una revisión es perder la única defensa del dueño
		// contra una clasificación mala (ADR-0034 §Decisión 2).
		return nil, fmt.Errorf("intakes: el payload de la revisión no es un objeto JSON: no hay dónde devolver el literal")
	}

	if lit.SourceText != "" {
		raw, err := json.Marshal(lit.SourceText)
		if err != nil {
			return nil, fmt.Errorf("intakes: serializar %s: %w", PayloadKeySourceText, err)
		}
		root[PayloadKeySourceText] = raw
	}

	if len(lit.Evidence) > 0 {
		lines, _ := asList(root[PayloadKeyLines])
		for pos, phrase := range lit.Evidence {
			i, cerr := strconv.Atoi(pos)
			if cerr != nil || i < 0 || i >= len(lines) {
				// La revisión cambió de forma bajo el sobre (o el sobre es de otra).
				// Se falla: devolver la evidencia a la línea EQUIVOCADA sería peor
				// que no devolverla — el dueño leería como prueba de una línea una
				// frase que sostiene otra.
				return nil, fmt.Errorf("intakes: la evidencia %q no corresponde a ninguna de las %d líneas de la revisión", pos, len(lines))
			}
			line, isObject := asObject(lines[i])
			if !isObject {
				return nil, fmt.Errorf("intakes: la línea %d de la revisión no es un objeto JSON", i)
			}
			raw, merr := json.Marshal(phrase)
			if merr != nil {
				return nil, fmt.Errorf("intakes: serializar la %s de la línea %d: %w", LineKeyEvidence, i, merr)
			}
			line[LineKeyEvidence] = raw
			rebuilt, merr := json.Marshal(line)
			if merr != nil {
				return nil, fmt.Errorf("intakes: reserializar la línea %d con su %s: %w", i, LineKeyEvidence, merr)
			}
			lines[i] = rebuilt
		}
		relisted, merr := json.Marshal(lines)
		if merr != nil {
			return nil, fmt.Errorf("intakes: reserializar %s con las evidencias: %w", PayloadKeyLines, merr)
		}
		root[PayloadKeyLines] = relisted
	}

	out, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("intakes: reserializar el payload con el literal: %w", err)
	}
	return out, nil
}

// asObject decodifica un JSON a objeto SIN interpretar sus valores (quedan en
// json.RawMessage). Devuelve false —y no error— cuando no es un objeto: los
// llamantes de aquí tratan «no es de esta forma» como un caso normal, no como un
// fallo. Era `comoObjeto` en el paquete viejo.
func asObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

// asList es asObject para arrays. El bool distingue «no hay lista» de «hay una
// lista vacía», que es la diferencia entre no tocar la clave y reescribirla con [].
// Era `comoLista` en el paquete viejo.
func asList(raw json.RawMessage) ([]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil || list == nil {
		return nil, false
	}
	return list, true
}

// LiteralExpired dice si un literal de edad `age` ya pasó su plazo de retención
// `ttl`: true cuando ttl > 0 y age >= ttl. Una edad EXACTAMENTE igual al TTL está
// vencida (>=, no >); un nanosegundo menos, no.
//
// 🔴 LOS DOS INSTANTES DE LOS QUE SALE LA EDAD TIENEN QUE VENIR DEL MISMO RELOJ, y
// por eso esta función NO llama a time.Now(): quien la use decide de dónde salen. En
// el store Postgres los dos salen de Postgres (la edad se calcula en SQL,
// `now() - created_at`), porque `created_at` lo pone la BD y compararlo contra el
// reloj de Go es comparar dos relojes — un incidente con ficha propia en esta casa.
// En el MemoryStore los dos salen del reloj inyectado, que en los tests es falso a
// propósito.
//
// ttl == 0 significa RETENCIÓN INDEFINIDA (sin poda), no «vencido siempre»: es el
// mismo 0 de `event_history_ttl_seconds` y la lectura contraria destruiría el
// literal de todo tenant que dejase la clave a cero, en la primera lectura y sin
// vuelta atrás. Un ttl negativo se trata igual que 0: nunca vence.
//
// Era `LiteralVencido` en el paquete viejo.
func LiteralExpired(age, ttl time.Duration) bool {
	if ttl <= 0 {
		return false
	}
	return age >= ttl
}
