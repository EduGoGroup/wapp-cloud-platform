// Porta internal/catalogimport/tabular.go @ 3c74b80

package catalogimport

import "github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"

// ============================ lectura de la planilla (D-041.9) ============================

// Este fichero es la mitad que LEE del contrato tabular: template.go emite las
// filas de la planilla canónica y ParseTabular las devuelve al documento del
// contrato. El nombre de las columnas y los separadores de las celdas múltiples
// están declarados UNA vez —las constantes de aquí, la lista de template.go— porque
// dos copias del literal se desincronizan al primer retoque y el síntoma sería una
// planilla que el propio wApp no sabe releer.
//
// LA PLANILLA NO TIENE VALIDADOR PROPIO, y esa es la decisión que gobierna todo lo
// que sigue: este parser traduce filas a CatalogImport y se lo entrega al MISMO
// Validate del JSON. Si la planilla juzgara por su cuenta habría dos definiciones de
// «catálogo correcto» y una de las dos envejecería sin que nadie se enterase.
//
// LO ÚNICO QUE AÑADE EL CAMINO TABULAR ES DÓNDE ESTÁ EL DEFECTO. En una hoja de
// cálculo se busca por FILA: «la categoría 2, artículo 3» es una coordenada del JSON
// que quien llenó la planilla no ha visto nunca. Por eso los defectos que devuelve
// el validador se re-ubican en la fila que produjo cada elemento antes de salir
// (localize), y por eso ImportFieldError tiene Row.
//
// El tabular.go viejo mide 609 líneas y aquí nace partido por tema (05 E-13), solo
// moviendo declaraciones: tabular.go (la entrada, la cabecera, los topes y las
// filas), tabular_cells.go (la mini-sintaxis de las celdas múltiples y el precio) y
// tabular_locate.go (el documento que se arma y la vuelta de cada defecto a su
// fila). Los dos trozos solo llevan auxiliares no exportados, así que nacen en el
// verde (05 E-4); sus tests, que salen de este contrato, nacen ya en rojo.

// ParseTabular traduce las filas de la planilla canónica al documento del contrato y
// lo valida con el MISMO validador del JSON. La primera fila es la CABECERA (los
// nombres de las columnas) y cada fila siguiente es UN artículo con la cabecera de su
// categoría repetida, que es como se filtra y se ordena en una hoja sin cruzar
// pestañas.
//
// Los defectos salen ubicados por FILA, en el número que la hoja enseña en su margen
// izquierdo: la cabecera es la 1 y el primer artículo, la 2.
//
// LEER Y JUZGAR SON DOS PASADAS Y LA SEGUNDA NO SE HACE SI LA PRIMERA ENCUENTRA
// ALGO: no se puede opinar sobre lo que dice un documento que no se ha podido leer, y
// hacerlo produciría defectos inventados —un combo cuyo integrante «no existe» solo
// porque la fila que lo declaraba tenía la celda de categoría rota—. Dentro de cada
// pasada sí se acumula TODO (el mismo criterio de Validate): quien llena una planilla
// no puede permitirse veinte viajes arreglando un defecto por vez.
//
// LO QUE DEVUELVE. El documento ya validado —el mismo catálogo que daría su
// equivalente JSON, con Source.Kind "planilla"— o el cero de CatalogImport y la lista
// de defectos. Cada defecto lleva Row (0 = de la planilla entera), SIN índices de
// categoría ni de artículo, y en Field el nombre de la COLUMNA de la planilla, no el
// campo del JSON ("precio", no "price"; "variantes", no "variants[1].price"). Los
// que juzga el validador conservan su motivo —hablan de «el artículo 2 de la
// categoría …»— y solo cambian de coordenada. Salen en orden de fila. El tope de 200
// defectos y su resumen son los de Validate.
//
// LA CABECERA (fila 1). Las columnas se localizan por NOMBRE, no por posición: sin
// espacios alrededor, sin BOM delante, sin distinguir mayúsculas y sin tildes
// («Descripción» es descripcion). Una columna repetida la gana la primera; una que
// no es del contrato se ignora. Son obligatorias categoria, codigo, sku, nombre y
// precio: si falta alguna, un único defecto (fila 1, campo "cabecera") las NOMBRA y
// no se lee nada más. Las demás pueden faltar: una columna ausente es una columna
// vacía. Sin ninguna fila, o con solo la cabecera, un único defecto de campo
// "planilla".
//
// LAS FILAS. Una fila sin nada escrito (o solo espacios, también Unicode) ni cuenta
// ni estorba; una fila más corta que la cabecera tiene vacías las celdas que le
// faltan. Toda celda se lee sin espacios alrededor. El tope Limits.MaxItems cuenta
// las filas NO vacías y se comprueba antes de leer ninguna: pasado, un único defecto
// de fila 0 y campo "planilla".
//
// LAS CELDAS:
//   - categoria y subcategoria se escriben «codigo|nombre», los dos obligatorios; la
//     subcategoría es opcional y queda declarada en su categoría por el hecho de
//     escribirla. Dos filas que le dan nombres distintos al mismo código son un
//     defecto de la segunda, que cita la fila de la primera;
//   - tags: entradas separadas por «;»; una entrada con «|» es un defecto;
//   - atributos: «clave|valor» por entrada; una clave repetida se queda con el
//     último valor;
//   - variantes: «codigo|nombre|precio» por entrada;
//   - componentes: «sku|cantidad» por entrada, con la cantidad SIEMPRE escrita: un
//     entero de 1 o más (lo que acepta strconv.Atoi: «+2» y «007» valen);
//   - en toda celda múltiple las entradas vacías se descartan (un «;» de más, o
//     repetido, es un desliz y no un defecto) y los campos de cada entrada se leen
//     sin espacios alrededor; un «|» de más o repetido sí rompe la entrada;
//   - precio (el del artículo y el de cada variante): el ÚNICO campo que la lectura
//     juzga por su cuenta, porque una celda ilegible llegaría al validador como 0,
//     que es un precio válido, y el artículo se importaría regalado. Vacío es un
//     defecto; lo demás tiene que leerlo strconv.ParseFloat y no ser NaN ni
//     infinito. Los dígitos no ASCII no son un número; «1e3», «0x1p4» y «1_000» sí
//     lo son para ParseFloat y pasan. Que sea negativo lo juzga el validador.
func ParseTabular(rows [][]string, limits Limits) (CatalogImport, *ImportValidationError) {
	panic(pendiente.Implementar("catalogimport.ParseTabular"))
}
