package usecase_test

// invitations_test.go — EL SERVICIO DE INVITACIONES (Plan 047 · Ola A · T-A2 y
// T-A8), contra el doble en memoria.
//
// Lo que se prueba aquí y no en el transporte: INV-04 (la empresa sale del
// contexto), el default y el clamp del TTL —que viven en UN solo sitio, este— y
// los tres desenlaces de la revocación.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
)

// invitationFixture arma un InvitationService sobre los dobles en memoria.
type invitationFixture struct {
	svc   *usecase.InvitationService
	store *memory.Store
}

func newInvitationFixture(t *testing.T) invitationFixture {
	t.Helper()
	store := memory.NewStore()
	svc, err := usecase.NewInvitationService(testResolver, store.Invitations, store.Roles)
	if err != nil {
		t.Fatalf("NewInvitationService: %v", err)
	}
	return invitationFixture{svc: svc, store: store}
}

// emitir emite una invitación para el tenant dado y aborta si falla.
func (f invitationFixture) emitir(t *testing.T, tenantID string, input in.IssueInvitationInput) in.IssuedInvitation {
	t.Helper()
	emitida, err := f.svc.IssueInvitation(ctxOf(tenantID), input)
	if err != nil {
		t.Fatalf("IssueInvitation(%s): %v", tenantID, err)
	}
	return emitida
}

// TestIssueInvitation_EmiteParaLaEmpresaDelContexto_YGuardaSoloElDigest.
//
// Las dos mitades van juntas a propósito: que el tenant salga del contexto
// (INV-04 — el Input ni siquiera tiene campo donde meter otro) y que lo que
// quede escrito sea el SHA-256 del token y no el token.
func TestIssueInvitation_EmiteParaLaEmpresaDelContexto_YGuardaSoloElDigest(t *testing.T) {
	t.Parallel()
	f := newInvitationFixture(t)

	emitida := f.emitir(t, testTenant, in.IssueInvitationInput{})

	if emitida.Invitation.TenantID != testTenant {
		t.Fatalf("tenant de la invitación = %q, quiero %q (INV-04)", emitida.Invitation.TenantID, testTenant)
	}
	if emitida.Token == "" {
		t.Fatal("la emisión no devolvió el token en claro: quien emite no tendría qué repartir")
	}
	// 🔴 Lo guardado es el DIGEST. La comprobación es contra la función de hash y
	// no contra "algo distinto del token": guardar el token XOR una constante
	// también sería "distinto" y seguiría siendo el secreto.
	esperado := domain.HashInvitationToken(emitida.Token)
	if string(emitida.Invitation.TokenHash) != string(esperado) {
		t.Fatalf("token_hash = %x, quiero el SHA-256 del token (%x)", emitida.Invitation.TokenHash, esperado)
	}
	if string(emitida.Invitation.TokenHash) == emitida.Token {
		t.Fatal("se guardó el token EN CLARO en la columna del digest")
	}
	if len(emitida.Invitation.TokenHash) != 32 {
		t.Fatalf("el digest mide %d bytes: el CHECK de la tabla exige 32 exactos", len(emitida.Invitation.TokenHash))
	}
}

// TestIssueInvitation_SinEmpresaEnElContextoNoEmite: un Context Token sin
// empresa (D-056.12, quien todavía no tiene membresía) no puede invitar a nada.
func TestIssueInvitation_SinEmpresaEnElContextoNoEmite(t *testing.T) {
	t.Parallel()
	f := newInvitationFixture(t)

	casos := map[string]context.Context{
		"contexto sin identidad":     context.Background(),
		"identidad con tenant vacío": withCaller(context.Background(), in.Caller{UserID: "sin-empresa"}),
	}
	for nombre, ctx := range casos {
		if _, err := f.svc.IssueInvitation(ctx, in.IssueInvitationInput{}); !errors.Is(err, domain.ErrNoTenant) {
			t.Errorf("%s: err = %v, quiero ErrNoTenant", nombre, err)
		}
	}
	if invs, err := f.store.Invitations.ListByTenant(context.Background(), testTenant); err != nil || len(invs) != 0 {
		t.Fatalf("no debió escribirse ninguna invitación: %d filas (err=%v)", len(invs), err)
	}
}

