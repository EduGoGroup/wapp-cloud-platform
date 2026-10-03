//go:build integracion

package procesos

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Los dos kill-switch de P1 —el del lease de UN Edge (anti-clon, lo dispara la administradora de la
// empresa) y el comercial de la empresa ENTERA (lo dispara la plataforma)—, el candado ADR-0007 y
// el cierre del proceso.

const (
	// p1TenantRevoked dice si la empresa está cortada: tenants.revoked_at poblado.
	p1TenantRevoked = `SELECT (revoked_at IS NOT NULL)::text FROM public.tenants WHERE id = $1::uuid`
	rutaLeaseRevoke = "/admin/leases/revoke"
	rutaTenantCut   = rutaTenants + "/revoke"
	rutaTenantBack  = rutaTenants + "/restore"
)

// checkPermissions comprueba que cada kill-switch pide SU permiso y que un rechazo no cambia nada:
// la administradora de la empresa no puede cortar ni reactivar empresas (403: el deny «*.any» de
// tenant_admin), el staff de plataforma no puede revocar el lease de un Edge (403: platform_admin no
// tiene leases.revoke), y sin token ninguna ruta pasa (401). El rechazo ocurre antes del handler y
// de su auditoría: no deja fila con la acción de la ruta.
func (p *p1Run) checkPermissions(t *testing.T) {
	esc, e := p.esc, p.first
	objetivo := map[string]string{"tenant_id": esc.Tenant}
	edgeID := map[string]string{"edge_id": e.EdgeID}
	casos := []struct {
		nombre, token, ruta string
		cuerpo              map[string]string
		quiere              int
	}{
		{"la administradora corta su propia empresa", esc.TokenAdmin, rutaTenantCut, objetivo, http.StatusForbidden},
		{"la administradora reactiva su propia empresa", esc.TokenAdmin, rutaTenantBack, objetivo, http.StatusForbidden},
		{"la administradora corta la empresa de plataforma", esc.TokenAdmin, rutaTenantCut, map[string]string{"tenant_id": tenantPlataformaID}, http.StatusForbidden},
		{"el staff revoca el lease de un Edge", esc.TokenStaff, rutaLeaseRevoke, edgeID, http.StatusForbidden},
		{"sin token, cortar", "", rutaTenantCut, objetivo, http.StatusUnauthorized},
		{"sin token, revocar un lease", "", rutaLeaseRevoke, edgeID, http.StatusUnauthorized},
	}
	for _, c := range casos {
		if r := esc.S.Admin(c.token).Post(t, c.ruta, c.cuerpo); r.Codigo != c.quiere {
			t.Errorf("%s: HTTP %d, quería %d\ncuerpo: %s", c.nombre, r.Codigo, c.quiere, recortar(r.Cuerpo))
		}
	}
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.tenants WHERE revoked_at IS NOT NULL`); n != 0 {
		t.Errorf("un rechazo dejó %d empresas cortadas", n)
	}
	p1WaitLease(t, esc, e, 41, false)
	if !e.puedeOperar() {
		t.Errorf("un rechazo le quitó al Edge su lease")
	}
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.audit_events WHERE action IN ('tenants.revoke.any', 'tenants.restore.any', 'leases.revoke')`); n != 0 {
		t.Errorf("los rechazos por permiso dejaron %d filas de auditoría con la acción de la ruta, quería 0", n)
	}
}

