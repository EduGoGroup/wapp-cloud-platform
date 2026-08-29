package publicapi_test

// invitations_test.go — CONTRACT TESTS DE LA PUERTA DE INVITACIONES
// (Plan 047 · Ola A · T-A2 emitir/listar, T-A8 revocar).
//
// Corren contra el registro REAL (publicapi.Register) y contra el usecase REAL
// sobre los dobles en memoria. No hay ni un fake de in.InvitationAdmin: un doble
// del propio puerto probaría el mapeo de este fichero contra sí mismo, y lo que
// hay que demostrar —que el token no sale por el listado, que el tenant sale del
// TOKEN y no del cuerpo, que revocar deja la fila revocada— nace DENTRO del
// usecase y solo cuenta si llega vivo hasta el cable.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	iamusecase "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

const (
	// keyAInvita administra las invitaciones de la empresa A: lleva members.read
	// Y members.write, que son los dos scopes que gobiernan este recurso.
	keyAInvita = "key-a-invita"
	// keyBInvita es su gemela en la empresa B: sirve para demostrar que lo que a
	// A le da 404 a B le funciona, es decir, que la invitación EXISTE.
	keyBInvita = "key-b-invita"
	// keyAViewerInvita porta el glob del rol viewer ('*.read'): alcanza
	// members.read y NINGÚN .write. Es lo que separa «ver las invitaciones» de
	// «emitir una», que es meter gente en la empresa.
	keyAViewerInvita = "key-a-viewer-invita"
)

// clavesPermitidasDelListado es el conjunto EXACTO de claves que puede traer una
// invitación del listado.
//
// 🔴 ES LA MUTACIÓN HECHA TEST. El criterio de T-A2 pide asegurar que el `token`
// no aparece, pero un assert que solo busque esa palabra dejaría pasar un
// `token_hash`, un `digest` o un `secret` con el mismo contenido dentro. Se
// comprueba el conjunto entero: cualquier campo NUEVO en invitationDTO —el hash
// incluido— pone este test rojo con el nombre de la clave delante.
var clavesPermitidasDelListado = map[string]bool{
	"id": true, "status": true, "expires_at": true,
	"role_id": true, "created_at": true, "redeemed_at": true, "revoked_at": true,
}

// planoDeInvitaciones es el montaje completo: la API con las rutas reales y los
// stores para sembrar y para leer el estado escrito.
type planoDeInvitaciones struct {
	api         *testAPI
	invitations *memory.InvitationStore
	roles       *memory.RoleStore
}

// nuevoPlanoDeInvitaciones arma la API con el usecase real.
func nuevoPlanoDeInvitaciones(t *testing.T) *planoDeInvitaciones {
	t.Helper()
	st := memory.NewStore()

	// El MISMO CallerResolver que cablea bootstrap.buildRolePlane. Es la pieza que
	// hace que el tenant salga del token y no del cuerpo (INV-04): si este test
	// inventara aquí un tenant fijo, dejaría de probar lo que prueba.
	caller := in.CallerResolverFunc(func(ctx context.Context) (in.Caller, bool) {
		id, ok := httpapi.IdentityFromContext(ctx)
		return in.Caller{TenantID: id.TenantID, UserID: id.Subject}, ok
	})
	svc, err := iamusecase.NewInvitationService(caller, st.Invitations, st.Roles)
	if err != nil {
		t.Fatalf("NewInvitationService: %v", err)
	}
	p := &planoDeInvitaciones{invitations: st.Invitations, roles: st.Roles}
	p.api = newAPI(publicapi.Deps{Invitations: svc}, map[string]testIdentity{
		keyAInvita:       {TenantID: tenantA, Subject: "admin-a", Grants: []string{"members.read", "members.write"}},
		keyBInvita:       {TenantID: tenantB, Subject: "admin-b", Grants: []string{"members.read", "members.write"}},
		keyAViewerInvita: {TenantID: tenantA, Subject: "viewer-a", Grants: []string{"*.read"}},
	})
	return p
}

// emitida es lo que devuelve el POST: la proyección más el token en claro.
type emitida struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	ExpiresAt string `json:"expires_at"`
	RoleID    string `json:"role_id"`
	Token     string `json:"token"`
}

