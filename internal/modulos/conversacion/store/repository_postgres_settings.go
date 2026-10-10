// Porta internal/flujos/store/repository_postgres.go @ c0c0c03
//
// Trozo de repository_postgres.go (05 E-13): public.tenant_settings y
// public.conversation_welcomes. Las reglas comunes del adaptador están en la cabecera
// de repository_postgres.go.
//
// El auxiliar que decodifica buyer_fields es parseBuyerFields, como en el viejo. Su
// regla es del contrato de GetTenantSettings: es TOLERANTE a
// propósito, y un blob ilegible o de otra forma devuelve el checklist VACÍO en vez de
// un error (esa lectura está en el camino de CADA mensaje del cliente, no en un
// endpoint de administración donde un 400 sería útil).
//
// 🔴 MUTANTES (nivel complejo): TouchContact y MarkWelcomed llevan las guardas que la
// suite tiene que morder una a una — el estado PREVIO (no el tocado), el toque que no
// pisa welcomed_at, los tres predicados de la clave, y el compare-and-set
// `IS NOT DISTINCT FROM` sobre el testigo (ni `IS NULL`, ni `=`, ni sin condición).

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// GetTenantSettings devuelve la config del carrito para tenantID desde
// public.tenant_settings (Plan 016 · T0). Si el tenant no tiene fila, devuelve los
// DEFAULTS de DefaultTenantSettings SIN error (design.md §9.E/§9.G).
//
// HAY FILA vs NO HAY FILA SON DOS CAMINOS DISTINTOS, Y ESO ES EL PUNTO (Plan 043 ·
// T1.3). Con fila, los valores se devuelven TAL CUAL vienen de la columna, sin
// sustituir ceros por defaults: `event_inactivity_ttl_seconds = 0` es el override
// explícito «sin vencimiento» de una empresa (D-043.7 / E-6), no un hueco que
// rellenar. Como 0 es además el cero de Go, un `if x == 0 { x = Default }` aquí
// convertiría ese override en 2 h sin que nadie se entere: no lo introduzcas.
//
// Mapeo de la fila (diez columnas, en este orden: page_size, order_ttl_seconds,
// conversation_ttl_seconds, buyer_fields, event_inactivity_ttl_seconds,
// event_history_ttl_seconds, aggregation_window_seconds, aggregation_max_seconds,
// welcome_text, welcome_silence_seconds): las seis columnas `_seconds` salen como
// time.Duration de ese número de segundos; welcome_text sale TAL CUAL, ” incluido (el
// ” significa «el texto de plataforma» y quien lo traduce es el runtime, no este
// método); buyer_fields se decodifica con TOLERANCIA: un blob vacío, ilegible o de
// otra forma da el checklist vacío y el resto de la fila se devuelve igual. TenantID
// es el argumento.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: leer config de tenant: %w"
func (r *PostgresRepository) GetTenantSettings(ctx context.Context, tenantID string) (TenantSettings, error) {
	var (
		pageSize        int
		ttlSecs         int
		convTTLSecs     int
		buyerFields     []byte
		evInactTTLSecs  int
		evHistoryTTLSec int
		aggWindowSecs   int
		aggMaxSecs      int
		welcomeText     string
		welcomeSilence  int
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT page_size, order_ttl_seconds, conversation_ttl_seconds, buyer_fields,
		       event_inactivity_ttl_seconds, event_history_ttl_seconds,
		       aggregation_window_seconds, aggregation_max_seconds,
		       welcome_text, welcome_silence_seconds
		FROM public.tenant_settings
		WHERE tenant_id = $1
	`, tenantID).Scan(&pageSize, &ttlSecs, &convTTLSecs, &buyerFields,
		&evInactTTLSecs, &evHistoryTTLSec, &aggWindowSecs, &aggMaxSecs,
		&welcomeText, &welcomeSilence)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return DefaultTenantSettings(tenantID), nil
	case err != nil:
		return TenantSettings{}, fmt.Errorf("store: leer config de tenant: %w", err)
	}
	return TenantSettings{
		TenantID:           tenantID,
		PageSize:           pageSize,
		OrderTTL:           time.Duration(ttlSecs) * time.Second,
		ConversationTTL:    time.Duration(convTTLSecs) * time.Second,
		BuyerFields:        parseBuyerFields(buyerFields),
		EventInactivityTTL: time.Duration(evInactTTLSecs) * time.Second,
		// 🔴 EventHistoryTTL SE LEE Y NADIE LA OBEDECE (D-046.14, ADR-0043): esta línea
		// es su único destino. No hay poda construida que la consuma, y no la va a
		// haber. Se sigue cargando para no romper el struct ni pedir otra migración.
		EventHistoryTTL: time.Duration(evHistoryTTLSec) * time.Second,
		// AggregationWindow (Plan 044 · T1.2, migración 0072). Se devuelve TAL CUAL,
		// sin sustituir el 0 por el default: aquí el 0 es el override explícito «flush
		// inmediato» (ver el CHECK >= 0 de la 0072), exactamente la misma regla que
		// esta función documenta arriba para EventInactivityTTL. Un
		// `if x == 0 { x = Default }` en esta línea apagaría ese override sin que
		// nadie se entere.
		AggregationWindow: time.Duration(aggWindowSecs) * time.Second,
		// AggregationMax (Plan 044 · T1.8-1, migración 0076). MISMA regla, y por eso va
		// pegada a su hermana: se devuelve TAL CUAL, sin sustituir el 0 por el default.
		// Aquí el 0 es el override explícito «vencido siempre» (CHECK >= 0 de la 0076).
		// Un `if x == 0 { x = Default }` en esta línea apagaría ese override sin que
		// nadie se entere, exactamente igual que lo haría en la línea de arriba.
		AggregationMax: time.Duration(aggMaxSecs) * time.Second,
		// WelcomeText (Plan 044 · T1.8-2, migración 0076). Se devuelve TAL CUAL, '' y
		// todo, porque ese es el contrato de este método y no se le hace una excepción a
		// una columna. ⚠️ PERO EL '' NO ES UN OVERRIDE, al revés que los ceros de las
		// dos columnas de arriba: es el DEFAULT de la columna, lo trae TODA fila
		// preexistente, y significa «el texto de PLATAFORMA», no «sin bienvenida» ni
		// «manda un mensaje vacío». Quien lo traduce es el runtime, en UN solo sitio
		// (welcome.go · textoDeBienvenida), para que los dos caminos —fila con '' y
		// tenant sin fila— acaben en la misma frase sin que este método invente nada.
		WelcomeText: welcomeText,
		// WelcomeSilence (Plan 044 · T1.8-2). MISMA regla que AggregationWindow/Max: se
		// devuelve TAL CUAL, sin sustituir el 0, porque aquí el 0 SÍ es un override
		// explícito («vencido siempre», CHECK >= 0 de la 0076).
		WelcomeSilence: time.Duration(welcomeSilence) * time.Second,
	}, nil
}

// parseBuyerFields decodifica la columna buyer_fields (JSONB, D-041.13). Es
// TOLERANTE a propósito: un blob ilegible o de otra forma devuelve el checklist
// VACÍO en vez de un error, y el carrito sigue vendiendo sin preguntar nada.
//
// La alternativa —propagar el error— dejaría al tenant sin poder cerrar un pedido
// por una config mal escrita a mano, que es exactamente el fallo que no se quiere:
// esta lectura está en el camino de CADA mensaje del cliente (la siembra de
// reanudación), no en un endpoint de administración donde un 400 sería útil.
func parseBuyerFields(raw []byte) []BuyerField {
	if len(raw) == 0 {
		return nil
	}
	var out []BuyerField
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// TouchContact implementa WelcomeStore: registra que el contacto acaba de escribir
// y devuelve el estado que había ANTES de este turno.
//
// # UNA SOLA SENTENCIA, Y POR QUÉ ESO IMPORTA
//
// Corre EN LÍNEA con el mensaje del cliente, en el mismo tramo que ya tiene
// presupuesto escrito para el agregador (D-044.26: una sentencia, cero lecturas,
// cero cripto, cero red). Partirlo en un SELECT y un UPSERT sería duplicar los
// round-trips del camino caliente para responder una pregunta que la BD puede
// contestar de una vez.
//
// 🔴 EL CTE `previo` VE LA FILA VIEJA, Y ESA ES TODA LA MECÁNICA. En PostgreSQL
// todas las sub-sentencias de un mismo statement comparten UN snapshot: `previo`
// es un SELECT normal, así que lee lo que había antes de que `toque` escribiera,
// aunque el planificador los ejecute en el orden que quiera. Un `RETURNING` sobre
// el `ON CONFLICT DO UPDATE` NO serviría —devuelve la fila YA actualizada, o sea
// `last_incoming_at = now`— y el umbral de silencio saldría 0 siempre: la
// bienvenida no volvería nunca después de la primera. Es la clase de defecto que
// solo se ve con un reloj falso y varias horas de diferencia.
//
// El CTE de escritura se ejecuta SIEMPRE, aunque la consulta principal no lea ni
// una fila suya: es garantía documentada de PostgreSQL para las sentencias
// modificadoras dentro de WITH. Por eso `previo` puede venir vacío (contacto nuevo)
// sin que el toque se pierda.
//
// Ese caso vacío —`sql.ErrNoRows`— es el contacto que escribe por primera vez: se
// devuelve el WelcomeMark CERO sin error, que es exactamente «nunca habló, nunca
// se le saludó».
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: registrar actividad del contacto (bienvenida): %w"
func (r *PostgresRepository) TouchContact(ctx context.Context, key Key, now time.Time) (WelcomeMark, error) {
	var last, welcomed sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		WITH previo AS (
		    SELECT last_incoming_at, welcomed_at
		      FROM public.conversation_welcomes
		     WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3
		), toque AS (
		    INSERT INTO public.conversation_welcomes
		           (tenant_id, session_id, contact_id, last_incoming_at)
		    VALUES ($1, $2, $3, $4)
		    ON CONFLICT (tenant_id, session_id, contact_id)
		    DO UPDATE SET last_incoming_at = EXCLUDED.last_incoming_at
		    RETURNING 1
		)
		SELECT last_incoming_at, welcomed_at FROM previo
	`, key.TenantID, key.SessionID, key.ContactID, now).Scan(&last, &welcomed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Contacto nuevo para esta conversación: no había fila que leer y el `toque`
		// acaba de crearla. Marca CERO = «nunca habló, nunca se le saludó».
		return WelcomeMark{}, nil
	case err != nil:
		return WelcomeMark{}, fmt.Errorf("store: registrar actividad del contacto (bienvenida): %w", err)
	}
	return WelcomeMark{LastIncomingAt: last.Time, WelcomedAt: welcomed.Time}, nil
}