// checkLeaseRevocation es el paso 5: la administradora revoca el lease del primer Edge. Antes, el
// caso adversario: el edge_id con un U+00A0 pegado es OTRO Edge para el servidor viejo (no recorta),
// que contesta 204 y deja una revocación anticipada de ese id (contador 0) sin tocar al Edge real.
// Después, la revocación de verdad: 204, el Edge recibe la revocación firmada y deja de poder
// operar, leases.revoked pasa a true SIN mover el contador, la sesión sigue online (revocar no
// desconecta) y un latido posterior no lo resucita: el servidor reafirma la revocación.
func (p *p1Run) checkLeaseRevocation(t *testing.T) {
	esc, e := p.esc, p.first
	admin := esc.S.Admin(esc.TokenAdmin)

	parecido := e.EdgeID + " "
	if r := admin.Post(t, rutaLeaseRevoke, map[string]string{"edge_id": parecido}); r.Codigo != http.StatusNoContent {
		t.Fatalf("revocar un edge_id con U+00A0: HTTP %d, quería 204\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	edgeEsperarValor(t, esc.DB, "0/true", "la revocación anticipada del edge_id con U+00A0",
		`SELECT counter::text || '/' || revoked::text FROM public.leases WHERE tenant_id = $1::uuid AND edge_id = $2`, esc.Tenant, parecido)
	p1WaitLease(t, esc, e, 41, false)
	if !e.puedeOperar() || e.revocado() {
		t.Fatalf("revocar un edge_id parecido revocó al Edge real")
	}
	if r := admin.Post(t, rutaLeaseRevoke, map[string]string{"edge_id": ""}); r.Codigo != http.StatusBadRequest {
		t.Errorf("revocar sin edge_id: HTTP %d, quería 400", r.Codigo)
	}

	if r := admin.Post(t, rutaLeaseRevoke, map[string]string{"edge_id": e.EdgeID}); r.Codigo != http.StatusNoContent {
		t.Fatalf("revocar el lease: HTTP %d, quería 204\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	edgeEsperar(t, edgeTopeFila, "que el Edge quede revocado", e.revocado)
	if e.puedeOperar() {
		t.Errorf("el Edge revocado sigue pudiendo operar")
	}
	p1WaitLease(t, esc, e, 41, true)
	edgeEsperarValor(t, esc.DB, "online", "la sesión del Edge revocado", edgeEstadoSesion, esc.Tenant, e.EdgeID, e.SessionID)

	antes := e.Leases()
	e.latir(t, 41)
	e.esperarLeases(t, antes+1, edgeTopeFila)
	if e.puedeOperar() || !e.revocado() {
		t.Errorf("un latido resucitó al Edge revocado")
	}
	p1WaitLease(t, esc, e, 41, true)

	p1WaitAdminAudit(t, esc.DB, 2, p1AuditRow{esc.Tenant, "leases.revoke", "lease", "success", http.StatusNoContent, ""})
	p1WaitAdminAudit(t, esc.DB, 1, p1AuditRow{esc.Tenant, "leases.revoke", "lease", "failure", http.StatusBadRequest, ""})
}

// checkRevocationSticky comprueba, con el primer Edge ya reconectado, que reconectar no deshace la
// revocación: el servidor le manda la revocación como lease inicial, la sesión vuelve a estar
// online, hay una segunda fila edge.session.open, y Postgres sigue igual.
func (p *p1Run) checkRevocationSticky(t *testing.T) {
	esc, e := p.esc, p.first
	if e.puedeOperar() || !e.revocado() {
		t.Errorf("tras reconectar el Edge revocado: puedeOperar=%v revocado=%v; quería falso y verdadero", e.puedeOperar(), e.revocado())
	}
	edgeEsperarValor(t, esc.DB, "online", "la sesión del Edge revocado tras reconectar", edgeEstadoSesion, esc.Tenant, e.EdgeID, e.SessionID)
	edgeEsperarValor(t, esc.DB, "2", "las aperturas de sesión auditadas del primer Edge", p1SessionAudit, esc.Tenant, e.EdgeID, e.SessionID)
	p1WaitLease(t, esc, e, 41, true)
}

// checkCommercialCut es la primera mitad del paso 6, con el segundo Edge conectado y operando: el
// staff corta la empresa. Antes, los adversarios: cortar una empresa que no existe contesta 204 y
// no corta nada (el viejo no mira cuántas filas tocó), y sin tenant_id, 400. Después, el corte:
// 204, tenants.revoked_at poblado SOLO en esta empresa, el Edge conectado recibe la revocación y
// deja de operar, y —lo que distingue al corte comercial de la revocación de un lease—
// leases.revoked de ese Edge NO cambia ni cambia con el siguiente latido: el corte vive en tenants.
func (p *p1Run) checkCommercialCut(t *testing.T) {
	esc, e := p.esc, p.second
	staff := esc.S.Admin(esc.TokenStaff)
	e.esperarLeases(t, 2, edgeTopeFila)
	p1WaitLease(t, esc, e, 2, false)
	if !e.puedeOperar() || e.revocado() {
		t.Fatalf("el segundo Edge no opera antes del corte: la revocación del primero no debía alcanzarlo")
	}

	fantasma := uuidAleatorio(t)
	if r := staff.Post(t, rutaTenantCut, map[string]string{"tenant_id": fantasma}); r.Codigo != http.StatusNoContent {
		t.Errorf("cortar una empresa que no existe: HTTP %d, quería 204", r.Codigo)
	}
	if r := staff.Post(t, rutaTenantCut, map[string]string{"tenant_id": ""}); r.Codigo != http.StatusBadRequest {
		t.Errorf("cortar sin tenant_id: HTTP %d, quería 400", r.Codigo)
	}
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.tenants WHERE revoked_at IS NOT NULL`); n != 0 || !e.puedeOperar() {
		t.Fatalf("los cortes adversarios cortaron algo: %d empresas con revoked_at, puedeOperar=%v", n, e.puedeOperar())
	}

	if r := staff.Post(t, rutaTenantCut, map[string]string{"tenant_id": esc.Tenant}); r.Codigo != http.StatusNoContent {
		t.Fatalf("cortar la empresa: HTTP %d, quería 204\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	edgeEsperar(t, edgeTopeFila, "que el Edge conectado reciba el corte", e.revocado)
	if e.puedeOperar() {
		t.Errorf("el Edge de una empresa cortada sigue pudiendo operar")
	}
	edgeEsperarValor(t, esc.DB, "true", "tenants.revoked_at de la empresa cortada", p1TenantRevoked, esc.Tenant)
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.tenants WHERE revoked_at IS NOT NULL`); n != 1 {
		t.Errorf("empresas cortadas = %d, quería solo la del proceso", n)
	}
	p1WaitLease(t, esc, e, 2, false)

	antes := e.Leases()
	e.latir(t, 2)
	e.esperarLeases(t, antes+1, edgeTopeFila) // el servidor reafirma la revocación, sin persistir
	p1WaitLease(t, esc, e, 2, false)
	if e.puedeOperar() || !e.revocado() {
		t.Errorf("un latido durante el corte devolvió al Edge a operar")
	}
	edgeEsperarValor(t, esc.DB, "online", "la sesión del Edge cortado", edgeEstadoSesion, esc.Tenant, e.EdgeID, e.SessionID)

	p1WaitAdminAudit(t, esc.DB, 1, p1AuditRow{tenantPlataformaID, "tenants.revoke.any", "tenant", "success", http.StatusNoContent, esc.Tenant})
	p1WaitAdminAudit(t, esc.DB, 1, p1AuditRow{tenantPlataformaID, "tenants.revoke.any", "tenant", "success", http.StatusNoContent, fantasma})
	p1WaitAdminAudit(t, esc.DB, 1, p1AuditRow{tenantPlataformaID, "tenants.revoke.any", "tenant", "failure", http.StatusBadRequest, ""})
}

// checkBornUnderCut lleva por la puerta la regla de TestIntegration_TenantRevokedGatesNeverSeenEdge:
// con la empresa cortada, la plataforma todavía puede emitir un código y un Edge nuevo todavía se
// enrola y conecta (el corte no cierra el enrolamiento ni el mTLS), pero NACE revocado: su lease
// inicial es una revocación, no puede operar y no deja fila en leases.
func (p *p1Run) checkBornUnderCut(t *testing.T) {
	esc, e := p.esc, p.third
	if e.puedeOperar() || !e.revocado() {
		t.Errorf("el Edge nacido con la empresa cortada: puedeOperar=%v revocado=%v; quería falso y verdadero", e.puedeOperar(), e.revocado())
	}
	edgeEsperarValor(t, esc.DB, "online", "la sesión del Edge nacido cortado", edgeEstadoSesion, esc.Tenant, e.EdgeID, e.SessionID)
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.leases WHERE tenant_id = $1::uuid AND edge_id = $2`, esc.Tenant, e.EdgeID); n != 0 {
		t.Errorf("el Edge nacido cortado dejó %d filas en leases, quería 0", n)
	}
}

// checkCommercialRestore es la segunda mitad del paso 6: el staff reactiva la empresa (204,
// revoked_at vuelve a NULL). MEDIDO contra el viejo: la reactivación no empuja nada; en el
// siguiente latido el servidor vuelve a EMITIR un lease vigente (leases.counter avanza a n+1 y el
// Edge nacido cortado estrena su fila), pero un Edge que sigue conectado NO vuelve a operar: su
// Validator —la misma clase que usa el Edge real— es pegajoso y descarta sin error cualquier
// renovación tras una revocación. Para volver hace falta reconectar (un arranque nuevo del Edge),
// que es lo que afirma el paso siguiente.
func (p *p1Run) checkCommercialRestore(t *testing.T) {
	esc := p.esc
	if r := esc.S.Admin(esc.TokenStaff).Post(t, rutaTenantBack, map[string]string{"tenant_id": esc.Tenant}); r.Codigo != http.StatusNoContent {
		t.Fatalf("reactivar la empresa: HTTP %d, quería 204\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	edgeEsperarValor(t, esc.DB, "false", "tenants.revoked_at tras reactivar", p1TenantRevoked, esc.Tenant)
	p1WaitAdminAudit(t, esc.DB, 1, p1AuditRow{tenantPlataformaID, "tenants.restore.any", "tenant", "success", http.StatusNoContent, esc.Tenant})

	for _, c := range []struct {
		e       *edge
		late    int64
		counter int64
	}{{p.second, 2, 3}, {p.third, 1, 2}} {
		antes := c.e.Leases()
		c.e.latir(t, c.late)
		c.e.esperarLeases(t, antes+1, edgeTopeFila)
		p1WaitLease(t, esc, c.e, c.counter, false)
		if c.e.puedeOperar() || !c.e.revocado() || len(c.e.Errores()) != 0 {
			t.Errorf("%s tras reactivar y latir sin reconectar: puedeOperar=%v revocado=%v errores=%v; quería falso, verdadero y ninguno",
				c.e.EdgeID, c.e.puedeOperar(), c.e.revocado(), c.e.Errores())
		}
	}
}

// checkReconnectAfterRestore comprueba, con los tres Edges reconectados tras la reactivación, que
// los dos sujetos de corte son independientes: los Edges que solo sufrieron el corte comercial
// vuelven a operar, y el que tiene SU lease revocado sigue revocado (reactivar la empresa no
// des-revoca a un Edge).
func (p *p1Run) checkReconnectAfterRestore(t *testing.T) {
	esc := p.esc
	for _, e := range []*edge{p.second, p.third} {
		e.esperarLeases(t, 2, edgeTopeFila)
		if !e.puedeOperar() || e.revocado() {
			t.Errorf("%s tras reactivar y reconectar: puedeOperar=%v revocado=%v; quería verdadero y falso", e.EdgeID, e.puedeOperar(), e.revocado())
		}
		p1WaitLease(t, esc, e, 2, false)
		edgeEsperarValor(t, esc.DB, "online", "la sesión de "+e.EdgeID, edgeEstadoSesion, esc.Tenant, e.EdgeID, e.SessionID)
	}
	if p.first.puedeOperar() || !p.first.revocado() {
		t.Errorf("reactivar la empresa des-revocó al Edge con el lease revocado")
	}
	p1WaitLease(t, esc, p.first, 41, true)
}

// p1ConfigKinds son los kind de ConfigUpdate que el contrato conoce: material público (jwks) y
// configuración de negocio (filters, intents). Ninguno es material de llaves del Edge.
var p1ConfigKinds = []string{"jwks", "filters", "intents"}

// checkDoubleKeyLock es el 🔒 candado ADR-0007 (doble llave) de P1. La mitad servidora —EMITIR,
// RENOVAR y REVOCAR el lease— ya quedó afirmada por los pasos anteriores, con leases que solo
// verifican contra la pública del servidor. Aquí se afirma la otra mitad: la DEK, que custodia el
// cliente, NO cruza. De todo lo que el Edge recibió del servidor en el proceso entero:
//
//   - la respuesta de EnrollEdge trae exactamente sus cinco campos (certificado, cadena, tenant y
//     las dos PÚBLICAS del servidor), sin campos desconocidos y sin más bloques PEM que
//     certificados: ni una clave privada ni material simétrico;
//   - los ConfigUpdate son solo de los kind conocidos, sus claves JSON no nombran ninguna DEK, y el
//     jwks trae claves públicas (sin el parámetro privado «d»);
//   - el servidor no pidió ningún diagnóstico ni empujó ningún texto, y las únicas inferencias que
//     pidió son calentamientos (sin prompt cifrado que abrir).
//
// LO QUE EL DOBLE NO DEJA VER: no guarda los frames crudos. El blob firmado de cada LeaseUpdate
// pasa al Validator y se descarta (se sabe que verifica, no qué más lleva); un SendMedia o un
// comando de un tipo que el doble no conoce se acusa sin registrarse. Un comando NUEVO que pidiera
// material de la DEK no se vería desde aquí: lo que este candado cierra es el contrato de hoy.
func (p *p1Run) checkDoubleKeyLock(t *testing.T) {
	p.checkEnrollmentResponse(t)
	for _, e := range []*edge{p.first, p.second, p.third} {
		p.checkEdgeReceived(t, e)
	}
}

// checkEnrollmentResponse es la primera viñeta del candado: la respuesta cruda de EnrollEdge trae
// sus cinco campos y nada más, solo certificados en sus PEM y las dos PÚBLICAS del servidor.
func (p *p1Run) checkEnrollmentResponse(t *testing.T) {
	s := p.esc.S
	resp := p.rawEnrollment
	if resp == nil {
		t.Fatal("no hay respuesta cruda de EnrollEdge que mirar")
	}
	quiere := []string{"ca_chain_pem", "cloud_enc_pubkey", "edge_cert_pem", "lease_pubkey", "tenant_id"}
	var presentes []string
	m := resp.ProtoReflect()
	declarados := make([]string, 0, m.Descriptor().Fields().Len())
	m.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		presentes = append(presentes, string(fd.Name()))
		return true
	})
	for i := range m.Descriptor().Fields().Len() {
		declarados = append(declarados, string(m.Descriptor().Fields().Get(i).Name()))
	}
	slices.Sort(presentes)
	slices.Sort(declarados)
	if !slices.Equal(presentes, quiere) || !slices.Equal(declarados, quiere) || len(m.GetUnknown()) != 0 {
		t.Errorf("EnrollEdgeResponse trae %v (el contrato declara %v, %d bytes desconocidos); quería exactamente %v", presentes, declarados, len(m.GetUnknown()), quiere)
	}
	for nombre, datos := range map[string][]byte{"edge_cert_pem": resp.GetEdgeCertPem(), "ca_chain_pem": resp.GetCaChainPem()} {
		for resto := datos; len(bytes.TrimSpace(resto)) > 0; {
			var bloque *pem.Block
			if bloque, resto = pem.Decode(resto); bloque == nil || bloque.Type != "CERTIFICATE" {
				t.Errorf("%s trae algo que no es un certificado: %.40q", nombre, resto)
				break
			}
		}
	}
	if !bytes.Equal(resp.GetCloudEncPubkey(), s.Claves.NubePub) || !bytes.Equal(resp.GetLeasePubkey(), s.Claves.LeasePub) ||
		bytes.Equal(resp.GetCloudEncPubkey(), s.Claves.NubePriv) {
		t.Errorf("las claves de EnrollEdgeResponse no son las dos PÚBLICAS del servidor")
	}
}

// checkEdgeReceived son las otras dos viñetas del candado, para un Edge: sus ConfigUpdate no traen
// material de llaves, nadie le pidió un diagnóstico ni le empujó un texto, y sus inferencias son
// solo calentamientos.
func (p *p1Run) checkEdgeReceived(t *testing.T, e *edge) {
	configs := e.Configs()
	if len(configs) == 0 {
		t.Errorf("%s no recibió ninguna config: el candado no tendría nada que mirar", e.EdgeID)
	}
	for _, c := range configs {
		if !slices.Contains(p1ConfigKinds, c.Kind) {
			t.Errorf("%s recibió un ConfigUpdate de kind %q, fuera de %v", e.EdgeID, c.Kind, p1ConfigKinds)
		}
		var contenido any
		if err := json.Unmarshal(c.Payload, &contenido); err != nil {
			t.Errorf("%s: el ConfigUpdate %q no es JSON: %v", e.EdgeID, c.Kind, err)
			continue
		}
		for _, clave := range p1JSONKeys(contenido) {
			if strings.Contains(strings.ToLower(clave), "dek") || (c.Kind == "jwks" && clave == "d") {
				t.Errorf("%s: el ConfigUpdate %q trae la clave JSON %q: material de llaves que no debe cruzar", e.EdgeID, c.Kind, clave)
			}
		}
	}
	if n := len(e.Diagnosticos()); n != 0 {
		t.Errorf("%s recibió %d peticiones de diagnóstico que nadie pidió", e.EdgeID, n)
	}
	if n := len(e.Textos()); n != 0 {
		t.Errorf("%s recibió %d textos que nadie mandó", e.EdgeID, n)
	}
	for _, inf := range e.Inferencias() {
		if !inf.GetWarmup() || len(inf.GetEncPrompt()) != 0 {
			t.Errorf("%s recibió una inferencia que no es un calentamiento: %q", e.EdgeID, inf.GetCommandId())
		}
	}
}

// p1JSONKeys devuelve todas las claves de objeto de un valor JSON decodificado, a cualquier
// profundidad.
func p1JSONKeys(v any) []string {
	var claves []string
	switch x := v.(type) {
	case map[string]any:
		for k, hijo := range x {
			claves = append(append(claves, k), p1JSONKeys(hijo)...)
		}
	case []any:
		for _, hijo := range x {
			claves = append(claves, p1JSONKeys(hijo)...)
		}
	}
	return claves
}

// checkFinalState es el paso 9: lo que el proceso entero dejó en Postgres, leído de una vez. Los
// Edges se desconectan antes para ver también el cierre de la sesión (offline).
func (p *p1Run) checkFinalState(t *testing.T) {
	esc := p.esc
	edges := []*edge{p.first, p.second, p.third}
	for _, e := range edges {
		e.desconectar(t)
		edgeEsperarValor(t, esc.DB, "offline", "la sesión de "+e.EdgeID+" tras desconectar", edgeEstadoSesion, esc.Tenant, e.EdgeID, e.SessionID)
	}
	cuentas := []struct {
		que, consulta string
		quiere        int
	}{
		{"empresas cortadas", `SELECT count(*) FROM public.tenants WHERE revoked_at IS NOT NULL`, 0},
		{"códigos emitidos a la empresa", `SELECT count(*) FROM public.enrollment_codes WHERE tenant_id = $1::uuid`, p.codesIssued},
		{"códigos sin usar", `SELECT count(*) FROM public.enrollment_codes WHERE tenant_id = $1::uuid AND used_at IS NULL`, 0},
		{"certificados emitidos (tres Edges y el ganador de la carrera)", `SELECT count(*) FROM public.edge_certs WHERE tenant_id = $1::uuid`, 4},
		{"leases (tres Edges y la revocación anticipada)", `SELECT count(*) FROM public.leases WHERE tenant_id = $1::uuid`, 4},
		{"leases revocados", `SELECT count(*) FROM public.leases WHERE tenant_id = $1::uuid AND revoked`, 2},
		{"sesiones de flota", `SELECT count(*) FROM public.fleet_sessions WHERE tenant_id = $1::uuid`, 3},
		{"sesiones online", `SELECT count(*) FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND state = 'online'`, 0},
		{"aperturas de sesión auditadas", `SELECT count(*) FROM public.audit_events WHERE tenant_id = $1::uuid AND action = 'edge.session.open' AND result = 'ok'`, 7},
		{"códigos emitidos auditados", `SELECT count(*) FROM public.audit_events WHERE action = 'enrollment.issue.any' AND result = 'success' AND meta->>'target_tenant_id' = $1`, p.codesIssued},
	}
	if p.codesIssued != 4 {
		t.Errorf("el proceso emitió %d códigos, quería 4 (primer Edge, carrera, segundo y tercero)", p.codesIssued)
	}
	for _, c := range cuentas {
		consulta, args := c.consulta, []any{esc.Tenant}
		if !strings.Contains(consulta, "$1") {
			args = nil
		}
		if n := consultaEntero(t, esc.DB, consulta, args...); n != c.quiere {
			t.Errorf("%s = %d, quería %d", c.que, n, c.quiere)
		}
	}
	// La auditoría es de ids opacos: ni un correo ni un teléfono como actor.
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.audit_events WHERE actor LIKE '%@%' OR actor ~ '^[0-9+]+$'`); n != 0 {
		t.Errorf("hay %d filas de auditoría cuyo actor parece un correo o un número", n)
	}
}

// checkNoErrors es el cierre (paso 10), ANTES de parar el servidor: el Validator de ningún Edge
// rechazó un lease y el servidor no registró ningún ERROR en todo el proceso.
func (p *p1Run) checkNoErrors(t *testing.T) {
	for _, e := range []*edge{p.first, p.second, p.third} {
		if errs := e.Errores(); len(errs) != 0 {
			t.Errorf("el núcleo de %s anotó errores: %v", e.EdgeID, errs)
		}
	}
	edgeSinErrores(t, p.esc.S, nil)
}