// emitir hace el POST y exige el 201.
func (p *planoDeInvitaciones) emitir(t *testing.T, credencial, cuerpo string) emitida {
	t.Helper()
	rec := call(p.api, credencial, http.MethodPost, "/api/v1/invitations", cuerpo)
	exigirCodigo(t, rec, http.StatusCreated, "POST /api/v1/invitations")
	var e emitida
	decodificar(t, rec.Body.Bytes(), &e)
	return e
}

// TestInvitaciones_ElListadoNOTraeElToken — 🔴 EL CRITERIO DE T-A2.
//
// Tres asertos que fallan por motivos DISTINTOS, y esa es la gracia: el conjunto
// de claves caza un campo nuevo aunque se llame de otra forma; el barrido del
// texto crudo caza el token en claro aunque viajara embebido en otro campo; y el
// barrido del digest en hex caza el hash aunque se serializara a mano. Un solo
// aserto de los tres dejaría un hueco por el que pasa la mutación.
func TestInvitaciones_ElListadoNOTraeElToken(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeInvitaciones(t)
	e := p.emitir(t, keyAInvita, `{}`)

	if e.Token == "" {
		t.Fatal("la emisión no devolvió el token: el fixture no puede probar que el listado lo oculta")
	}
	if !strings.HasPrefix(e.Token, domain.InvitationTokenPrefix) {
		t.Fatalf("el token %q no lleva el prefijo %q", e.Token, domain.InvitationTokenPrefix)
	}

	rec := call(p.api, keyAInvita, http.MethodGet, "/api/v1/invitations", "")
	exigirCodigo(t, rec, http.StatusOK, "GET /api/v1/invitations")
	crudo := rec.Body.String()

	// (1) El conjunto EXACTO de claves. Un `token`, un `token_hash` o cualquier
	// otro campo nuevo cae aquí, se llame como se llame.
	var filas []map[string]any
	decodificar(t, rec.Body.Bytes(), &filas)
	if len(filas) != 1 {
		t.Fatalf("el listado trae %d filas, quiero 1", len(filas))
	}
	for clave := range filas[0] {
		if !clavesPermitidasDelListado[clave] {
			t.Errorf("el listado proyecta la clave %q, que NO está en el contrato: el token y su digest "+
				"no pueden salir por aquí (fila: %v)", clave, filas[0])
		}
	}
	if _, hay := filas[0]["token"]; hay {
		t.Error("el listado trae el campo `token`: el código en claro existe UNA vez, en la respuesta del POST")
	}

	// (2) El token EN CLARO no aparece en ninguna parte del cuerpo, ni siquiera
	// dentro de otro campo.
	if strings.Contains(crudo, e.Token) {
		t.Errorf("el token en claro aparece en el cuerpo del listado: %s", crudo)
	}
	// (3) Ni su digest, en la codificación en que lo serializaría un DTO
	// descuidado (hex) o el JSON de Go para un []byte (base64).
	digest := domain.HashInvitationToken(e.Token)
	if strings.Contains(crudo, hex.EncodeToString(digest)) {
		t.Errorf("el DIGEST del token aparece en el listado (hex): %s", crudo)
	}
	if b64, err := json.Marshal(digest); err == nil {
		if desnudo := strings.Trim(string(b64), `"`); desnudo != "" && strings.Contains(crudo, desnudo) {
			t.Errorf("el DIGEST del token aparece en el listado (base64): %s", crudo)
		}
	}
}

// TestInvitaciones_ElTenantSaleDelTokenYNoDelCuerpo — INV-04 en el cable.
//
// 🔴 ES LA MUTACIÓN ROJA QUE EL PLAN DECLARA. Se manda un `tenant_id` de OTRA
// empresa en el cuerpo y se exige que la invitación nazca en la del TOKEN. Como
// issueInvitationRequest no tiene ese campo, el JSON se ignora; el día que
// alguien lo añada, este test se pone rojo y no una revisión de código.
func TestInvitaciones_ElTenantSaleDelTokenYNoDelCuerpo(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeInvitaciones(t)

	e := p.emitir(t, keyAInvita, `{"tenant_id":"`+tenantB+`"}`)

	fila, ok := p.invitations.Get(e.ID)
	if !ok {
		t.Fatalf("la invitación %s no quedó escrita", e.ID)
	}
	if fila.TenantID != tenantA {
		t.Fatalf("la invitación nació en el tenant %s: el `tenant_id` del CUERPO se coló (INV-04). "+
			"Quiero %s, el del token", fila.TenantID, tenantA)
	}
	// Y la empresa B no la ve, que es la consecuencia observable de lo mismo.
	rec := call(p.api, keyBInvita, http.MethodGet, "/api/v1/invitations", "")
	exigirCodigo(t, rec, http.StatusOK, "GET /api/v1/invitations (empresa B)")
	var deB []map[string]any
	decodificar(t, rec.Body.Bytes(), &deB)
	if len(deB) != 0 {
		t.Fatalf("la empresa B ve %d invitaciones que no son suyas: %v", len(deB), deB)
	}
}

