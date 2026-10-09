// Porta internal/casebank/anonimizar.go @ 8d875ab

package casebank

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// anonymize.go — retirar del literal del cliente lo que identifica a una persona,
// ANTES de que ese literal entre al banco de casos (Plan 044 · T5.3).
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 ESTO ES UNA BATERÍA DE EXPRESIONES REGULARES, NO UN NER
// ════════════════════════════════════════════════════════════════════════════
//
// La distinción no es un matiz: es la diferencia entre «este texto está limpio» y
// «este texto no tiene NINGUNO DE LOS TRES PATRONES QUE SÉ BUSCAR». Lo segundo es
// lo único que este fichero puede afirmar, y por eso está escrito aquí y en el
// COMMENT de la 0082 en vez de dejarlo a la suposición del que lea el nombre de
// la función.
//
// LO QUE SÍ CUBRE
//
//   - JID de WhatsApp: `<lo que sea>@s.whatsapp.net`, `@g.us`, `@c.us`, `@lid` y
//     `@broadcast`, sin distinguir mayúsculas. La parte local son letras ASCII,
//     dígitos ASCII, `.`, `_`, `:` y `-`, y hace falta al menos uno. Es el
//     identificador que de verdad aparece en los datos de esta casa
//     (`fleet_sessions`, el entrante de CloudLink), y lleva el número de teléfono
//     dentro. Una CADENA de JID pegados, sin nada entre ellos, cae entera: una
//     marca por JID (ver `jidsIn`).
//   - TELÉFONOS: rachas de 8 a 15 dígitos ASCII, con o sin `+` delante y
//     separados por espacios, tabuladores, SALTOS DE LÍNEA, guiones, guiones
//     bajos, BARRAS, puntos o paréntesis, repetidos o no. La racha empieza y
//     acaba en dígito (un `+` inmediatamente delante entra; un separador suelto
//     por fuera, no). El suelo de 8 es lo que separa un teléfono de una CANTIDAD
//     del pedido: «10 o 12 porciones», «paquete de 30» y «22/07» tienen que
//     sobrevivir intactos o el dataset deja de servir para evaluar al pipeline,
//     que es lo único para lo que existe.
//   - NOMBRES PROPIOS DE UNA LISTA QUE SE LE PASA. La comparación es insensible a
//     mayúsculas (también en las letras acentuadas: «FUSIÓN» cae con «Fusión») y
//     respeta los límites de palabra en Unicode, así que «ambar» y «Ambar» caen
//     igual y «ambarina» NO cae. El acento SÍ cuenta: «fusion» no es «Fusión».
//
// EL LÍMITE DE PALABRA vale para las tres clases: una aparición pegada a una
// letra o a un dígito (de cualquier alfabeto: `unicode.IsLetter`/`IsDigit`) por
// cualquiera de sus dos lados NO se toca. «tel04121234567», «04121234567bs» y
// «Ambar2» pasan enteros. La única excepción es la cadena de JID pegados, a la
// que el límite se le exige por sus dos extremos y no entre eslabones.
//
// 🔴 LO QUE **NO** CUBRE — Y NO ES UNA LISTA DE PENDIENTES, ES EL ALCANCE
//
//   - NOMBRES QUE NO ESTÉN EN LA LISTA. No hay reconocimiento de entidades: un
//     nombre propio que nadie declaró pasa entero. Ésta es la limitación grande y
//     la razón de que el consentimiento del tenant no sea prescindible. Tampoco
//     cae un nombre de la lista escrito con el acento DESCOMPUESTO (NFD) si la
//     lista lo trae compuesto, ni viceversa.
//   - DIRECCIONES POSTALES, referencias a lugares, nombres de negocio.
//   - CORREOS ELECTRÓNICOS (`algo@dominio.com`): el patrón de JID exige uno de
//     los cinco dominios de WhatsApp, así que un correo NO se toca. Es
//     deliberado: un patrón de correo genérico se comería los JID mal formados y
//     cualquier `@` del texto, y prefiero un agujero declarado a un recorte
//     silencioso que no sé medir.
//   - DOCUMENTOS DE IDENTIDAD, matrículas, IBAN, tarjetas.
//   - APODOS, iniciales, «mi hermana la de Valencia».
//   - Cualquier identificador de 7 dígitos o menos (un fijo corto local pasa).
//   - 🔴 UN TELÉFONO ESCRITO CON UN SEPARADOR QUE NO ESTÉ EN LA CLASE DE
//     `rePhoneCandidate`. Éste es el límite REAL y el peligroso, y no estaba
//     escrito hasta el 2026-08-27, cuando una auditoría lo midió: con `/` fuera
//     de la clase, `0412/1234567` salía INTACTO de `Anonymize` y `Remains`
//     devolvía `[]` sobre él. No es un recorte a medias: es un pase entero con
//     el barrido diciendo «limpio», que es la peor forma de fallar que tiene
//     este fichero. La clase se amplió (`/`, `_`, `\r`, `\n`), pero LA CLASE DE
//     FALLO SIGUE VIVA: un separador exótico —el guion largo «–», el espacio
//     duro U+00A0, el punto medio «·», un emoji entre dígitos— vuelve a
//     producirlo. ⚠️ Quien añada un separador aquí NO está afinando: está
//     cerrando un agujero de PII, y le toca añadir el caso a
//     `TestAnonymize_SeparatorsThatOnceEscaped` **en las dos mitades**
//     (`Anonymize` y `Remains`).
//   - 🔴 UN TELÉFONO ESCRITO CON DÍGITOS QUE NO SON ASCII (árabes-índicos
//     «٠٤١٢…», de ancho completo «０４１２…»): ni el candidato ni el conteo los
//     ven. Medido en F7 (hallazgo 40), no estaba escrito.
//   - 🔴 DOS TELÉFONOS SEGUIDOS SEPARADOS SOLO POR SEPARADORES DE LA CLASE
//     («04121234567 04149876543», o uno por línea): el candidato los funde en UNA
//     racha de más de 15 dígitos, que supera el techo y PASA ENTERA — los dos
//     números, con `Remains` diciendo «limpio». Con una coma o una «y» por medio
//     caen los dos. Medido en F7 (hallazgo 40), no estaba escrito; se conserva la
//     conducta del viejo y se deja fijada en el corpus del test.
//   - Un JID pegado a una letra o a un dígito por la derecha
//     («…@s.whatsapp.netx») sin que lo que sigue sea otro JID: no es JID, y la
//     pasada de teléfonos se lleva solo el número de delante, DEJANDO EL DOMINIO.
//   - Un número partido por PALABRAS («cero cuatro uno dos…»), o por letras
//     («0412 ext 1234567»).
//
// ⚠️ FALSOS POSITIVOS CONOCIDOS, que van hacia el lado seguro (tapar de más) y
// que CRECIERON al ampliar la clase de separadores:
//
//   - una fecha larga escrita con separadores que el patrón admite —`2026 08 27`—
//     suma 8 dígitos y se redacta como si fuera un teléfono;
//   - desde que la barra entra en la clase, una FECHA COMPLETA en formato
//     `22/07/2026` también suma 8 y se redacta. Es una pérdida real —una fecha de
//     entrega es dato del pedido— y se acepta a sabiendas: el intercambio es
//     «tapo alguna fecha» contra «publico algún teléfono», y en un barrido de PII
//     ese intercambio no está empatado. La fecha CORTA del pedido (`22/07`, 4
//     dígitos) sigue intacta, que es la forma en que aparece en el caso Ambar;
//   - la parte local del JID se lleva lo que tenga pegado delante si es de su
//     clase: «grupo:120363…@g.us» se redacta entero, «grupo:» incluido;
//   - desde que la cadena de JID pegados cae entera, un JID pegado por la derecha
//     a algo que acaba siendo otro JID también cae: «user@lidia584@lid» son dos
//     marcas, aunque «user@lidia» a solas no sea un JID.
//
// # LOS DOS SENTIDOS: `Anonymize` REDACTA, `Remains` DELATA
//
// Comparten detectores a propósito, y eso tiene una consecuencia que hay que
// decir en voz alta: `Remains(Anonymize(x))` está VACÍO. Como comprobación es una
// tautología y no prueba nada.
//
// ⚠️ Ese «vacío» vuelve a ser cierto para los JID desde el 2026-10-08, y lo
// vigila `TestAnonymize_ThenRemains_EmptyOverTheWholeCorpus` sobre el corpus
// adversario entero. En el viejo no lo era: las pasadas de `Anonymize` corren en
// cadena, y con dos JID pegados ninguno pasaba el límite de palabra, la pasada de
// teléfonos tapaba el primer número y el segundo SALÍA EN CLARO con su dominio
// (hallazgo 1 de F7). Aquí la cadena cae entera: DIVERGENCIA DELIBERADA del
// viejo, decisión de Jhoan.
//
// Queda UNA excepción declarada, heredada y sin número dentro: una marca puede
// dejar al descubierto un límite de palabra que antes no existía. En
// «José.maria@lid» el candidato «.maria@lid» está pegado a una letra y no es JID;
// tapado el nombre, queda «[NOMBRE].maria@lid», y barrido eso SÍ lo es. La fija
// `TestAnonymize_JIDAfterAccentedName_LeavesAJIDBehind`.
//
// `Remains` NO existe para auditar a `Anonymize`. Existe para auditar TEXTO QUE
// NO PASÓ POR ÉL: el fixture escrito a mano (`seed.go`), el caso que alguien
// pegue en un ticket, la fila que llegue por una puerta futura. Ahí sí responde
// una pregunta abierta. Por eso el test de la semilla es un test del BARRIDO
// sobre un texto redactado a mano, y va acompañado de un control negativo —un
// texto con teléfono, JID y nombre— que exige que el barrido SÍ encuentre cosas:
// sin ese control, «la semilla pasa el barrido» lo satisfaría también un barrido
// que no mira nada.

