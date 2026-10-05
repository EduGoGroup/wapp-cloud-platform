// Porta internal/gateway/fleet/repository_postgres.go @ 809345b

package fleet

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Logger es el puerto ESTRECHO de log que este repositorio necesita: una sola
// línea de aviso. Se declara aquí —y no se importa wapp-shared/logger— para que
// el dominio de flota no dependa de una implementación concreta de log; la
// interfaz la cumple tal cual sharedlogger.Logger (y también *slog.Logger).
//
// 🔴 SOLO se usa para el ÚNICO caso que no puede ser ni un error ni un silencio:
// un sobre de self_pn que no descifra al SERVIR EL LISTADO (ver scanSession y
// selfPnDecryptTally, que lo emite AGREGADO: una línea por llamada, no por fila).
// En ningún caso se le pasa el número: es PII.
type Logger interface {
	Warn(msg string, args ...any)
}

// nopLogger es el default cuando nadie enchufa un logger (tests, dobles): traga
// el aviso. Es preferible a un puntero nil, que obligaría a comprobar en cada
// punto de uso.
type nopLogger struct{}

func (nopLogger) Warn(string, ...any) {}

// Option configura al repositorio en su construcción.
type Option func(*PostgresRepository)

// WithLogger enchufa el logger del proceso. Sin él, el aviso de «un sobre de
// self_pn no descifra» se pierde: el listado sigue sirviéndose, pero nadie se
// entera de que un número quedó ilegible. El arranque SIEMPRE debe pasarlo.
func WithLogger(l Logger) Option {
	return func(r *PostgresRepository) {
		if l != nil {
			r.log = l
		}
	}
}

// PostgresRepository implementa Repository con SQL raw sobre
// public.fleet_sessions.
//
// 🔒 EL self_pn VA CIFRADO EN REPOSO desde el Plan 046 · T4.1 (migración 0068).
// La fila guarda CUATRO columnas —self_pn_enc (envelope), self_pn_dek (DEK
// envuelta), self_pn_kek_id (con qué KEK desenvolver) y self_pn_bidx (índice
// ciego, para buscar/contar sin descifrar)— y la columna EN CLARO `self_pn`
// queda VACÍA: este código NO la escribe nunca más y NO la lee nunca más para
// obtener el número. El número en claro solo vive en memoria, en el borde.
//
// Es el MISMO molde que contact.PostgresResolver (Plan 011, ADR-0017), y a
// propósito: cipher hace el envelope, kp calcula el índice ciego. Dos moldes
// distintos para el mismo problema serían dos rotaciones de KEK que gestionar.
type PostgresRepository struct {
	db     *sql.DB
	cipher *crypto.FieldCipher
	kp     crypto.KeyProvider
	log    Logger
}

// NewPostgresRepository construye el repositorio sobre el pool dado. cipher y kp
// son OBLIGATORIOS desde T4.1: sin ellos no se puede ni escribir ni leer el
// self_pn, así que se piden por parámetro posicional y no por Option — un
// repositorio a medio construir escribiría filas sin número y las leería vacías,
// en silencio y para siempre.
//
// No abre ni comprueba la conexión. Sin WithLogger —o con WithLogger(nil)— el
// logger es el mudo (nopLogger).
func NewPostgresRepository(db *sql.DB, cipher *crypto.FieldCipher, kp crypto.KeyProvider, opts ...Option) *PostgresRepository {
	r := &PostgresRepository{db: db, cipher: cipher, kp: kp, log: nopLogger{}}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// PostgresRepository cumple el puerto entero (los diez métodos de Repository).
var _ Repository = (*PostgresRepository)(nil)

// MarkOnline registra/actualiza la sesión como online.
//
// 🔴 EL INSERT NO NOMBRA `profile`, Y ESO ES EL CIERRE DE T3.1, NO UN OLVIDO. Esta
// es la vía de alta REAL de una sesión —la fila nace aquí, en el registro del stream
// CloudLink—, así que dejar la columna fuera de la lista hace que Postgres aplique su
// `DEFAULT 'passive'` (0063:112) y la sesión NAZCA PASIVA (D-046.7): no auto-responde
// hasta que alguien la active a mano. Nombrarla aquí —aunque fuera para escribir
// 'passive'— movería la decisión del esquema al código y abriría la puerta a que una
// vía de alta futura eligiera otra cosa sin que nadie lo note.
//
// El ON CONFLICT tampoco la toca: una sesión que RECONECTA conserva su perfil. Quien
// mueve el eje es SetProfile y solo SetProfile (ver su docstring y el de la 0065).
//
// Un fallo del driver vuelve envuelto como "fleet: marcar online: …".
func (r *PostgresRepository) MarkOnline(ctx context.Context, tenantID, edgeID, sessionID string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.fleet_sessions
			(tenant_id, edge_id, session_id, state, last_connected_at, last_seen_at, updated_at)
		VALUES ($1, $2, $3, 'online', now(), now(), now())
		ON CONFLICT (tenant_id, edge_id, session_id) DO UPDATE
		SET state = 'online',
		    last_connected_at = now(),
		    last_seen_at = now(),
		    updated_at = now()
	`, tenantID, edgeID, sessionID)
	if err != nil {
		return fmt.Errorf("fleet: marcar online: %w", err)
	}
	return nil
}

// MarkOffline marca la sesión como offline. No falla si la sesión no existía
// (UPDATE de 0 filas es válido: nunca llegó a registrarse online).
// Un fallo del driver vuelve envuelto como "fleet: marcar offline: …".
func (r *PostgresRepository) MarkOffline(ctx context.Context, tenantID, edgeID, sessionID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET state = 'offline', last_seen_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
	`, tenantID, edgeID, sessionID)
	if err != nil {
		return fmt.Errorf("fleet: marcar offline: %w", err)
	}
	return nil
}

