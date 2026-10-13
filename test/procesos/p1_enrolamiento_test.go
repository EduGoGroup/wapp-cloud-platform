//go:build integracion

package procesos

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"net/http"
	"regexp"
	"strconv"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// P1 · Enrolamiento de un Edge y su lease (diseno.md §4, T9.13). El proceso de negocio entero, de
// caja negra y contra el binario que elija el arnés: la plataforma da de alta una empresa y le
// emite un código de activación; el Edge lo canjea por su certificado, conecta con mTLS y recibe su
// lease, que el servidor renueva en cada latido; la administradora de la empresa revoca el lease de
// UN Edge (kill-switch anti-clon) y la plataforma corta y reactiva a la empresa ENTERA (kill-switch
// comercial). Cada paso se mira por la puerta (HTTP, gRPC) y en Postgres.
//
// Está partido por tema (ninguna pieza pasa de 500 líneas):
//   - p1_enrolamiento_test.go                 el recorrido, el alta del Edge y su lease
//   - p1_enrolamiento_adversarial_test.go     las tablas adversarias (slugs y códigos) y la carrera
//   - p1_enrolamiento_kill_switch_test.go     los dos kill-switch, el candado ADR-0007 y el cierre
//
// Los helpers edge… son los del arnés (TestArnes_EdgeEnrolaYConecta ya prueba el doble); aquí se
// reutilizan como pasos y se añade lo que el arnés no afirma: la auditoría, el formato del código,
// la fila del certificado, el corte comercial y su vuelta, la carrera por un código y los casos
// adversarios.

// p1Run es el estado que comparten los pasos de P1: el escenario y los tres Edges de la empresa.
type p1Run struct {
	esc edgeEscenario
	// code es el código de activación con el que se enrola el primer Edge.
	code string
	// first es el Edge al que se le revoca SU lease; second, el que está conectado cuando llega el
	// corte comercial; third, el que nace (se enrola y conecta) con la empresa ya cortada.
	first, second, third *edge
	// rawEnrollment es una respuesta de EnrollEdge tal como llegó del servidor (la de quien ganó la
	// carrera por un código), para el candado ADR-0007.
	rawEnrollment *cloudlinkv1.EnrollEdgeResponse
	// tenantsCreated es cuántas empresas creó el proceso por POST /admin/tenants con 201 (la del
	// escenario incluida) y tenantsRejected cuántas altas rechazó el servidor (409 y 400).
	tenantsCreated, tenantsRejected int
	// codesIssued es cuántos códigos de activación emitió el proceso.
	codesIssued int
}

// p1CodeFormat es la forma del código de activación: «WAPP-» y 20 hexadecimales en minúscula.
var p1CodeFormat = regexp.MustCompile(`^WAPP-[0-9a-f]{20}$`)

// TestP1_EnrollmentAndLease recorre P1 sobre un solo servidor, con los pasos en orden (cada uno
// parte de lo que dejó el anterior, así que el primero que falla detiene el proceso). Las conexiones
// de los Edges se abren en el test padre y no en los subtests: el stream vive mientras viva el
// contexto del test que lo abrió, y el de un subtest muere al terminar el subtest. Necesita Docker.
func TestP1_EnrollmentAndLease(t *testing.T) {
	t.Parallel()
	p := &p1Run{esc: edgeEscenarioNuevo(t, "p1", "p1-enrolamiento", true), tenantsCreated: 1}
	s := p.esc.S

	p.step(t, "tenant_slugs_adversarial", p.checkTenantSlugs)
	p.step(t, "enrollment_code_issued", p.checkCodeIssued)
	p.step(t, "activation_codes_adversarial", p.checkAdversarialCodes)

	p.first = enrolar(t, s, p.code)
	p.step(t, "enrollment", p.checkEnrollment)
	p.step(t, "code_is_single_use", p.checkSingleUse)

	p.first.conectar(t)
	p.step(t, "connect_and_lease_renewal", p.checkConnectAndRenewal)
	p.step(t, "foreign_ca_rejected", func(t *testing.T) { edgeVerificarCertificadoAjeno(t, p.esc, p.first) })
	p.step(t, "kill_switches_need_their_permission", p.checkPermissions)

	p.step(t, "lease_revocation", p.checkLeaseRevocation)
	p.first.conectar(t) // no falla: una revocación aceptada por el Validator no es un rechazo
	p.step(t, "lease_revocation_is_sticky", p.checkRevocationSticky)

	p.second = enrolar(t, s, p.issueCode(t))
	p.second.conectar(t)
	p.step(t, "commercial_cut", p.checkCommercialCut)
	p.third = enrolar(t, s, p.issueCode(t))
	p.third.conectar(t)
	p.step(t, "edge_born_under_cut", p.checkBornUnderCut)
	p.step(t, "commercial_restore", p.checkCommercialRestore)
	for _, e := range []*edge{p.first, p.second, p.third} {
		e.conectar(t)
	}
	p.step(t, "reconnect_after_restore", p.checkReconnectAfterRestore)

	p.step(t, "ADR0007_double_key_lock", p.checkDoubleKeyLock)
	p.step(t, "postgres_final_state", p.checkFinalState)
	p.step(t, "no_unexpected_errors", p.checkNoErrors)
}