// Las tres marcas de redacción. Sus valores son los del paquete viejo, literales:
// son texto que acaba en la tabla.
const (
	// MarkJID (antes `MarcaJID`) sustituye a un JID de WhatsApp.
	MarkJID = "[JID]"
	// MarkPhone (antes `MarcaTelefono`) sustituye a una racha de dígitos con pinta
	// de teléfono.
	MarkPhone = "[TELEFONO]"
	// MarkName (antes `MarcaNombre`) sustituye a un nombre propio de la lista.
	MarkName = "[NOMBRE]"
)

// Los dos umbrales del teléfono, en DÍGITOS (no en longitud de la cadena: los
// separadores no cuentan).
//
// 🔴 EL SUELO ES EL PARÁMETRO QUE IMPORTA. Con 8, «10 o 12 porciones», «paquete
// de 30» y «22/07» sobreviven; con 6 se empezarían a comer cantidades del pedido
// y el banco de casos dejaría de poder evaluar a P4, que es justo la etapa que
// vive de esos números. El techo de 15 es el máximo de E.164: por encima ya no es
// un teléfono, es un identificador de otra cosa, y este fichero no sabe de cuál.
const (
	minPhoneDigits = 8
	maxPhoneDigits = 15
)

var (
	// reJID exige uno de los CINCO dominios de WhatsApp. Ver «lo que no cubre».
	reJID = regexp.MustCompile(`(?i)[0-9A-Za-z._:\-]+@(?:s\.whatsapp\.net|g\.us|c\.us|lid|broadcast)`)

	// rePhoneCandidate (antes `reCandidatoTelefono`) es solo el CANDIDATO: empieza
	// y acaba en dígito y admite separadores por medio. Quién es teléfono de
	// verdad lo decide el conteo de dígitos, no este patrón — meterlo en la
	// expresión regular obligaría a enumerar formatos y se escaparía el primero
	// que no estuviera en la lista.
	//
	// 🔴 LA CLASE DE SEPARADORES ES EL PUNTO FRÁGIL DE TODO ESTE FICHERO, y hay
	// que decirlo aquí porque no se ve: un separador que NO esté en la clase no
	// produce un recorte parcial, produce un PASE ENTERO. `0412/1234567` con la
	// barra fuera de la clase no casa por ningún lado —ni «0412» ni «1234567»
	// llegan a 8 dígitos por separado— así que el número sale intacto Y `Remains`
	// dice «cero hallazgos» sobre un teléfono completo. Ese fallo se midió el
	// 2026-08-27 con `/`, `_` y el salto de línea, los tres a la vez.
	//
	// Por eso la clase es DELIBERADAMENTE ANCHA: barra, guion bajo, guion, punto,
	// paréntesis, espacio, tabulador y los dos caracteres de fin de línea. El
	// coste de meter uno de más es tapar alguna fecha (ver el falso positivo
	// declarado arriba); el de dejar uno fuera es publicar un teléfono. No son
	// errores del mismo tamaño y la clase se elige por el segundo.
	rePhoneCandidate = regexp.MustCompile(`\+?[0-9][0-9 \t\r\n_/().\-]*[0-9]`)
)

