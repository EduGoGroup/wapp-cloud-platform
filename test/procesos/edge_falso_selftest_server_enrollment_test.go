//go:build integracion

package procesos

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	cllease "github.com/EduGoGroup/wapp-cloudlink/lease"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestArnes_EdgeEnrolaYConecta, contra el servidor real: el Edge de prueba se enrola, conecta con
// mTLS y recibe su lease y su config inicial; y los rechazos (código repetido, otra CA, otra clave).
// Sale de edge_falso_test.go (D-F9-11: solo se movieron declaraciones).

// TestArnes_EdgeEnrolaYConecta prueba el Edge de prueba contra el servidor real, de punta a punta:
// el staff crea una empresa y pide un código; el Edge se enrola (certificado, cadena, tenant, las
// dos públicas, todo coherente con las claves del servidor y con las filas de Postgres); conecta
// con mTLS y recibe su lease inicial, su renovación por el primer latido y la config inicial
// (jwks y filters); el canal de control recibe la suya sin registrar sesión; el mismo código no se
// puede canjear dos veces; un certificado de OTRA CA no pasa el handshake; y un Edge que espera
// otra clave de lease conecta pero conectarErr devuelve el rechazo del lease inicial. Necesita
// Docker.
func TestArnes_EdgeEnrolaYConecta(t *testing.T) {
	t.Parallel()
	esc := edgeEscenarioNuevo(t, "edge_enrola", "edge-enrola", false)
	codigo := edgeEmitirCodigo(t, esc.S, esc.TokenStaff, esc.Tenant)

	e := enrolar(t, esc.S, codigo)
	edgeVerificarEnrolado(t, esc, e, codigo)
	edgeVerificarCodigoRepetido(t, esc, codigo)

	e.conectar(t)
	edgeVerificarConectado(t, esc, e)
	edgeVerificarConfigsIniciales(t, esc, e)
	edgeVerificarCanalControl(t, esc, e)
	edgeVerificarCertificadoAjeno(t, esc, e)
	edgeCheckForeignLeaseKey(t, esc, e)

	if errs := e.Errores(); len(errs) != 0 {
		t.Errorf("el núcleo del Edge anotó errores: %v", errs)
	}
	edgeSinErrores(t, esc.S, nil)
}

// edgeVerificarEnrolado comprueba lo que entregó el enrolamiento: el tenant de la empresa, las dos
// públicas idénticas a las del servidor, un certificado de cliente con la identidad del Edge que
// verifica contra la CA del servidor, y en Postgres el código consumido y el certificado registrado.
func edgeVerificarEnrolado(t *testing.T, esc edgeEscenario, e *edge, codigo string) {
	t.Helper()
	s := esc.S
	if e.TenantID != esc.Tenant {
		t.Errorf("tenant del Edge = %s, quería %s", e.TenantID, esc.Tenant)
	}
	if !bytes.Equal(e.CloudEncPub, s.Claves.NubePub) {
		t.Errorf("cloud_enc_pubkey no es la pública X25519 del servidor")
	}
	if !e.LeasePub.Equal(s.Claves.LeasePub) {
		t.Errorf("lease_pubkey no es la pública Ed25519 del servidor")
	}
	if e.SessionID == "" || e.SessionID == edgeSesionControl || !strings.HasPrefix(e.EdgeID, "edge-") {
		t.Errorf("identidad del Edge inesperada: edge %q sesión %q", e.EdgeID, e.SessionID)
	}
	edgeVerificarCertificadoEmitido(t, esc, e)

	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.enrollment_codes WHERE code = $1 AND tenant_id = $2::uuid AND used_at IS NOT NULL`, codigo, esc.Tenant); n != 1 {
		t.Errorf("códigos de enrolamiento consumidos = %d, quería 1", n)
	}
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.edge_certs WHERE tenant_id = $1::uuid AND subject_cn = $2`, esc.Tenant, e.EdgeID); n != 1 {
		t.Errorf("certificados registrados del Edge = %d, quería 1", n)
	}
}