// MarkLoggedOut marca la sesión como zombie (StateLoggedOut): WhatsApp cerró el
// device (Plan 020 · T3). Como MarkOffline es un UPDATE acotado por identidad; no
// falla si la sesión no existía (UPDATE de 0 filas es válido). Se distingue del
// offline-por-red por el estado escrito, no por el camino de código.
// Un fallo del driver vuelve envuelto como "fleet: marcar loggedout: …".
func (r *PostgresRepository) MarkLoggedOut(ctx context.Context, tenantID, edgeID, sessionID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET state = 'loggedout', last_seen_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
	`, tenantID, edgeID, sessionID)
	if err != nil {
		return fmt.Errorf("fleet: marcar loggedout: %w", err)
	}
	return nil
}

// SetState fija el estado (offline|loggedout) de la sesión del tenant. UPDATE
// acotado por tenant_id + session_id (aislamiento multi-tenant, INV-8): toca TODAS
// las filas de esa sesión bajo el tenant. found=false si 0 filas (sesión
// inexistente o de otro tenant ⇒ 404 opaco). Valida el estado antes de tocar la BD.
// Un estado inválido devuelve (false, ErrInvalidState) sin emitir sentencia; un
// fallo del driver vuelve envuelto como "fleet: fijar estado: …" y el de leer las
// filas afectadas como "fleet: filas afectadas al fijar estado: …".
func (r *PostgresRepository) SetState(ctx context.Context, tenantID, sessionID string, state State) (bool, error) {
	if !ValidAdminState(state) {
		return false, ErrInvalidState
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET state = $3, last_seen_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID, string(state))
	if err != nil {
		return false, fmt.Errorf("fleet: fijar estado: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("fleet: filas afectadas al fijar estado: %w", err)
	}
	return n > 0, nil
}

// Get devuelve la sesión, o found=false si no existe: sin fila la respuesta es
// (Session{}, false, nil). Un fallo del driver o del escaneo vuelve envuelto como
// "fleet: leer sesión: …". Un sobre de self_pn que no abre NO es error: la sesión
// sale con SelfPn vacío y queda un Warn (ver scanSession).
func (r *PostgresRepository) Get(ctx context.Context, tenantID, edgeID, sessionID string) (Session, bool, error) {
	// Una sola fila ⇒ el tally no acota nada aquí (cota: una línea). Se usa igual
	// para no tener DOS caminos de aviso que puedan divergir en formato.
	var tally selfPnDecryptTally
	defer func() { tally.flush(r.log) }()
	s, err := r.scanSession(r.db.QueryRowContext(ctx, selectSessionCols+`
		FROM public.fleet_sessions
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
	`, tenantID, edgeID, sessionID), &tally)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Session{}, false, nil
	case err != nil:
		return Session{}, false, fmt.Errorf("fleet: leer sesión: %w", err)
	}
	return s, true, nil
}

// List devuelve las sesiones de un tenant, en el orden (edge_id, session_id) que
// fija la sentencia; sin filas devuelve nil. Los errores vuelven envueltos:
// "fleet: listar sesiones: …" (la consulta), "fleet: escanear sesión: …" (una
// fila) y "fleet: iterar sesiones: …" (la iteración). Un fallo al CERRAR las filas
// tras recorrerlas enteras también sale como "fleet: iterar sesiones: …":
// database/sql cierra solo al agotar la iteración y entrega ese error por
// rows.Err(), así que la rama "fleet: cerrar filas: …" del defer no se alcanza por
// ese camino (se porta tal cual). Los sobres de self_pn que no abren dejan su
// SelfPn vacío y UN solo Warn para el listado entero.
func (r *PostgresRepository) List(ctx context.Context, tenantID string) (out []Session, err error) {
	// UN tally para el listado entero: la cota es UNA línea de Warn por llamada,
	// pase lo que pase con las filas. Ver selfPnDecryptTally.
	var tally selfPnDecryptTally
	defer func() { tally.flush(r.log) }()
	rows, err := r.db.QueryContext(ctx, selectSessionCols+`
		FROM public.fleet_sessions
		WHERE tenant_id = $1
		ORDER BY edge_id, session_id
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("fleet: listar sesiones: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("fleet: cerrar filas: %w", cerr)
		}
	}()

	for rows.Next() {
		s, scanErr := r.scanSession(rows, &tally)
		if scanErr != nil {
			return nil, fmt.Errorf("fleet: escanear sesión: %w", scanErr)
		}
		out = append(out, s)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("fleet: iterar sesiones: %w", rowsErr)
	}
	return out, nil
}