// Class (antes `Clase`) es la clase de dato identificable que un detector
// reconoce.
type Class string

// Las tres clases que este barrido sabe reconocer, con los valores del paquete
// viejo. La lista es CERRADA a propósito: lo que no está aquí no se detecta, y la
// cabecera de arriba dice cuáles son esos huecos en vez de dejarlos al
// descubrimiento de quien depure.
const (
	// ClassJID (antes `ClaseJID`).
	ClassJID Class = "jid"
	// ClassPhone (antes `ClaseTelefono`).
	ClassPhone Class = "telefono"
	// ClassName (antes `ClaseNombre`).
	ClassName Class = "nombre"
)

// Finding (antes `Hallazgo`) es UNA aparición que el barrido considera
// identificable.
type Finding struct {
	Class Class
	// Text (antes `Texto`) es el fragmento tal cual aparece. 🔴 Va aquí porque
	// quien llama a `Remains` está CURANDO un caso y necesita ver qué tapar; NO se
	// loguea y no se persiste: es PII, y meterlo en un log sería exactamente el
	// fallo que este paquete existe para evitar.
	Text string
	// Start y End (antes `Ini` y `Fin`) son los índices de BYTE en el texto
	// examinado: Text == texto[Start:End].
	Start, End int
}

// Anonymizer (antes `Anonimizador`) redacta y barre. Es un valor y no un puntero:
// no tiene estado mutable y copiarlo es gratis. Su valor cero es un anonimizador
// sin nombres, igual que `NewAnonymizer()`.
type Anonymizer struct {
	names []string
	// reNames es nil cuando la lista viene vacía, y ese caso es LEGÍTIMO: un texto
	// sin nombres propios conocidos se anonimiza igual en sus otras dos mitades.
	// Lo que no puede pasar es que un nil aquí haga creer que el barrido de
	// nombres se ejecutó — por eso `Remains` no devuelve nada de clase `nombre` en
	// ese caso, en vez de devolver «cero hallazgos» como si hubiera mirado.
	reNames *regexp.Regexp
}