// TestInvitaciones_ElCuerpoEsOpcionalYElTTLSeAplica: `{}` y ningún cuerpo son la
// misma petición, y las dos dan 24 h; un `ttl` explícito manda, y los dos
// extremos se recortan al clamp.
//
// 🔴 SE COMPRUEBA EL expires_at DE LA FILA PERSISTIDA, no solo el de la
// respuesta, y las dos mitades importan por razones distintas:
//
//   - LA FILA es lo que gobierna el canje: si la respuesta dijera una cosa y la
//     columna guardara otra, quien emitió creería que la invitación dura una hora
//     mientras la base la deja viva un mes, y ningún test que solo mire el JSON
//     lo vería.
//   - LA RESPUESTA tiene que coincidir con la fila: un `expires_at` calculado en
//     el DTO en vez de leído de la fila mentiría igual, al revés.
//
// 🔴 Y ADEMÁS ES LO QUE CAZA LA CLAVE DEL CABLE. `encoding/json` ignora en
// SILENCIO las claves desconocidas: si el DTO declarara `json:"ttl_seconds"` en
// vez de `json:"ttl"`, el número enviado no llegaría nunca, TODO caería al
// default de 24 h y un test que solo comprobara el 201 seguiría verde. Por eso
// hay casos con un `ttl` DISTINTO del default: son los tres que se ponen rojos si
// alguien renombra la clave (mutación ejecutada, cayeron los tres).
func TestInvitaciones_ElCuerpoEsOpcionalYElTTLSeAplica(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeInvitaciones(t)

	casos := []struct {
		nombre string
		cuerpo string
		quiero time.Duration
	}{
		{"sin cuerpo", "", 24 * time.Hour},
		{"cuerpo vacío", `{}`, 24 * time.Hour},
		{"ttl explícito, DISTINTO del default", `{"ttl":3600}`, time.Hour},
		{"ttl por debajo del suelo se sube a 60 s", `{"ttl":1}`, time.Minute},
		{"ttl por encima del techo se baja a 30 días", `{"ttl":99999999}`, 30 * 24 * time.Hour},
	}
	for _, c := range casos {
		antes := time.Now().UTC()
		e := p.emitir(t, keyAInvita, c.cuerpo)

		// (1) La FILA: es lo que el canje leerá.
		fila, ok := p.invitations.Get(e.ID)
		if !ok {
			t.Fatalf("%s: la invitación %s no quedó escrita", c.nombre, e.ID)
		}
		if vida := fila.ExpiresAt.Sub(antes); vida < c.quiero-time.Second || vida > c.quiero+time.Second {
			t.Errorf("%s: el expires_at GUARDADO da una vida de %v, quiero ~%v", c.nombre, vida, c.quiero)
		}

		// (2) La RESPUESTA dice lo mismo que la fila, al segundo (el wire va en
		// RFC3339, que trunca).
		vence, err := time.Parse(time.RFC3339, e.ExpiresAt)
		if err != nil {
			t.Fatalf("%s: expires_at %q no es RFC3339: %v", c.nombre, e.ExpiresAt, err)
		}
		if desfase := vence.Sub(fila.ExpiresAt); desfase > time.Second || desfase < -time.Second {
			t.Errorf("%s: la respuesta dice %s y la fila guarda %s: quien emite creería una caducidad "+
				"que la base no aplica", c.nombre, e.ExpiresAt, fila.ExpiresAt)
		}
		if e.Status != string(domain.InvitationPending) {
			t.Errorf("%s: status = %q, quiero pending", c.nombre, e.Status)
		}
	}
}

