package fleet

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
func NewPostgresRepository(db *sql.DB, cipher *crypto.FieldCipher, kp crypto.KeyProvider, opts ...Option) *PostgresRepository {
	r := &PostgresRepository{db: db, cipher: cipher, kp: kp, log: nopLogger{}}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// selfPnEnvelope prepara las CUATRO columnas cifradas del número propio:
// self_pn_bidx (índice ciego), self_pn_enc (envelope), self_pn_dek (DEK
// envuelta) y el key_id de la KEK que la envolvió. Es el gemelo de
// contact.encodeRef; el número en claro no sale de aquí.
//
// 🔴 NORMALIZA ANTES DE INDEXAR, Y ESE ORDEN ES TODO EL ASUNTO. BlindIndex es un
// HMAC crudo: no normaliza nada por su cuenta (keyprovider.go:322-328), así que
// "+34600111222" y "34600111222" darían DOS índices distintos para el MISMO
// número y el conteo del tope de dispositivos (REQ-D4) contaría dos veces al
// mismo teléfono. Con la columna en claro ese fallo era visible al mirar la
// tabla; con el índice ciego es invisible por construcción.
//
// ⚠️ SE NORMALIZA AQUÍ AUNQUE EL LLAMADOR YA NORMALICE (connect.go:591-598,
// persistSelfPn). Normalize es idempotente sobre un valor ya normalizado —solo
// deja dígitos— así que el segundo paso cuesta un recorrido de ≤15 caracteres y
// compra la garantía de que el bidx es canónico VENGA DE DONDE VENGA el valor.
// Confiar en el llamador ataría la integridad del índice a una convención no
// verificable: mañana un backfill, un endpoint de admin o un test escriben aquí
// sin pasar por el Heartbeat y el índice queda partido en dos poblaciones que ya
// nadie puede reconciliar (el valor en claro para compararlas ya no existe).
func (r *PostgresRepository) selfPnEnvelope(tenantID, selfPn string) (bidx string, enc, dek []byte, kekID string, err error) {
	norm, err := normalizeSelfPn(selfPn)
	if err != nil {
		// El error de Normalize NUNCA lleva el número (contact.go:121-131 lo
		// documenta: describe la causa con la longitud, no con el valor), así que
		// se puede envolver y subir tal cual sin filtrar PII a los logs.
		return "", nil, nil, "", fmt.Errorf("fleet: normalizar self_pn: %w", err)
	}
	bidx = r.kp.BlindIndex(tenantID, norm)
	enc, dek, kekID, err = r.cipher.Encrypt(norm)
	if err != nil {
		return "", nil, nil, "", fmt.Errorf("fleet: cifrar self_pn: %w", err)
	}
	return bidx, enc, dek, kekID, nil
}

// decryptSelfPn descifra el sobre leído de una fila y devuelve el número en
// claro. Un sobre AUSENTE (las cuatro columnas NULL: sesión sin emparejar, o
// fila anterior al backfill) devuelve "" sin error — es el mismo «todavía no hay
// número» que antes representaba el COALESCE sobre la columna. Un sobre INCOMPLETO o
// que no abre SÍ es error: ahí hay un dato corrupto y callarlo lo entierra.
//
// Se desenvuelve con la KEK que envolvió ESTA fila (self_pn_kek_id) y no con la
// current: tras una rotación parcial coexisten filas de varias KEK (Plan 012).
func (r *PostgresRepository) decryptSelfPn(enc, dek []byte, kekID sql.NullString) (string, error) {
	if len(enc) == 0 && len(dek) == 0 && !kekID.Valid {
		return "", nil
	}
	if len(enc) == 0 || len(dek) == 0 || !kekID.Valid {
		// Sin el número en el mensaje: solo el HECHO de que el sobre está a medias.
		return "", errors.New("fleet: sobre de self_pn incompleto (enc/dek/kek_id no viajan juntos)")
	}
	pn, err := r.cipher.Decrypt(enc, dek, kekID.String)
	if err != nil {
		return "", fmt.Errorf("fleet: descifrar self_pn: %w", err)
	}
	return pn, nil
}

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

// CountLiveBySelfPn cuenta las sesiones vivas (state != 'loggedout') del tenant con
// el self_pn dado (REQ-D4, aviso del tope de dispositivos). selfPn vacío ⇒ 0 sin
// tocar la BD.
//
// 🔒 COMPARA POR ÍNDICE CIEGO (Plan 046 · T4.1), no por el número: la columna en
// claro ya no tiene el dato. El bidx es determinista —mismo tenant + mismo número
// normalizado ⇒ mismo hex— así que la igualdad que antes hacía Postgres sobre el
// texto la sigue haciendo, ahora sobre el HMAC, y el índice parcial
// (tenant_id, self_pn_bidx) de la 0068 la sirve igual de barata.
//
// Un número que NO normaliza devuelve error (no 0): «no puedo contar» y «hay
// cero» son respuestas distintas, y devolver 0 aquí apagaría el aviso del tope
// justo en el caso raro. El llamador (warnDeviceLimit) ya lo traga en Debug.
func (r *PostgresRepository) CountLiveBySelfPn(ctx context.Context, tenantID, selfPn string) (int, error) {
	if selfPn == "" {
		return 0, nil
	}
	// Se normaliza por el MISMO camino que la escritura: si escritura y lectura
	// normalizaran distinto, el conteo daría 0 siempre y el aviso no saltaría nunca.
	norm, err := normalizeSelfPn(selfPn)
	if err != nil {
		return 0, fmt.Errorf("fleet: normalizar self_pn para contar: %w", err)
	}
	var n int
	err = r.db.QueryRowContext(ctx, `
		SELECT count(*) FROM public.fleet_sessions
		WHERE tenant_id = $1 AND self_pn_bidx = $2 AND state <> 'loggedout'
	`, tenantID, r.kp.BlindIndex(tenantID, norm)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("fleet: contar sesiones vivas por self_pn: %w", err)
	}
	return n, nil
}

// SetSelfPn persiste el self_pn reportado en el Heartbeat (Plan 020 · T2). UPDATE
// acotado por (tenant_id, edge_id, session_id). selfPn vacío es un no-op: NO
// sobrescribe un valor previo bueno (protege el dato). Un UPDATE de 0 filas
// (sesión aún sin registrar) es válido: el próximo Heartbeat lo fijará.
//
// 🔒 DESDE T4.1 ESCRIBE EL SOBRE Y VACÍA LA COLUMNA EN CLARO EN EL MISMO UPDATE.
// `self_pn = NULL` no es una limpieza aparte ni un script de una vez: va aquí,
// atómico con la escritura del sobre, para que la fila NUNCA quede un instante
// con el número en claro Y el sobre a la vez, y para que toda sesión viva se
// sanee sola en su siguiente latido aunque el backfill de arranque no la
// alcanzara. La columna sigue existiendo en el DDL (la retira una migración
// posterior, cuando nadie la lea); lo que se retira aquí es al ÚLTIMO escritor.
//
// ⚠️ EL NÚMERO NO SE PASA COMO PARÁMETRO SQL EN NINGÚN SITIO: solo viajan el
// envelope (bytes opacos), la DEK envuelta, el key_id y el HMAC. Un log de
// sentencias lentas de Postgres ya no puede filtrar un teléfono.
//
// 🔴 LA GUARDA DEL WHERE EVITA REESCRIBIR LA FILA EN CADA LATIDO, y no es un
// adorno de rendimiento. Encrypt genera una DEK FRESCA por llamada, así que el
// sobre sale distinto cada vez aunque el número sea el mismo: sin guarda, cada
// sesión reescribiría cuatro columnas cada 30 s, para siempre, generando WAL y
// bloat por un dato que no cambió. Con ella el UPDATE solo entra en TRES casos:
//
//	(1) el número CAMBIÓ — bidx distinto. El bidx sí es determinista, por eso es
//	    el comparando correcto y el sobre no lo sería. Mismo criterio que el
//	    `IS DISTINCT FROM` de contact.resolveExisting.
//	(2) queda plano que limpiar — `self_pn IS NOT NULL`, que es exactamente el
//	    caso de la primera pasada tras la 0068 y el del rollback a un binario
//	    viejo que volviera a escribir la columna en claro.
//	(3) el sobre está envuelto por una KEK que YA NO ES LA CURRENT —
//	    `self_pn_kek_id IS DISTINCT FROM $6`. Ver abajo: es una vía de
//	    RECUPERACIÓN, no una optimización.
//
// 🔧 CORRECCIÓN DEL 2026-08-21 (revisión de T4.1). Este docstring afirmaba que
// «un sobre ya escrito no se re-envuelve solo tras una rotación de KEK, y es
// correcto porque re-envolver es trabajo de crypto.Rekey». LA SEGUNDA MITAD ERA
// FALSA, y con ella la primera dejaba de ser aceptable:
//
//	Rekey re-envuelve haciendo UnwrapDEK(dek, kek_id) → WrapDEK(current)
//	(rekey.go:173). Necesita la KEK VIEJA. Si esa KEK desapareció del keyring
//	—un despliegue con WAPP_KEK_KEYRING incompleto, la rotación mal cerrada de
//	§10.F— Rekey NO PUEDE con esa fila: falla al desenvolver y la deja igual.
//
// Sin el caso (3), esa fila quedaba ATRAPADA PARA SIEMPRE: el bidx casa (es
// determinista, no depende de la KEK), así que la guarda decía «nada que hacer»
// aunque el Edge estuviera reportando el mismo número cada 30 s; la consola
// servía "" y scanSession dejaba un Warn por fila y por poll, indefinidamente y
// SIN vía de recuperación automática. Y es el único caso del sistema en que el
// dato en claro SÍ está disponible —llega en cada latido— y aun así no se
// restauraba: cifrar de nuevo desde el latido no necesita la KEK vieja para nada.
//
// Con (3), el latido siguiente re-cifra la fila con la KEK current y la fila se
// AUTO-SANA. Converge en una sola escritura: tras ella `self_pn_kek_id = $6` y
// la guarda vuelve a bloquear. El precio, dicho entero: tras una rotación de KEK,
// cada sesión viva paga UN reescritura extra en su siguiente latido (y de paso le
// ahorra esa fila a Rekey). No reintroduce la reescritura perpetua que la guarda
// existe para impedir, porque el comparando de (3) también es estable.
//
// ⚠️ EL COMPARANDO DE (3) ES $6, NO UN PARÁMETRO NUEVO. $6 es el kekID que
// devuelve selfPnEnvelope, y ese ES por construcción el key_id de la KEK current:
// FieldCipher.Encrypt envuelve la DEK con kp.WrapDEK, que documenta «envuelve con
// la KEK current» (keyprovider.go:88-90). Pasar además kp.CurrentKeyID() sería un
// segundo testigo del mismo hecho, y dos testigos que pueden divergir son peores
// que uno.
func (r *PostgresRepository) SetSelfPn(ctx context.Context, tenantID, edgeID, sessionID, selfPn string) error {
	if selfPn == "" {
		return nil
	}
	bidx, enc, dek, kekID, err := r.selfPnEnvelope(tenantID, selfPn)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET self_pn_enc    = $4,
		    self_pn_dek    = $5,
		    self_pn_kek_id = $6,
		    self_pn_bidx   = $7,
		    self_pn        = NULL,
		    updated_at     = now()
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
		  AND (self_pn_bidx   IS DISTINCT FROM $7
		    OR self_pn        IS NOT NULL
		    OR self_pn_kek_id IS DISTINCT FROM $6)
	`, tenantID, edgeID, sessionID, enc, dek, kekID, bidx)
	if err != nil {
		return fmt.Errorf("fleet: fijar self_pn: %w", err)
	}
	return nil
}

// PendingGreeting responde a la ÚNICA pregunta del emisor del saludo de T3.2 (b):
// «¿a esta sesión hay que avisarla, y a qué número?». Devuelve pending=true —con el
// self_pn ya normalizado que persistió SetSelfPn— solo si la fila existe, tiene
// número conocido, `greeted_at IS NULL` y —🔴 la tercera, ver abajo— está en perfil
// PASIVO.
//
// Las tres condiciones van en el SQL y no en el llamante a propósito: son estado de la
// FILA, y evaluarlas en Go exigiría traerse la sesión entera para mirar tres campos.
// Cero filas es la respuesta NORMAL, no un error: la da toda sesión ya saludada, toda
// sesión ACTIVA, toda sesión sin emparejar y el canal de control (que no tiene fila en
// esta tabla).
//
// 🔴 POR QUÉ FILTRA POR PERFIL, Y POR QUÉ SIN ESTO EL AVISO MENTÍA (decisión de Jhoan
// del 2026-08-21, durante el barrido de la Ola 3). El texto AVISO_SESION_PASIVA_V1
// afirma tres cosas —«nació en perfil PASIVA», «wApp todavía no responde solo» y
// «cámbiala a ACTIVA»— y las tres son FALSAS dichas a una sesión que ya está activa.
// Sin este predicado las activas también salían pendientes: se verificó ejecutando
// esta misma consulta contra Postgres con dos filas sembradas, y la activa aparecía.
// No era hipotético: al aplicar la 0066, la ÚNICA sesión de UAT (profile='active',
// self_pn poblado, greeted_at NULL) habría recibido ese mensaje en su siguiente
// latido, en un teléfono real.
//
// ⚠️ `= 'passive'` Y NO `<> 'active'`, aunque hoy sean equivalentes (la columna es NOT
// NULL y su CHECK la acota a los dos valores, 0063:104). La equivalencia solo dura
// mientras el dominio tenga DOS valores: el día que aparezca un tercer perfil,
// `<> 'active'` le mandaría un aviso que lo describe mal, y `= 'passive'` no le manda
// nada. El aviso habla de UN perfil concreto, así que la pregunta correcta es «¿es
// pasiva?», no «¿no es activa?». Fail-closed ante lo que todavía no existe.
//
// ⚠️ CONSECUENCIA ACEPTADA: quien active su sesión dentro de los ~30 s que van del
// emparejamiento al latido que consigue entregar, NO recibe el aviso. Es correcto —ya
// no le aplica—, y su `greeted_at` se queda NULL, así que si algún día vuelve a
// pasiva sí lo recibirá. Eso también es correcto: en ese momento el texto vuelve a
// ser cierto.
//
// ⚠️ NO es un claim: no reserva nada. Entre este SELECT y el MarkGreeted posterior
// hay un envío a WhatsApp de por medio, y esa es justo la ventana que el centinela de
// MarkGreeted cierra.
//
// NUNCA loguea: devuelve el número, que es PII, y quien lo recibe se encarga.
//
// 🔴 ESTE LECTOR SE INCORPORÓ A T4.1 SOBRE LA MARCHA, Y NO ESTÁ EN EL CENSO DEL
// PLAN. El censo de lectores de `self_pn` se escribió ANTES que este método:
// PendingGreeting nació con la Ola 3 (el aviso de sesión pasiva, T3.2 (b)), o sea
// DESPUÉS. Si el cifrado hubiera seguido el censo al pie de la letra, este SELECT
// habría quedado leyendo una columna que a partir de la 0068 está VACÍA: el
// predicado que exigía la columna no vacía no casaría NUNCA, PendingGreeting
// devolvería pending=false siempre, y el saludo se quedaría sin destino EN
// SILENCIO —sin error, sin log, sin nada que delatara que dejó de emitirse—.
// De ahí las dos correcciones de abajo, y de ahí esta nota: el que venga detrás
// tiene que poder ver que este lector se añadió a mano y por qué.
//
//   - El predicado pasa a `self_pn_bidx IS NOT NULL`. El índice ciego es la
//     señal de «esta fila tiene número»: se escribe con el sobre y en el mismo
//     UPDATE, así que su presencia es exactamente lo que antes decía ese filtro.
//   - El SELECT trae el SOBRE y el número se descifra EN GO, en memoria, justo
//     para pasárselo a SendText. No hay forma de hacerlo en SQL —ni debe haberla:
//     la KEK no vive en la base—.
//
// Un sobre que no abre devuelve ERROR (no pending=false): sin número no hay a
// quién avisar, y decir «no está pendiente» convertiría un dato corrupto en un
// saludo que no se manda nunca y que nadie echa de menos. El emisor lo registra
// en Warn con IDs opacos y reintenta al siguiente latido (greeting.go:156-160).
func (r *PostgresRepository) PendingGreeting(ctx context.Context, tenantID, edgeID, sessionID string) (string, bool, error) {
	var (
		enc, dek []byte
		kekID    sql.NullString
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT self_pn_enc, self_pn_dek, self_pn_kek_id
		FROM public.fleet_sessions
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
		  AND greeted_at IS NULL
		  AND self_pn_bidx IS NOT NULL
		  AND profile = 'passive'
	`, tenantID, edgeID, sessionID).Scan(&enc, &dek, &kekID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("fleet: consultar saludo pendiente: %w", err)
	}
	// A partir de aquí el número vive en memoria y no se loguea (§8 del Plan 011).
	selfPn, err := r.decryptSelfPn(enc, dek, kekID)
	if err != nil {
		return "", false, err
	}
	if selfPn == "" {
		// El bidx estaba pero el sobre no: la fila casó el WHERE y aun así no hay
		// destino. No puede pasar si el sobre se escribe entero (SetSelfPn y el
		// backfill lo hacen), pero si pasara, «pendiente sin número» sería mentira.
		return "", false, errors.New("fleet: la fila tiene índice ciego de self_pn pero no sobre; no hay destino para el saludo")
	}
	return selfPn, true, nil
}

