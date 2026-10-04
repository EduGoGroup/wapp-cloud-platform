//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// P10 · Plataforma (diseno.md §4, T9.35, D-F9-4). Re-expresa por las puertas reales las reglas de
// los 9 ficheros de integración con BD de internal/platform/ (F10-relevo/diseno.md §6), que
// sobreviven al relevo y hoy leen WAPP_TEST_DB_DSN. Las puertas de este proceso son tres:
//
//   - cmd/migrate como subproceso sobre una base clonada (p10_plataforma_migrations_test.go): el
//     full-replay, su idempotencia y lo que deja en el catálogo;
//   - el servidor: /healthz, las rutas con permiso de los dos planos (p10_plataforma_grants_test.go),
//     POST /admin/crypto/rekey (p10_plataforma_rekey_test.go) y /metrics para el colector de
//     flow_events (p10_plataforma_collector_test.go);
//   - el gRPC del Edge de prueba, para que los sobres cifrados que mira la rotación los escriba el
//     propio servidor.
//
// Este fichero trae el mundo que comparten los subtests del servidor, las ayudas comunes y el
// cierre. Lo que NO se pudo expresar sin ampliar el arnés está dicho en el comentario de cada
// fichero y en el cuerpo del commit.

const (
	// p10PollEvery es cada cuánto se sondea lo que tarda segundos en ocurrir (el colector da una
	// vuelta cada 15 s: trampa T-6); p10PollTimeout es el tope de esos sondeos.
	p10PollEvery   = 250 * time.Millisecond
	p10PollTimeout = 60 * time.Second
)

// p10World es lo que comparten los subtests de TestP10_Platform: el servidor con la empresa del
// proceso, su administradora y un Edge activo con el menú de prueba (el escenario de P3), más las
// otras personas que llaman por las puertas y lo que cada subtest deja para los siguientes.
type p10World struct {
	sc p3Scene
	// calls solo presta su método call: reintenta el 429 sondeando y apunta la fila de
	// audit_events que cada llamada tiene que dejar (hallazgo 32 de F9: no se duplica).
	calls *p9World

	admin    p9Caller // administradora de la empresa del proceso, en el listener admin
	other    p9Caller // administradora de OTRA empresa, en el listener admin
	staff    p9Caller // staff de plataforma, en el listener admin
	viewer   p10Person
	operator p10Person

	otherTenant string // la otra empresa: destino de las rutas de plataforma y dueña de los sobres de fixture

	env     p10Envelopes       // lo que dejó rekey_envelopes
	metrics map[string]float64 // las series del colector tras el último subtest que las movió
	errors  map[string]int     // líneas ERROR del log del servidor que el proceso provoca a sabiendas
}

// p10Person es alguien de una empresa con un rol: su id y su Context Token.
type p10Person struct {
	ID    string
	Token string
}

// TestP10_Platform es el proceso P10 contra el servidor. Los subtests comparten UN servidor y
// corren EN ORDEN (ninguno es paralelo): cada uno parte de lo que dejó el anterior. Se afirma lo
// que hace el binario viejo; el mismo test corre contra el nuevo sin distinguirlos.
func TestP10_Platform(t *testing.T) {
	t.Parallel()
	w := p10NewWorld(t)

	// integration_test.go · TestIntegration_HealthCheck: el check «postgres» sano, visto en /healthz.
	t.Run("postgres_health", func(t *testing.T) { p0HealthzAdmin(t, w.sc.S) })
	t.Run("grants_seed", func(t *testing.T) { p10CheckGrantSeeds(t, w.sc.DB) })
	t.Run("grants_role_plane", w.grantsRolePlane)
	t.Run("grants_perimeters", w.grantsPerimeters)
	t.Run("grants_deny_rows_hold", w.grantsDenyRowsHold)
	t.Run("collector_series", w.collectorSeries)
	t.Run("collector_no_recount", w.collectorNoRecount)
	t.Run("collector_leader_lock", w.collectorLeaderLock)
	t.Run("rekey_envelopes", w.rekeyEnvelopes)
	t.Run("rekey_noop_pass", w.rekeyNoopPass)
	t.Run("rekey_census", w.rekeyCensus)
	t.Run("rekey_adversarial", w.rekeyAdversarial)
	t.Run("rekey_audit", w.rekeyAudit)
	t.Run("closing", w.closing)
}

