//go:build pendiente

package fleet

// Los tests de fichero del núcleo de fleet.PostgresRepository (constructor, logger, Mark*,
// SetState, Get, List y el escaneo), con un driver de database/sql de mentira
// (repository_postgres_fakedb_test.go): el texto EXACTO de cada sentencia, sus argumentos y el
// mapeo de filas y errores. Que ese SQL haga en un Postgres de verdad lo que dice lo prueba la
// suite fleethelpertest.ContratoRepository en los procesos de F9 (sesión F3-05, D-F3-8).

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// PostgresRepository cumple el puerto entero, y el espía cumple el puerto estrecho de log.
var (
	_ Repository = (*PostgresRepository)(nil)
	_ Logger     = (*spyLogger)(nil)
)

// Las sentencias del núcleo, byte a byte, escritas aquí a mano y NO copiadas de una constante de
// producción: si alguien toca el SQL del adaptador, este fichero lo dice. Los espacios en blanco
// (el salto inicial, las dos tabulaciones de sangría, la tabulación final) son los del literal
// del paquete viejo, internal/gateway/fleet/repository_postgres.go @ 809345b.
const (
	// MarkOnline: el INSERT no nombra `profile` (la sesión nace con el DEFAULT 'passive' del
	// esquema) y el SET del ON CONFLICT tampoco: reconectar conserva el perfil.
	sqlMarkOnline = "\n" +
		"\t\tINSERT INTO public.fleet_sessions\n" +
		"\t\t\t(tenant_id, edge_id, session_id, state, last_connected_at, last_seen_at, updated_at)\n" +
		"\t\tVALUES ($1, $2, $3, 'online', now(), now(), now())\n" +
		"\t\tON CONFLICT (tenant_id, edge_id, session_id) DO UPDATE\n" +
		"\t\tSET state = 'online',\n" +
		"\t\t    last_connected_at = now(),\n" +
		"\t\t    last_seen_at = now(),\n" +
		"\t\t    updated_at = now()\n" +
		"\t"
	sqlMarkOffline = "\n" +
		"\t\tUPDATE public.fleet_sessions\n" +
		"\t\tSET state = 'offline', last_seen_at = now(), updated_at = now()\n" +
		"\t\tWHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3\n" +
		"\t"
	sqlMarkLoggedOut = "\n" +
		"\t\tUPDATE public.fleet_sessions\n" +
		"\t\tSET state = 'loggedout', last_seen_at = now(), updated_at = now()\n" +
		"\t\tWHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3\n" +
		"\t"
	// SetState acota por tenant + sesión, SIN edge: toca todas las filas de la sesión.
	sqlSetState = "\n" +
		"\t\tUPDATE public.fleet_sessions\n" +
		"\t\tSET state = $3, last_seen_at = now(), updated_at = now()\n" +
		"\t\tWHERE tenant_id = $1 AND session_id = $2\n" +
		"\t"
	// Las 27 columnas que comparten Get y List. No está `greeted_at`, no está la columna en
	// claro `self_pn`, y el bloque del worker va SIN COALESCE.
	sqlSessionCols = "\n" +
		"\t\tSELECT tenant_id::text, edge_id, session_id, state,\n" +
		"\t\t       COALESCE(profile, 'passive'),\n" +
		"\t\t       self_pn_enc, self_pn_dek, self_pn_kek_id,\n" +
		"\t\t       COALESCE(last_connected_at, 'epoch'), COALESCE(last_seen_at, 'epoch'),\n" +
		"\t\t       COALESCE(whatsapp_state, ''), COALESCE(degraded_reason, ''),\n" +
		"\t\t       degraded_since, last_health_at,\n" +
		"\t\t       COALESCE(last_event_age_s, 0), COALESCE(outbox_depth, 0),\n" +
		"\t\t       COALESCE(binary_version, ''), COALESCE(uptime_s, 0),\n" +
		"\t\t       COALESCE(dek_load_duration_ms, 0), COALESCE(intent_circuit, ''),\n" +
		"\t\t       COALESCE(worker_taskset, ''), intent_p50_ms, intent_omitted_by_reason,\n" +
		"\t\t       stuck_heads, stuck_head_polls, failed_seal_dispatch, failed_seal_budget"
	sqlGet = sqlSessionCols + "\n" +
		"\t\tFROM public.fleet_sessions\n" +
		"\t\tWHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3\n" +
		"\t"
	sqlList = sqlSessionCols + "\n" +
		"\t\tFROM public.fleet_sessions\n" +
		"\t\tWHERE tenant_id = $1\n" +
		"\t\tORDER BY edge_id, session_id\n" +
		"\t"
)