// MarkGreeted deja constancia de que a esta sesión YA se le entregó el aviso
// AVISO_SESION_PASIVA_V1 (Plan 046 · T3.2 (b), migración 0066). Devuelve marked=true
// solo si ESTA llamada fue la que puso la marca.
//
// 🔴 EL CENTINELA `greeted_at IS NULL` ES LA IDEMPOTENCIA, y no es decorativo: si dos
// latidos de la misma sesión llegan a la vez —dos Edges reportando la misma sesión,
// dos streams durante una reconexión—, los dos pueden haber leído `pending=true` y
// los dos llegar aquí. Con el centinela, el UPDATE del segundo casa CERO filas y
// devuelve marked=false, así que el llamante sabe que su envío fue el duplicado y
// puede decirlo en el log en vez de creerse el primero. Sin él, la marca se
// re-escribiría en cada latido y `greeted_at` dejaría de ser «cuándo se avisó» para
// pasar a ser «el último latido», que es otra columna que ya existe (updated_at).
//
// ⚠️ Solo se llama con el Ack del Edge en la mano y ok=true. Un envío que muere en la
// ventana del lease (el Validator del Edge nace cerrado y tarda 0,5-1,1 s en abrirse,
// medido en campo) NO puede marcar: el reintento es el latido siguiente y nada más.
//
// NO toca `profile_updated_at`, y eso es obligatorio: la regla de la 0065 dice que
// SOLO SetProfile mueve el reloj del eje. Mover `updated_at` sí es correcto —es el
// reloj de la FILA y esta escritura la cambia—.
func (r *PostgresRepository) MarkGreeted(ctx context.Context, tenantID, edgeID, sessionID string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET greeted_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
		  AND greeted_at IS NULL
	`, tenantID, edgeID, sessionID)
	if err != nil {
		return false, fmt.Errorf("fleet: marcar saludo entregado: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("fleet: filas afectadas al marcar el saludo: %w", err)
	}
	return n > 0, nil
}

// SetProfile fija el PERFIL (active|passive) de la sesión del tenant.
//
// 📌 Hasta la 0064 escribía además el alias legado `role` en el MISMO UPDATE. Al
// retirarse esa columna, el DROP costó exactamente lo que su comentario predijo:
// borrar SetRole y la asignación de `role` de aquí. La decisión de NO delegar el eje
// nuevo en el viejo es la que hizo que el borrado saliera barato.
//
// UPDATE acotado por tenant_id + session_id (aislamiento multi-tenant, INV-8): toca
// TODAS las filas de esa sesión bajo el tenant. found=false si 0 filas (sesión
// inexistente o de otro tenant ⇒ 404 opaco). Valida el perfil en el dominio antes de
// tocar la BD.
//
// 🔴 ESTA ES LA ÚNICA ESCRITURA DEL REPO QUE MUEVE `profile_updated_at` (0065), y esa
// exclusividad es TODA la garantía de que la `version` del kind:"filters" solo avanza
// cuando avanza el CONTENIDO. No la impone el motor —no hay trigger— sino esta regla:
//
//	ninguna otra escritura de fleet_sessions añade `profile_updated_at = now()`.
//	Ni MarkOnline, ni MarkOffline, ni MarkLoggedOut, ni SetState, ni SetSelfPn, ni
//	SaveHealth. Todas mueven `updated_at`, que es el reloj de la FILA, y ninguna toca
//	el del EJE.
//
// Si alguien la rompe vuelve el bug entero: MarkOnline corre justo antes de
// pushConfigsOnConnect (connect.go:514 vs :523/:537), así que un Edge con N sesiones
// del tenant publicaría N versiones nuevas con el mapa idéntico en CADA reconexión —
// y el WARN «versión anterior o igual» del Edge pasaría a ser ruido de operación
// normal, enterrando el caso real que ese log existe para delatar.
func (r *PostgresRepository) SetProfile(ctx context.Context, tenantID, sessionID string, profile Profile) (bool, error) {
	if !ValidProfile(profile) {
		return false, ErrInvalidProfile
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET profile = $3, profile_updated_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID, string(profile))
	if err != nil {
		return false, fmt.Errorf("fleet: fijar perfil: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("fleet: filas afectadas al fijar perfil: %w", err)
	}
	return n > 0, nil
}

// ProfilesByTenant devuelve la FOTO del eje `profile` de TODAS las sesiones del
// tenant más su versión (Plan 046 · T2.1). Es la única lectura que alimenta el
// kind:"filters" que se empuja al Edge.
//
// Tres decisiones viven en el SQL y no en el llamante:
//
//  1. GROUP BY session_id. La clave física es (tenant_id, edge_id, session_id): una
//     misma sesión puede tener FILA POR EDGE. El payload de filters se indexa por
//     session_id (contrato D-046.2), así que aquí se colapsa.
//  2. max(profile) para desempatar. SetProfile escribe TODAS las filas de la sesión a
//     la vez, así que discrepar es un estado que no debería existir; si existe, el
//     orden alfabético hace ganar a 'passive' sobre 'active' — que es justo la
//     lectura segura («ante la duda, no auto-responde»), la misma que ya eligió el
//     COALESCE de selectSessionCols.
//  3. La versión sale de `profile_updated_at` (0065) y NO de `updated_at`. Son dos
//     relojes distintos y la diferencia es el bug que el code review del 2026-08-21
//     encontró: `updated_at` es el reloj de la FILA y lo mueve cualquier escritura
//     —MarkOnline, el SaveHealth de CADA heartbeat, SetSelfPn—, así que la versión
//     avanzaba sin que avanzara el mapa. Con MarkOnline corriendo justo antes de
//     pushConfigsOnConnect, un Edge con N sesiones publicaba N versiones nuevas e
//     idénticas por reconexión. `profile_updated_at` lo mueve SOLO SetProfile.
//  4. La versión NO se calcula en SQL. Se escanea `profile_updated_at` como
//     time.Time y el microsegundo lo saca Go con UnixMicro(). Un
//     EXTRACT(EPOCH …)*1000000 daría el mismo número en Postgres 14+ (donde EXTRACT
//     devuelve numeric, exacto) pero en versiones anteriores devuelve double y
//     1,7e15 roza el límite de dígitos significativos del float64: el redondeo se
//     comería el microsegundo justo cuando dos cambios seguidos tienen que
//     distinguirse.
//
// Un tenant sin filas devuelve Sessions vacío (nunca nil) y Version 0, SIN error: es
// una respuesta legítima que igualmente se empuja (regla 2 de T2.1).
func (r *PostgresRepository) ProfilesByTenant(ctx context.Context, tenantID string) (tp TenantProfiles, err error) {
	tp = TenantProfiles{Sessions: make(map[string]Profile)}
	rows, err := r.db.QueryContext(ctx, `
		SELECT session_id,
		       max(COALESCE(profile, 'passive')) AS profile,
		       max(profile_updated_at)           AS profile_updated_at
		FROM public.fleet_sessions
		WHERE tenant_id = $1
		GROUP BY session_id
		ORDER BY session_id
	`, tenantID)
	if err != nil {
		return TenantProfiles{}, fmt.Errorf("fleet: leer perfiles del tenant: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("fleet: cerrar filas de perfiles: %w", cerr)
		}
	}()

	for rows.Next() {
		var sessionID, profile string
		var profileUpdatedAt time.Time
		if scanErr := rows.Scan(&sessionID, &profile, &profileUpdatedAt); scanErr != nil {
			return TenantProfiles{}, fmt.Errorf("fleet: escanear perfil de sesión: %w", scanErr)
		}
		tp.Sessions[sessionID] = Profile(profile)
		if us := profileUpdatedAt.UnixMicro(); us > tp.Version {
			tp.Version = us
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return TenantProfiles{}, fmt.Errorf("fleet: iterar perfiles del tenant: %w", rowsErr)
	}
	return tp, nil
}

// SaveHealth persiste el snapshot de salud reportado en el Heartbeat (Plan 031 ·
// T3). UPDATE acotado por (tenant_id, edge_id, session_id): NO toca `state` (link
// CloudLink), solo las columnas de salud. degraded_since se calcula en SQL con un
// CASE que preserva el instante de entrada: al entrar en degradado usa el valor
// previo o now() (COALESCE) y al salir lo pone NULL — atómico contra el valor
// actual de la fila. Un UPDATE de 0 filas (sesión aún sin registrar) es válido.
// El bloque del WORKER (Plan 051 · T4.3, campos 9-15) se escribe en columnas
// NULLABLE: un puntero nil / un texto vacío / un mapa vacío se persisten como NULL
// («este Edge no lo sabe»), NUNCA como cero. Y se escriben SIEMPRE, también cuando
// son NULL: un snapshot que dejó de saber el taskset debe BORRAR el valor previo,
// porque conservar un "disjunta" viejo es publicar una salud inventada.
func (r *PostgresRepository) SaveHealth(ctx context.Context, tenantID, edgeID, sessionID string, h HealthSnapshot) error {
	// El desglose de motivos va al JSONB tal cual, SIN sumar nada (INV-051.3). Un
	// mapa nil o vacío se queda como interfaz nil ⇒ NULL, y no como '{}': el
	// contrato solo envía las claves con valor distinto de cero, así que un Edge
	// nuevo SIN omisiones y un Edge viejo llegan indistinguibles — y ante la duda
	// la lectura honesta es «no lo sé», no «cero omisiones».
	var omitted any
	if len(h.IntentOmittedByReason) > 0 {
		raw, merr := json.Marshal(h.IntentOmittedByReason)
		if merr != nil {
			return fmt.Errorf("fleet: serializar desglose de motivos: %w", merr)
		}
		omitted = raw
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET whatsapp_state           = $4,
		    degraded_reason          = $5,
		    last_event_age_s         = $6,
		    dek_load_duration_ms     = $7,
		    intent_circuit           = $8,
		    outbox_depth             = $9,
		    binary_version           = $10,
		    uptime_s                 = $11,
		    last_health_at           = now(),
		    degraded_since           = CASE WHEN $12 THEN COALESCE(degraded_since, now()) ELSE NULL END,
		    worker_taskset           = $13,
		    intent_p50_ms            = $14,
		    intent_omitted_by_reason = $15,
		    stuck_heads              = $16,
		    stuck_head_polls         = $17,
		    failed_seal_dispatch     = $18,
		    failed_seal_budget       = $19,
		    updated_at               = now()
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
	`, tenantID, edgeID, sessionID,
		h.WhatsappState, h.DegradedReason, h.LastEventAgeS, h.DekLoadDurationMs,
		h.IntentCircuit, h.OutboxDepth, h.BinaryVersion, h.UptimeS, h.Degraded(),
		nullText(h.WorkerTaskset), nullInt64(h.IntentP50Ms), omitted,
		nullInt64(h.StuckHeads), nullInt64(h.StuckHeadPolls),
		nullInt64(h.FailedSealDispatch), nullInt64(h.FailedSealBudget))
	if err != nil {
		return fmt.Errorf("fleet: persistir salud: %w", err)
	}
	return nil
}

