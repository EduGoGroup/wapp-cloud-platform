package degradation

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Techos de la lectura. La bandeja de avisos de un tenant es corta por
// construcción (ver el techo del dedupe en la 0075), así que estos números no son
// una defensa contra el volumen: son la defensa contra un cliente roto que pida
// «todo» y contra un teléfono que no pagina.
const (
	// defaultListLimit es la página sin `limit` en la query.
	defaultListLimit = 50
	// maxListLimit es el tope duro. Por encima se RECORTA en silencio, al revés
	// que en eventstelemetry (que devuelve 422). La diferencia es deliberada: allí
	// el consumidor es un integrador que pagina con cursor y necesita enterarse de
	// que su página no cabe; aquí el consumidor es una pantalla que enseña avisos,
	// y devolverle un error en vez de 200 avisos por pedir 500 la deja en blanco
	// por un detalle que no le importa.
	maxListLimit = 200
)

// Postgres es la implementación real de Store sobre database/sql (mismo estilo
// que internal/tenantllm/postgres.go y internal/integrations/postgres.go: SQL
// raw con placeholders $1..$n, sin ORM).
//
// 🔴 NO LLEVA FieldCipher, al revés que tenantllm.Postgres, y esa ausencia es una
// afirmación: en esta tabla NO HAY NADA QUE CIFRAR porque no hay nada sensible
// (INV-6). Si algún día alguien tiene que añadir un cifrador aquí, lo que ha
// pasado es que se ha colado una columna que no debía existir.
type Postgres struct {
	db *sql.DB
}

// NewPostgres construye el store.
func NewPostgres(db *sql.DB) *Postgres { return &Postgres{db: db} }

// saveSQL es EL DEDUPE, y vive aquí y no en Go a propósito.
//
// 🔴 POR QUÉ LA BASE Y NO EL CÓDIGO: la alternativa —«SELECT si hay aviso
// reciente; si no, INSERT»— tiene una carrera que dos réplicas del servidor
// pierden siempre: las dos leen «no hay» y las dos escriben. Aquí no hay lectura
// previa: se INSERTA, y si choca contra ux_owner_degradation_notices_ventana la
// propia sentencia colapsa sobre la fila que ya estaba. Una sola ida a la base,
// sin transacción explícita, sin ventana de carrera.
//
// EL ARBITRIO SE INFIERE por las cuatro columnas del índice único, y por eso ese
// índice y esta lista TIENEN que decir lo mismo: si alguien cambia una columna en
// la 0075 sin cambiarla aquí, este INSERT falla en tiempo de EJECUCIÓN («no
// unique or exclusion constraint matching the ON CONFLICT specification»), no al
// compilar. El test de integración es lo único que lo atrapa antes de UAT.
//
// `AS n` NO ES COSMÉTICO: sin alias, el `SET` tendría que referirse a la tabla
// destino por su nombre completo y la sentencia se vuelve ilegible; con alias, el
// contraste `n.` (lo que hay) contra `EXCLUDED.` (lo que se intentaba meter) se
// lee de un vistazo, que es exactamente la distinción que hace el dedupe.
//
// `created_at` NO se pisa: el aviso nació cuando nació, aunque siga acumulando
// fallos. `last_seen_at` se pisa con GREATEST y NO con asignación directa, porque
// dos réplicas pueden escribir fuera de orden y un aviso no debe RETROCEDER en el
// tiempo.
//
// 🔧 `created_at` SE ESCRIBE EXPLÍCITO CON EL MISMO $6 QUE `last_seen_at`, y no se
// deja en el `DEFAULT now()` de la 0075 (barrido del CLI, 2026-08-23). El contrato
// del campo —«CreatedAt es el nacimiento del aviso (el primer fallo de la
// ventana)», degradation.go— es el instante del FALLO, no el de la escritura, y el
// default lo sacaba del reloj de PostgreSQL mientras `last_seen_at` salía del
// reloj del llamante: DOS relojes para dos columnas que se comparan entre sí. Con
// un `at` anterior al ahora de la base —un bucket recuperado, un reproceso, o
// simplemente un test con fecha fija— la fila nacía con `last_seen_at` ANTERIOR a
// `created_at`, que es un aviso que dice haber visto su último fallo antes de
// existir. Lo cazó TestDedupeNFallosUnaFila contra Postgres real; con el fake no
// se veía, porque el fake no tiene el otro reloj.
//
// 🔴 SE DEVUELVE `occurrences` PARA SABER SI NACIÓ, y no el truco de `(xmax = 0)`
// que se usa habitualmente para distinguir INSERT de UPDATE. `occurrences = 1`
// significa, por construcción de esta tabla, «este era el primer fallo de la
// ventana», que es EXACTAMENTE la pregunta que el llamante hace — y se lee sin
// saber qué es xmax, sin depender de un operador de columna de sistema y sin la
// letra pequeña de xmax con subtransacciones.
const saveSQL = `
	INSERT INTO public.owner_degradation_notices AS n
	       (tenant_id, reason, via, window_start, window_end, occurrences, created_at, last_seen_at)
	VALUES ($1, $2, $3, $4, $5, 1, $6, $6)
	ON CONFLICT (tenant_id, reason, via, window_start) DO UPDATE
	   SET occurrences  = n.occurrences + 1,
	       last_seen_at = GREATEST(n.last_seen_at, EXCLUDED.last_seen_at)
	RETURNING n.id, n.occurrences, n.created_at, n.last_seen_at`

