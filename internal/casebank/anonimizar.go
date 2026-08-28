package casebank

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// anonimizar.go — retirar del literal del cliente lo que identifica a una persona,
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
//     `@broadcast`. Es el identificador que de verdad aparece en los datos de
//     esta casa (`fleet_sessions`, el entrante de CloudLink), y lleva el número
//     de teléfono dentro.
//   - TELÉFONOS: rachas de 8 a 15 dígitos, con o sin `+` delante y separados por
//     espacios, tabuladores, SALTOS DE LÍNEA, guiones, guiones bajos, BARRAS,
//     puntos o paréntesis. El suelo de 8 es lo que separa un teléfono de una
//     CANTIDAD del pedido: «10 o 12 porciones», «paquete de 30» y «22/07» tienen
//     que sobrevivir intactos o el dataset deja de servir para evaluar al
//     pipeline, que es lo único para lo que existe.
//   - NOMBRES PROPIOS DE UNA LISTA QUE SE LE PASA. Sin acentos ni mayúsculas de
//     por medio: la comparación es insensible a mayúsculas y respeta los límites
//     de palabra en Unicode, así que «ambar» y «Ambar» caen igual y «ambarina»
//     NO cae.
//
// 🔴 LO QUE **NO** CUBRE — Y NO ES UNA LISTA DE PENDIENTES, ES EL ALCANCE
//
//   - NOMBRES QUE NO ESTÉN EN LA LISTA. No hay reconocimiento de entidades: un
//     nombre propio que nadie declaró pasa entero. Ésta es la limitación grande y
//     la razón de que el consentimiento del tenant no sea prescindible.
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
//     `reCandidatoTelefono`. Éste es el límite REAL y el peligroso, y no estaba
//     escrito hasta el 2026-08-27, cuando una auditoría lo midió: con `/` fuera
//     de la clase, `0412/1234567` salía INTACTO de `Anonimizar` y `Restos`
//     devolvía `[]` sobre él. No es un recorte a medias: es un pase entero con
//     el barrido diciendo «limpio», que es la peor forma de fallar que tiene
//     este fichero. La clase se amplió (`/`, `_`, `\r`, `\n`), pero LA CLASE DE
//     FALLO SIGUE VIVA: un separador exótico —el guion largo «–», el espacio
//     duro U+00A0, el punto medio «·», un emoji entre dígitos— vuelve a
//     producirlo. ⚠️ Quien añada un separador aquí NO está afinando: está
//     cerrando un agujero de PII, y le toca añadir el caso a
//     `TestAnonimizar_SeparadoresQueUnaVezSeEscaparon` **en las dos mitades**
//     (`Anonimizar` y `Restos`).
//   - Un número partido por PALABRAS («cero cuatro uno dos…»), o por letras
//     («0412 ext 1234567»).
//
// ⚠️ FALSOS POSITIVOS CONOCIDOS, que van hacia el lado seguro (tapar de más) y
// que CRECIERON al ampliar la clase de separadores:
//
//   - una fecha larga escrita con separadores que el patrón admite —`2026 08 27`—
//     suma 8 dígitos y se redacta como si fuera un teléfono;
//   - 🆕 desde que la barra entra en la clase, una FECHA COMPLETA en formato
//     `22/07/2026` también suma 8 y se redacta. Es una pérdida real —una fecha de
//     entrega es dato del pedido— y se acepta a sabiendas: el intercambio es
//     «tapo alguna fecha» contra «publico algún teléfono», y en un barrido de PII
//     ese intercambio no está empatado. La fecha CORTA del pedido (`22/07`, 4
//     dígitos) sigue intacta, que es la forma en que aparece en el caso Ambar.
//
// # LOS DOS SENTIDOS: `Anonimizar` REDACTA, `Restos` DELATA
//
// Comparten detectores a propósito, y eso tiene una consecuencia que hay que
// decir en voz alta: `Restos(Anonimizar(x))` está VACÍO SIEMPRE, para cualquier
// `x`. Como comprobación es una tautología y no prueba nada.
//
// `Restos` NO existe para auditar a `Anonimizar`. Existe para auditar TEXTO QUE
// NO PASÓ POR ÉL: el fixture escrito a mano (`semilla.go`), el caso que alguien
// pegue en un ticket, la fila que llegue por una puerta futura. Ahí sí responde
// una pregunta abierta. Por eso el test de la semilla es un test del BARRIDO
// sobre un texto redactado a mano, y va acompañado de un control negativo —un
// texto con teléfono, JID y nombre— que exige que el barrido SÍ encuentre cosas:
// sin ese control, «la semilla pasa el barrido» lo satisfaría también un barrido
// que no mira nada.
const (
	// MarcaJID sustituye a un JID de WhatsApp.
	MarcaJID = "[JID]"
	// MarcaTelefono sustituye a una racha de dígitos con pinta de teléfono.
	MarcaTelefono = "[TELEFONO]"
	// MarcaNombre sustituye a un nombre propio de la lista.
	MarcaNombre = "[NOMBRE]"
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
	minDigitosTelefono = 8
	maxDigitosTelefono = 15
)

