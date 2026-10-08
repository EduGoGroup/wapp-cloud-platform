// Porta internal/intakes/quotetext/quotetext.go @ 36d5a04

package quotetext

// quotetext_fewshot.go — parte de quotetext.go (E-13): EL FEW-SHOT de D-044.11. Las
// dos cotas, la ref de la semilla, cómo se arma la lista de ejemplos y cómo se lee el
// blob de `tenant_content`.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// ════════════════════════════════════════════════════════════════════════════
// 🔴 LAS DOS COTAS DEL FEW-SHOT — DECISIÓN DE T5.1, PORQUE EL PLAN NO DECLARA NINGUNA
// ════════════════════════════════════════════════════════════════════════════
//
// QUE QUEDE ESCRITO: `llm.GenerateQuoteTextInput.Examples` NO TIENE TOPE DE TAMAÑO EN
// NINGUNA PARTE. Ni en el puerto («Puede venir vacío» es todo lo que su contrato dice),
// ni en el design, ni en `tasks.md`, ni en `requirements.md` — la palabra `Examples` no
// aparece en ninguno de los tres. Y el material del few-shot no está acotado por ningún
// otro sitio tampoco:
//
//   - la SEMILLA sí hereda una cota, la de `tenant_content` (1 MiB por blob,
//     design.md §2.2), que es TRES ÓRDENES DE MAGNITUD más de lo que cabe en un prompt;
//   - el HISTORIAL no hereda ninguna: `intake_revisions.rendered_text` es un `TEXT`
//     pelado (migración 0045) y `POST /api/v1/intakes/{id}/approve` —a diferencia de
//     `reanalyze`, del callback del CRM y de la importación de catálogo— decodifica su
//     cuerpo SIN `http.MaxBytesReader`. O sea que el texto que alimenta el few-shot no
//     tiene tope ni en la entrada HTTP, ni en la columna, ni en el prompt.
//
// POR QUÉ ESO IMPORTA AQUÍ Y NO ES UNA PRECAUCIÓN ABSTRACTA. P5 sale por la vía local:
// viaja al Edge por CloudLink y se ejecuta contra el Ollama del cliente, en CPU. Dos
// números medidos de este mismo plan lo enmarcan:
//
//   - el worker le da a CADA llamada `pipeline.PlazoPorLlamadaSuelo` = 48 s, de los que
//     el Edge se queda 41 s (MargenVeredicto = 7 s);
//   - el PREFILL EN FRÍO son ~50 s medidos en UAT (config.go, bootstrap.go). Es decir:
//     **más que el plazo entero**. Un prompt cuyo prefijo no esté caliente no cabe.
//
// Y el bloque de ejemplos ES PREFIJO: `BuildGenerateQuoteTextPromptCon` lo compone
// ANTES del borrador (instrucción + reglas + esquema + EJEMPLOS + borrador), y ese
// orden es contrato del ADR-0046 precisamente para que se cachee. Así que el tamaño de
// este bloque es exactamente lo que se paga en cada prefill frío — y se paga a menudo,
// porque el few-shot cambia cada vez que el dueño aprueba una cotización nueva.
//
// Sin cota, cinco cotizaciones largas concatenadas matan a P5 por timeout, y el
// síntoma sería un fallo de INFRAESTRUCTURA que no señala a ningún sitio. Esta ola ya
// vio morir a P4 así.
// ════════════════════════════════════════════════════════════════════════════

// MaxExampleRunes es la PRIMERA cota del few-shot, por ejemplo: 1200 runas. Un
// ejemplo más largo se descarta ENTERO, nunca se trunca (uno de justo 1200 entra).
// Es ~8 veces una cotización real, así que no muerde en el caso normal: corta el blob
// que un tenant pegue por error en `quote_style_examples` —que admite 1 MiB— o un
// `rendered_text` desmesurado, que no tiene tope en ningún otro sitio.
//
// Era `MaxRunasEjemplo` en el paquete viejo.
const MaxExampleRunes = 1200

// MaxFewShotRunes es la SEGUNDA cota: el presupuesto AGREGADO del bloque de
// ejemplos, 3000 runas (justo 3000 cabe). Existe porque el bloque de ejemplos es
// PREFIJO del prompt de P5 y su tamaño es lo que se paga en cada prefill frío de la
// vía local; sin cota, cinco cotizaciones largas matan a P5 por timeout.
//
// # DE DÓNDE SALE EL NÚMERO, QUE ES LA MITAD DE LA DECISIÓN
//
// 3000 runas son unos 3,3 KB en UTF-8 con acentos. Sumado a lo que el prompt de P5
// lleva siempre —instrucción, reglas de JSON, esquema y el borrador, del orden de 1,5 KB
// para un pedido de diez líneas— da un prompt de ~4,8 KB. El único dato de campo que
// hay sobre el tamaño de un prompt en este plan es el que MATÓ a una etapa: 7.786 bytes
// con un plazo de 30 s. 4,8 KB queda claramente por debajo, y con 48 s en vez de 30.
//
// Por el otro lado: cinco cotizaciones típicas son ~750 runas, así que el presupuesto
// es CUATRO VECES el caso normal y en la práctica solo muerde cuando los ejemplos son
// desmesurados — que es justo lo que se quiere acotar.
//
// 🔴 NO ES UN NÚMERO HEREDADO NI MEDIDO: se eligió en T5.1 contra los dos números de
// arriba porque el plan no declaraba ninguna cota. Quien lo mueva tiene que mover el
// razonamiento, no solo el literal — y lo barato de medir es el prefill de P5 con el
// few-shot lleno, que hoy nadie ha medido.
//
// Era `MaxRunasFewShot` en el paquete viejo.
const MaxFewShotRunes = 3000

