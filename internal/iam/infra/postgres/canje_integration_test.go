package iampostgres_test

// canje_integration_test.go — LOS CRITERIOS DE CAMPO DEL CANJE
// (Plan 047 · Ola A · T-A3 + T-A4 + T-A5).
//
// 🔴 POR QUÉ CONTRA POSTGRES Y NO CONTRA UN DOBLE. Lo que se prueba aquí no es
// lógica, son COSTURAS entre tres tablas que nadie declaró relacionadas:
// tenant_invitations, tenant_members y access_requests. El «un solo uso» lo da
// un UPDATE condicionado contando filas afectadas; el orden entre la guarda de
// membresía y el marcado solo se puede observar mirando qué quedó escrito
// después de un rollback; y la caducidad la decide `now()` del servidor de base
// de datos, no el reloj de este proceso. Un doble en memoria daría verde a las
// tres cosas estando las tres rotas.
//
// ⚠️ Se SALTA sin WAPP_TEST_DB_DSN, y un --- SKIP no es un --- PASS.
// `make test-integration` lo exporta contra un postgres:16 efímero.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	iamhttp "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/transport/http"
	iamusecase "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// ---------------------------------------------------------------------------
// Utillaje
// ---------------------------------------------------------------------------

// invitacionSembrada es lo que un test necesita saber de la fila que acaba de
// crear: el token EN CLARO (que la base no guarda) y el id de la fila.
type invitacionSembrada struct {
	token string
	id    string
}

// sembrarInvitacion crea una invitación viva para ese tenant y devuelve su token
// en claro.
//
// Inserta por SQL directo a propósito: el emisor de verdad (T-A2) es de otra
// tarea y este fichero no debe depender de que su forma final ya esté escrita —
// lo que se prueba aquí es el CANJE. El token se genera con el mismo generador
// del dominio, y el digest con la misma función que usará el canje: si se
// escribieran a mano, este test probaría un acuerdo consigo mismo.
func sembrarInvitacion(ctx context.Context, t *testing.T, env itEnv, tenantID string, expiraEn time.Duration) invitacionSembrada {
	t.Helper()
	token, err := domain.NewInvitationToken()
	if err != nil {
		t.Fatalf("generar token: %v", err)
	}
	var id string
	err = env.db.QueryRowContext(ctx, `
		INSERT INTO public.tenant_invitations (tenant_id, token_hash, expires_at, created_by)
		VALUES ($1, $2, now() + $3::interval, $4)
		RETURNING id::text
	`, tenantID, domain.HashInvitationToken(token), expiraEn.String(), uuid.NewString()).Scan(&id)
	if err != nil {
		t.Fatalf("sembrar invitación: %v", err)
	}
	return invitacionSembrada{token: token, id: id}
}

// estadoDeInvitacion lee las dos marcas terminales de una invitación.
func estadoDeInvitacion(ctx context.Context, t *testing.T, env itEnv, id string) (redeemedAt sql.NullTime, redeemedBy sql.NullString) {
	t.Helper()
	err := env.db.QueryRowContext(ctx, `
		SELECT redeemed_at, redeemed_by::text FROM public.tenant_invitations WHERE id = $1
	`, id).Scan(&redeemedAt, &redeemedBy)
	if err != nil {
		t.Fatalf("leer invitación %s: %v", id, err)
	}
	return redeemedAt, redeemedBy
}

// sembrarSolicitudPendiente reproduce lo que el signup público deja al
// registrarse el invitado (platformadmin.CreateAccessRequest, signup.go paso 3).
func sembrarSolicitudPendiente(ctx context.Context, t *testing.T, env itEnv, userID string) {
	t.Helper()
	_, err := env.db.ExecContext(ctx, `
		INSERT INTO public.access_requests (user_id, email, origin, status)
		VALUES ($1, $2, 'bff', 'pending')
	`, userID, "invitado-"+userID[:8]+"@ejemplo.test")
	if err != nil {
		t.Fatalf("sembrar solicitud pendiente: %v", err)
	}
}

// estadoDeSolicitud devuelve el status de la solicitud de ese usuario ("" si no
// hay ninguna).
func estadoDeSolicitud(ctx context.Context, t *testing.T, env itEnv, userID string) string {
	t.Helper()
	var status string
	err := env.db.QueryRowContext(ctx, `
		SELECT status FROM public.access_requests WHERE user_id = $1
	`, userID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("leer solicitud de %s: %v", userID, err)
	}
	return status
}

// tenantsDeUsuario devuelve las empresas de las que ese usuario es miembro.
func tenantsDeUsuario(ctx context.Context, t *testing.T, env itEnv, userID string) []string {
	t.Helper()
	tenants, err := iampostgres.NewMembershipRepo(env.db).TenantsOfUser(ctx, userID)
	if err != nil {
		t.Fatalf("leer membresías de %s: %v", userID, err)
	}
	return tenants
}

