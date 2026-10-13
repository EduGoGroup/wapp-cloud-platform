//go:build integracion

package procesos

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

// La cara del Edge por el cable, con REINICIO del servidor (diseno.md de F3 §6: «R-L2/R-L3/R-L8 con
// reinicio del proceso»). Los unitarios del lease afirman que revocar gana a emitir y a renovar
// dentro de UN Manager; aquí se afirma lo que solo se ve con dos procesos y un Postgres: el
// kill-switch no vive en la memoria del servidor. Un despliegue, o una caída, no des-revoca a nadie.
//
//   - R-L2 · el Edge con SU lease revocado sigue revocado tras reiniciar: al reconectar recibe la
//     revocación como lease inicial, un latido no lo resucita, y su fila de leases no se mueve (ni
//     el contador retrocede ni revoked vuelve a false). La revocación anticipada de un Edge que aún
//     no existe también sigue ahí.
//   - R-L3 · la empresa cortada sigue cortada: su Edge de antes vuelve revocado y un Edge que se
//     enrola DESPUÉS del reinicio nace revocado y sin fila en leases.
//   - R-L8 · la revocación sobrevive al reinicio de quien firma: el proceso nuevo firma con la misma
//     clave (el Validator del Edge, que solo tiene la pública, acepta sus leases y sus revocaciones).
//
// Y que lo demás sigue funcionando: el Edge que no estaba revocado vuelve a operar y recibe un
// envío, y reactivar la empresa en el proceso nuevo devuelve a sus Edge a operar.

const (
	// p1WireLeaseSnapshot es la foto de lo que el kill-switch guarda en Postgres: cada fila de leases
	// (de todas las empresas) con su contador, su revocación y su vencimiento, y qué empresas están
	// cortadas. Dos fotos iguales dicen que nada lo reescribió entre una y otra.
	p1WireLeaseSnapshot = `SELECT
		coalesce((SELECT string_agg(edge_id || '=' || counter || '/' || revoked || '@' || expires_at, ', ' ORDER BY edge_id) FROM public.leases), '')
		|| ' | ' ||
		coalesce((SELECT string_agg(slug || '=' || (revoked_at IS NOT NULL), ', ' ORDER BY slug) FROM public.tenants), '')`
	// p1WireEarlyEdge es el edge_id de la revocación anticipada: un Edge que nunca se enroló.
	p1WireEarlyEdge = "edge-que-aun-no-existe"
)

// p1WireRestartRun es el estado del proceso con reinicio: el de la cara por el cable (la empresa
// del proceso y la que la plataforma corta, en otherTenant) y sus Edge.
type p1WireRestartRun struct {
	*p1WireRun
	// cutEsc es el escenario visto desde la empresa cortada, para las esperas sobre sus leases.
	cutEsc edgeEscenario
	// revoked es el Edge al que se le revoca SU lease; kept, el de la misma empresa al que no;
	// cutEdge, el de la empresa que la plataforma corta; born, el que se enrola en ella DESPUÉS del
	// reinicio.
	revoked, kept, cutEdge, born *edge
	// snapshot es p1WireLeaseSnapshot justo antes de parar el servidor.
	snapshot string
}

// TestP1_EdgeFaceOverTheWireRestart deja dos kill-switch disparados (el lease de un Edge y una
// empresa entera), reinicia el servidor sobre la misma base y comprueba que los dos siguen
// disparados, por la puerta y en Postgres. Los Edge están CONECTADOS cuando el servidor se para,
// como en un despliegue. Los pasos van en orden y son llamadas, no subtests: cada uno reconecta
// Edges, y un stream vive lo que el contexto del test que lo abrió. Necesita Docker.
func TestP1_EdgeFaceOverTheWireRestart(t *testing.T) {
	t.Parallel()
	esc := edgeEscenarioNuevo(t, "p1_wire_restart", "p1-wire-restart", true)
	p := &p1WireRestartRun{p1WireRun: &p1WireRun{esc: esc, pub: esc.S.Publica(esc.TokenAdmin), admin: esc.S.Admin(esc.TokenAdmin)}}
	p.otherTenant = crearTenant(t, esc.S, esc.TokenStaff, "p1-wire-restart-cortada")
	p.cutEsc = edgeEscenario{S: esc.S, DB: esc.DB, Tenant: p.otherTenant}
	p.revoked, p.kept, p.cutEdge = p.newEdge(t, esc.Tenant), p.newEdge(t, esc.Tenant), p.newEdge(t, p.otherTenant)

	p.fireKillSwitches(t)
	p.restart(t)
	p.checkRevokedStays(t)
	p.checkKeptOperates(t)
	p.checkCutTenantStays(t)
	p.checkRestoreAfterRestart(t)
	p.closing(t)
}

