// Porta internal/intakes/quotetext/precios.go @ 36d5a04

package quotetext

import "github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"

// ════════════════════════════════════════════════════════════════════════════
// 🔴 EL VERIFICADOR DE PRECIOS — INV-2: EL LLM NUNCA CALCULA PRECIOS
// ════════════════════════════════════════════════════════════════════════════
//
// El prompt PIDE que los importes se copien del borrador; esto lo GARANTIZA. Y lo que
// hay que garantizar no es «que no invente números»: un texto puede usar SOLO importes
// legítimos y aun así mandarle al cliente un precio que no es el suyo (dos precios
// intercambiados, un cargo nuevo con un importe reutilizado, una repetición). Por eso
// la regla no pregunta si el importe EXISTE, sino si está DONDE LE TOCA y las veces
// que le toca.
//
// # LA REGLA, ENTERA
//
// Del Draft sale una SECUENCIA ESPERADA de importes (ExpectedSequence). De ella se
// derivan ESPERADOS (su conjunto), PERMITIDOS (ESPERADOS más los números que ya viven
// en las etiquetas, las personalizaciones y las cantidades del borrador) y el TECHO =
// máx(ESPERADOS).
//
// Del texto se extraen todos los números —dígitos ASCII con puntos y comas dentro; el
// guion NO forma parte, así que «10-12 porciones» son dos números— y cada uno es:
//
//	MARCADO — lleva marca de dinero pegada: «$» delante, o detrás una palabra de
//	          moneda sin distinguir mayúsculas («peso», «pesos», «clp», «usd», «bs»,
//	          «soles», «dolar», «dólar», «dolares», «dólares»; «sol», en singular,
//	          NO lo es). Entre la marca y el número cabe, como mucho, UN blanco
//	          ASCII (espacio, tabulador, salto): con un espacio de no separación o
//	          con dos espacios la marca no cuenta. La palabra delante («USD 2950»)
//	          y el símbolo detrás («2950 $») tampoco.
//	DESNUDO — cualquier otro número.
//
// El texto se ACEPTA si y solo si:
//
//	C3  todo importe MARCADO está en ESPERADOS                    (no se inventa nada)
//	C4  ningún número DESNUDO por encima del TECHO es ajeno a PERMITIDOS
//	C1  cada `unit_price` > 0 aparece MARCADO en el texto          (cobertura)
//	C2  el total aparece MARCADO en el texto                       (cobertura)
//	C5  la SECUENCIA de importes marcados del texto es EXACTAMENTE la esperada
//
// C1 y C2 están contenidas en C5 y se comprueban antes igualmente: dan el diagnóstico
// útil («falta el precio de la línea 2») donde C5 solo diría «no cuadran».
//
// C5 es una secuencia y no un recuento porque contar apariciones NO cierra el
// intercambio de dos precios. El precio que se paga: un texto correcto que enumere las
// líneas en otro orden que el borrador se rechaza. Es conservador a propósito.
//
// C4 mira el TECHO y no el suelo: con una galleta de $2 en el carrito, un listón en el
// importe más barato rechazaba «te llamo en 3 días». Por debajo del techo, un desnudo
// no se juzga. 🔴 Eso deja un agujero DECLARADO: «el total sería 3000» —sin marca y por
// debajo del techo— pasa.
//
// # CÓMO SE LEE UN NÚMERO DEL TEXTO
//
//   - Los separadores del final se recortan primero («$1234,50.» al acabar una frase
//     es 1234,50, no 123450).
//   - Con punto Y coma, el ÚLTIMO de los dos manda como decimal y el otro es de miles
//     («2.950,00» y «2,950.00» son 2950).
//   - Con uno solo: es DECIMAL si aparece una vez y le siguen exactamente uno o dos
//     dígitos («2100,50» ⇒ 2100,5); en cualquier otro caso es de MILES y se borra
//     («2.100» ⇒ 2100, «1.234.567» ⇒ 1234567, y también un separador repetido:
//     «2..100» ⇒ 2100).
//   - Todo —los importes del borrador y los números leídos— se redondea a CÉNTIMOS
//     antes de compararse, y dos importes son el mismo si difieren en menos de medio
//     céntimo. Es la precisión con la que Amount escribe.
//   - Un número que no se puede leer (desborda el float64, o deja de ser finito al
//     pasarlo a céntimos) NO se salta: aborta la verificación con
//     ReasonUnreadableNumber. Nunca un pánico ni un cero silencioso.
//
// CONSERVADOR ANTE LA DUDA: un `$2.10` de un modelo que quería decir 2100 se lee 2,10,
// no estará en ESPERADOS y el texto se rechaza. No hay ninguna lectura del texto bajo
// la cual se le mande al cliente un importe que no salió de las líneas.
// ════════════════════════════════════════════════════════════════════════════

