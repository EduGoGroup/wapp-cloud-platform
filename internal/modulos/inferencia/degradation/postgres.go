// Porta internal/degradation/postgres.go @ ebf4eb7

package degradation

import (
	"context"
	"database/sql"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Postgres es la implementación real de Store sobre database/sql (mismo estilo
// que tenantllm.Postgres: SQL raw con placeholders $1..$n, sin ORM), contra
// public.owner_degradation_notices (migración 0075).
//
// Las dos sentencias se portan BYTE A BYTE del paquete viejo —su test afirma el
// texto exacto de cada una contra constantes escritas aparte— y no se «mejoran»
// al portar. Que ese SQL haga en un Postgres de verdad lo que el puerto promete
// lo prueba degradationhelpertest.Contrato en los procesos de F9.
//
// 🔴 NO LLEVA FieldCipher, al revés que tenantllm.Postgres, y esa ausencia es una
// afirmación: en esta tabla NO HAY NADA QUE CIFRAR porque no hay nada sensible
// (INV-6). Si algún día alguien tiene que añadir un cifrador aquí, lo que ha
// pasado es que se ha colado una columna que no debía existir.
type Postgres struct{}

// NewPostgres construye el store sobre esa conexión. No la abre ni la comprueba:
// construir no emite ninguna sentencia.
func NewPostgres(db *sql.DB) *Postgres {
	panic(pendiente.Implementar("degradation.NewPostgres"))
}

// Save implementa Store.Save con UNA sentencia, un `INSERT … ON CONFLICT
// (tenant_id, reason, via, window_start) DO UPDATE … RETURNING`, y esa sentencia
// ES EL DEDUPE, que vive en la base y no en Go a propósito.
//
// 🔴 POR QUÉ LA BASE Y NO EL CÓDIGO: la alternativa —«SELECT si hay aviso
// reciente; si no, INSERT»— tiene una carrera que dos réplicas del servidor
// pierden siempre: las dos leen «no hay» y las dos escriben. Aquí no hay lectura
// previa: se INSERTA, y si choca contra ux_owner_degradation_notices_ventana la
// propia sentencia colapsa sobre la fila que ya estaba. Una sola ida a la base,
// sin transacción explícita, sin ventana de carrera. El arbitrio se infiere por
// las cuatro columnas del índice único: ese índice y el ON CONFLICT TIENEN que
// decir lo mismo, o el INSERT falla en tiempo de EJECUCIÓN, no al compilar.
//
// Lo que la sentencia hace con cada columna:
//
//   - el INSERT lleva seis argumentos, en este orden: tenant, motivo (como
//     cadena), vía, inicio y fin de la ventana y el último visto, los tres
//     instantes EN UTC. `occurrences` nace en 1 (literal).
//   - `created_at` SE ESCRIBE EXPLÍCITO CON EL MISMO $6 QUE `last_seen_at`, y no
//     se deja en el `DEFAULT now()` de la 0075 (barrido del 2026-08-23): el
//     nacimiento del aviso es el instante del FALLO, no el de la escritura. Con
//     el default eran DOS relojes —el de PostgreSQL y el del llamante— para dos
//     columnas que se comparan entre sí, y un `at` anterior al ahora de la base
//     dejaba una fila con `last_seen_at` ANTERIOR a `created_at`: un aviso que
//     dice haber visto su último fallo antes de existir.
//   - al colapsar, el DO UPDATE toca DOS columnas y ninguna más: `occurrences`
//     sube en uno y `last_seen_at` se pisa con GREATEST y NO con asignación
//     directa, porque dos réplicas pueden escribir fuera de orden y un aviso no
//     debe RETROCEDER en el tiempo. `created_at` NO se pisa: el aviso nació
//     cuando nació. Tampoco `window_end` ni `read_at`.
//   - devuelve id, occurrences, created_at y last_seen_at de la fila que quedó.
//
// creado = (occurrences == 1): el aviso nació con este fallo. 🔴 Se lee
// `occurrences` y no el truco de `(xmax = 0)`: `occurrences = 1` significa, por
// construcción de esta tabla, «este era el primer fallo de la ventana», que es
// EXACTAMENTE la pregunta que el llamante hace.
//
// Un LastSeenAt cero se sustituye por el FIN DE LA VENTANA —y no por time.Now()
// a propósito—: una fila sin último-visto no sabría decir cuánto lleva durando la
// degradación, y así queda COHERENTE con el bucket que la produjo y el store no
// depende del reloj. Los campos ID, Occurrences, ReadAt y CreatedAt de n no
// viajan.
//
// 🔴 NO VALIDA EL VOCABULARIO, y no es un olvido: quien custodia el enum cerrado
// es Notifier.Record, ANTES de llegar aquí, y el CHECK de la 0075 detrás como red.
// Si esta capa validara también, el test que demuestra «motivo sano ⇒ cero filas»
// dejaría de demostrar nada sobre el escritor. Un motivo o una vía cualquiera
// llegan al driver tal cual.
//
// Un fallo —del driver, o una sentencia que no devuelve fila— vuelve envuelto como
// "degradation: escribir aviso de <tenant> (<motivo>/<vía>): …", con creado=false.
// El motivo y la vía SÍ entran en el mensaje —son vocabulario cerrado de wApp, no
// dato del cliente— y el tenant también (INV-6 protege el contenido del cliente,
// no los identificadores internos).
func (p *Postgres) Save(ctx context.Context, n Notice) (bool, error) {
	panic(pendiente.Implementar("degradation.Postgres.Save"))
}

// List implementa Store.List con UNA sentencia: las diez columnas de los avisos
// de UN tenant (`WHERE tenant_id = $1`), con cuatro argumentos: tenant,
// SoloSinLeer, límite y desplazamiento.
//
// El filtro «sin leer» va como predicado parametrizado y no como dos consultas
// distintas: `NOT $2::boolean` es TRUE cuando no se filtra, así que el WHERE
// entero se reduce al tenant y el planificador usa idx_…_reciente; cuando sí se
// filtra, `read_at IS NULL` lo lleva al índice PARCIAL idx_…_sin_leer. Dos
// caminos, una sentencia, ninguna concatenación de SQL.
//
// EL ORDEN LLEVA DESEMPATE (`window_start DESC, created_at DESC, id`) y no solo
// la ventana: dentro de la misma puede haber hasta dieciséis filas —ocho motivos
// por dos vías— y sin un criterio total dos páginas consecutivas podrían repetir
// o saltar una fila. Un orden no determinista con LIMIT/OFFSET es una paginación
// que miente, y miente poco y de vez en cuando, que es la peor forma.
//
// La página se acota ANTES del SQL: Limit <= 0 ⇒ 50; Limit > 200 ⇒ 200, RECORTADO
// en silencio (al revés que en eventstelemetry, que devuelve 422: allí el
// consumidor es un integrador que pagina con cursor; aquí es una pantalla que
// enseña avisos, y devolverle un error en vez de 200 avisos por pedir 500 la deja
// en blanco por un detalle que no le importa); Offset < 0 ⇒ 0.
//
// El mapeo de cada fila:
//
//   - `read_at` NULL ⇒ ReadAt en el instante cero ⇒ Notice.Leida() false. Es la
//     única columna que admite NULL, y esa traducción vive aquí y en ningún otro
//     sitio.
//   - el motivo se convierte al tipo cerrado SIN validar: lo que hay en la base
//     ya pasó el CHECK, y una fila que la base admitió no puede desaparecer de
//     una lectura porque este código no la reconozca. Si algún día se AÑADE un
//     motivo a la migración y no aquí, la lista lo enseña igual y Reason.Valid()
//     dirá false sobre él — que es la señal correcta, no un aviso perdido.
//
// Devuelve una lista NO-nil aunque esté vacía: quien la serialice tiene que
// producir `[]` y no `null`, y hacerlo aquí evita que cada llamante se acuerde.
//
// Los fallos vuelven envueltos, con lista nil: el de la consulta, como
// "degradation: listar avisos de <tenant>: …"; una fila ilegible, como
// "degradation: leer aviso de <tenant>: …"; uno a mitad del recorrido, como
// "degradation: recorrer avisos de <tenant>: …"; y el del cierre de las filas,
// cuando no hay otro en curso, como "degradation: cerrar filas de avisos de
// <tenant>: …" (un Close que falla después de haber leído filas significa que la
// lectura pudo quedarse a medias).
func (p *Postgres) List(ctx context.Context, tenantID string, f ListFilter) ([]Notice, error) {
	panic(pendiente.Implementar("degradation.Postgres.List"))
}