// fireKillSwitches es el antes: los dos Edge de la empresa del proceso laten (sus contadores dejan
// de ser los de un Edge recién conectado), la administradora revoca el lease de uno y,
// anticipadamente, el de un Edge que aún no existe, y la plataforma corta la OTRA empresa. Deja en
// snapshot la foto del kill-switch.
func (p *p1WireRestartRun) fireKillSwitches(t *testing.T) {
	t.Helper()
	esc := p.esc
	for _, c := range []struct {
		e    *edge
		beat int64
	}{{p.revoked, 5}, {p.kept, 9}} {
		before := c.e.Leases()
		c.e.latir(t, c.beat)
		c.e.esperarLeases(t, before+1, edgeTopeFila)
		p1WaitLease(t, esc, c.e, c.beat+1, false)
	}
	for _, id := range []string{p.revoked.EdgeID, p1WireEarlyEdge} {
		if r := p.admin.Post(t, rutaLeaseRevoke, map[string]string{"edge_id": id}); !p1WireIs(r, http.StatusNoContent, "") {
			t.Fatalf("revocar el lease de %s: HTTP %d %q, quería 204 sin cuerpo", id, r.Codigo, recortar(r.Cuerpo))
		}
	}
	edgeEsperar(t, edgeTopeFila, "que el Edge quede revocado", p.revoked.revocado)
	p1WaitLease(t, esc, p.revoked, 6, true)
	if r := esc.S.Admin(esc.TokenStaff).Post(t, rutaTenantCut, map[string]string{"tenant_id": p.otherTenant}); !p1WireIs(r, http.StatusNoContent, "") {
		t.Fatalf("cortar la empresa: HTTP %d %q, quería 204 sin cuerpo", r.Codigo, recortar(r.Cuerpo))
	}
	edgeEsperar(t, edgeTopeFila, "que el Edge de la empresa cortada reciba el corte", p.cutEdge.revocado)
	edgeEsperarValor(t, esc.DB, "true", "tenants.revoked_at de la empresa cortada", p1TenantRevoked, p.otherTenant)
	if !p.kept.puedeOperar() || p.kept.revocado() {
		t.Fatalf("ningún kill-switch debía alcanzar al Edge que se conserva: puedeOperar=%v revocado=%v", p.kept.puedeOperar(), p.kept.revocado())
	}
	p.snapshot = p9Scalar(t, esc.DB, p1WireLeaseSnapshot)
}

// restart reinicia el servidor con los tres Edge conectados y comprueba que ni la parada ni el
// arranque tocaron el estado del kill-switch (la foto es la misma, la revocación anticipada sigue
// ahí) y que el proceso parado salió sin pánicos ni líneas ERROR.
func (p *p1WireRestartRun) restart(t *testing.T) {
	t.Helper()
	previous := serverRestart(t, p.esc.S)
	p1WireNoPanics(t, previous)
	for _, l := range lineasJSON(previous) {
		if l["level"] == "ERROR" {
			t.Errorf("línea ERROR inesperada en el log del proceso parado: %v", l)
		}
	}
	if got := p9Scalar(t, p.esc.DB, p1WireLeaseSnapshot); got != p.snapshot {
		t.Fatalf("parar y arrancar el servidor reescribió el estado del kill-switch:\nantes   %s\ndespués %s", p.snapshot, got)
	}
	edgeEsperarValor(t, p.esc.DB, "0/true", "la revocación anticipada tras reiniciar",
		`SELECT counter::text || '/' || revoked::text FROM public.leases WHERE tenant_id = $1::uuid AND edge_id = $2`, p.esc.Tenant, p1WireEarlyEdge)
}

// checkRevokedStays es R-L2 con reinicio: el Edge revocado reconecta contra el proceso nuevo y
// sigue revocado —recibe la revocación como lease inicial, un latido no lo resucita, su fila no se
// mueve— y un envío hacia él no se entrega.
func (p *p1WireRestartRun) checkRevokedStays(t *testing.T) {
	t.Helper()
	esc, e := p.esc, p.revoked
	e.conectar(t) // no falla: una revocación aceptada por el Validator no es un rechazo
	if e.puedeOperar() || !e.revocado() {
		t.Fatalf("tras reiniciar, el Edge revocado: puedeOperar=%v revocado=%v; quería falso y verdadero (el reinicio lo des-revocó)", e.puedeOperar(), e.revocado())
	}
	edgeEsperarValor(t, esc.DB, "online", "la sesión del Edge revocado tras reiniciar", edgeEstadoSesion, esc.Tenant, e.EdgeID, e.SessionID)
	before := e.Leases()
	e.latir(t, 6)
	e.esperarLeases(t, before+1, edgeTopeFila) // el servidor reafirma la revocación
	if e.puedeOperar() || !e.revocado() {
		t.Errorf("tras reiniciar, un latido resucitó al Edge revocado")
	}
	p1WaitLease(t, esc, e, 6, true)
	edgeCheckNotDelivered(t, esc, e, "tras reiniciar")
}

