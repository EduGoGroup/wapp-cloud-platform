package integrations

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// outbox_stats.go añade la SEGUNDA cosa que la superficie HTTP necesita y el
// Store no tenía (la primera es la huella del secreto, crud.go): mirar la cola
// por encima SIN abrirla.
//
// Vive fuera de Store a propósito, igual que SecretFingerprint. Store es lo que
// necesitan el sink que encola y el worker que entrega; ninguno de los dos cuenta
// nada. Esto es material de OBSERVACIÓN, y meterlo en el mismo puerto obligaría a
// los fakes del worker a implementar una consulta que ese worker nunca hace.

// OutboxCounts es el estado agregado de la cola de entregas de UN tenant: cuántas
// filas hay en cada uno de los cuatro estados del CHECK de la migración 0046, más
// la antigüedad de lo más viejo que sigue esperando.
//
// SON CONTADORES Y UNA MARCA DE TIEMPO. Aquí no viaja ni un payload ni un
// last_error: el payload de las entregadas está vacío a propósito (migración
// 0050) y el de las `dead` es lo único que queda del intento fallido — sacarlo
// por una puerta de «cuántas hay» sería reintroducir por observabilidad lo que la
// purga quitó por diseño.
//
// OldestPendingAt ES PARTE DE LA RESPUESTA, y no un adorno: los cuatro contadores
// no distinguen los dos mundos que más importa distinguir. «3 pendientes» es
// normal si se encolaron hace dos segundos y el worker las coge en el siguiente
// poll, y es una cola rota si llevan seis horas ahí porque el endpoint del cliente
// devuelve 500. El número es EL MISMO en los dos casos; la antigüedad no. Se toma
// de created_at y no de next_attempt_at porque la pregunta es «¿desde cuándo
// espera algo?», no «¿cuándo se reintenta?» — que en una fila con backoff está en
// el futuro y no dice nada del retraso acumulado.
type OutboxCounts struct {
	Pending    int64
	Delivering int64
	Delivered  int64
	Dead       int64
	// OldestPendingAt es el created_at de la entrega en cola más antigua. CERO
	// (time.Time{}) cuando no hay ninguna pendiente: no hay «la más vieja de
	// ninguna», y esa ausencia no se disfraza de fecha.
	OldestPendingAt time.Time
}

// CountOutbox devuelve el estado agregado de la cola del tenant.
//
// UNA sola consulta con agregados condicionales, no cinco: los cuatro contadores
// y la antigüedad salen del MISMO recorrido de las filas del tenant, así que la
// foto es coherente por construcción. Con cinco consultas los números podrían
// venir de instantes distintos y sumar mal —el worker mueve filas entre estados
// mientras se pregunta— y la pantalla enseñaría una cola que nunca existió.
//
// El filtro por tenant_id lo sirve webhook_outbox_tenant_idx (0046). El escaneo
// crece con las `delivered` acumuladas, y 🔴 NO HAY RETENCIÓN POR ANTIGÜEDAD EN
// CAMINO: el Plan 046 la descartó el 2026-08-20 (D-046.16, ADR-0043). Queda como
// deuda de RENDIMIENTO, sin dueño y sin fecha — no de privacidad: las `delivered`
// ya vacían su payload desde la 0050, así que lo que se acumula son filas de
// metadatos, no contenido. A las escalas de hoy no es un problema (la tabla tiene
// UNA fila) y cuando lo sea lo arregla una purga que habrá que escribir, no un
// índice más.
//
// SIN ErrNoRows POSIBLE: un agregado sin GROUP BY devuelve SIEMPRE una fila, con
// ceros si el tenant no tiene ninguna entrega. Por eso «este tenant nunca encoló
// nada» y «este tenant tiene la cola vacía» responden lo mismo, que es lo
// correcto: las dos cosas significan que no hay nada esperando.
func (p *Postgres) CountOutbox(ctx context.Context, tenantID string) (OutboxCounts, error) {
	var (
		counts OutboxCounts
		oldest sql.NullTime
	)
	err := p.db.QueryRowContext(ctx, `
		SELECT
		    COUNT(*) FILTER (WHERE status = $2) AS pending,
		    COUNT(*) FILTER (WHERE status = $3) AS delivering,
		    COUNT(*) FILTER (WHERE status = $4) AS delivered,
		    COUNT(*) FILTER (WHERE status = $5) AS dead,
		    MIN(created_at) FILTER (WHERE status = $2) AS oldest_pending_at
		FROM public.webhook_outbox
		WHERE tenant_id = $1
	`, tenantID, StatusPending, StatusDelivering, StatusDelivered, StatusDead).
		Scan(&counts.Pending, &counts.Delivering, &counts.Delivered, &counts.Dead, &oldest)
	if err != nil {
		return OutboxCounts{}, fmt.Errorf("integrations: contar la cola de entregas: %w", err)
	}
	counts.OldestPendingAt = oldest.Time // NULL ⇒ cero (no hay nada en cola)
	return counts, nil
}