// TestIssueInvitation_ElTTLTieneDefaultYClamp.
//
// Los tres números son los del precedente de los códigos de enrolamiento
// (platformadmin/handlers.go:249-260) y viven en UN solo sitio: el servicio. Se
// comprueban por la DISTANCIA entre expires_at y ahora, con un margen que
// absorbe lo que tarda la llamada — no con una igualdad exacta, que sería un
// test que falla por el reloj y no por el código.
func TestIssueInvitation_ElTTLTieneDefaultYClamp(t *testing.T) {
	t.Parallel()
	f := newInvitationFixture(t)

	const (
		unDia      = 86400 * time.Second
		unMinuto   = 60 * time.Second
		treintaDia = 30 * 86400 * time.Second
	)
	casos := []struct {
		nombre string
		ttl    int
		quiero time.Duration
	}{
		{"ausente (cero) ⇒ 24 h por defecto", 0, unDia},
		{"negativo ⇒ tampoco caduca ya: se trata como ausente", -1, unDia},
		{"por debajo del suelo ⇒ 60 s", 5, unMinuto},
		{"justo el suelo ⇒ 60 s", 60, unMinuto},
		{"un valor normal se respeta", 3600, time.Hour},
		{"justo el techo ⇒ 30 días", 30 * 86400, treintaDia},
		{"por encima del techo ⇒ 30 días", 365 * 86400, treintaDia},
	}
	for _, c := range casos {
		antes := time.Now().UTC()
		emitida := f.emitir(t, testTenant, in.IssueInvitationInput{TTLSeconds: c.ttl})
		vida := emitida.Invitation.ExpiresAt.Sub(antes)
		// El margen es de un segundo por arriba: la vida medida no puede ser MENOR
		// que la pedida (el reloj se tomó después de `antes`) ni mayor que ella más
		// lo que tardó la llamada.
		if vida < c.quiero || vida > c.quiero+time.Second {
			t.Errorf("%s: vida = %v, quiero ~%v", c.nombre, vida, c.quiero)
		}
	}
}

// TestIssueInvitation_ElRolPrometidoTieneQueSerVisible.
//
// Sin esta guarda, quien emite podría teclear el UUID de un rol de OTRA empresa:
// la FK de la tabla apunta a iam_roles a secas, que contiene los roles de todas.
// El agujero no se vería al emitir —201 perfecto— sino al canjear, dando de alta
// a alguien con un rol ajeno.
func TestIssueInvitation_ElRolPrometidoTieneQueSerVisible(t *testing.T) {
	t.Parallel()
	f := newInvitationFixture(t)
	tA, tB := testTenant, testTenantB
	propio := f.store.Roles.Seed(domain.Role{TenantID: &tA, Name: "soporte-a"}, nil)
	ajeno := f.store.Roles.Seed(domain.Role{TenantID: &tB, Name: "soporte-b"}, nil)
	plantilla := f.store.Roles.Seed(domain.Role{Name: "viewer"}, nil)

	// El rol propio y la plantilla global se aceptan y quedan guardados.
	for nombre, rol := range map[string]domain.Role{"propio": propio, "plantilla global": plantilla} {
		emitida := f.emitir(t, testTenant, in.IssueInvitationInput{RoleID: &rol.ID})
		if emitida.Invitation.RoleID == nil || *emitida.Invitation.RoleID != rol.ID {
			t.Errorf("%s: role_id guardado = %v, quiero %s", nombre, emitida.Invitation.RoleID, rol.ID)
		}
	}

	// El de otra empresa es ErrNotFound (404), no un "prohibido": un 403
	// confirmaría que ese rol existe fuera.
	if _, err := f.svc.IssueInvitation(ctxOf(testTenant), in.IssueInvitationInput{RoleID: &ajeno.ID}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("rol de otra empresa: err = %v, quiero ErrNotFound", err)
	}
	// Y uno inventado, igual.
	inventado := uuid.NewString()
	if _, err := f.svc.IssueInvitation(ctxOf(testTenant), in.IssueInvitationInput{RoleID: &inventado}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("rol inexistente: err = %v, quiero ErrNotFound", err)
	}

	// Solo las DOS aceptadas quedaron escritas: el rechazo no dejó fila.
	invs, err := f.store.Invitations.ListByTenant(context.Background(), testTenant)
	if err != nil || len(invs) != 2 {
		t.Fatalf("invitaciones escritas = %d (err=%v), quiero 2: un rechazo no puede dejar fila", len(invs), err)
	}
}

// TestIssueInvitation_SinRolEsLegitimo: `role_id` es opcional y nil significa
// «alta sin rol», que es un caso normal (dar de alta y dar un rol son dos
// decisiones distintas, memberships.go:196-198).
func TestIssueInvitation_SinRolEsLegitimo(t *testing.T) {
	t.Parallel()
	f := newInvitationFixture(t)

	vacio := ""
	for nombre, rol := range map[string]*string{"nil": nil, "cadena vacía": &vacio} {
		emitida := f.emitir(t, testTenant, in.IssueInvitationInput{RoleID: rol})
		if emitida.Invitation.RoleID != nil {
			t.Errorf("%s: role_id = %v, quiero nil", nombre, *emitida.Invitation.RoleID)
		}
	}
}