// Save implementa Store.Save.
//
// creado = (occurrences == 1): el aviso nació con este fallo. Ver saveSQL.
//
// 🔴 NO VALIDA EL VOCABULARIO, y no es un olvido: quien custodia el enum cerrado
// es Notifier.Record, ANTES de llegar aquí, y el CHECK de la 0075 detrás como red.
// Si esta capa validara también, el test que demuestra «motivo sano ⇒ cero filas»
// dejaría de demostrar nada sobre el escritor —pasaría por el motivo equivocado— y
// la guarda de verdad quedaría sin custodia.
func (p *Postgres) Save(ctx context.Context, n Notice) (bool, error) {
	lastSeen := n.LastSeenAt
	if lastSeen.IsZero() {
		// Un aviso sin instante de último-visto sería una fila que no sabe decir
		// cuánto lleva durando la degradación. Se rellena con el fin de la ventana
		// y no con time.Now() a propósito: mantiene la fila COHERENTE con el bucket
		// que la produjo y hace que el store no dependa del reloj (dos ejecuciones
		// del mismo test dan el mismo valor).
		lastSeen = n.WindowEnd
	}
	var id string
	var occurrences int
	var createdAt, lastSeenAt time.Time
	err := p.db.QueryRowContext(ctx, saveSQL,
		n.TenantID, string(n.Reason), n.Via,
		n.WindowStart.UTC(), n.WindowEnd.UTC(), lastSeen.UTC(),
	).Scan(&id, &occurrences, &createdAt, &lastSeenAt)
	if err != nil {
		// El motivo y la vía SÍ entran en el mensaje —son vocabulario cerrado de
		// wApp, no dato del cliente— y el tenant también (INV-6 protege el
		// contenido del cliente, no los identificadores internos). Lo que NO entra
		// es nada más: aquí no hay nada más que pudiera entrar.
		return false, fmt.Errorf("degradation: escribir aviso de %s (%s/%s): %w",
			n.TenantID, n.Reason, n.Via, err)
	}
	return occurrences == 1, nil
}