// selectSessionCols es la lista de columnas (con COALESCE para las nullable) que
// Get y List comparten; el orden DEBE casar con scanSession. Las columnas de salud
// (Plan 031 · T3) van al final: degraded_since/last_health_at se escanean como
// NullTime (NULL ⇒ time.Time cero, que la API lee con IsZero); el resto colapsa a
// su cero con COALESCE.
//
// 🔴 El bloque del WORKER (Plan 051 · T4.3) va SIN COALESCE a propósito: colapsar
// su NULL a 0 borraría la distinción entre «no medible» y «cero», que es
// justamente la información que estas columnas existen para conservar. Se escanean
// como sql.NullInt64 / []byte y se traducen a punteros y mapa nil-able.
//
// `profile` (Plan 046 · T1.1) es el ÚNICO eje con el que se decide. Ya no viaja al
// lado de nada: `role` —que este comentario describía como «alias deprecado hasta su
// DROP»— se retiró en la 0064, y con él su columna, su tipo Go y sus dos rutas HTTP.
// El COALESCE de `profile` es defensivo (la 0063 la deja NOT NULL) y cae a 'passive',
// NUNCA a 'active': si algún día faltara el dato, la lectura segura es «no
// auto-responde».
//
// `greeted_at` (Plan 046 · T3.2) NO está en esta lista a propósito: es un hecho
// OPERATIVO de una sola sesión —«ya se le entregó el aviso»— que solo consume el
// emisor del saludo por su propia consulta (PendingGreeting). Meterlo aquí obligaría
// a añadirle un campo a Session y a escanearlo en cada List del dashboard para que
// nadie lo lea.
//
// 🔒 `self_pn` (Plan 046 · T4.1) YA NO SE SELECCIONA: la columna en claro está
// vacía y leerla devolvería "" para toda sesión emparejada. En su lugar viajan
// las TRES columnas del sobre y el número se descifra en Go (ver scanSession).
// El orden de las tres respeta el sitio que ocupaba la columna en claro para que
// el resto del scan no se mueva.
const selectSessionCols = `
		SELECT tenant_id::text, edge_id, session_id, state,
		       COALESCE(profile, 'passive'),
		       self_pn_enc, self_pn_dek, self_pn_kek_id,
		       COALESCE(last_connected_at, 'epoch'), COALESCE(last_seen_at, 'epoch'),
		       COALESCE(whatsapp_state, ''), COALESCE(degraded_reason, ''),
		       degraded_since, last_health_at,
		       COALESCE(last_event_age_s, 0), COALESCE(outbox_depth, 0),
		       COALESCE(binary_version, ''), COALESCE(uptime_s, 0),
		       COALESCE(dek_load_duration_ms, 0), COALESCE(intent_circuit, ''),
		       COALESCE(worker_taskset, ''), intent_p50_ms, intent_omitted_by_reason,
		       stuck_heads, stuck_head_polls, failed_seal_dispatch, failed_seal_budget`