// MaxTextRunes acota la salida del modelo: 4000 runas. No es una regla de negocio: es
// la cota que impide que una respuesta desbocada —un modelo en bucle repitiendo la
// lista— se convierta en un mensaje de WhatsApp de un megabyte.
//
// Era `MaxRunasTexto` en el paquete viejo.
const MaxTextRunes = 4000

// Motivos por los que un texto NO se acepta. Son un vocabulario CERRADO porque salen
// por el log y por la API (`fallback_reason`): los VALORES no cambian aunque el
// identificador esté en inglés. Eran los `Motivo…` del paquete viejo.
const (
	// ReasonDraftWithoutAmounts — el borrador no tiene ni un importe positivo, así
	// que no hay nada que verificar. Pasa con un pedido cuyas líneas están todas por
	// confirmar. Era `MotivoSinImportes`.
	ReasonDraftWithoutAmounts = "borrador_sin_importes"
	// ReasonUnreadableText — la salida no es texto utilizable: no es UTF-8, trae
	// caracteres de control, viene vacía o se pasa de MaxTextRunes. Era
	// `MotivoTextoIlegible`.
	ReasonUnreadableText = "texto_ilegible"
	// ReasonUnreadableNumber — hay un número en el texto que no se puede leer como
	// número. ES UN ERROR DE DATO y no un pánico. Era `MotivoNumeroIlegible`.
	ReasonUnreadableNumber = "numero_ilegible"
	// ReasonTextWithoutAmounts — el texto no trae NI UN importe marcado. No dice los
	// precios, así que no es una cotización. Era `MotivoSinImportesEnTexto`.
	ReasonTextWithoutAmounts = "texto_sin_importes"
	// ReasonMissingUnitPrice — falta en el texto el precio unitario de alguna línea
	// (C1). Era `MotivoFaltaUnitario`.
	ReasonMissingUnitPrice = "falta_precio_de_linea"
	// ReasonMissingTotal — falta en el texto el total (C2). Era `MotivoFaltaTotal`.
	ReasonMissingTotal = "falta_total"
	// ReasonForeignAmount — el texto trae un importe que no sale de ninguna línea
	// (C3). Era `MotivoImporteAjeno`.
	ReasonForeignAmount = "importe_ajeno"
	// ReasonForeignNumber — el texto trae un número sin marca de moneda, por encima
	// de todo lo que cuesta el pedido, que tampoco sale del borrador (C4). Era
	// `MotivoNumeroAjeno`.
	ReasonForeignNumber = "numero_ajeno"
	// ReasonAmountsOutOfPlace — todos los importes del texto salen del borrador,
	// pero no están donde les toca: sobran, faltan o van en otro orden (C5). Es el
	// motivo del intercambio, del cargo inventado con importe reutilizado y de la
	// repetición. Era `MotivoImportesFueraDeSitio`.
	ReasonAmountsOutOfPlace = "importes_fuera_de_sitio"
)

// Verdict es el resultado de verificar un texto contra su borrador.
//
// Era `Veredicto` en el paquete viejo.
type Verdict struct {
	// OK dice si el texto se puede mandar.
	OK bool
	// Reason es uno de los Reason… de este fichero cuando OK es falso, y vacío
	// cuando es cierto. Era `Motivo`.
	Reason string
	// Detail amplía el motivo PARA EL LOG. Vacío cuando OK es cierto; nunca vacío en
	// un rechazo. Era `Detalle`.
	//
	// 🔴 NUNCA LLEVA UN FRAGMENTO DEL TEXTO. Solo números y nombres de campo. El
	// texto es una cotización redactada, y lo que un modelo escribe no es material
	// que este código deba copiar a un log (misma regla que P2/P3/P4 con la
	// evidencia, ADR-0034 / INV-6).
	Detail string
}

// ValidateOutput comprueba que lo que devolvió el modelo es texto utilizable, ANTES
// de buscarle números o de dárselo a nadie. Devuelve nil si lo es y, si no, un error
// cuyo texto empieza por ReasonUnreadableText, en este orden de comprobación:
//
//   - no es UTF-8 válido ⇒ `texto_ilegible: la salida no es UTF-8`;
//   - está vacío o solo tiene blancos ⇒ `texto_ilegible: la salida está vacía`;
//   - pasa de MaxTextRunes RUNAS (no bytes; justo MaxTextRunes es válido) ⇒
//     `texto_ilegible: la salida tiene <n> runas y el tope es 4000`;
//   - trae un carácter de control (categoría Cc: el nulo, ESC, DEL, U+0085…) ⇒
//     `texto_ilegible: la salida trae un carácter de control (U+XXXX)`, con el
//     PRIMERO que aparezca en hexadecimal de al menos cuatro cifras.
//
// El salto de línea, el retorno y el tabulador SÍ pasan: son el formato del mensaje.
//
// 🔴 VA PRIMERO Y NO ES CEREMONIA. Lo que sale de aquí acaba en un `rendered_text`
// —una columna TEXT de Postgres, que rechaza `0x00` con SQLSTATE 22021— y en un
// mensaje de WhatsApp.
//
// Era `ValidarSalida` en el paquete viejo.
func ValidateOutput(text string) error {
	panic(pendiente.Implementar("quotetext.ValidateOutput"))
}