// listSQL lee los avisos de UN tenant, el más reciente primero.
//
// El filtro «sin leer» va como predicado parametrizado y no como dos consultas
// distintas: `NOT $2::boolean` es TRUE cuando no se filtra, así que el WHERE
// entero se reduce al tenant y el planificador usa idx_…_reciente; cuando sí se
// filtra, `read_at IS NULL` lo lleva al índice PARCIAL idx_…_sin_leer. Dos
// caminos, una sentencia, ninguna concatenación de SQL.
//
// EL ORDEN LLEVA DESEMPATE (`created_at DESC, id`) y no solo `window_start DESC`:
// dentro de la misma ventana puede haber hasta dieciséis filas —ocho motivos por dos
// vías— y sin un criterio total dos páginas consecutivas podrían repetir o saltar
// una fila. Un orden no determinista con LIMIT/OFFSET es una paginación que
// miente, y miente poco y de vez en cuando, que es la peor forma.
const listSQL = `
	SELECT id, tenant_id, reason, via, window_start, window_end,
	       occurrences, read_at, created_at, last_seen_at
	  FROM public.owner_degradation_notices
	 WHERE tenant_id = $1
	   AND (NOT $2::boolean OR read_at IS NULL)
	 ORDER BY window_start DESC, created_at DESC, id
	 LIMIT $3 OFFSET $4`

// List implementa Store.List. Devuelve una lista NO-nil aunque esté vacía: quien
// la serialice tiene que producir `[]` y no `null`, y hacerlo aquí evita que cada
// llamante se acuerde.
//
// Los retornos van NOMBRADOS para que el `defer` pueda promover el fallo del
// Close cuando no haya otro error en curso — mismo patrón que
// intakes/postgres.go:79-82. No es cosmético con `errcheck check-blank`: un
// `_ = rows.Close()` no eximiría del lint, y un Close que falla después de haber
// leído filas significa que la lectura pudo quedarse a medias.
func (p *Postgres) List(ctx context.Context, tenantID string, f ListFilter) (out []Notice, err error) {
	limit, offset := f.acotar()
	rows, err := p.db.QueryContext(ctx, listSQL, tenantID, f.SoloSinLeer, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("degradation: listar avisos de %s: %w", tenantID, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, err = nil, fmt.Errorf("degradation: cerrar filas de avisos de %s: %w", tenantID, cerr)
		}
	}()

	out = make([]Notice, 0, limit)
	for rows.Next() {
		n, serr := escanear(rows)
		if serr != nil {
			return nil, fmt.Errorf("degradation: leer aviso de %s: %w", tenantID, serr)
		}
		out = append(out, n)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("degradation: recorrer avisos de %s: %w", tenantID, rerr)
	}
	return out, nil
}

// escanear proyecta UNA fila. Se extrae del bucle porque gocyclo mide la función
// madre y porque el único NULL de la tabla —read_at— necesita su NullTime, y esa
// traducción («NULL significa sin leer») merece vivir en un sitio nombrado.
func escanear(rows *sql.Rows) (Notice, error) {
	var n Notice
	var reason string
	var readAt sql.NullTime
	if err := rows.Scan(&n.ID, &n.TenantID, &reason, &n.Via,
		&n.WindowStart, &n.WindowEnd, &n.Occurrences, &readAt,
		&n.CreatedAt, &n.LastSeenAt); err != nil {
		return Notice{}, err
	}
	// El motivo se convierte al tipo cerrado SIN validar: lo que hay en la base ya
	// pasó el CHECK, y una fila que la base admitió no puede desaparecer de una
	// lectura porque este código no la reconozca. Si algún día se AÑADE un motivo
	// a la migración y no aquí, la lista lo enseña igual y `Reason.Valid()` dirá false
	// sobre él — que es la señal correcta, no un aviso perdido.
	n.Reason = Reason(reason)
	// NULL ⇒ tiempo cero ⇒ Notice.Leida() false. La traducción vive aquí y en
	// ningún otro sitio.
	n.ReadAt = readAt.Time
	return n, nil
}

// acotar resuelve los techos de la página. Es un método del filtro y no código
// suelto en List para que el mismo criterio valga si mañana hay un segundo
// llamante (la consola de plataforma, por ejemplo).
func (f ListFilter) acotar() (limit, offset int) {
	limit = f.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	offset = f.Offset
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