// warnUndecryptable es el único aviso del adaptador, literal.
const warnUndecryptable = "fleet: hay self_pn que no se pudieron descifrar; se sirven vacíos"

// pgEpoch es lo que la sentencia devuelve en una marca de tiempo NULL: COALESCE(…, 'epoch').
var pgEpoch = time.Unix(0, 0).UTC()

// sessionRow son las 27 columnas de la sentencia, en su orden. Las que pueden venir NULL son
// driver.Value para poder guionizar un nil sin tipo.
type sessionRow struct {
	tenant, edge, session, state, profile string
	enc, dek, kekID                       driver.Value
	lastConnected, lastSeen               time.Time
	whatsappState, degradedReason         string
	degradedSince, lastHealthAt           driver.Value
	lastEventAgeS, outboxDepth            int64
	binaryVersion                         string
	uptimeS, dekLoadMs                    int64
	intentCircuit, workerTaskset          string
	p50, omitted                          driver.Value
	stuckHeads, stuckPolls                driver.Value
	sealDispatch, sealBudget              driver.Value
}

func (r sessionRow) values() []driver.Value {
	return []driver.Value{
		r.tenant, r.edge, r.session, r.state, r.profile,
		r.enc, r.dek, r.kekID,
		r.lastConnected, r.lastSeen,
		r.whatsappState, r.degradedReason, r.degradedSince, r.lastHealthAt,
		r.lastEventAgeS, r.outboxDepth, r.binaryVersion, r.uptimeS,
		r.dekLoadMs, r.intentCircuit,
		r.workerTaskset, r.p50, r.omitted,
		r.stuckHeads, r.stuckPolls, r.sealDispatch, r.sealBudget,
	}
}

// bareRow es la fila de una sesión que nunca reportó nada: todo lo anulable en NULL y lo demás en
// el cero que le pone el COALESCE de la sentencia.
func bareRow(edge, session string) sessionRow {
	return sessionRow{
		tenant: pgTenant, edge: edge, session: session, state: "online", profile: "passive",
		lastConnected: pgEpoch, lastSeen: pgEpoch,
	}
}

// withEnvelope sella plain con cipher y lo pone en la fila.
func (r sessionRow) withEnvelope(t *testing.T, cipher *crypto.FieldCipher, plain string) sessionRow {
	t.Helper()
	enc, dek, kekID, err := cipher.Encrypt(plain)
	if err != nil {
		t.Fatalf("sellar el self_pn de la fila: %v", err)
	}
	r.enc, r.dek, r.kekID = enc, dek, kekID
	return r
}

// foreignCipher sella con una KEK ("Z") que NO está en el keyring del montaje: el sobre no abre.
func foreignCipher(t *testing.T) *crypto.FieldCipher {
	t.Helper()
	return crypto.NewFieldCipher(keyringKP(t, "Z:"+key32(0x33), "Z"))
}

func int64p(v int64) *int64 { return &v }

// requireWarning afirma que el logger recibió UN solo aviso, el literal, con el conteo y la
// muestra dados, y devuelve el error que viaja en él.
func requireWarning(t *testing.T, log *spyLogger, failed int, edge, session, kekID string) error {
	t.Helper()
	warnings := log.seen()
	if len(warnings) != 1 {
		t.Fatalf("el logger recibió %d avisos, quería 1 (uno por llamada, no uno por fila): %+v", len(warnings), warnings)
	}
	w := warnings[0]
	if w.msg != warnUndecryptable {
		t.Errorf("aviso = %q, quería el literal %q", w.msg, warnUndecryptable)
	}
	want := []any{
		"filas_afectadas", failed,
		"muestra_tenant_id", pgTenant, "muestra_edge_id", edge,
		"muestra_session_id", session,
		"muestra_kek_id", kekID, "error",
	}
	if len(w.args) != len(want)+1 || !reflect.DeepEqual(w.args[:len(want)], want) {
		t.Fatalf("claves y valores del aviso = %#v, quería %#v más el error", w.args, want)
	}
	cause, ok := w.args[len(want)].(error)
	if !ok || cause == nil {
		t.Fatalf("el valor de la clave error es %#v, quería un error", w.args[len(want)])
	}
	return cause
}