// Verify aplica C1–C5 (ver la cabecera del fichero) y dice si el texto se puede
// mandar: Verdict{OK: true} sin motivo ni detalle, o un rechazo con su Reason y un
// Detail que nunca cita el texto. Es PURA: sin BD, sin reloj y sin log.
//
// El orden de las comprobaciones es contrato, porque decide qué motivo sale cuando
// fallan varias. Gana la PRIMERA que falle:
//
//  1. ValidateOutput ⇒ ReasonUnreadableText, con el texto de ese error como Detail;
//  2. ExpectedSequence vacía ⇒ ReasonDraftWithoutAmounts, Detail `ninguna línea del
//     borrador tiene importe`;
//  3. un número que no se puede leer ⇒ ReasonUnreadableNumber. Detail
//     `quotetext: número ilegible: <n> caracteres, separadores no interpretables`
//     (n = largo del literal ya limpio; desborda el float64) o `quotetext: número
//     ilegible: el valor no es finito` (deja de ser finito al pasarlo a céntimos).
//     Vale para marcados y desnudos;
//  4. recorriendo los números EN EL ORDEN DEL TEXTO: un marcado fuera de ESPERADOS ⇒
//     ReasonForeignAmount (C3), Detail `el texto trae el importe <Amount> y no sale
//     de ninguna línea`; un desnudo por encima del TECHO y fuera de PERMITIDOS ⇒
//     ReasonForeignNumber (C4), Detail `el texto trae el número <n>, por encima de lo
//     más caro del pedido (<Amount del techo>), y no sale del borrador`;
//  5. ni un importe marcado ⇒ ReasonTextWithoutAmounts, Detail `el texto no dice ni
//     un precio`;
//  6. C1, línea a línea en el orden del borrador (las que no tienen unitario positivo
//     se saltan: no hay precio que exigir, y el que se inventara uno cae por C3) ⇒
//     ReasonMissingUnitPrice, Detail `la línea <i> vale <Amount> y ese importe no
//     está en el texto` (i desde 1);
//  7. C2 ⇒ ReasonMissingTotal, Detail `el total es <Amount> y no está en el texto`;
//  8. C5 ⇒ ReasonAmountsOutOfPlace: si el número de importes marcados no es el de la
//     secuencia, Detail `el texto dice <n> importes y el presupuesto tiene <m>`; si
//     coincide, el primer puesto que difiere, `el importe nº <i> del texto es
//     <Amount> y ahí va <Amount>` (i desde 1).
//
// Consecuencias que son contrato y que los tests viejos fijaron:
//
//   - el texto que produce Render para un borrador con algún importe pasa SIEMPRE
//     (también con decimales de más de dos cifras, cantidades mayores que uno, líneas
//     por confirmar y etiquetas con números mayores que el precio): el respaldo no
//     puede ser peor que lo que respalda;
//   - dos líneas al mismo precio son legítimas si el texto lo dice dos veces; «las
//     dos a $2100» (una sola aparición) se rechaza;
//   - con UNA línea de cantidad uno, su precio y el total son el mismo número y el
//     texto tiene que decirlo DOS veces;
//   - los números que ya estaban en el borrador (etiquetas, personalizaciones,
//     cantidades) se perdonan como desnudos aunque superen el techo; los que no se
//     pueden leer ahí no aportan permiso y no dan error;
//   - una línea barata no convierte fechas, horas ni cantidades en un rechazo.
//
// Era `Verificar` en el paquete viejo.
func Verify(draft Draft, text string) Verdict {
	panic(pendiente.Implementar("quotetext.Verify"))
}

// ExpectedSequence es el orden en que los importes del borrador tienen que aparecer
// en el texto, ya redondeados a céntimos: por cada línea con unitario positivo su
// `unit_price` y, solo si la cantidad es mayor que uno, su `line_total`; y al final,
// el `total` del pedido si es positivo. El `line_total` con cantidad uno NO entra: es
// idéntico al unitario y exigirlo obligaría a escribir el mismo número dos veces
// seguidas. Una línea sin unitario positivo (por confirmar) no aporta nada.
//
// Se exporta porque es la definición de «cotización bien puesta» que comparten el
// verificador y el render: los importes que escribe Render son EXACTAMENTE esta
// secuencia.
//
// Devuelve una lista vacía (largo 0) cuando no hay ni un importe —todas las líneas
// por confirmar, o ninguna línea—, y ese vacío es lo que el llamante traduce a
// ReasonDraftWithoutAmounts. No muta el borrador.
//
// Era `SecuenciaEsperada` en el paquete viejo.
func ExpectedSequence(draft Draft) []float64 {
	panic(pendiente.Implementar("quotetext.ExpectedSequence"))
}