// NewAnonymizer (antes `NuevoAnonimizador`) arma el anonimizador con la lista de
// nombres propios a retirar. A cada nombre se le recortan los espacios de los
// extremos y los que quedan vacíos se descartan; con la lista vacía el
// anonimizador sigue tapando JID y teléfonos, y ningún nombre. Un nombre se busca
// como TEXTO LITERAL: sus signos (`.`, `*`, `(`…) no son metacaracteres.
//
// Los nombres se ordenan de MÁS LARGO A MÁS CORTO (en bytes; a igual longitud, en
// el orden en que llegaron), y eso no es cosmético: con `["Ana","Ana María"]` en
// ese orden se taparía «Ana» y quedaría « María» suelto detrás de la marca.
func NewAnonymizer(names ...string) Anonymizer {
	clean := make([]string, 0, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			clean = append(clean, n)
		}
	}
	if len(clean) == 0 {
		return Anonymizer{}
	}
	// De más largo a más corto: RE2 casa la alternativa que aparece ANTES en el
	// patrón, no la más larga.
	sort.SliceStable(clean, func(i, j int) bool { return len(clean[i]) > len(clean[j]) })

	alternatives := make([]string, 0, len(clean))
	for _, n := range clean {
		alternatives = append(alternatives, regexp.QuoteMeta(n))
	}
	return Anonymizer{
		names:   clean,
		reNames: regexp.MustCompile(`(?i)` + strings.Join(alternatives, "|")),
	}
}