// nullText mapea el texto vacío a NULL: en las columnas de salud del worker,
// vacío significa «no lo sé» y NULL es su representación en el esquema.
func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullInt64 desreferencia el puntero a un valor que el driver entiende, o nil
// (NULL) si no hay dato. Se desreferencia a mano y no se pasa el *int64 crudo
// para no depender de cómo cada driver trate los punteros.
func nullInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// Get devuelve la sesión, o found=false si no existe.
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

// List devuelve las sesiones de un tenant.
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

// selfPnDecryptTally acumula los sobres de self_pn que NO abrieron durante UNA
// llamada al repositorio (un Get, o un List entero), para emitir UN SOLO Warn al
// final en vez de uno por fila.
//
// 🔴 POR QUÉ AGREGAR EN VEZ DE AVISAR POR FILA (corrección del 2026-08-21,
// revisión de T4.1). El aviso vivía dentro de scanSession, o sea DENTRO del bucle
// de List, y sin acotar. El modo de fallo realista que el propio docstring de
// scanSession nombra —un keyring incompleto tras una rotación— no rompe UNA fila:
// rompe TODAS a la vez. Y el consumidor de List es el dashboard del BFF, que
// POLEA. El resultado era N líneas idénticas por poll, para siempre, sepultando
// el log justo en el momento en que el operador lo abre para diagnosticar la
// rotación mal cerrada. Un aviso que solo se puede leer cuando no hace falta no
// es un aviso.
//
// 🔴 POR QUÉ AGREGADO Y NO MUESTREO. Se descartó el muestreo (1 de cada N, o uno
// cada X segundos) por dos razones. La primera: el muestreo pierde el CONTEO, que
// aquí es el dato que decide la gravedad —«1 fila ilegible» es una fila corrupta,
// «las 40» es una KEK que falta— y es justo lo que el operador necesita para
// distinguirlas. La segunda: un muestreo temporal exige estado compartido y por
// tanto un mutex en un repositorio que hoy NO tiene estado mutable (lo dice su
// propio docstring), y ese candado se pagaría en TODAS las lecturas para acotar
// un caso excepcional. El tally vive en la pila de la llamada: cero contención,
// cero estado en el repositorio, y una línea por llamada como cota dura.
//
// ⚠️ CERO PII, igual que antes: ni el número (que no se pudo obtener) ni el
// contenido del sobre. Solo identidades opacas y la causa. El key_id SÍ va —dice
// QUÉ KEK falta y no revela nada del número (§10.I)— y se conserva el de la
// PRIMERA fila que falló: en el fallo masivo todas comparten el mismo key_id, que
// es precisamente el dato accionable.
type selfPnDecryptTally struct {
	failed       int
	firstTenant  string
	firstEdge    string
	firstSession string
	firstKekID   string
	firstErr     error
}

// record anota un sobre que no abrió. Solo el PRIMERO deja muestra: los demás
// suman al contador.
func (t *selfPnDecryptTally) record(tenantID, edgeID, sessionID, kekID string, err error) {
	t.failed++
	if t.failed == 1 {
		t.firstTenant, t.firstEdge, t.firstSession = tenantID, edgeID, sessionID
		t.firstKekID, t.firstErr = kekID, err
	}
}

// flush emite el aviso agregado, o nada si no hubo fallos (el caso normal). Se
// llama SIEMPRE al terminar la lectura, incluso en el camino de error: si el
// listado se cortó a media iteración, las filas que ya fallaron siguen siendo
// información válida sobre el estado del keyring.
func (t *selfPnDecryptTally) flush(log Logger) {
	if t.failed == 0 {
		return
	}
	log.Warn("fleet: hay self_pn que no se pudieron descifrar; se sirven vacíos",
		"filas_afectadas", t.failed,
		"muestra_tenant_id", t.firstTenant, "muestra_edge_id", t.firstEdge,
		"muestra_session_id", t.firstSession,
		"muestra_kek_id", t.firstKekID, "error", t.firstErr)
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
	s.Profile = Profile(profile)
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