// step corre un paso como subtest y detiene el proceso si falla: los pasos siguientes parten de lo
// que este dejó, y seguir solo añadiría fallos derivados.
func (p *p1Run) step(t *testing.T, name string, fn func(t *testing.T)) {
	t.Helper()
	if !t.Run(name, fn) {
		t.Fatalf("P1 se detiene: falló el paso %s", name)
	}
}

// issueCode pide un código de activación para la empresa del proceso y lo cuenta.
func (p *p1Run) issueCode(t *testing.T) string {
	t.Helper()
	p.codesIssued++
	return edgeEmitirCodigo(t, p.esc.S, p.esc.TokenStaff, p.esc.Tenant)
}

// checkCodeIssued es el paso 2: el staff pide un código de activación. Afirma el 201, la forma del
// código (WAPP- y 20 hex), el plazo por defecto de 24 h, la fila de enrollment_codes sin usar y la
// auditoría de la ruta (acción = el permiso, enrollment.issue.any).
func (p *p1Run) checkCodeIssued(t *testing.T) {
	esc := p.esc
	antes := time.Now()
	r := esc.S.Admin(esc.TokenStaff).Post(t, rutaTenants+"/"+esc.Tenant+"/enrollment-codes", nil)
	p.codesIssued++
	var res struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	r.JSON(t, &res)
	if r.Codigo != http.StatusCreated || !p1CodeFormat.MatchString(res.Code) {
		t.Fatalf("emitir un código: HTTP %d código %q; quería 201 y WAPP-<20 hex>\ncuerpo: %s", r.Codigo, res.Code, recortar(r.Cuerpo))
	}
	if plazo := res.ExpiresAt.Sub(antes); plazo < 24*time.Hour-time.Minute || plazo > 24*time.Hour+time.Minute {
		t.Errorf("el código vence en %s, quería 24 h (el plazo por defecto)", plazo)
	}
	p.code = res.Code

	var (
		tenant  string
		vence   time.Time
		usadoEn sql.NullTime
	)
	if err := esc.DB.QueryRowContext(t.Context(),
		`SELECT tenant_id::text, expires_at, used_at FROM public.enrollment_codes WHERE code = $1`, res.Code).
		Scan(&tenant, &vence, &usadoEn); err != nil {
		t.Fatalf("leer el código emitido: %v", err)
	}
	if tenant != esc.Tenant || usadoEn.Valid || vence.Sub(res.ExpiresAt).Abs() > time.Second {
		t.Errorf("enrollment_codes = tenant %s, vence %s, used_at %v; quería %s, %s y sin usar", tenant, vence, usadoEn, esc.Tenant, res.ExpiresAt)
	}
	p1WaitAdminAudit(t, esc.DB, 1, p1AuditRow{tenantPlataformaID, "enrollment.issue.any", "enrollment", "success", http.StatusCreated, esc.Tenant})

	// Una empresa que no existe no recibe código: 404, y la auditoría lo anota como fallo.
	fantasma := uuidAleatorio(t)
	if r := esc.S.Admin(esc.TokenStaff).Post(t, rutaTenants+"/"+fantasma+"/enrollment-codes", nil); r.Codigo != http.StatusNotFound {
		t.Errorf("emitir un código para una empresa que no existe: HTTP %d, quería 404", r.Codigo)
	}
	p1WaitAdminAudit(t, esc.DB, 1, p1AuditRow{tenantPlataformaID, "enrollment.issue.any", "enrollment", "failure", http.StatusNotFound, fantasma})
}