// ---------------------------------------------------------------------------
// T-A3 · los cuatro desenlaces, contra la base
// ---------------------------------------------------------------------------

// TestIntegration_Canje_ElCaminoFeliz es el criterio principal: 204 con fila en
// tenant_members del tenant DE LA INVITACIÓN.
func TestIntegration_Canje_ElCaminoFeliz(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	repo := iampostgres.NewInvitationRedeemRepo(env.db)

	inv := sembrarInvitacion(ctx, t, env, env.tenantID, time.Hour)
	userID := uuid.NewString()
	sembrarSolicitudPendiente(ctx, t, env, userID)

	if err := repo.Redeem(ctx, domain.HashInvitationToken(inv.token), userID); err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	// (a) La membresía existe y es la de la invitación.
	tenants := tenantsDeUsuario(ctx, t, env, userID)
	if len(tenants) != 1 || tenants[0] != env.tenantID {
		t.Fatalf("membresías = %v, quiero exactamente [%s]", tenants, env.tenantID)
	}

	// (b) La invitación quedó marcada, con las DOS mitades del par (el CHECK
	// tenant_invitations_redeemed_pair_check habría rechazado una sola, pero
	// comprobarlo aquí dice ADEMÁS que redeemed_by es QUIEN canjeó y no otro).
	redAt, redBy := estadoDeInvitacion(ctx, t, env, inv.id)
	if !redAt.Valid {
		t.Error("redeemed_at sigue NULL: la invitación quedó reutilizable")
	}
	if !redBy.Valid || redBy.String != userID {
		t.Errorf("redeemed_by = %v, quiero %s", redBy, userID)
	}

	// (c) T-A4: la solicitud huérfana ya no está pendiente.
	if got := estadoDeSolicitud(ctx, t, env, userID); got != "approved" {
		t.Errorf("access_request.status = %q, quiero \"approved\": si sigue en \"pending\", el operador de "+
			"plataforma ve en SU bandeja a alguien que ya está dentro", got)
	}
}

// TestIntegration_Canje_LosTresRechazos cubre inexistente, caducada y ya
// canjeada — y que ninguno deja rastro escrito.
func TestIntegration_Canje_LosTresRechazos(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	repo := iampostgres.NewInvitationRedeemRepo(env.db)

	t.Run("inexistente", func(t *testing.T) {
		otro, err := domain.NewInvitationToken()
		if err != nil {
			t.Fatalf("generar token: %v", err)
		}
		userID := uuid.NewString()
		err = repo.Redeem(ctx, domain.HashInvitationToken(otro), userID)
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, quiero domain.ErrNotFound", err)
		}
		if tenants := tenantsDeUsuario(ctx, t, env, userID); len(tenants) != 0 {
			t.Errorf("un token inexistente dejó membresías: %v", tenants)
		}
	})

	t.Run("caducada", func(t *testing.T) {
		// TTL NEGATIVO: la fila nace ya vencida, y su `expires_at` lo calcula el
		// MISMO now() de Postgres que después la juzga. Sin ese detalle este test
		// compararía dos relojes y podría ser flaky por deriva.
		inv := sembrarInvitacion(ctx, t, env, env.tenantID, -time.Hour)
		userID := uuid.NewString()
		err := repo.Redeem(ctx, domain.HashInvitationToken(inv.token), userID)
		if !errors.Is(err, domain.ErrInvitationExpired) {
			t.Fatalf("err = %v, quiero domain.ErrInvitationExpired", err)
		}
		if tenants := tenantsDeUsuario(ctx, t, env, userID); len(tenants) != 0 {
			t.Errorf("una invitación caducada dio acceso: %v", tenants)
		}
	})

	t.Run("ya canjeada por otra persona", func(t *testing.T) {
		inv := sembrarInvitacion(ctx, t, env, env.tenantID, time.Hour)
		primero, segundo := uuid.NewString(), uuid.NewString()
		if err := repo.Redeem(ctx, domain.HashInvitationToken(inv.token), primero); err != nil {
			t.Fatalf("primer canje: %v", err)
		}
		err := repo.Redeem(ctx, domain.HashInvitationToken(inv.token), segundo)
		if !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("segundo canje: err = %v, quiero domain.ErrConflict", err)
		}
		if tenants := tenantsDeUsuario(ctx, t, env, segundo); len(tenants) != 0 {
			t.Errorf("el segundo canje dio acceso a %v: el token es de UN SOLO USO", tenants)
		}
		// Y el primero sigue siendo el dueño del canje.
		_, redBy := estadoDeInvitacion(ctx, t, env, inv.id)
		if redBy.String != primero {
			t.Errorf("redeemed_by = %s, quiero %s: el segundo intento pisó la marca del primero", redBy.String, primero)
		}
	})
}