// edgeVerificarCertificadoEmitido comprueba el certificado de cliente que emitió el servidor:
// CommonName = id del Edge, Organization = tenant, la clave pública del CSR, y que verifica como
// certificado de cliente contra la CA de la PKI del servidor.
func edgeVerificarCertificadoEmitido(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	cert, err := x509.ParseCertificate(edgePEMPrimero(t, "CERTIFICATE", e.certPEM))
	if err != nil {
		t.Fatalf("el certificado emitido no se interpreta: %v", err)
	}
	if cert.Subject.CommonName != e.EdgeID || len(cert.Subject.Organization) != 1 || cert.Subject.Organization[0] != esc.Tenant {
		t.Errorf("sujeto del certificado = %v, quería CN %s y O %s", cert.Subject, e.EdgeID, esc.Tenant)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: esc.S.PKI.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("el certificado emitido no verifica contra la CA del servidor: %v", err)
	}
	if pub, ok := cert.PublicKey.(*ecdsa.PublicKey); !ok || !pub.Equal(&e.clave.PublicKey) {
		t.Errorf("el certificado emitido no lleva la clave pública del CSR")
	}
}

// edgeVerificarCodigoRepetido comprueba que el código ya canjeado y uno que no existe se rechazan con
// PermissionDenied, y que el rechazo no deja un Edge.
func edgeVerificarCodigoRepetido(t *testing.T, esc edgeEscenario, codigo string) {
	t.Helper()
	for nombre, c := range map[string]string{"repetido": codigo, "inexistente": "WAPP-" + edgeAleatorioHex(t, 10)} {
		e, err := enrolarErr(t, esc.S, c)
		if err == nil || e != nil || status.Code(err) != codes.PermissionDenied {
			t.Errorf("enrolar con un código %s: edge %v, error %v (código %v); quería PermissionDenied", nombre, e, err, status.Code(err))
		}
	}
}

// edgeVerificarConectado comprueba la conexión: el Edge puede operar y no está revocado, recibió el
// lease inicial y la renovación del primer latido (contadores 1 y 2) sin que el Validator rechazara
// ninguno, y Postgres tiene la sesión online y el lease con el contador 2 sin revocar.
func edgeVerificarConectado(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	e.esperarLeases(t, 2, edgeTopeFila)
	if !e.puedeOperar() || e.revocado() {
		t.Errorf("tras conectar: puedeOperar=%v revocado=%v; quería verdadero y falso", e.puedeOperar(), e.revocado())
	}
	edgeEsperarValor(t, esc.DB, "online", "la sesión del Edge en la flota", edgeEstadoSesion, esc.Tenant, e.EdgeID, e.SessionID)
	// El servidor leyó el READY del primer latido como la transición que dispara el calentamiento.
	edgeEsperarLinea(t, esc.S, "calentamiento: el Edge acaba de decir que puede servir inferencia", "session_id", e.SessionID)
	edgeEsperarValor(t, esc.DB, "2/false", "el lease del Edge en Postgres (contador/revocado)",
		`SELECT counter::text || '/' || revoked::text FROM public.leases WHERE tenant_id = $1::uuid AND edge_id = $2`, esc.Tenant, e.EdgeID)
}

// edgeVerificarConfigsIniciales comprueba la config que el servidor empuja al conectar: un jwks
// cuya versión es el kid de la clave de firma del servidor y cuyo contenido es un JWKS ES256 con
// ese kid, y el mapa de filters; los dos dirigidos a la sesión del Edge y con command_id.
func edgeVerificarConfigsIniciales(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	jwks := e.esperarConfig(t, "jwks", edgeTopeFila)
	if jwks.Version != esc.S.Claves.JWTKid || jwks.Sesion != e.SessionID || jwks.ComandoID == "" {
		t.Errorf("jwks = versión %q sesión %q comando %q; quería %q, %q y un command_id", jwks.Version, jwks.Sesion, jwks.ComandoID, esc.S.Claves.JWTKid, e.SessionID)
	}
	var cuerpo struct {
		Keys []struct{ Kty, Crv, Kid, Alg string } `json:"keys"`
	}
	if err := json.Unmarshal(jwks.Payload, &cuerpo); err != nil || len(cuerpo.Keys) != 1 {
		t.Fatalf("el contenido del jwks no es un JWKS de una clave: %v\n%s", err, jwks.Payload)
	}
	if k := cuerpo.Keys[0]; k.Kty != "EC" || k.Crv != "P-256" || k.Alg != "ES256" || k.Kid != esc.S.Claves.JWTKid {
		t.Errorf("la clave del jwks = %+v, quería EC P-256 ES256 con kid %s", k, esc.S.Claves.JWTKid)
	}

	filters := e.esperarConfig(t, "filters", edgeTopeFila)
	if filters.Sesion != e.SessionID || filters.ComandoID == "" || !json.Valid(filters.Payload) {
		t.Errorf("filters = sesión %q comando %q payload válido %v", filters.Sesion, filters.ComandoID, json.Valid(filters.Payload))
	}
}

