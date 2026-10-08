// Porta internal/integrations/outbox_stats.go @ 36d5a04

package integrations

import "time"

// outbox_stats.go lleva el TIPO de la segunda cosa que la superficie HTTP necesita
// y el Store no tenía (la primera es la huella del secreto, crud.go): mirar la cola
// por encima SIN abrirla. La consulta que lo rellena, (*Postgres).CountOutbox, vivía
// aquí en el fichero viejo (outbox_stats.go:69) y nace en postgres.go (D-F6-6).
//
// Vive fuera de Store a propósito, igual que SecretFingerprint. Store es lo que
// necesitan el sink que encola y el worker que entrega; ninguno de los dos cuenta
// nada. Esto es material de OBSERVACIÓN, y meterlo en el mismo puerto obligaría a
// los dobles del worker a implementar una consulta que ese worker nunca hace.

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
//
// El valor cero es «la cola está vacía», y el tipo es comparable: dos fotos con
// los mismos cinco campos son iguales con ==.
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