// Anonymize (antes `Anonimizar`) devuelve el texto con JID, teléfonos y nombres
// conocidos sustituidos por `MarkJID`, `MarkPhone` y `MarkName`. Lo demás sale
// byte a byte; un texto sin nada que tapar (el vacío incluido) sale igual.
//
// EL ORDEN DE LAS TRES PASADAS ES PARTE DE LA CORRECCIÓN, no una preferencia: el
// JID va PRIMERO porque lleva un teléfono dentro (`584121234567@s.whatsapp.net`)
// y la pasada de teléfonos, si corriera antes, lo partiría en `[TELEFONO]@s.…` —
// dejando el dominio y perdiendo la marca buena. Los nombres van al final porque
// las dos marcas anteriores no contienen letras que puedan casar con un nombre.
// Cada pasada corre sobre lo que dejó la anterior.
func (a Anonymizer) Anonymize(text string) string {
	text = replaceSpans(text, jidsIn(text), MarkJID)
	text = replaceSpans(text, a.phonesIn(text), MarkPhone)
	return replaceSpans(text, a.namesIn(text), MarkName)
}

// Remains (antes `Restos`) es EL BARRIDO: devuelve lo que sigue pareciendo
// identificable, en orden de aparición. Vacío (de longitud cero, no nil)
// significa «ninguno de los tres patrones que sé buscar aparece», que NO es lo
// mismo que «este texto no identifica a nadie» (ver la cabecera del fichero). Sin
// lista de nombres no devuelve nada de clase `nombre`.
//
// 🔴 UN HALLAZGO NO SE CUENTA DOS VECES, y esto es la diferencia estructural con
// `Anonymize`: allí las tres pasadas corren EN CADENA —cada una sobre el texto
// que dejó la anterior—, así que cuando le toca a los teléfonos el JID ya es
// `[JID]` y no tiene dígitos. Aquí los tres detectores miran EL MISMO texto, y un
// JID como `584121234567@s.whatsapp.net` lleva dentro una racha de 12 dígitos que
// el detector de teléfonos reconoce con toda la razón. Reportar las dos cosas
// diría que hay DOS datos identificables donde hay uno, e inflaría cualquier
// recuento que alguien haga sobre esta salida.
//
// El desempate es por PRIORIDAD y respeta el orden de las pasadas de
// `Anonymize`: JID > teléfono > nombre. Gana el que tapa más contexto — un JID
// dice a la vez el número y que ese contacto es de WhatsApp. Un hallazgo que
// pisa a otro de más prioridad se descarta entero.
func (a Anonymizer) Remains(text string) []Finding {
	out := make([]Finding, 0, 4)
	candidates := []struct {
		locs  [][]int
		class Class
	}{
		{jidsIn(text), ClassJID},
		{a.phonesIn(text), ClassPhone},
		{a.namesIn(text), ClassName},
	}
	for _, c := range candidates {
		for _, f := range findings(text, c.locs, c.class) {
			if !overlaps(out, f) {
				out = append(out, f)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// overlaps (antes `solapa`) dice si el hallazgo pisa a alguno de los ya aceptados.
func overlaps(accepted []Finding, f Finding) bool {
	for _, v := range accepted {
		if f.Start < v.End && v.Start < f.End {
			return true
		}
	}
	return false
}

// Names (antes `Nombres`) son los nombres que este anonimizador conoce, ya
// recortados. Existe para que un test —y un informe de curación— pueda decir
// CONTRA QUÉ lista se barrió, en vez de afirmar «se barrieron los nombres» sin
// poder nombrar cuáles. Sin nombres devuelve nil.
//
// ⚠️ EL ORDEN NO ES EL QUE SE PASÓ A `NewAnonymizer`: es el de la alternancia,
// ordenado de más largo a más corto (ver allí por qué ese orden es obligatorio).
// Quien compare esta salida con una lista tiene que compararla como CONJUNTO. Se
// devuelve una copia para que el llamador pueda ordenarla sin tocar al
// anonimizador.
func (a Anonymizer) Names() []string {
	return append([]string(nil), a.names...)
}

// jidsIn localiza los JID. Una CADENA de JID pegados —cada uno empieza justo
// donde acaba el anterior— se trata como un bloque: el límite de palabra se le
// exige a la cadena por sus dos extremos, no a cada eslabón, y si lo cumple caen
// todos, cada uno con su tramo. Un JID suelto es una cadena de uno.
//
// 🔴 SIN ESTO, DOS JID PEGADOS DEJAN UN NÚMERO EN CLARO: cada uno tiene al otro
// pegado, ninguno pasa el límite, y la pasada de teléfonos solo alcanza al
// primero (el segundo está pegado a la «t» de «.net»). No existía en el viejo.
func jidsIn(text string) [][]int {
	all := reJID.FindAllStringIndex(text, -1)
	out := make([][]int, 0, len(all))
	for i := 0; i < len(all); {
		j := i + 1
		for j < len(all) && all[j][0] == all[j-1][1] {
			j++
		}
		if atBoundary(text, all[i][0], all[j-1][1]) {
			out = append(out, all[i:j]...)
		}
		i = j
	}
	return out
}

// phonesIn (antes `telefonos`) filtra los candidatos por conteo de dígitos. Es
// donde vive la decisión que separa un teléfono de una cantidad del pedido.
func (a Anonymizer) phonesIn(text string) [][]int {
	out := make([][]int, 0, 2)
	for _, loc := range findWithBoundaries(text, rePhoneCandidate) {
		n := countDigits(text[loc[0]:loc[1]])
		if n >= minPhoneDigits && n <= maxPhoneDigits {
			out = append(out, loc)
		}
	}
	return out
}

// namesIn (antes `nombresEn`) localiza los nombres conocidos respetando límites
// de palabra.
func (a Anonymizer) namesIn(text string) [][]int {
	if a.reNames == nil {
		return nil
	}
	return findWithBoundaries(text, a.reNames)
}

// findWithBoundaries (antes `buscarConLimites`) devuelve las apariciones de `re`
// que NO están pegadas a una letra o a un dígito por ninguno de sus dos lados.
//
// 🔴 EL LÍMITE SE COMPRUEBA AQUÍ Y NO EN LA EXPRESIÓN REGULAR, y las dos razones
// son concretas:
//
//   - `\b` de Go es ASCII: con «José» o «Ñoño» el límite se evalúa mal en el
//     borde acentuado. Aquí se decodifica la runa de verdad y se pregunta a
//     `unicode`;
//   - un patrón que CONSUMA el separador (`(^|[^\p{L}\p{N}])…`) se come el
//     espacio, y en «Ambar Herminia» la segunda aparición se quedaría sin límite
//     izquierdo que casar y NO se redactaría. RE2 no tiene lookahead con el que
//     evitarlo.
func findWithBoundaries(text string, re *regexp.Regexp) [][]int {
	if re == nil {
		return nil
	}
	all := re.FindAllStringIndex(text, -1)
	out := make([][]int, 0, len(all))
	for _, loc := range all {
		if atBoundary(text, loc[0], loc[1]) {
			out = append(out, loc)
		}
	}
	return out
}

// atBoundary (antes `enLimite`) dice si [start,end) no está pegado a letra o
// dígito por ningún lado.
func atBoundary(text string, start, end int) bool {
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(text[:start])
		if isLetterOrDigit(r) {
			return false
		}
	}
	if end < len(text) {
		r, _ := utf8.DecodeRuneInString(text[end:])
		if isLetterOrDigit(r) {
			return false
		}
	}
	return true
}

func isLetterOrDigit(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// replaceSpans (antes `sustituir`) reemplaza los tramos por la marca. Recorre de
// izquierda a derecha sobre tramos que `findWithBoundaries` devuelve YA ordenados
// y sin solapes (RE2 no devuelve solapes).
func replaceSpans(text string, locs [][]int, mark string) string {
	if len(locs) == 0 {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	prev := 0
	for _, loc := range locs {
		b.WriteString(text[prev:loc[0]])
		b.WriteString(mark)
		prev = loc[1]
	}
	b.WriteString(text[prev:])
	return b.String()
}

func findings(text string, locs [][]int, class Class) []Finding {
	out := make([]Finding, 0, len(locs))
	for _, loc := range locs {
		out = append(out, Finding{Class: class, Text: text[loc[0]:loc[1]], Start: loc[0], End: loc[1]})
	}
	return out
}

// countDigits (antes `digitos`) cuenta los dígitos ASCII: los de otros alfabetos
// no cuentan (ver «lo que no cubre»).
func countDigits(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}