// p10NewWorld arranca el servidor del proceso con el escenario de P3 (empresa, administradora, Edge
// activo y menú) y da de alta al resto de quienes llaman: un viewer y un operator de la empresa, y
// otra empresa con su administradora. El Edge se conecta con el test del PROCESO (hallazgo 33).
func p10NewWorld(t *testing.T) *p10World {
	t.Helper()
	sc := p3ActiveMenuScene(t, "p10", "p10-plataforma")
	w := &p10World{
		sc:      sc,
		calls:   &p9World{root: t, audit: map[string]int{}},
		admin:   p9Caller{client: sc.S.Admin(sc.TokenAdmin), tenant: sc.Tenant},
		staff:   p9Caller{client: sc.S.Admin(sc.TokenStaff), tenant: tenantPlataformaID},
		metrics: map[string]float64{},
		errors:  map[string]int{},
	}
	w.viewer = p10NewMember(t, sc.edgeEscenario, sc.Tenant, p2RoleViewer)
	w.operator = p10NewMember(t, sc.edgeEscenario, sc.Tenant, p2RoleOperator)

	w.otherTenant = crearTenant(t, sc.S, sc.TokenStaff, "p10-otra-empresa")
	otherAdmin := p10NewMember(t, sc.edgeEscenario, w.otherTenant, edgeRolTenantAdmin)
	w.other = p9Caller{client: sc.S.Admin(otherAdmin.Token), tenant: w.otherTenant}
	return w
}

// p10NewMember da de alta a una persona nueva en la empresa con ese rol (p2AddMember: no hay puerta
// HTTP sin identity-core) y canjea su token.
func p10NewMember(t *testing.T, esc edgeEscenario, tenant, role string) p10Person {
	t.Helper()
	id := uuidAleatorio(t)
	p2AddMember(t, esc.DB, id, tenant, role)
	return p10Person{ID: id, Token: p2Exchange(t, esc.S, id).ContextToken}
}

// p10Poll sondea cond cada p10PollEvery hasta que devuelve true, con tope. Falla (t.Fatalf) con la
// descripción —que se evalúa al vencer, para decir el último valor visto— si no se da.
func p10Poll(t *testing.T, timeout time.Duration, describe func() string, cond func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	tick := time.NewTicker(p10PollEvery)
	defer tick.Stop()
	for {
		if cond() {
			return
		}
		select {
		case <-ctx.Done():
			if !cond() {
				t.Fatalf("pasaron %s sin que se diera: %s", timeout, describe())
			}
			return
		case <-tick.C:
		}
	}
}

// p10Exec ejecuta una sentencia de fixture y devuelve cuántas filas tocó. Falla (t.Fatalf) si el
// SQL falla.
func p10Exec(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), topeFixture)
	defer cancel()
	res, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("fixture %q: filas tocadas: %v", query, err)
	}
	return n
}

// p10ExecErr ejecuta una sentencia que el esquema puede rechazar y devuelve el error tal cual.
func p10ExecErr(t *testing.T, db *sql.DB, query string, args ...any) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), topeFixture)
	defer cancel()
	_, err := db.ExecContext(ctx, query, args...)
	return err
}

// p10Diff describe en qué se diferencian dos mapas de series o de contadores, clave a clave.
func p10Diff(got, want map[string]float64) string {
	keys := slices.Collect(maps.Keys(got))
	for k := range want {
		if _, ok := got[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var out []string
	for _, k := range keys {
		g, hasG := got[k]
		w, hasW := want[k]
		if hasG != hasW || g != w {
			out = append(out, fmt.Sprintf("%q: vale %v (presente: %v), quería %v (presente: %v)", k, g, hasG, w, hasW))
		}
	}
	if len(out) == 0 {
		return "sin diferencias"
	}
	return strings.Join(out, "; ")
}

// closing es el cierre del proceso, ANTES de parar el servidor: el núcleo del Edge no anotó
// errores, no quedó ningún SendText sin leer y el log del servidor no trae más líneas ERROR que
// las que el proceso provocó a sabiendas.
func (w *p10World) closing(t *testing.T) {
	if errs := w.sc.Edge.Errores(); len(errs) != 0 {
		t.Errorf("el núcleo del Edge anotó errores: %v", errs)
	}
	w.sc.expectNoPendingText(t, "al cerrar el proceso")
	if r := w.sc.S.Admin("").Get(t, "/healthz", nil); r.Codigo != http.StatusOK {
		t.Errorf("GET /healthz al cerrar = %d, quería 200", r.Codigo)
	}
	edgeSinErrores(t, w.sc.S, w.errors)
	t.Logf("peticiones reintentadas por 429: %d", w.calls.throttled)
}