// checkEnrollment es el paso 3: lo que el Edge recibió al canjear el código. Sobre lo que ya
// afirma el arnés (tenant, las dos públicas iguales a las del servidor, CN = edge_id, O = tenant,
// verifica contra la CA, código consumido, certificado registrado) añade: el certificado es de hoja
// y vigente, la cadena es la CA del servidor, y la fila de edge_certs describe ESE certificado.
func (p *p1Run) checkEnrollment(t *testing.T) {
	esc, e := p.esc, p.first
	edgeVerificarEnrolado(t, esc, e, p.code)

	der := edgePEMPrimero(t, "CERTIFICATE", e.certPEM)
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("el certificado emitido no se interpreta: %v", err)
	}
	if cert.IsCA || !cert.NotAfter.After(time.Now()) || cert.NotBefore.After(time.Now()) {
		t.Errorf("el certificado del Edge: IsCA=%v, vigencia %s–%s; quería una hoja vigente", cert.IsCA, cert.NotBefore, cert.NotAfter)
	}
	if !bytes.Equal(edgePEMPrimero(t, "CERTIFICATE", e.caPEM), edgePEMPrimero(t, "CERTIFICATE", esc.S.PKI.CACertPEM)) {
		t.Errorf("ca_chain_pem no empieza por la CA del servidor")
	}

	var (
		huella, pemGuardado string
		venceEn             time.Time
		usadoEn             sql.NullTime
	)
	if err := esc.DB.QueryRowContext(t.Context(), `
		SELECT fingerprint, cert_pem, not_after FROM public.edge_certs WHERE tenant_id = $1::uuid AND subject_cn = $2`,
		esc.Tenant, e.EdgeID).Scan(&huella, &pemGuardado, &venceEn); err != nil {
		t.Fatalf("leer edge_certs: %v", err)
	}
	suma := sha256.Sum256(der)
	if huella != hex.EncodeToString(suma[:]) || pemGuardado != string(e.certPEM) || !venceEn.Equal(cert.NotAfter) {
		t.Errorf("edge_certs no describe el certificado entregado: huella %s (quería %x), not_after %s (quería %s), mismo PEM %v",
			huella, suma, venceEn, cert.NotAfter, pemGuardado == string(e.certPEM))
	}
	if err := esc.DB.QueryRowContext(t.Context(), `SELECT used_at FROM public.enrollment_codes WHERE code = $1`, p.code).Scan(&usadoEn); err != nil {
		t.Fatalf("leer used_at: %v", err)
	}
	if !usadoEn.Valid || time.Since(usadoEn.Time).Abs() > time.Minute {
		t.Errorf("enrollment_codes.used_at = %v, quería el instante del canje", usadoEn)
	}
	// Enrolarse no es conectarse: hasta el Connect no hay lease ni sesión de flota.
	if n := consultaEntero(t, esc.DB, `
		SELECT (SELECT count(*) FROM public.leases WHERE tenant_id = $1::uuid) +
		       (SELECT count(*) FROM public.fleet_sessions WHERE tenant_id = $1::uuid)`, esc.Tenant); n != 0 {
		t.Errorf("tras enrolar (sin conectar) hay %d filas entre leases y fleet_sessions, quería 0", n)
	}
}