var (
	// reJID exige uno de los CINCO dominios de WhatsApp. Ver «lo que no cubre».
	reJID = regexp.MustCompile(`(?i)[0-9A-Za-z._:\-]+@(?:s\.whatsapp\.net|g\.us|c\.us|lid|broadcast)`)

	// reCandidatoTelefono es solo el CANDIDATO: empieza y acaba en dígito y admite
	// separadores por medio. Quién es teléfono de verdad lo decide el conteo de
	// dígitos, no este patrón — meterlo en la expresión regular obligaría a
	// enumerar formatos y se escaparía el primero que no estuviera en la lista.
	//
	// 🔴 LA CLASE DE SEPARADORES ES EL PUNTO FRÁGIL DE TODO ESTE FICHERO, y hay
	// que decirlo aquí porque no se ve: un separador que NO esté en la clase no
	// produce un recorte parcial, produce un PASE ENTERO. `0412/1234567` con la
	// barra fuera de la clase no casa por ningún lado —ni «0412» ni «1234567»
	// llegan a 8 dígitos por separado— así que el número sale intacto Y `Restos`
	// dice «cero hallazgos» sobre un teléfono completo. Ese fallo se midió el
	// 2026-08-27 con `/`, `_` y el salto de línea, los tres a la vez.
	//
	// Por eso la clase es DELIBERADAMENTE ANCHA: barra, guion bajo, guion, punto,
	// paréntesis, espacio, tabulador y los dos caracteres de fin de línea. El
	// coste de meter uno de más es tapar alguna fecha (ver el falso positivo
	// declarado arriba); el de dejar uno fuera es publicar un teléfono. No son
	// errores del mismo tamaño y la clase se elige por el segundo.
	reCandidatoTelefono = regexp.MustCompile(`\+?[0-9][0-9 \t\r\n_/().\-]*[0-9]`)
)

// Clase es la clase de dato identificable que un detector reconoce.
type Clase string

// Las tres clases que este barrido sabe reconocer. La lista es CERRADA a
// propósito: lo que no está aquí no se detecta, y el docstring de arriba dice
// cuáles son esos huecos en vez de dejarlos al descubrimiento de quien depure.
const (
	ClaseJID      Clase = "jid"
	ClaseTelefono Clase = "telefono"
	ClaseNombre   Clase = "nombre"
)

// Hallazgo es UNA aparición que el barrido considera identificable.
type Hallazgo struct {
	Clase Clase
	// Texto es el fragmento tal cual aparece. 🔴 Va aquí porque quien llama a
	// `Restos` está CURANDO un caso y necesita ver qué tapar; NO se loguea y no
	// se persiste: es PII, y meterlo en un log sería exactamente el fallo que
	// este paquete existe para evitar.
	Texto string
	// Ini y Fin son los índices de byte en el texto examinado.
	Ini, Fin int
}