// TestIntegration_Canje_UnaInvitacionRevocadaNoSeCanjea es la mitad de T-A8 que
// el canje tiene que respetar.
func TestIntegration_Canje_UnaInvitacionRevocadaNoSeCanjea(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()

	inv := sembrarInvitacion(ctx, t, env, env.tenantID, time.Hour)
	if _, err := env.db.ExecContext(ctx, `
		UPDATE public.tenant_invitations SET revoked_at = now() WHERE id = $1
	`, inv.id); err != nil {
		t.Fatalf("revocar: %v", err)
	}

	userID := uuid.NewString()
	err := iampostgres.NewInvitationRedeemRepo(env.db).
		Redeem(ctx, domain.HashInvitationToken(inv.token), userID)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, quiero domain.ErrConflict", err)
	}
	if tenants := tenantsDeUsuario(ctx, t, env, userID); len(tenants) != 0 {
		t.Errorf("una invitación REVOCADA dio acceso a %v: la dueña la anuló y el canje la ignoró", tenants)
	}
	if redAt, _ := estadoDeInvitacion(ctx, t, env, inv.id); redAt.Valid {
		t.Error("una invitación revocada quedó ADEMÁS marcada como canjeada")
	}
}

// ---------------------------------------------------------------------------
// T-A5 · la guarda de membresía única, y EL ORDEN
// ---------------------------------------------------------------------------

// TestIntegration_Canje_QuienYaEsMiembroDeOtraEmpresaNoQuemaLaInvitacion es el
// criterio literal de T-A5, y las DOS mitades importan:
//
//	(a) 409 — lo da GrantTenantAccess, que cuenta antes de insertar. 🔧 Hasta el
//	    2026-08-29 esto se explicaba diciendo que con dos membresías esa persona
//	    «no podría volver a entrar»; ya no es cierto (Plan 047 · Ola 5 · T5.1: el
//	    canje resuelve por la empresa activa). El 409 se queda porque el alta en
//	    una segunda empresa tiene que ser una decisión, no un efecto colateral de
//	    canjear una invitación — MD-055.2.
//	(b) LA INVITACIÓN SIGUE USABLE — y esto es lo que fija el ORDEN de los
//	    pasos. Si el marcado ocurriera ANTES de la guarda, este rechazo dejaría
//	    la invitación quemada: terminal, sin membresía detrás, y la dueña
//	    tendría que emitir otra sin entender por qué.
func TestIntegration_Canje_QuienYaEsMiembroDeOtraEmpresaNoQuemaLaInvitacion(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()

	otraEmpresa := sembrarTenantVecino(t, env)
	userID := uuid.NewString()
	sembrarMiembro(t, env, userID, otraEmpresa)

	inv := sembrarInvitacion(ctx, t, env, env.tenantID, time.Hour)
	err := iampostgres.NewInvitationRedeemRepo(env.db).
		Redeem(ctx, domain.HashInvitationToken(inv.token), userID)

	// (a)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, quiero domain.ErrConflict", err)
	}
	tenants := tenantsDeUsuario(ctx, t, env, userID)
	if len(tenants) != 1 || tenants[0] != otraEmpresa {
		t.Fatalf("membresías = %v, quiero solo [%s]: el canje escribió una SEGUNDA y esa persona ya no puede entrar",
			tenants, otraEmpresa)
	}

	// (b) — la mitad que fija el orden.
	redAt, redBy := estadoDeInvitacion(ctx, t, env, inv.id)
	if redAt.Valid || redBy.Valid {
		t.Errorf("la invitación quedó MARCADA pese al rechazo (redeemed_at=%v, redeemed_by=%v).\n"+
			"El marcado tiene que ir DESPUÉS de GrantTenantAccess: al revés, un canje rechazado quema la "+
			"invitación y la dueña tiene que emitir otra sin saber por qué.", redAt, redBy)
	}

	// Y sigue sirviendo de verdad: otra persona la canjea sin problema.
	otroUsuario := uuid.NewString()
	if err := iampostgres.NewInvitationRedeemRepo(env.db).
		Redeem(ctx, domain.HashInvitationToken(inv.token), otroUsuario); err != nil {
		t.Fatalf("la invitación no sobrevivió al rechazo: %v", err)
	}
}