// checkKeptOperates: el Edge que no estaba revocado reconecta, vuelve a operar (el proceso nuevo
// firma con la misma clave de lease: su Validator lo acepta) y recibe un envío de la API pública.
func (p *p1WireRestartRun) checkKeptOperates(t *testing.T) {
	t.Helper()
	e := p.kept
	e.conectar(t)
	e.esperarLeases(t, 2, edgeTopeFila)
	if !e.puedeOperar() || e.revocado() {
		t.Fatalf("tras reiniciar, el Edge sin revocar: puedeOperar=%v revocado=%v; quería verdadero y falso", e.puedeOperar(), e.revocado())
	}
	const to, text = "573001110000", "hola tras el reinicio"
	r := p.pub.Post(t, rutaMessages, map[string]string{"session_id": e.SessionID, "to": to, "text": text})
	p1WireAcked(t, "enviar tras reiniciar", r, e, to, text)
}

// checkCutTenantStays es R-L3 con reinicio: la empresa cortada sigue cortada para el Edge que ya
// tenía (vuelve revocado y su fila de leases no se tocó: el corte vive en tenants) y para uno que
// se enrola contra el proceso nuevo (nace revocado y sin fila). Los únicos leases revocados siguen
// siendo los dos de antes, con sus contadores.
func (p *p1WireRestartRun) checkCutTenantStays(t *testing.T) {
	t.Helper()
	esc := p.esc
	p.cutEdge.conectar(t)
	p.born = enrolar(t, esc.S, edgeEmitirCodigo(t, esc.S, esc.TokenStaff, p.otherTenant))
	p.born.conectar(t)
	for name, e := range map[string]*edge{"el Edge de antes": p.cutEdge, "el Edge enrolado después": p.born} {
		if e.puedeOperar() || !e.revocado() {
			t.Errorf("tras reiniciar, %s de la empresa cortada: puedeOperar=%v revocado=%v; quería falso y verdadero", name, e.puedeOperar(), e.revocado())
		}
		edgeEsperarValor(t, esc.DB, "online", "la sesión de "+name, edgeEstadoSesion, p.otherTenant, e.EdgeID, e.SessionID)
	}
	p1WaitLease(t, p.cutEsc, p.cutEdge, 2, false)
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.leases WHERE tenant_id = $1::uuid AND edge_id = $2`, p.otherTenant, p.born.EdgeID); n != 0 {
		t.Errorf("el Edge nacido con la empresa cortada dejó %d filas en leases, quería 0", n)
	}
	want := []string{p1WireEarlyEdge + "=0/true", p.revoked.EdgeID + "=6/true"}
	slices.Sort(want)
	if got := p9Scalar(t, esc.DB, `SELECT string_agg(edge_id || '=' || counter || '/' || revoked, ', ' ORDER BY edge_id COLLATE "C")
		FROM public.leases WHERE revoked`); got != strings.Join(want, ", ") {
		t.Errorf("leases revocados tras reiniciar y reconectar = %q; quería %q (los dos de antes, con sus contadores)", got, strings.Join(want, ", "))
	}
}

// checkRestoreAfterRestart: el proceso nuevo sabe reactivar. La empresa vuelve y sus dos Edge, al
// reconectar, operan; y reactivar una empresa no des-revoca el lease de un Edge de OTRA.
func (p *p1WireRestartRun) checkRestoreAfterRestart(t *testing.T) {
	t.Helper()
	esc := p.esc
	if r := esc.S.Admin(esc.TokenStaff).Post(t, rutaTenantBack, map[string]string{"tenant_id": p.otherTenant}); !p1WireIs(r, http.StatusNoContent, "") {
		t.Fatalf("reactivar la empresa: HTTP %d %q, quería 204 sin cuerpo", r.Codigo, recortar(r.Cuerpo))
	}
	edgeEsperarValor(t, esc.DB, "false", "tenants.revoked_at tras reactivar", p1TenantRevoked, p.otherTenant)
	for _, e := range []*edge{p.cutEdge, p.born} {
		e.conectar(t)
		e.esperarLeases(t, 2, edgeTopeFila)
		if !e.puedeOperar() || e.revocado() {
			t.Errorf("%s tras reactivar y reconectar: puedeOperar=%v revocado=%v; quería verdadero y falso", e.EdgeID, e.puedeOperar(), e.revocado())
		}
		p1WaitLease(t, p.cutEsc, e, 2, false)
	}
	p1WaitLease(t, esc, p.revoked, 6, true)
}

// closing es el cierre, ANTES de parar el servidor: ni pánicos ni ERROR en el proceso nuevo, ningún
// lease rechazado por un Validator y ningún texto que nadie debió mandar.
func (p *p1WireRestartRun) closing(t *testing.T) {
	t.Helper()
	p1WireNoPanics(t, p.esc.S.Log())
	for _, e := range []*edge{p.revoked, p.kept, p.cutEdge, p.born} {
		if errs := e.Errores(); len(errs) != 0 {
			t.Errorf("el núcleo de %s anotó errores: %v", e.EdgeID, errs)
		}
		if n := len(e.Textos()); n != 0 {
			t.Errorf("el Edge %s tiene %d textos que nadie debió mandarle", e.EdgeID, n)
		}
	}
	edgeSinErrores(t, p.esc.S, nil)
}