// Anonimizador redacta y barre. Es un valor y no un puntero: no tiene estado
// mutable y copiarlo es gratis.
type Anonimizador struct {
	nombres []string
	// reNombres es nil cuando la lista viene vacía, y ese caso es LEGÍTIMO: un
	// texto sin nombres propios conocidos se anonimiza igual en sus otras dos
	// mitades. Lo que no puede pasar es que un nil aquí haga creer que el barrido
	// de nombres se ejecutó — por eso `Restos` no devuelve nada de clase
	// `nombre` en ese caso, en vez de devolver «cero hallazgos» como si hubiera
	// mirado.
	reNombres *regexp.Regexp
}

// NuevoAnonimizador arma el anonimizador con la lista de nombres propios a
// retirar. Los nombres se ordenan de MÁS LARGO A MÁS CORTO antes de armar la
// alternancia, y eso no es cosmético: con `["Ana","Ana María"]` en ese orden, RE2
// casa la alternativa que aparece antes en el patrón y dejaría « María» suelto
// detrás de la marca.
func NuevoAnonimizador(nombres ...string) Anonimizador {
	limpios := make([]string, 0, len(nombres))
	for _, n := range nombres {
		if n = strings.TrimSpace(n); n != "" {
			limpios = append(limpios, n)
		}
	}
	if len(limpios) == 0 {
		return Anonimizador{}
	}
	sort.SliceStable(limpios, func(i, j int) bool { return len(limpios[i]) > len(limpios[j]) })

	alternativas := make([]string, 0, len(limpios))
	for _, n := range limpios {
		alternativas = append(alternativas, regexp.QuoteMeta(n))
	}
	return Anonimizador{
		nombres:   limpios,
		reNombres: regexp.MustCompile(`(?i)` + strings.Join(alternativas, "|")),
	}
}

// Anonimizar devuelve el texto con JID, teléfonos y nombres conocidos sustituidos
// por sus marcas.
//
// EL ORDEN DE LAS TRES PASADAS ES PARTE DE LA CORRECCIÓN, no una preferencia: el
// JID va PRIMERO porque lleva un teléfono dentro (`584121234567@s.whatsapp.net`)
// y la pasada de teléfonos, si corriera antes, lo partiría en `[TELEFONO]@s.…` —
// dejando el dominio y perdiendo la marca buena. Los nombres van al final porque
// las dos marcas anteriores no contienen letras que puedan casar con un nombre.
func (a Anonimizador) Anonimizar(texto string) string {
	texto = sustituir(texto, buscarConLimites(texto, reJID), MarcaJID)
	texto = sustituir(texto, a.telefonos(texto), MarcaTelefono)
	return sustituir(texto, a.nombresEn(texto), MarcaNombre)
}