// TestInvitaciones_CuerpoMalFormadoDa400.
func TestInvitaciones_CuerpoMalFormadoDa400(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeInvitaciones(t)
	rec := call(p.api, keyAInvita, http.MethodPost, "/api/v1/invitations", `{"ttl":`)
	exigirCodigo(t, rec, http.StatusBadRequest, "POST con JSON roto")
}

// TestInvitaciones_ElRolAjenoDa404: el rol de otra empresa no se puede prometer,
// y el código es 404 y no 403 — un "prohibido" confirmaría que ese rol existe
// fuera.
func TestInvitaciones_ElRolAjenoDa404(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeInvitaciones(t)
	tA, tB := tenantA, tenantB
	propio := p.roles.Seed(domain.Role{TenantID: &tA, Name: "soporte-a"}, nil)
	ajeno := p.roles.Seed(domain.Role{TenantID: &tB, Name: "soporte-b"}, nil)

	// El propio se acepta y viaja al listado.
	e := p.emitir(t, keyAInvita, `{"role_id":"`+propio.ID+`"}`)
	if e.RoleID != propio.ID {
		t.Fatalf("role_id devuelto = %q, quiero %q", e.RoleID, propio.ID)
	}

	// El ajeno, 404.
	rec := call(p.api, keyAInvita, http.MethodPost, "/api/v1/invitations", `{"role_id":"`+ajeno.ID+`"}`)
	exigirCodigo(t, rec, http.StatusNotFound, "emitir con el rol de otra empresa")

	// La otra mitad, sin la cual el 404 podría venir de que ese rol no exista: a
	// la empresa B SÍ le funciona.
	rec = call(p.api, keyBInvita, http.MethodPost, "/api/v1/invitations", `{"role_id":"`+ajeno.ID+`"}`)
	exigirCodigo(t, rec, http.StatusCreated, "la empresa B emite con SU rol")
}

// TestInvitaciones_LosScopesSeparanVerDeEmitir.
//
// Un `viewer` ('*.read') puede mirar quién tiene invitación pendiente y NO puede
// emitir ni revocar: emitir es meter gente en la empresa. Es el criterio de
// T1.0-3 aplicado a este recurso.
func TestInvitaciones_LosScopesSeparanVerDeEmitir(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeInvitaciones(t)
	e := p.emitir(t, keyAInvita, `{}`)

	rec := call(p.api, keyAViewerInvita, http.MethodGet, "/api/v1/invitations", "")
	exigirCodigo(t, rec, http.StatusOK, "el viewer LISTA (members.read por el glob '*.read')")

	rec = call(p.api, keyAViewerInvita, http.MethodPost, "/api/v1/invitations", `{}`)
	exigirCodigo(t, rec, http.StatusForbidden, "el viewer NO emite (members.write)")

	rec = call(p.api, keyAViewerInvita, http.MethodDelete, "/api/v1/invitations/"+e.ID, "")
	exigirCodigo(t, rec, http.StatusForbidden, "el viewer NO revoca (members.write)")

	// Y sin credencial, ni lo uno ni lo otro.
	rec = call(p.api, "", http.MethodGet, "/api/v1/invitations", "")
	exigirCodigo(t, rec, http.StatusUnauthorized, "sin token no se lista")
}