// SeedStyleRef es la `ref` de `public.tenant_content` donde el tenant deja sus
// cotizaciones de muestra (D-044.11). Que el generador funcione sin ella no es un
// fallback: es el caso normal mientras ningún tenant la escriba.
//
// 🔴 ESTE PAQUETE ES SU PRIMER CONSUMIDOR. Antes de T5.1 la ref no aparecía en una
// sola línea de código de producción, así que NO hay nada cableado que la escriba: la
// semilla estará vacía en campo hasta que alguien haga el
// `PUT /api/v1/tenant-content/quote_style_examples`.
//
// Era `RefEstiloSemilla` en el paquete viejo.
const SeedStyleRef = "quote_style_examples"

// examples arma el few-shot: las últimas N cotizaciones aprobadas del tenant MÁS los
// ejemplos semilla, saneados y acotados.
//
// # EL REPARTO DEL CUPO, QUE ES UNA DECISIÓN DE T5.1
//
// D-044.11 dice «las últimas N aprobadas + semilla opcional» y no dice cuántas de cada
// una cuando hay de las dos. Con el cupo entero para el historial, un tenant que se
// haya molestado en escribir su semilla dejaría de verla en cuanto tuviera cinco
// aprobadas; con el cupo entero para la semilla, la voz real del tenant se pierde. El
// reparto es: **la semilla tiene reservada la mitad del cupo, y el historial se queda
// con el resto** — con la semilla vacía, el historial usa el cupo entero.
//
// El orden final es historial primero (más reciente antes) y semilla después, porque
// lo que el tenant escribió de verdad la semana pasada retrata mejor su voz que una
// muestra que puso el día que se dio de alta. Ese orden es, además, el de PRIORIDAD
// DECRECIENTE, y por eso el presupuesto de runas se gasta recorriéndolo de principio a
// fin (ver trimToBudget): el reparto de arriba decide cuántas RANURAS le tocan a cada
// fuente, y la cota agregada decide cuántas de ésas caben de verdad en el prompt.
//
// Ningún fallo de lectura tumba nada: un few-shot más pobre solo significa un texto
// más sobrio, y en el peor caso el determinista.
//
// Era `ejemplos` en el paquete viejo.
func (s *Service) examples(ctx context.Context, tenantID string) []string {
	seed := s.seedExamples(ctx, tenantID)

	history, err := s.history.ApprovedRenderedTexts(ctx, tenantID, s.quota)
	if err != nil {
		s.log.Warn("quotetext: no se pudo leer el historial aprobado del tenant; el few-shot va sin él",
			"tenant_id", tenantID, "error", err)
		history = nil
	}
	history = sanitize(history)

	if share := s.quota - s.quota/2; len(seed) > 0 && len(history) > share {
		history = history[:share]
	}
	out := history
	for _, example := range seed {
		if len(out) >= s.quota {
			break
		}
		if !containsText(out, example) {
			out = append(out, example)
		}
	}
	return s.trimToBudget(tenantID, out)
}

// trimToBudget aplica MaxFewShotRunes sobre la lista YA ordenada por prioridad, y
// devuelve el prefijo que cabe.
//
// # LA REGLA DEL RECORTE, QUE ES LO QUE HAY QUE PODER PREDECIR
//
// Se recorre de principio a fin sumando runas. En cuanto un ejemplo NO CABE, se para:
// se descartan él y TODOS LOS SIGUIENTES. Dos decisiones dentro de esa frase:
//
//  1. **Se descartan ejemplos ENTEROS, jamás se trunca uno.** Un ejemplo cortado a
//     media frase no es un ejemplo peor: es un ejemplo de otra cosa. Le está enseñando
//     al modelo que las cotizaciones de este negocio acaban de golpe, y P5 redacta
//     EXACTAMENTE lo que el cliente va a leer por WhatsApp. Perder un ejemplo cuesta
//     algo de estilo; truncarlo enseña un defecto.
//  2. **Se para en el primero que no cabe, en vez de saltárselo y seguir probando.**
//     Saltar haría que la lista final dependiera de las longitudes de los ejemplos
//     POSTERIORES: dos tenants con el mismo historial y un ejemplo largo en medio se
//     llevarían few-shots distintos, y explicar por qué exigiría releer los cinco
//     textos. Parando, el resultado es siempre «los K primeros», y K se explica solo.
//
// Como la lista viene en orden de prioridad decreciente —historial de más reciente a
// más antiguo, y la semilla detrás—, recortar por la cola descarta primero lo más
// antiguo del historial, que es lo que menos dice sobre cómo escribe el negocio HOY.
//
// El recorte se avisa por log: un few-shot que encoge sin decirlo sería exactamente la
// clase de merma silenciosa que esta cota existe para hacer visible.
//
// Era `acotarPorPresupuesto` en el paquete viejo.
func (s *Service) trimToBudget(tenantID string, examples []string) []string {
	total := 0
	for i, example := range examples {
		n := utf8.RuneCountInString(example)
		if total+n > MaxFewShotRunes {
			s.log.Warn("quotetext: el few-shot no cabe en su presupuesto; se recorta por la cola",
				"tenant_id", tenantID, "ejemplos_pedidos", len(examples), "ejemplos_usados", i,
				"runas_usadas", total, "presupuesto_runas", MaxFewShotRunes)
			return examples[:i]
		}
		total += n
	}
	return examples
}