// TestIntegration_Canje_ElRechazoNoCierraLaSolicitud comprueba que el rollback
// alcanza también al cuarto paso.
//
// Es un test aparte y no un assert más del de arriba porque prueba otra cosa:
// allí se fija el ORDEN entre dos escrituras, aquí que la ATOMICIDAD abarca la
// tabla de la OTRA bandeja. Una implementación que cerrara la solicitud fuera de
// la transacción —o antes— pasaría aquel test y fallaría este, dejando al
// invitado sin empresa y sin solicitud: invisible para la dueña Y para el
// operador.
func TestIntegration_Canje_ElRechazoNoCierraLaSolicitud(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()

	otraEmpresa := sembrarTenantVecino(t, env)
	userID := uuid.NewString()
	sembrarMiembro(t, env, userID, otraEmpresa)
	sembrarSolicitudPendiente(ctx, t, env, userID)

	inv := sembrarInvitacion(ctx, t, env, env.tenantID, time.Hour)
	if err := iampostgres.NewInvitationRedeemRepo(env.db).
		Redeem(ctx, domain.HashInvitationToken(inv.token), userID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, quiero domain.ErrConflict", err)
	}

	if got := estadoDeSolicitud(ctx, t, env, userID); got != "pending" {
		t.Errorf("access_request.status = %q, quiero \"pending\": un canje RECHAZADO cerró la solicitud, "+
			"y esa persona se ha quedado sin empresa y sin nadie a quien pedírsela", got)
	}
}

// TestIntegration_Canje_SinSolicitudPendienteNoEsUnFallo: no toda persona que
// canjea dejó una solicitud (el operador pudo atenderla antes, o llegó por otra
// vía). Que el canje exigiera encontrarla convertiría un caso normal en un 500.
func TestIntegration_Canje_SinSolicitudPendienteNoEsUnFallo(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()

	inv := sembrarInvitacion(ctx, t, env, env.tenantID, time.Hour)
	userID := uuid.NewString() // sin solicitud sembrada
	if err := iampostgres.NewInvitationRedeemRepo(env.db).
		Redeem(ctx, domain.HashInvitationToken(inv.token), userID); err != nil {
		t.Fatalf("Redeem sin solicitud pendiente: %v", err)
	}
	if tenants := tenantsDeUsuario(ctx, t, env, userID); len(tenants) != 1 {
		t.Errorf("membresías = %v, quiero una", tenants)
	}
}

// ---------------------------------------------------------------------------
// INV-04 · de extremo a extremo, por HTTP
// ---------------------------------------------------------------------------

// TestIntegration_Canje_ElTenantDelCuerpoSeIGNORA es la mutación declarada roja
// de T-A3, ejercitada de verdad: se manda un `tenant_id` ajeno en el cuerpo y la
// membresía tiene que salir con el de la INVITACIÓN.
//
// Se monta la pila entera (handler → usecase → repositorio → Postgres) menos el
// middleware: la Identity se inyecta a mano en el contexto, que es exactamente
// lo que hace Authenticate. Así se ejercita el decodificador real del cuerpo,
// que es donde un `tenant_id` entraría.
//
// 🔴 Y se comprueba SIN empresa en la Identity, que es el otro criterio: quien
// canjea llega con un Context Token sin tenant. Si esta pila exigiera empresa,
// aquí saldría un 403 en vez de un 204.
func TestIntegration_Canje_ElTenantDelCuerpoSeIgnora(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()

	empresaAjena := sembrarTenantVecino(t, env)
	inv := sembrarInvitacion(ctx, t, env, env.tenantID, time.Hour)
	userID := uuid.NewString()

	caller := in.CallerResolverFunc(func(ctx context.Context) (in.Caller, bool) {
		id, ok := httpapi.IdentityFromContext(ctx)
		return in.Caller{TenantID: id.TenantID, UserID: id.Subject}, ok
	})
	svc, err := iamusecase.NewRedeemService(caller, iampostgres.NewInvitationRedeemRepo(env.db))
	if err != nil {
		t.Fatalf("NewRedeemService: %v", err)
	}
	h := iamhttp.NewInvitationRedeemHandler(svc).Accept()

	cuerpo, err := json.Marshal(map[string]string{"token": inv.token, "tenant_id": empresaAjena})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/invitations/accept", strings.NewReader(string(cuerpo)))
	req.Header.Set("Content-Type", "application/json")
	// Identity SIN TenantID: el token de quien canjea no trae empresa (D-056.12).
	req = req.WithContext(httpapi.WithIdentity(req.Context(), httpapi.Identity{Subject: userID}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, quiero 204 (cuerpo: %s).\nSi es 403, alguien le exigió empresa a un token que "+
			"por definición no la trae: este endpoint es justo el que la estrena.", rec.Code, rec.Body.String())
	}

	tenants := tenantsDeUsuario(ctx, t, env, userID)
	if len(tenants) != 1 || tenants[0] != env.tenantID {
		t.Fatalf("membresías = %v, quiero [%s] (el de la INVITACIÓN).\nSi salió %s, el canje leyó el tenant "+
			"del CUERPO y eso es INV-04 roto: la empresa la eligió quien emitió la invitación, no quien la canjea.",
			tenants, env.tenantID, empresaAjena)
	}
}
