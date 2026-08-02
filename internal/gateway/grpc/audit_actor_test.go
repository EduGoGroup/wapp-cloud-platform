package gatewaygrpc_test

import (
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
)

// Estos casos cubren el criterio de aceptación de T3.3 (identity Plan 003 ·
// design.md Ola 3 §1.3): no basta con que cada llamada viaje con su identidad;
// después tiene que poder distinguirse en la bitácora «lo hizo el operador X»
// de «lo hizo el edge Y».
//
// El Edge tiene DOS identidades y esta ola las reparte: la de la PERSONA (su
// `sub` en identity) y la de la MÁQUINA (el EdgeID, que es el `CN` del cert
// mTLS). Un evento sin etiqueta obliga a adivinar por el formato del
// identificador, que es exactamente lo que aquí se prueba que no hace falta.

// actorTypeOf devuelve la etiqueta de actor del evento (o "" si no la lleva).
func actorTypeOf(t *testing.T, ev in.AuditInput) string {
	t.Helper()
	raw, present := ev.Meta["actor_type"]
	if !present {
		return ""
	}
	label, ok := raw.(string)
	if !ok {
		t.Fatalf("actor_type no es una cadena: %T", raw)
	}
	return label
}

func TestAuditoria_LaAccionDelOperadorLlevaSuSub(t *testing.T) {
	t.Parallel()
	ca := newDevCA(t)
	const operatorSub = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	//nolint:gosec // no son credenciales: son los tokens de mentira que devuelve el doble del IAM
	authn := &fakeAuthenticator{loginResult: domain.AuthResult{
		AccessToken:  "context.token",
		RefreshToken: "rft_de_identity",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(15 * time.Minute),
		Context:      domain.IdentityContext{TenantID: testTenantID, UserID: operatorSub},
	}}
	auditor := &fakeAuditor{}
	h := newAuthHarness(t, ca, issueEdgeCert(t, ca, testTenantID, testEdgeID), authn, auditor)

	if resp := recvAuthResponse(t, h.client, loginFrame("s1", "cmd-1", "op@example.com", "pw")); resp.GetTokens() == nil {
		t.Fatalf("el login debía entregar tokens: %+v", resp.GetError())
	}

	ev := findAudit(t, auditor.snapshot(), "edge.auth.login")
	if got := actorTypeOf(t, ev); got != "operator" {
		t.Errorf("actor_type = %q, want operator", got)
	}
	// El actor es la PERSONA, no el Edge por el que entró.
	if ev.Actor != operatorSub {
		t.Errorf("actor = %q, want el sub del operador %q", ev.Actor, operatorSub)
	}
	// Y el Edge sigue anotado, como canal: dice por dónde entró, no quién fue.
	if ev.Meta["edge_id"] != testEdgeID {
		t.Errorf("edge_id de meta = %v, want %q", ev.Meta["edge_id"], testEdgeID)
	}
}

func TestAuditoria_LaAccionDelDaemonLlevaElEdgeID(t *testing.T) {
	t.Parallel()
	ca := newDevCA(t)
	auditor := &fakeAuditor{}
	h := newAuthHarness(t, ca, issueEdgeCert(t, ca, testTenantID, testEdgeID), &fakeAuthenticator{}, auditor)

	// Abrir una sesión CloudLink es una acción del PROCESO del Edge, amparada por
	// el mTLS del canal. Ninguna persona interviene.
	if resp := recvAuthResponse(t, h.client, loginFrame("s-daemon", "cmd-1", "op@example.com", "pw")); resp == nil {
		t.Fatal("sin respuesta del gateway")
	}

	ev := findAudit(t, auditor.snapshot(), "edge.session.open")
	if got := actorTypeOf(t, ev); got != "daemon" {
		t.Errorf("actor_type = %q, want daemon", got)
	}
	// El actor es el EdgeID: el `CN` de su certificado, identidad criptográfica
	// por-Edge, no una credencial compartida.
	if ev.Actor != testEdgeID {
		t.Errorf("actor = %q, want el EdgeID %q", ev.Actor, testEdgeID)
	}
	if ev.TenantID != testTenantID {
		t.Errorf("tenant = %q, want %q", ev.TenantID, testTenantID)
	}
}

func TestAuditoria_LosDosActoresSeDistinguenEnLaMismaBitacora(t *testing.T) {
	t.Parallel()
	ca := newDevCA(t)
	const operatorSub = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	authn := &fakeAuthenticator{loginResult: domain.AuthResult{
		AccessToken: "context.token",
		Context:     domain.IdentityContext{TenantID: testTenantID, UserID: operatorSub},
	}}
	auditor := &fakeAuditor{}
	h := newAuthHarness(t, ca, issueEdgeCert(t, ca, testTenantID, testEdgeID), authn, auditor)

	if resp := recvAuthResponse(t, h.client, loginFrame("s1", "cmd-1", "op@example.com", "pw")); resp == nil {
		t.Fatal("sin respuesta del gateway")
	}

	// La pregunta que el criterio de aceptación exige poder contestar: de todo lo
	// que pasó por este canal, ¿qué lo hizo la persona y qué lo hizo la máquina?
	porActor := map[string][]string{}
	for _, ev := range auditor.snapshot() {
		porActor[actorTypeOf(t, ev)] = append(porActor[actorTypeOf(t, ev)], ev.Actor)
	}
	if len(porActor["operator"]) == 0 || len(porActor["daemon"]) == 0 {
		t.Fatalf("la bitácora no distingue los dos actores: %v", porActor)
	}
	if porActor["operator"][0] == porActor["daemon"][0] {
		t.Error("operador y daemon quedaron con el mismo actor")
	}
	if _, sinEtiqueta := porActor[""]; sinEtiqueta {
		t.Error("hay eventos sin actor_type: obligan a adivinar quién los hizo")
	}
}