// TestNewPostgresRepository_DoesNotTouchTheDatabase: construir no abre ni consulta nada.
func TestNewPostgresRepository_DoesNotTouchTheDatabase(t *testing.T) {
	fake := &fakeDB{}
	kp := keyringKP(t, fixtureKeyring, fixtureCurrent)
	repo := NewPostgresRepository(fake.open(t), crypto.NewFieldCipher(kp), kp)
	if repo == nil {
		t.Fatal("NewPostgresRepository devolvió nil")
	}
	requireStatements(t, fake)
}

// TestWithLogger: el logger enchufado recibe el aviso; sin logger, o con WithLogger(nil), el
// aviso se pierde pero la lectura se sirve igual (no hay puntero nil que desreferenciar).
func TestWithLogger(t *testing.T) {
	spy := &spyLogger{}
	cases := []struct {
		name  string
		opts  []Option
		warns int
	}{
		{"plugged logger gets the warning", []Option{WithLogger(spy)}, 1},
		{"nil logger is ignored", []Option{WithLogger(spy), WithLogger(nil)}, 2},
		{"no option at all", nil, 2},
		{"only a nil logger", []Option{WithLogger(nil)}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeDB{}
			fake.script(reply{rows: [][]driver.Value{
				bareRow(pgEdge, pgSession).withEnvelope(t, foreignCipher(t), "34600111222").values(),
			}})
			kp := keyringKP(t, fixtureKeyring, fixtureCurrent)
			repo := NewPostgresRepository(fake.open(t), crypto.NewFieldCipher(kp), kp, c.opts...)
			s, found, err := repo.Get(context.Background(), pgTenant, pgEdge, pgSession)
			if err != nil || !found || s.SelfPn != "" {
				t.Fatalf("Get = (SelfPn %q, %v, %v), quería la sesión con SelfPn vacío y sin error", s.SelfPn, found, err)
			}
			if got := len(spy.seen()); got != c.warns {
				t.Errorf("el espía lleva %d avisos, quería %d", got, c.warns)
			}
		})
	}
}

// markCases son las tres escrituras por identidad completa, que comparten forma.
func markCases() []struct {
	name   string
	call   func(*PostgresRepository) error
	query  string
	prefix string
} {
	ctx := context.Background()
	return []struct {
		name   string
		call   func(*PostgresRepository) error
		query  string
		prefix string
	}{
		{"MarkOnline", func(r *PostgresRepository) error { return r.MarkOnline(ctx, pgTenant, pgEdge, pgSession) },
			sqlMarkOnline, "fleet: marcar online: "},
		{"MarkOffline", func(r *PostgresRepository) error { return r.MarkOffline(ctx, pgTenant, pgEdge, pgSession) },
			sqlMarkOffline, "fleet: marcar offline: "},
		{"MarkLoggedOut", func(r *PostgresRepository) error { return r.MarkLoggedOut(ctx, pgTenant, pgEdge, pgSession) },
			sqlMarkLoggedOut, "fleet: marcar loggedout: "},
	}
}

// TestPostgresRepository_Mark_Statement: la sentencia exacta y sus tres argumentos; y cero filas
// afectadas (la sesión no existía) no es un error.
func TestPostgresRepository_Mark_Statement(t *testing.T) {
	for _, c := range markCases() {
		for _, affected := range []int64{0, 1} {
			f := newFixture(t, reply{affected: affected})
			if err := c.call(f.repo); err != nil {
				t.Errorf("%s con %d filas afectadas: error inesperado %v", c.name, affected, err)
			}
			requireStatements(t, f.fake, statement{c.query, []driver.Value{pgTenant, pgEdge, pgSession}})
		}
	}
}