// checkConnectAndRenewal es el paso 4: el Edge conectó y el servidor EMITIÓ su lease (el Validator
// lo aceptó: puedeOperar), empujó la config inicial (jwks y filters) y dejó la sesión online y la
// fila edge.session.open en la auditoría; y RENUEVA por latido: latir(n) deja leases.counter en n+1
// y un vencimiento futuro, con el Edge operando y sin que el Validator rechace nada.
func (p *p1Run) checkConnectAndRenewal(t *testing.T) {
	esc, e := p.esc, p.first
	edgeVerificarConectado(t, esc, e)
	edgeVerificarConfigsIniciales(t, esc, e)
	edgeEsperarValor(t, esc.DB, "1", "la fila edge.session.open de la auditoría", p1SessionAudit, esc.Tenant, e.EdgeID, e.SessionID)

	for _, n := range []int64{2, 7, 40} {
		antes := e.Leases()
		e.latir(t, n)
		e.esperarLeases(t, antes+1, edgeTopeFila)
		p1WaitLease(t, esc, e, n+1, false)
	}
	if !e.puedeOperar() || e.revocado() || len(e.Errores()) != 0 {
		t.Errorf("tras renovar: puedeOperar=%v revocado=%v errores=%v", e.puedeOperar(), e.revocado(), e.Errores())
	}
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.leases WHERE tenant_id = $1::uuid AND edge_id = $2 AND expires_at > now()`, esc.Tenant, e.EdgeID); n != 1 {
		t.Errorf("el lease renovado no tiene un vencimiento futuro en Postgres")
	}
}

// p1SessionAudit cuenta las filas de auditoría de la apertura de UNA sesión de un Edge: la acción y
// el recurso del plano de máquina, el actor = edge_id (el CN del certificado mTLS) y resultado ok.
const p1SessionAudit = `SELECT count(*)::text FROM public.audit_events
	WHERE tenant_id = $1::uuid AND actor = $2 AND action = 'edge.session.open' AND resource = 'edge.session'
	  AND result = 'ok' AND meta->>'edge_id' = $2 AND meta->>'session_id' = $3`

// p1WaitLease espera a que la fila de leases del Edge valga ese contador y ese estado de revocación.
func p1WaitLease(t *testing.T, esc edgeEscenario, e *edge, counter int64, revoked bool) {
	t.Helper()
	quiere := "false"
	if revoked {
		quiere = "true"
	}
	edgeEsperarValor(t, esc.DB, strconv.FormatInt(counter, 10)+"/"+quiere, "el lease de "+e.EdgeID+" en Postgres (contador/revocado)",
		`SELECT counter::text || '/' || revoked::text FROM public.leases WHERE tenant_id = $1::uuid AND edge_id = $2`, esc.Tenant, e.EdgeID)
}

// p1AuditRow describe una fila de auditoría de una ruta /admin: la empresa del llamante (sale de su
// token), la acción (el permiso de la ruta), el recurso, el resultado, el código HTTP anotado en
// meta y la empresa objetivo que publicó el handler ("" si no publicó ninguna).
type p1AuditRow struct {
	callerTenant, action, resource, result string
	status                                 int
	target                                 string
}

// p1WaitAdminAudit espera a que haya exactamente want filas de auditoría como fila, cuyo actor sea
// además un miembro de la empresa del llamante (un id opaco de usuario, no un correo). Sondea
// porque la fila se escribe DESPUÉS de contestar al cliente.
func p1WaitAdminAudit(t *testing.T, db *sql.DB, want int, fila p1AuditRow) {
	t.Helper()
	edgeEsperarValor(t, db, strconv.Itoa(want), "auditoría de "+fila.action+" ("+fila.result+")", `
		SELECT count(*)::text FROM public.audit_events
		WHERE tenant_id = $1::uuid AND action = $2 AND resource = $3 AND result = $4
		  AND meta->>'status' = $5 AND COALESCE(meta->>'target_tenant_id', '') = $6
		  AND actor IN (SELECT user_id::text FROM public.tenant_members WHERE tenant_id = $1::uuid)`,
		fila.callerTenant, fila.action, fila.resource, fila.result, strconv.Itoa(fila.status), fila.target)
}