// rowScanner abstrae *sql.Row y *sql.Rows para reusar el escaneo.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanSession materializa una fila en Session. Es MÉTODO y ya no función suelta
// (Plan 046 · T4.1) por una razón sola: necesita el cipher para abrir el sobre
// del self_pn. Pasarlo por parámetro habría sido lo mismo con más ruido.
//
// 🔴 MODO DE FALLO DEL DESCIFRADO, DECIDIDO A PROPÓSITO: un sobre que no abre
// deja `SelfPn` VACÍO, AVISA por log (sin PII) y NO tumba la fila ni la lista.
//
// El razonamiento, escrito para que se pueda refutar y no solo obedecer. El
// consumidor de esto es GET /api/v1/sessions —la consola del dueño y el
// dashboard del BFF—, y `self_pn` es UN campo de veintitantos de una fila de un
// listado. Si una KEK faltara del keyring (el fallo realista: un despliegue con
// WAPP_KEK_KEYRING incompleto tras una rotación), el fallo NO es de una fila:
// es de TODAS a la vez. Con error duro, la consola entera se queda en blanco
// —estados, salud, degradados, todo— por un campo cosmético, y encima justo en
// el momento en que el operador la necesita para diagnosticar. Con campo vacío,
// el dueño ve su flota completa y una casilla de número sin rellenar, que es
// EXACTAMENTE lo que ya ve una sesión sin emparejar: un estado que la UI sabe
// pintar desde el Plan 020 (el campo es `omitempty` en publicapi/sessions.go:28).
//
// El precio, dicho sin rebajarlo: un número ilegible se confunde con «todavía no
// hay número». Por eso el aviso NO es opcional — es la única diferencia entre
// las dos situaciones, y sin logger enchufado (WithLogger) esto sí sería un
// fallo silencioso. Si algún día hiciera falta distinguirlas en la API, la
// salida es un campo de estado explícito, no reventar el listado.
//
// ⚠️ NO se aplica el mismo criterio en PendingGreeting: allí el número ES el
// destino del mensaje, no un adorno, así que sin él la operación no existe.
//
// 🔧 EL AVISO YA NO SE EMITE AQUÍ (corrección del 2026-08-21, revisión de T4.1).
// Se ACUMULA en el tally que pasa el llamante y se emite UNA vez por llamada. El
// porqué está en selfPnDecryptTally: el modo de fallo que este mismo docstring
// describe rompe TODAS las filas a la vez, y un Warn por fila dentro del bucle de
// List multiplicaba el ruido por el tamaño de la flota Y por la frecuencia de
// poll del dashboard, justo cuando el operador necesita leer el log.
func (r *PostgresRepository) scanSession(sc rowScanner, tally *selfPnDecryptTally) (Session, error) {
	var s Session
	var state, profile string
	var degradedSince, lastHealthAt sql.NullTime
	var p50, stuckHeads, stuckPolls, sealDispatch, sealBudget sql.NullInt64
	var omittedRaw []byte
	var pnEnc, pnDek []byte
	var pnKekID sql.NullString
	if err := sc.Scan(&s.TenantID, &s.EdgeID, &s.SessionID, &state, &profile,
		&pnEnc, &pnDek, &pnKekID,
		&s.LastConnectedAt, &s.LastSeenAt,
		&s.WhatsappState, &s.DegradedReason, &degradedSince, &lastHealthAt,
		&s.LastEventAgeS, &s.OutboxDepth, &s.BinaryVersion, &s.UptimeS,
		&s.DekLoadDurationMs, &s.IntentCircuit,
		&s.WorkerTaskset, &p50, &omittedRaw,
		&stuckHeads, &stuckPolls, &sealDispatch, &sealBudget); err != nil {
		return Session{}, err
	}
	s.State = State(state)
	// El perfil pasa por defaultProfile: la sentencia ya trae COALESCE(profile,
	// 'passive') y el CHECK de la 0063 no admite el vacío, así que con Postgres no
	// cambia nada; es la misma política («vacío ⇒ pasivo») dicha también en Go.
	s.Profile = defaultProfile(Profile(profile))
	// El número se descifra SOLO aquí, en memoria, para servirlo por la API (el
	// contrato público NO cambia con T4.1: `self_pn` sigue siendo el número en
	// claro en el JSON). Un sobre ausente da "" sin error: sesión sin emparejar.
	selfPn, pnErr := r.decryptSelfPn(pnEnc, pnDek, pnKekID)
	if pnErr != nil {
		tally.record(s.TenantID, s.EdgeID, s.SessionID, pnKekID.String, pnErr)
	}
	s.SelfPn = selfPn
	if degradedSince.Valid {
		s.DegradedSince = degradedSince.Time
	}
	if lastHealthAt.Valid {
		s.LastHealthAt = lastHealthAt.Time
	}
	// Bloque del worker (Plan 051 · T4.3): NULL ⇒ puntero nil («no lo sé»), nunca 0.
	s.IntentP50Ms = int64Ptr(p50)
	s.StuckHeads = int64Ptr(stuckHeads)
	s.StuckHeadPolls = int64Ptr(stuckPolls)
	s.FailedSealDispatch = int64Ptr(sealDispatch)
	s.FailedSealBudget = int64Ptr(sealBudget)
	if len(omittedRaw) > 0 {
		if err := json.Unmarshal(omittedRaw, &s.IntentOmittedByReason); err != nil {
			return Session{}, fmt.Errorf("fleet: deserializar desglose de motivos: %w", err)
		}
	}
	return s, nil
}

// int64Ptr convierte un sql.NullInt64 en *int64: NULL ⇒ nil («no lo sé»), nunca 0.
func int64Ptr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}