// Restos es EL BARRIDO: devuelve lo que sigue pareciendo identificable, en orden
// de aparición. Vacío significa «ninguno de los tres patrones que sé buscar
// aparece», que NO es lo mismo que «este texto no identifica a nadie» (ver la
// cabecera del fichero).
//
// 🔴 UN HALLAZGO NO SE CUENTA DOS VECES, y esto es la diferencia estructural con
// `Anonimizar`: allí las tres pasadas corren EN CADENA —cada una sobre el texto
// que dejó la anterior—, así que cuando le toca a los teléfonos el JID ya es
// `[JID]` y no tiene dígitos. Aquí los tres detectores miran EL MISMO texto, y un
// JID como `584121234567@s.whatsapp.net` lleva dentro una racha de 12 dígitos que
// el detector de teléfonos reconoce con toda la razón. Reportar las dos cosas
// diría que hay DOS datos identificables donde hay uno, e inflaría cualquier
// recuento que alguien haga sobre esta salida.
//
// El desempate es por PRIORIDAD y respeta el orden de las pasadas de
// `Anonimizar`: JID > teléfono > nombre. Gana el que tapa más contexto — un JID
// dice a la vez el número y que ese contacto es de WhatsApp.
func (a Anonimizador) Restos(texto string) []Hallazgo {
	out := make([]Hallazgo, 0, 4)
	candidatos := []struct {
		locs  [][]int
		clase Clase
	}{
		{buscarConLimites(texto, reJID), ClaseJID},
		{a.telefonos(texto), ClaseTelefono},
		{a.nombresEn(texto), ClaseNombre},
	}
	for _, c := range candidatos {
		for _, h := range hallazgos(texto, c.locs, c.clase) {
			if !solapa(out, h) {
				out = append(out, h)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ini < out[j].Ini })
	return out
}

// solapa dice si el hallazgo pisa a alguno de los ya aceptados.
func solapa(ya []Hallazgo, h Hallazgo) bool {
	for _, v := range ya {
		if h.Ini < v.Fin && v.Ini < h.Fin {
			return true
		}
	}
	return false
}

// Nombres son los nombres que este anonimizador conoce. Existe para que un test
// —y un informe de curación— pueda decir CONTRA QUÉ lista se barrió, en vez de
// afirmar «se barrieron los nombres» sin poder nombrar cuáles.
//
// ⚠️ EL ORDEN NO ES EL QUE SE PASÓ A `NuevoAnonimizador`: es el de la alternancia,
// ordenado de más largo a más corto (ver allí por qué ese orden es obligatorio).
// Quien compare esta salida con una lista tiene que compararla como CONJUNTO; un
// `reflect.DeepEqual` contra el orden de entrada falla, y falla por un detalle de
// implementación, no por un nombre que falte. Se devuelve una copia para que el
// llamador pueda ordenarla sin tocar al anonimizador.
func (a Anonimizador) Nombres() []string {
	return append([]string(nil), a.nombres...)
}

// telefonos filtra los candidatos por conteo de dígitos. Es donde vive la
// decisión que separa un teléfono de una cantidad del pedido.
func (a Anonimizador) telefonos(texto string) [][]int {
	out := make([][]int, 0, 2)
	for _, loc := range buscarConLimites(texto, reCandidatoTelefono) {
		n := digitos(texto[loc[0]:loc[1]])
		if n >= minDigitosTelefono && n <= maxDigitosTelefono {
			out = append(out, loc)
		}
	}
	return out
}

// nombresEn localiza los nombres conocidos respetando límites de palabra.
func (a Anonimizador) nombresEn(texto string) [][]int {
	if a.reNombres == nil {
		return nil
	}
	return buscarConLimites(texto, a.reNombres)
}

// buscarConLimites devuelve las apariciones de `re` que NO están pegadas a una
// letra o a un dígito por ninguno de sus dos lados.
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
func buscarConLimites(texto string, re *regexp.Regexp) [][]int {
	if re == nil {
		return nil
	}
	todas := re.FindAllStringIndex(texto, -1)
	out := make([][]int, 0, len(todas))
	for _, loc := range todas {
		if enLimite(texto, loc[0], loc[1]) {
			out = append(out, loc)
		}
	}
	return out
}

// enLimite dice si [ini,fin) no está pegado a letra o dígito por ningún lado.
func enLimite(texto string, ini, fin int) bool {
	if ini > 0 {
		r, _ := utf8.DecodeLastRuneInString(texto[:ini])
		if esLetraODigito(r) {
			return false
		}
	}
	if fin < len(texto) {
		r, _ := utf8.DecodeRuneInString(texto[fin:])
		if esLetraODigito(r) {
			return false
		}
	}
	return true
}

func esLetraODigito(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// sustituir reemplaza los tramos por la marca. Recorre de izquierda a derecha
// sobre tramos que `buscarConLimites` devuelve YA ordenados y sin solapes (RE2 no
// devuelve solapes).
func sustituir(texto string, locs [][]int, marca string) string {
	if len(locs) == 0 {
		return texto
	}
	var b strings.Builder
	b.Grow(len(texto))
	prev := 0
	for _, loc := range locs {
		b.WriteString(texto[prev:loc[0]])
		b.WriteString(marca)
		prev = loc[1]
	}
	b.WriteString(texto[prev:])
	return b.String()
}

func hallazgos(texto string, locs [][]int, clase Clase) []Hallazgo {
	out := make([]Hallazgo, 0, len(locs))
	for _, loc := range locs {
		out = append(out, Hallazgo{Clase: clase, Texto: texto[loc[0]:loc[1]], Ini: loc[0], Fin: loc[1]})
	}
	return out
}

func digitos(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}