// TestPostgresRepository_Mark_DriverError: el fallo del driver vuelve envuelto con su prefijo.
func TestPostgresRepository_Mark_DriverError(t *testing.T) {
	for _, c := range markCases() {
		f := newFixture(t, reply{err: errBoom})
		requireWrapped(t, c.call(f.repo), errBoom, c.prefix)
	}
}

// TestPostgresRepository_MarkOnline_NeverNamesTheProfile: ni el INSERT ni el SET nombran el
// perfil ni el reloj del eje; una sesión nace con el DEFAULT del esquema y reconectar no lo mueve.
func TestPostgresRepository_MarkOnline_NeverNamesTheProfile(t *testing.T) {
	if strings.Contains(sqlMarkOnline, "profile") {
		t.Errorf("la sentencia de MarkOnline nombra profile o profile_updated_at:\n%s", sqlMarkOnline)
	}
}

// TestPostgresRepository_SetState_InvalidState: un estado fuera de offline|loggedout se rechaza
// con el centinela y SIN que llegue nada al driver.
func TestPostgresRepository_SetState_InvalidState(t *testing.T) {
	for _, state := range []State{StateOnline, "", "OFFLINE", "zombie"} {
		f := newFixture(t, reply{affected: 1})
		found, err := f.repo.SetState(context.Background(), pgTenant, pgSession, state)
		if found || !errors.Is(err, ErrInvalidState) {
			t.Errorf("SetState(%q) = (%v, %v), quería (false, ErrInvalidState)", state, found, err)
		}
		requireStatements(t, f.fake)
	}
}

// TestPostgresRepository_SetState: la sentencia exacta, el estado como texto en $3, y found según
// las filas afectadas (cero ⇒ no existe o es de otro tenant; varias ⇒ una fila por Edge).
func TestPostgresRepository_SetState(t *testing.T) {
	cases := []struct {
		state    State
		affected int64
		want     bool
	}{
		{StateOffline, 0, false},
		{StateOffline, 1, true},
		{StateLoggedOut, 3, true},
	}
	for _, c := range cases {
		f := newFixture(t, reply{affected: c.affected})
		found, err := f.repo.SetState(context.Background(), pgTenant, pgSession, c.state)
		if err != nil || found != c.want {
			t.Errorf("SetState(%q) con %d filas = (%v, %v), quería (%v, nil)", c.state, c.affected, found, err, c.want)
		}
		requireStatements(t, f.fake, statement{sqlSetState, []driver.Value{pgTenant, pgSession, string(c.state)}})
	}
}

// TestPostgresRepository_SetState_Errors: el fallo del driver y el de leer las filas afectadas
// vuelven envueltos, cada uno con su prefijo, y con found=false.
func TestPostgresRepository_SetState_Errors(t *testing.T) {
	cases := []struct {
		name   string
		reply  reply
		prefix string
	}{
		{"driver error", reply{err: errBoom}, "fleet: fijar estado: "},
		{"rows affected error", reply{affected: 1, affectedErr: errBoom}, "fleet: filas afectadas al fijar estado: "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, c.reply)
			found, err := f.repo.SetState(context.Background(), pgTenant, pgSession, StateOffline)
			if found {
				t.Error("found = true con error, quería false")
			}
			requireWrapped(t, err, errBoom, c.prefix)
		})
	}
}

// TestPostgresRepository_Get_NoRow: una sesión que no existe es (Session{}, false, nil), con la
// sentencia exacta y sus tres argumentos.
func TestPostgresRepository_Get_NoRow(t *testing.T) {
	f := newFixture(t)
	s, found, err := f.repo.Get(context.Background(), pgTenant, pgEdge, pgSession)
	if err != nil || found || !reflect.DeepEqual(s, Session{}) {
		t.Errorf("Get sin fila = (%+v, %v, %v), quería (Session{}, false, nil)", s, found, err)
	}
	requireStatements(t, f.fake, statement{sqlGet, []driver.Value{pgTenant, pgEdge, pgSession}})
	if got := f.log.seen(); len(got) != 0 {
		t.Errorf("Get sin fila dejó avisos: %+v", got)
	}
}