// TestInvitaciones_RevocarDejaLaFilaRevocada — 🔴 EL CRITERIO DE T-A8.
//
// Se comprueba el ESTADO ESCRITO, no el código de respuesta: un Revoke que
// contestara 204 sin tocar la fila pasaría un test que solo mire el 204, y el
// canje posterior seguiría aceptando el token. El contrato que hereda T-A3 es
// exactamente este: `revoked_at` informado y estado `revoked`, nunca `pending`.
func TestInvitaciones_RevocarDejaLaFilaRevocada(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeInvitaciones(t)
	e := p.emitir(t, keyAInvita, `{}`)

	antes, ok := p.invitations.Get(e.ID)
	if !ok || antes.RevokedAt != nil {
		t.Fatal("la invitación nació revocada o no se escribió: el fixture no prueba nada")
	}

	rec := call(p.api, keyAInvita, http.MethodDelete, "/api/v1/invitations/"+e.ID, "")
	exigirCodigo(t, rec, http.StatusNoContent, "DELETE /api/v1/invitations/{id}")

	despues, ok := p.invitations.Get(e.ID)
	if !ok {
		t.Fatal("la invitación desapareció: revocar MARCA, no borra (el rastro tiene que quedar)")
	}
	if despues.RevokedAt == nil {
		t.Fatal("revoked_at sigue NULL tras un 204: el canje aceptaría ese token")
	}
	if got := despues.Status(time.Now()); got != domain.InvitationRevoked {
		t.Fatalf("estado tras revocar = %q, quiero %q", got, domain.InvitationRevoked)
	}

	// Y el listado lo cuenta: la dueña ve que esa puerta está cerrada.
	rec = call(p.api, keyAInvita, http.MethodGet, "/api/v1/invitations", "")
	var filas []map[string]any
	decodificar(t, rec.Body.Bytes(), &filas)
	if len(filas) != 1 || filas[0]["status"] != string(domain.InvitationRevoked) {
		t.Fatalf("el listado dice %v, quiero una sola fila con status=revoked", filas)
	}
	if filas[0]["revoked_at"] == nil || filas[0]["revoked_at"] == "" {
		t.Fatalf("el listado no trae revoked_at: %v", filas[0])
	}
}

// TestInvitaciones_RevocarEsIdempotenteYNoAlcanzaALaAjena.
func TestInvitaciones_RevocarEsIdempotenteYNoAlcanzaALaAjena(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeInvitaciones(t)
	deA := p.emitir(t, keyAInvita, `{}`)
	deB := p.emitir(t, keyBInvita, `{}`)

	// Idempotente: dos veces, 204 las dos.
	for i := range 2 {
		rec := call(p.api, keyAInvita, http.MethodDelete, "/api/v1/invitations/"+deA.ID, "")
		exigirCodigo(t, rec, http.StatusNoContent, "revocación repetida nº "+string(rune('1'+i)))
	}

	// La de otra empresa: 404, no 403 (no se confirma que exista fuera).
	rec := call(p.api, keyAInvita, http.MethodDelete, "/api/v1/invitations/"+deB.ID, "")
	exigirCodigo(t, rec, http.StatusNotFound, "A revocando la invitación de B")
	fila, ok := p.invitations.Get(deB.ID)
	if !ok || fila.RevokedAt != nil {
		t.Fatal("la invitación de B quedó revocada por alguien de A")
	}
	// Y B sí puede: lo que convierte el 404 de arriba en aislamiento y no en «no
	// había nada».
	rec = call(p.api, keyBInvita, http.MethodDelete, "/api/v1/invitations/"+deB.ID, "")
	exigirCodigo(t, rec, http.StatusNoContent, "B revocando la suya")

	// Un id inexistente y uno que ni siquiera es UUID: los dos 404, nunca 500.
	for _, id := range []string{uuid.NewString(), "no-soy-un-uuid"} {
		rec = call(p.api, keyAInvita, http.MethodDelete, "/api/v1/invitations/"+id, "")
		exigirCodigo(t, rec, http.StatusNotFound, "DELETE de un id "+id)
	}
}

// TestInvitaciones_RevocarUnaYaCanjeadaDa409 — la otra mitad del criterio de
// T-A8: revocar una YA CANJEADA no deshace la membresía, así que no se puede
// contestar 204.
func TestInvitaciones_RevocarUnaYaCanjeadaDa409(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeInvitaciones(t)
	ahora := time.Now().UTC()
	quien := uuid.NewString()
	canjeada := p.invitations.Seed(domain.Invitation{
		TenantID:   tenantA,
		TokenHash:  domain.HashInvitationToken("WAPP-INV-yacanjeadaenelcable"),
		ExpiresAt:  ahora.Add(time.Hour),
		CreatedBy:  uuid.NewString(),
		RedeemedBy: &quien,
		RedeemedAt: &ahora,
	})

	rec := call(p.api, keyAInvita, http.MethodDelete, "/api/v1/invitations/"+canjeada.ID, "")
	exigirCodigo(t, rec, http.StatusConflict, "revocar una invitación ya canjeada")

	fila, _ := p.invitations.Get(canjeada.ID)
	if fila.RevokedAt != nil {
		t.Fatal("una invitación canjeada quedó además marcada como revocada")
	}
}

