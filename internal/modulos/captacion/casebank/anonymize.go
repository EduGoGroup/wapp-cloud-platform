// Porta internal/casebank/anonimizar.go @ 8d875ab

package casebank

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
//     dentro.
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
// «Ambar2» pasan enteros.
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
//     («…@s.whatsapp.netx», o dos JID sin separador): no es JID, y la pasada de
//     teléfonos se lleva solo el número de delante, DEJANDO EL DOMINIO.
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
//     clase: «grupo:120363…@g.us» se redacta entero, «grupo:» incluido.
//
// # LOS DOS SENTIDOS: `Anonymize` REDACTA, `Remains` DELATA
//
// Comparten detectores a propósito, y eso tiene una consecuencia que hay que
// decir en voz alta: `Remains(Anonymize(x))` está VACÍO SIEMPRE, para cualquier
// `x`. Como comprobación es una tautología y no prueba nada.
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
type Anonymizer struct{}

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
	panic(pendiente.Implementar("casebank.NewAnonymizer"))
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
	panic(pendiente.Implementar("casebank.Anonymizer.Anonymize"))
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
	panic(pendiente.Implementar("casebank.Anonymizer.Remains"))
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
	panic(pendiente.Implementar("casebank.Anonymizer.Names"))
}