// TestPostgresRepository_Get_MapsEveryColumn: cada una de las 27 columnas a su campo, con el
// self_pn descifrado en memoria.
func TestPostgresRepository_Get_MapsEveryColumn(t *testing.T) {
	connected := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	seen := connected.Add(time.Minute)
	degraded := connected.Add(2 * time.Minute)
	health := connected.Add(3 * time.Minute)
	f := newFixture(t)
	row := sessionRow{
		tenant: pgTenant, edge: pgEdge, session: pgSession, state: "loggedout", profile: "active",
		lastConnected: connected, lastSeen: seen,
		whatsappState: "degraded", degradedReason: "dek_load_timeout",
		degradedSince: degraded, lastHealthAt: health,
		lastEventAgeS: 11, outboxDepth: 12, binaryVersion: "v1.2.3", uptimeS: 13,
		dekLoadMs: 14, intentCircuit: "half_open",
		workerTaskset: "disjunta", p50: int64(15), omitted: []byte(`{"fastlane":5,"breaker":2}`),
		stuckHeads: int64(16), stuckPolls: int64(17), sealDispatch: int64(18), sealBudget: int64(19),
	}.withEnvelope(t, f.cipher, "34600111222")
	f.fake.script(reply{rows: [][]driver.Value{row.values()}})

	got, found, err := f.repo.Get(context.Background(), pgTenant, pgEdge, pgSession)
	if err != nil || !found {
		t.Fatalf("Get = (found %v, err %v), quería la sesión", found, err)
	}
	want := Session{
		TenantID: pgTenant, EdgeID: pgEdge, SessionID: pgSession,
		State: StateLoggedOut, Profile: ProfileActive, SelfPn: "34600111222",
		LastConnectedAt: connected, LastSeenAt: seen,
		WhatsappState: "degraded", DegradedReason: "dek_load_timeout",
		DegradedSince: degraded, LastHealthAt: health,
		LastEventAgeS: 11, OutboxDepth: 12, BinaryVersion: "v1.2.3", UptimeS: 13,
		DekLoadDurationMs: 14, IntentCircuit: "half_open",
		WorkerTaskset: "disjunta", IntentP50Ms: int64p(15),
		IntentOmittedByReason: map[string]int64{"fastlane": 5, "breaker": 2},
		StuckHeads:            int64p(16), StuckHeadPolls: int64p(17),
		FailedSealDispatch: int64p(18), FailedSealBudget: int64p(19),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Get =\n%+v\nquería\n%+v", got, want)
	}
	if got := f.log.seen(); len(got) != 0 {
		t.Errorf("una fila que descifra bien dejó avisos: %+v", got)
	}
}

// TestPostgresRepository_Get_NullIsNotZero: lo anulable en NULL vuelve en su cero o en nil («no
// lo sé»), un 0 MEDIDO vuelve como puntero a 0, y el 'epoch' del COALESCE llega tal cual (1970,
// que NO es el time.Time cero).
func TestPostgresRepository_Get_NullIsNotZero(t *testing.T) {
	t.Run("everything null", func(t *testing.T) {
		f := newFixture(t, reply{rows: [][]driver.Value{bareRow(pgEdge, pgSession).values()}})
		got, _, err := f.repo.Get(context.Background(), pgTenant, pgEdge, pgSession)
		if err != nil {
			t.Fatalf("Get: error inesperado %v", err)
		}
		want := Session{
			TenantID: pgTenant, EdgeID: pgEdge, SessionID: pgSession,
			State: StateOnline, Profile: ProfilePassive,
			LastConnectedAt: pgEpoch, LastSeenAt: pgEpoch,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Get =\n%+v\nquería\n%+v", got, want)
		}
		if got.LastConnectedAt.IsZero() || !got.DegradedSince.IsZero() || !got.LastHealthAt.IsZero() {
			t.Errorf("marcas de tiempo: last_connected_at %v (quería 1970, no el cero), degraded_since %v y last_health_at %v (quería el cero)",
				got.LastConnectedAt, got.DegradedSince, got.LastHealthAt)
		}
	})
	t.Run("measured zero is a pointer to zero", func(t *testing.T) {
		row := bareRow(pgEdge, pgSession)
		row.p50, row.stuckHeads, row.stuckPolls = int64(0), int64(0), int64(0)
		row.sealDispatch, row.sealBudget = int64(0), int64(0)
		f := newFixture(t, reply{rows: [][]driver.Value{row.values()}})
		got, _, err := f.repo.Get(context.Background(), pgTenant, pgEdge, pgSession)
		if err != nil {
			t.Fatalf("Get: error inesperado %v", err)
		}
		pointers := map[string]*int64{
			"IntentP50Ms": got.IntentP50Ms, "StuckHeads": got.StuckHeads, "StuckHeadPolls": got.StuckHeadPolls,
			"FailedSealDispatch": got.FailedSealDispatch, "FailedSealBudget": got.FailedSealBudget,
		}
		for name, p := range pointers {
			if p == nil || *p != 0 {
				t.Errorf("%s = %v, quería un puntero a 0 (medido, no desconocido)", name, p)
			}
		}
	})
	t.Run("empty breakdown is nil", func(t *testing.T) {
		row := bareRow(pgEdge, pgSession)
		row.omitted = []byte{}
		f := newFixture(t, reply{rows: [][]driver.Value{row.values()}})
		got, _, err := f.repo.Get(context.Background(), pgTenant, pgEdge, pgSession)
		if err != nil || got.IntentOmittedByReason != nil {
			t.Errorf("desglose vacío = (%v, %v), quería (nil, nil)", got.IntentOmittedByReason, err)
		}
	})
}