// TestInvitaciones_ElListadoCuentaLosCuatroEstados: la dueña distingue de un
// vistazo lo vivo de lo que ya no sirve, incluida la caducada — que es la única
// que nadie escribió.
func TestInvitaciones_ElListadoCuentaLosCuatroEstados(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeInvitaciones(t)
	ahora := time.Now().UTC()
	antes := ahora.Add(-time.Hour)
	quien := uuid.NewString()

	base := func(sufijo string) domain.Invitation {
		return domain.Invitation{
			TenantID:  tenantA,
			TokenHash: domain.HashInvitationToken("WAPP-INV-" + sufijo),
			ExpiresAt: ahora.Add(time.Hour),
			CreatedBy: uuid.NewString(),
		}
	}
	viva := base("viva")
	vencida := base("vencida")
	vencida.ExpiresAt = antes
	revocada := base("revocada")
	revocada.RevokedAt = &antes
	canjeada := base("canjeada")
	canjeada.RedeemedBy, canjeada.RedeemedAt = &quien, &antes

	quiero := map[string]string{
		p.invitations.Seed(viva).ID:     string(domain.InvitationPending),
		p.invitations.Seed(vencida).ID:  string(domain.InvitationExpired),
		p.invitations.Seed(revocada).ID: string(domain.InvitationRevoked),
		p.invitations.Seed(canjeada).ID: string(domain.InvitationRedeemed),
	}

	rec := call(p.api, keyAInvita, http.MethodGet, "/api/v1/invitations", "")
	exigirCodigo(t, rec, http.StatusOK, "GET /api/v1/invitations")
	var filas []map[string]any
	decodificar(t, rec.Body.Bytes(), &filas)
	if len(filas) != len(quiero) {
		t.Fatalf("el listado trae %d filas, quiero %d", len(filas), len(quiero))
	}
	for _, fila := range filas {
		id, ok := fila["id"].(string)
		if !ok {
			t.Fatalf("una fila del listado no trae `id` como cadena: %v", fila)
		}
		if fila["status"] != quiero[id] {
			t.Errorf("la invitación %s sale como %v, quiero %q", id, fila["status"], quiero[id])
		}
	}
}

// TestInvitaciones_SinAdministracionLasRutasNOExisten: con Deps.Invitations nil
// las tres rutas dan 404 de ruta inexistente, no 500. Es el mismo criterio que
// el resto del plano de roles.
func TestInvitaciones_SinAdministracionLasRutasNoExisten(t *testing.T) {
	t.Parallel()
	api := newAPI(publicapi.Deps{}, map[string]testIdentity{
		keyAInvita: {TenantID: tenantA, Subject: "admin-a", Grants: []string{"members.read", "members.write"}},
	})
	casos := []struct {
		metodo string
		ruta   string
	}{
		{http.MethodGet, "/api/v1/invitations"},
		{http.MethodPost, "/api/v1/invitations"},
		{http.MethodDelete, "/api/v1/invitations/" + uuid.NewString()},
	}
	for _, c := range casos {
		rec := call(api, keyAInvita, c.metodo, c.ruta, "")
		exigirCodigo(t, rec, http.StatusNotFound, c.metodo+" "+c.ruta+" sin administración cableada")
	}
}

// TestInvitaciones_MetodoEquivocadoDa405: lo produce el http.ServeMux de Go 1.22
// por los patrones método+ruta del registro, no un `if r.Method` del handler.
func TestInvitaciones_MetodoEquivocadoDa405(t *testing.T) {
	t.Parallel()
	p := nuevoPlanoDeInvitaciones(t)
	e := p.emitir(t, keyAInvita, `{}`)

	casos := []struct {
		metodo string
		ruta   string
	}{
		{http.MethodDelete, "/api/v1/invitations"},
		{http.MethodPut, "/api/v1/invitations"},
		{http.MethodGet, "/api/v1/invitations/" + e.ID},
		{http.MethodPost, "/api/v1/invitations/" + e.ID},
	}
	for _, c := range casos {
		rec := call(p.api, keyAInvita, c.metodo, c.ruta, "")
		exigirCodigo(t, rec, http.StatusMethodNotAllowed, c.metodo+" "+c.ruta)
	}
}