// edgeVerificarCanalControl comprueba que un frame por el canal de control hace que el servidor
// empuje la config inicial por ese mismo stream (jwks con la sesión de control) sin registrar el
// canal como sesión de flota.
func edgeVerificarCanalControl(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	e.usarCanalControl(t)
	edgeEsperar(t, edgeTopeFila, "un ConfigUpdate jwks por el canal de control", func() bool {
		return slices.ContainsFunc(e.Configs(), func(c configRecibida) bool { return c.Kind == "jwks" && c.Sesion == edgeSesionControl })
	})
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND session_id = $2`, esc.Tenant, edgeSesionControl); n != 0 {
		t.Errorf("el canal de control quedó registrado como sesión de flota (%d filas)", n)
	}
}

// edgeVerificarCertificadoAjeno comprueba el caso negativo del mTLS: el mismo Edge con un certificado
// firmado por la CA de otra PKI no completa la conexión (conectarErr devuelve un error de transporte),
// no deja sesión en la flota, y el Edge legítimo sigue conectado.
func edgeVerificarCertificadoAjeno(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	ajeno := e.conCertificadoDe(t, nuevaPKI(t))
	err := ajeno.conectarErr(t)
	if err == nil {
		t.Fatalf("un certificado de otra CA conectó: el servidor no exige su CA")
	}
	t.Logf("conectar con el certificado de otra CA: %v", err)
	// El texto del error varía según quién gane la carrera entre la alerta TLS del servidor y el
	// reinicio de la conexión («unknown certificate authority» o «connection reset by peer»); lo
	// estable es que el canal falla al instante como Unavailable y no como un silencio.
	if errors.Is(err, errEdgeLeaseNoLlego) || status.Code(err) != codes.Unavailable || ajeno.Leases() != 0 || ajeno.puedeOperar() {
		t.Errorf("el rechazo debía ser un fallo inmediato del transporte (Unavailable), no un silencio: error %v, leases %d", err, ajeno.Leases())
	}
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND session_id = $2`, esc.Tenant, ajeno.SessionID); n != 0 {
		t.Errorf("el certificado de otra CA dejó %d sesiones en la flota", n)
	}
	if !e.puedeOperar() {
		t.Errorf("el Edge legítimo dejó de poder operar tras el intento ajeno")
	}
}

// edgeCheckForeignLeaseKey comprueba, contra el servidor real, que conectar NO da por buena una
// conexión cuyo lease inicial rechazó el Validator: el mismo Edge, con su certificado bueno pero
// esperando otra clave de lease, pasa el mTLS, recibe el lease inicial del servidor y conectarErr
// devuelve el rechazo (errEdgeInitialLeaseRejected y cllease.ErrBadSignature), con el Edge sin
// poder operar y el enlace cerrado; y el Edge legítimo, en su propia sesión, sigue operando.
func edgeCheckForeignLeaseKey(t *testing.T, esc edgeEscenario, e *edge) {
	t.Helper()
	other := e.withLeasePub(t, nuevasClaves(t).LeasePub)
	err := other.conectarErr(t)
	if !errors.Is(err, errEdgeInitialLeaseRejected) || !errors.Is(err, cllease.ErrBadSignature) {
		t.Fatalf("conectar esperando otra clave de lease = %v; quería el rechazo del lease inicial por firma inválida", err)
	}
	if other.puedeOperar() || other.revocado() || other.Leases() == 0 {
		t.Errorf("tras el rechazo: puedeOperar=%v revocado=%v leases=%d; quería falso, falso y al menos 1 recibido",
			other.puedeOperar(), other.revocado(), other.Leases())
	}
	if err := other.emitir(edgeLatido(other.SessionID, edgeContadorInicial)); !errors.Is(err, errEdgeSinSalida) {
		t.Errorf("tras el rechazo el enlace debía quedar cerrado: emitir = %v, quería errEdgeSinSalida", err)
	}
	// El servidor sí llegó a registrar esa sesión (el mTLS era bueno) y la ve irse.
	edgeEsperarValor(t, esc.DB, "offline", "la sesión del Edge que rechazó su lease", edgeEstadoSesion, esc.Tenant, e.EdgeID, other.SessionID)
	if !e.puedeOperar() || len(e.Errores()) != 0 {
		t.Errorf("el Edge legítimo quedó afectado: puedeOperar=%v errores=%v", e.puedeOperar(), e.Errores())
	}
}