// TestPostgresRepository_Get_Profile: el perfil llega tal cual, salvo el vacío, que se lee como
// pasivo (defaultProfile); un valor desconocido pasa intacto.
func TestPostgresRepository_Get_Profile(t *testing.T) {
	cases := map[string]Profile{"active": ProfileActive, "passive": ProfilePassive, "": ProfilePassive, "bot": "bot"}
	for column, want := range cases {
		row := bareRow(pgEdge, pgSession)
		row.profile = column
		f := newFixture(t, reply{rows: [][]driver.Value{row.values()}})
		got, _, err := f.repo.Get(context.Background(), pgTenant, pgEdge, pgSession)
		if err != nil || got.Profile != want {
			t.Errorf("columna profile %q: Profile = %q, err = %v; quería %q", column, got.Profile, err, want)
		}
	}
}

// TestPostgresRepository_Get_Errors: el fallo del driver y un desglose de motivos que no es JSON
// son errores DUROS (no hay sesión a medias), con el prefijo de la lectura.
func TestPostgresRepository_Get_Errors(t *testing.T) {
	t.Run("driver error", func(t *testing.T) {
		f := newFixture(t, reply{err: errBoom})
		s, found, err := f.repo.Get(context.Background(), pgTenant, pgEdge, pgSession)
		if found || !reflect.DeepEqual(s, Session{}) {
			t.Errorf("Get con error = (%+v, %v), quería (Session{}, false)", s, found)
		}
		requireWrapped(t, err, errBoom, "fleet: leer sesión: ")
	})
	t.Run("breakdown is not json", func(t *testing.T) {
		row := bareRow(pgEdge, pgSession)
		row.omitted = []byte(`{"fastlane":`)
		f := newFixture(t, reply{rows: [][]driver.Value{row.values()}})
		s, found, err := f.repo.Get(context.Background(), pgTenant, pgEdge, pgSession)
		if found || !reflect.DeepEqual(s, Session{}) {
			t.Errorf("Get con JSON roto = (%+v, %v), quería (Session{}, false)", s, found)
		}
		const prefix = "fleet: leer sesión: fleet: deserializar desglose de motivos: "
		if err == nil || !strings.HasPrefix(err.Error(), prefix) {
			t.Errorf("err = %v, quería el prefijo %q", err, prefix)
		}
	})
}

// TestPostgresRepository_List_NoRows: un tenant sin sesiones devuelve una lista vacía sin error,
// con la sentencia exacta y el tenant como único argumento. El resto de List, en
// repository_postgres_list_test.go.
func TestPostgresRepository_List_NoRows(t *testing.T) {
	f := newFixture(t)
	got, err := f.repo.List(context.Background(), pgTenant)
	if err != nil || len(got) != 0 {
		t.Errorf("List sin filas = (%+v, %v), quería una lista vacía y nil", got, err)
	}
	requireStatements(t, f.fake, statement{sqlList, []driver.Value{pgTenant}})
}