// TestListInvitations_SoloLasDeSuEmpresa: el aislamiento por tenant del listado.
func TestListInvitations_SoloLasDeSuEmpresa(t *testing.T) {
	t.Parallel()
	f := newInvitationFixture(t)
	deA := f.emitir(t, testTenant, in.IssueInvitationInput{})
	deB := f.emitir(t, testTenantB, in.IssueInvitationInput{})

	invsA, err := f.svc.ListInvitations(ctxOf(testTenant))
	if err != nil {
		t.Fatalf("ListInvitations(A): %v", err)
	}
	if len(invsA) != 1 || invsA[0].ID != deA.Invitation.ID {
		t.Fatalf("la empresa A ve %d invitaciones (%+v), quiero solo la suya", len(invsA), invsA)
	}
	for _, inv := range invsA {
		if inv.ID == deB.Invitation.ID {
			t.Fatal("la empresa A ve una invitación de la empresa B")
		}
	}
}

// TestListInvitations_LasMasRecientesPrimero: el orden que fija T-A2. Un listado
// que cambia de orden entre dos recargas está roto para quien lo pagine.
func TestListInvitations_LasMasRecientesPrimero(t *testing.T) {
	t.Parallel()
	f := newInvitationFixture(t)
	primera := f.emitir(t, testTenant, in.IssueInvitationInput{})
	segunda := f.emitir(t, testTenant, in.IssueInvitationInput{})
	tercera := f.emitir(t, testTenant, in.IssueInvitationInput{})

	invs, err := f.svc.ListInvitations(ctxOf(testTenant))
	if err != nil {
		t.Fatalf("ListInvitations: %v", err)
	}
	quiero := []string{tercera.Invitation.ID, segunda.Invitation.ID, primera.Invitation.ID}
	if len(invs) != len(quiero) {
		t.Fatalf("listado de %d, quiero %d", len(invs), len(quiero))
	}
	for i, id := range quiero {
		if invs[i].ID != id {
			t.Fatalf("posición %d = %s, quiero %s (orden: las más recientes primero)", i, invs[i].ID, id)
		}
	}
}

// TestRevokeInvitation_DejaLaFilaRevocada — el criterio de T-A8 visto desde el
// ESTADO, no desde el código de respuesta.
//
// 🔴 Se comprueba leyendo la fila, no dándose por satisfecho con el `nil`: un
// Revoke que devolviera nil sin escribir nada pasaría un test que solo mire el
// error, y el canje posterior seguiría aceptando el token.
func TestRevokeInvitation_DejaLaFilaRevocada(t *testing.T) {
	t.Parallel()
	f := newInvitationFixture(t)
	emitida := f.emitir(t, testTenant, in.IssueInvitationInput{})

	antesDe, _ := f.store.Invitations.Get(emitida.Invitation.ID)
	if antesDe.RevokedAt != nil {
		t.Fatal("la invitación nació revocada: el fixture no prueba nada")
	}
	if antesDe.Status(time.Now()) != domain.InvitationPending {
		t.Fatalf("la invitación recién emitida está %q, quiero pending", antesDe.Status(time.Now()))
	}

	if err := f.svc.RevokeInvitation(ctxOf(testTenant), emitida.Invitation.ID); err != nil {
		t.Fatalf("RevokeInvitation: %v", err)
	}

	despues, ok := f.store.Invitations.Get(emitida.Invitation.ID)
	if !ok {
		t.Fatal("la invitación desapareció: revocar no borra, marca")
	}
	if despues.RevokedAt == nil {
		t.Fatal("revoked_at sigue NULL: la revocación no se escribió y el canje aceptaría el token")
	}
	if despues.Status(time.Now()) != domain.InvitationRevoked {
		t.Fatalf("estado = %q, quiero revoked", despues.Status(time.Now()))
	}
	// El contrato para el canje (T-A3): la fila revocada NO está pendiente, que es
	// la única condición que el canje puede aceptar.
	if despues.Status(time.Now()) == domain.InvitationPending {
		t.Fatal("una invitación revocada sigue contando como pendiente")
	}
}

// TestRevokeInvitation_EsIdempotenteSobreUnaYaRevocada: la baja de algo ya dado
// de baja es el estado que se pedía.
func TestRevokeInvitation_EsIdempotenteSobreUnaYaRevocada(t *testing.T) {
	t.Parallel()
	f := newInvitationFixture(t)
	emitida := f.emitir(t, testTenant, in.IssueInvitationInput{})

	for i := range 3 {
		if err := f.svc.RevokeInvitation(ctxOf(testTenant), emitida.Invitation.ID); err != nil {
			t.Fatalf("revocación nº %d: %v", i+1, err)
		}
	}
}