// MarkWelcomed implementa WelcomeStore: sella la bienvenida como entregada, con
// CENTINELA sobre el testigo que TouchContact devolvió.
//
// # POR QUÉ EL CENTINELA ES UN COMPARE-AND-SET Y NO UN `IS NULL`
//
// El precedente directo —fleet_sessions.greeted_at (0066)— usa `WHERE greeted_at IS
// NULL`, y allí basta porque aquella marca se pone UNA vez y para siempre. Esta
// vuelve a ponerse cada vez que el contacto reaparece tras el silencio, así que un
// `IS NULL` solo protegería la PRIMERA bienvenida y dejaría todas las demás sin
// centinela. `IS NOT DISTINCT FROM` compara incluyendo el NULL (un `=` con NULL da
// NULL, o sea ninguna fila, y la primera bienvenida no se marcaría JAMÁS: el
// contacto la recibiría en cada mensaje).
//
// Devuelve false SIN error cuando el centinela no casa: otro turno ganó la carrera
// entre el TouchContact y este UPDATE. La BD queda bien; lo que ya no tiene arreglo
// es que el mensaje de ESTE camino salió, y ese duplicado se ve en el log del
// llamante y en ningún otro sitio — misma honestidad que documenta greeting.go.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: marcar bienvenida entregada: %w"
//   - "store: filas afectadas al marcar bienvenida: %w"
func (r *PostgresRepository) MarkWelcomed(ctx context.Context, key Key, witness WelcomeMark, now time.Time) (bool, error) {
	var expected any
	if !witness.WelcomedAt.IsZero() {
		expected = witness.WelcomedAt
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE public.conversation_welcomes
		   SET welcomed_at = $4
		 WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3
		   AND welcomed_at IS NOT DISTINCT FROM $5
	`, key.TenantID, key.SessionID, key.ContactID, now, expected)
	if err != nil {
		return false, fmt.Errorf("store: marcar bienvenida entregada: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: filas afectadas al marcar bienvenida: %w", err)
	}
	return n > 0, nil
}