// seedExamples lee y sanea los ejemplos de `tenant_content`. Devuelve nada —nunca un
// error— cuando no hay lector, cuando la ref no existe (el caso NORMAL hoy) o cuando
// el blob no tiene una forma que este paquete sepa leer.
//
// Era `deLaSemilla` en el paquete viejo.
func (s *Service) seedExamples(ctx context.Context, tenantID string) []string {
	if s.seed == nil {
		return nil
	}
	blob, err := s.seed.GetTenantContent(ctx, tenantID, SeedStyleRef)
	if err != nil {
		// La ausencia de la ref NO es un problema y es lo que va a pasar en todos los
		// tenants hasta que alguien la escriba: por eso es Debug y no Warn.
		s.log.Debug("quotetext: sin ejemplos semilla para este tenant",
			"tenant_id", tenantID, "ref", SeedStyleRef, "error", err)
		return nil
	}
	examples, err := ParseSeed(blob)
	if err != nil {
		s.log.Warn("quotetext: el blob de ejemplos semilla no tiene una forma legible; se ignora",
			"tenant_id", tenantID, "ref", SeedStyleRef, "error", err)
		return nil
	}
	return sanitize(examples)
}

// ParseSeed lee el blob de `tenant_content` ref `quote_style_examples`.
//
// 🔴 ESTE FORMATO LO FIJÓ T5.1 PORQUE NO EXISTÍA. No hay ni un tenant con esta ref
// escrita, así que no hay compatibilidad que romper. Admite las
// DOS formas obvias y devuelve los textos tal cual, sin sanear:
//
//	["texto 1", "texto 2"]                 — el array pelado
//	{"examples": ["texto 1", "texto 2"]}   — envuelto, por si algún día lleva más claves
//
// Un array vacío, en cualquiera de las dos formas, es válido. El `null` de JSON se
// lee como el array pelado sin ejemplos: (nil, nil). Cualquier otra cosa es un
// error, y son dos:
//
//   - «quotetext: la semilla no es un array de textos ni un objeto con `examples`»
//     — no es JSON, es un escalar, o el array (o `examples`) no es de textos;
//   - «quotetext: el objeto de la semilla no trae la clave `examples`» — es un
//     objeto sin esa clave, o con ella a null.
//
// El llamante ignora el error con un aviso: un blob mal formado no puede dejar sin
// cotización a nadie.
//
// Era `ParseSemilla` en el paquete viejo.
func ParseSeed(blob []byte) ([]string, error) {
	var list []string
	if err := json.Unmarshal(blob, &list); err == nil {
		return list, nil
	}
	var wrapped struct {
		Examples []string `json:"examples"`
	}
	if err := json.Unmarshal(blob, &wrapped); err != nil {
		return nil, errors.New("quotetext: la semilla no es un array de textos ni un objeto con `examples`")
	}
	if wrapped.Examples == nil {
		return nil, errors.New("quotetext: el objeto de la semilla no trae la clave `examples`")
	}
	return wrapped.Examples, nil
}

// sanitize deja los ejemplos utilizables: sin vacíos, sin repetidos, sin los que se
// pasan de MaxExampleRunes y sin los que no son UTF-8. Conserva el orden de entrada.
//
// Es la PRIMERA de las dos cotas y actúa por ejemplo: el que se pasa se descarta
// ENTERO, nunca se trunca (el porqué está en trimToBudget, y vale igual aquí). La
// segunda —el presupuesto agregado— se aplica después, sobre la lista ya ordenada.
//
// Era `sanear` en el paquete viejo.
func sanitize(examples []string) []string {
	out := make([]string, 0, len(examples))
	for _, example := range examples {
		example = strings.TrimSpace(example)
		if example == "" || utf8.RuneCountInString(example) > MaxExampleRunes || !utf8.ValidString(example) {
			continue
		}
		if !containsText(out, example) {
			out = append(out, example)
		}
	}
	return out
}

// containsText es la pertenencia por igualdad exacta. El dedupe es literal a
// propósito: dos cotizaciones que se parecen mucho son DOS ejemplos legítimos de la
// misma voz, y decidir cuánto parecido es demasiado sería inventar un umbral.
//
// Era `contieneTexto` en el paquete viejo.
func containsText(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