// TestRevokeInvitation_UnaYaCANJEADAConflictua.
//
// No es lo mismo que "ya revocada" y no se puede fingir que sí: revocar una
// invitación consumida NO deshace la membresía que el canje escribió. Un 204 le
// diría a quien administra que acaba de retirarle el acceso a alguien que sigue
// dentro.
func TestRevokeInvitation_UnaYaCanjeadaConflictua(t *testing.T) {
	t.Parallel()
	f := newInvitationFixture(t)
	ahora := time.Now().UTC()
	quien := uuid.NewString()
	canjeada := f.store.Invitations.Seed(domain.Invitation{
		TenantID:   testTenant,
		TokenHash:  domain.HashInvitationToken("WAPP-INV-yacanjeada"),
		ExpiresAt:  ahora.Add(time.Hour),
		CreatedBy:  uuid.NewString(),
		RedeemedBy: &quien,
		RedeemedAt: &ahora,
	})

	err := f.svc.RevokeInvitation(ctxOf(testTenant), canjeada.ID)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("revocar una canjeada: err = %v, quiero ErrConflict", err)
	}
	// Y no la marcó: la fila sigue exactamente como estaba.
	fila, _ := f.store.Invitations.Get(canjeada.ID)
	if fila.RevokedAt != nil {
		t.Fatal("una invitación canjeada quedó además marcada como revocada")
	}
}

// TestRevokeInvitation_NoAlcanzaALaDeOtraEmpresa.
//
// 🔴 La otra mitad del test es la que lo hace valer: se comprueba que la
// invitación de B SIGUE VIVA después del intento de A. Sin ella, un Revoke que
// devolviera ErrNotFound SIEMPRE (y no escribiera nunca) pasaría.
func TestRevokeInvitation_NoAlcanzaALaDeOtraEmpresa(t *testing.T) {
	t.Parallel()
	f := newInvitationFixture(t)
	deB := f.emitir(t, testTenantB, in.IssueInvitationInput{})

	if err := f.svc.RevokeInvitation(ctxOf(testTenant), deB.Invitation.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("A revocando la invitación de B: err = %v, quiero ErrNotFound (no un 403: confirmaría que existe)", err)
	}
	fila, ok := f.store.Invitations.Get(deB.Invitation.ID)
	if !ok || fila.RevokedAt != nil {
		t.Fatal("la invitación de la empresa B quedó revocada por alguien de la empresa A")
	}
	// Y B sí puede: la fila EXISTE, que es lo que convierte el 404 de arriba en
	// aislamiento y no en "no había nada".
	if err := f.svc.RevokeInvitation(ctxOf(testTenantB), deB.Invitation.ID); err != nil {
		t.Fatalf("B revocando la suya: %v", err)
	}
}

// TestRevokeInvitation_IdInvalidoEsNotFound: un id que ni siquiera es un UUID no
// puede estar en una columna `uuid`, así que su destino es el mismo que el de un
// id que no existe. La guarda vive en el usecase para que el doble en memoria y
// Postgres contesten lo mismo (Postgres daría un 22P02 → 500).
func TestRevokeInvitation_IdInvalidoEsNotFound(t *testing.T) {
	t.Parallel()
	f := newInvitationFixture(t)

	casos := map[string]error{
		"":                   domain.ErrInvalidInput,
		"no-soy-un-uuid":     domain.ErrNotFound,
		"1234":               domain.ErrNotFound,
		"'; DROP TABLE x;--": domain.ErrNotFound,
	}
	for id, quiero := range casos {
		if err := f.svc.RevokeInvitation(ctxOf(testTenant), id); !errors.Is(err, quiero) {
			t.Errorf("id %q: err = %v, quiero %v", id, err, quiero)
		}
	}
}

// TestNewInvitationService_RechazaDependenciasNil: fail-fast al arrancar. Las
// tres son estructurales; ninguna admite nil (a diferencia del `systems` de
// MembershipService, que sí tiene un despliegue legítimo sin él).
func TestNewInvitationService_RechazaDependenciasNil(t *testing.T) {
	t.Parallel()
	store := memory.NewStore()

	if _, err := usecase.NewInvitationService(nil, store.Invitations, store.Roles); err == nil {
		t.Error("un CallerResolver nil debe abortar el arranque: sin él no hay empresa a la que acotar")
	}
	if _, err := usecase.NewInvitationService(testResolver, nil, store.Roles); err == nil {
		t.Error("un InvitationRepo nil debe abortar el arranque")
	}
	if _, err := usecase.NewInvitationService(testResolver, store.Invitations, nil); err == nil {
		t.Error("un RoleRepo nil debe abortar el arranque: sin él no se puede validar el rol prometido")
	}
}
