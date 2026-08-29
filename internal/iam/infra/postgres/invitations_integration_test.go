package iampostgres_test

// invitations_integration_test.go — EL ADAPTADOR DE tenant_invitations CONTRA
// POSTGRES DE VERDAD (Plan 047 · Ola A · T-A2 y T-A8).
//
// Lo que NO se puede probar con el doble en memoria y por eso vive aquí:
//
//   - que el CHECK de 32 bytes rechaza de verdad un token guardado en claro;
//   - que el índice ÚNICO sobre token_hash existe y muerde;
//   - que el UPDATE condicionado de la revocación se comporta igual contra el
//     motor real que contra el doble (los tres desenlaces);
//   - que el ORDER BY (created_at DESC, id DESC) lo aplica la base.
//
// ⚠️ Se salta sin WAPP_TEST_DB_DSN, como el resto de la integración del repo. Un
// `--- SKIP` NO es un `--- PASS`.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	iamusecase "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
)

// invitacionDe arma una invitación pendiente para el tenant del entorno, con un
// digest derivado del sufijo (distinto por caso, para no chocar con el índice
// único entre tests que corren en paralelo sobre la MISMA base).
func invitacionDe(tenantID, sufijo string) domain.Invitation {
	return domain.Invitation{
		TenantID:  tenantID,
		TokenHash: domain.HashInvitationToken("WAPP-INV-" + sufijo + "-" + uuid.NewString()),
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour).Truncate(time.Microsecond),
		CreatedBy: uuid.NewString(),
	}
}

// TestIntegration_Invitaciones_EmitirYListar: la fila nace pendiente, la base
// pone id y created_at, y el listado la devuelve entera —digest incluido, que es
// lo que el canje (T-A3) comparará—.
func TestIntegration_Invitaciones_EmitirYListar(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	repo := iampostgres.NewInvitationRepo(env.db)

	creada, err := repo.Create(ctx, invitacionDe(env.tenantID, "recorrido"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// La base pone id y created_at con sus defaults: si el RETURNING no los
	// trajera, el llamante no tendría qué citar en el listado ni qué revocar.
	if creada.ID == "" || creada.CreatedAt.IsZero() {
		t.Fatalf("la base no devolvió id/created_at: %+v", creada)
	}
	if creada.RedeemedAt != nil || creada.RevokedAt != nil || creada.RoleID != nil {
		t.Fatalf("la invitación no nació pendiente y sin rol: %+v", creada)
	}
	if got := creada.Status(time.Now()); got != domain.InvitationPending {
		t.Fatalf("estado recién creada = %q, quiero pending", got)
	}

	filas, err := repo.ListByTenant(ctx, env.tenantID)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(filas) != 1 || filas[0].ID != creada.ID {
		t.Fatalf("listado = %+v, quiero solo la recién creada (%s)", filas, creada.ID)
	}
	// El digest vuelve entero de la base: son los 32 bytes que se guardaron, y es
	// lo que el canje (T-A3) comparará.
	if string(filas[0].TokenHash) != string(creada.TokenHash) {
		t.Fatalf("token_hash leído = %x, escrito = %x", filas[0].TokenHash, creada.TokenHash)
	}
}

// TestIntegration_Invitaciones_RevocarMarcaYNoBorra: la revocación deja la fila
// con revoked_at informado y el rastro puesto — no la borra.
func TestIntegration_Invitaciones_RevocarMarcaYNoBorra(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	repo := iampostgres.NewInvitationRepo(env.db)

	creada, err := repo.Create(ctx, invitacionDe(env.tenantID, "revocable"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.Revoke(ctx, creada.ID, env.tenantID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	filas, err := repo.ListByTenant(ctx, env.tenantID)
	if err != nil {
		t.Fatalf("ListByTenant tras revocar: %v", err)
	}
	if len(filas) != 1 {
		t.Fatalf("revocar BORRÓ la fila: quedan %d, quiero 1 (revocar marca, no borra)", len(filas))
	}
	if filas[0].RevokedAt == nil {
		t.Fatal("revoked_at sigue NULL en la base tras un Revoke sin error")
	}
	if got := filas[0].Status(time.Now()); got != domain.InvitationRevoked {
		t.Fatalf("estado tras revocar = %q, quiero revoked", got)
	}
}

// TestIntegration_Invitaciones_ElCheckDe32BytesMuerde.
//
// 🔴 Es la mitad que el doble en memoria NO puede sostener: un `[]byte(token)`
// descuidado —el token en claro escrito donde va el digest— falla en el INSERT
// en vez de colarse. Y la otra mitad, sin la cual esto no probaría nada: la fila
// BUENA sí entra (un rechazo sobre una tabla que no acepta nada no dice nada).
func TestIntegration_Invitaciones_ElCheckDe32BytesMuerde(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	repo := iampostgres.NewInvitationRepo(env.db)

	if _, err := repo.Create(ctx, invitacionDe(env.tenantID, "buena")); err != nil {
		t.Fatalf("la fila BUENA tiene que entrar: %v", err)
	}

	mala := invitacionDe(env.tenantID, "mala")
	// El token EN CLARO tal cual, que es exactamente el descuido que el CHECK
	// existe para cazar (mide 41 bytes, no 32).
	mala.TokenHash = []byte("WAPP-INV-0123456789abcdef0123456789abcdef")
	if _, err := repo.Create(ctx, mala); err == nil {
		t.Fatal("la base aceptó un token_hash que NO mide 32 bytes: el CHECK " +
			"tenant_invitations_token_hash_len_check no está vigilando")
	}

	// Y un digest en hex (64 bytes), el otro descuido clásico.
	hexeado := invitacionDe(env.tenantID, "hex")
	hexeado.TokenHash = []byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if _, err := repo.Create(ctx, hexeado); err == nil {
		t.Fatal("la base aceptó un digest guardado en hex (64 bytes)")
	}
}

// TestIntegration_Invitaciones_ElDigestEsUNICO: dos invitaciones con el mismo
// digest harían ambiguo el canje, así que la base lo impide.
func TestIntegration_Invitaciones_ElDigestEsUnico(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	repo := iampostgres.NewInvitationRepo(env.db)

	inv := invitacionDe(env.tenantID, "unico")
	if _, err := repo.Create(ctx, inv); err != nil {
		t.Fatalf("primer Create: %v", err)
	}
	_, err := repo.Create(ctx, inv)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("segundo Create con el mismo digest: err = %v, quiero ErrConflict", err)
	}
}

// TestIntegration_Invitaciones_LosTresDesenlacesDeLaRevocacion contra el motor
// real. El UPDATE lleva la condición dentro (redeemed_at IS NULL AND revoked_at
// IS NULL), que es lo único que resuelve la carrera con el canje.
func TestIntegration_Invitaciones_LosTresDesenlacesDeLaRevocacion(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	repo := iampostgres.NewInvitationRepo(env.db)

	// (1) Viva ⇒ se revoca; y repetirlo es idempotente.
	viva, err := repo.Create(ctx, invitacionDe(env.tenantID, "viva"))
	if err != nil {
		t.Fatalf("Create viva: %v", err)
	}
	if err := repo.Revoke(ctx, viva.ID, env.tenantID); err != nil {
		t.Fatalf("primera revocación: %v", err)
	}
	if err := repo.Revoke(ctx, viva.ID, env.tenantID); err != nil {
		t.Fatalf("revocación repetida (debe ser idempotente): %v", err)
	}

	// (2) Ya CANJEADA ⇒ ErrConflict, y la fila NO queda además revocada. El canje
	// se simula con el UPDATE que hará T-A3: las dos mitades del par a la vez, que
	// es lo que exige el CHECK tenant_invitations_redeemed_pair_check.
	canjeada, err := repo.Create(ctx, invitacionDe(env.tenantID, "canjeada"))
	if err != nil {
		t.Fatalf("Create canjeada: %v", err)
	}
	if _, err := env.db.ExecContext(ctx, `
		UPDATE public.tenant_invitations
		SET redeemed_by = $2, redeemed_at = now()
		WHERE id = $1 AND redeemed_at IS NULL AND revoked_at IS NULL
	`, canjeada.ID, uuid.NewString()); err != nil {
		t.Fatalf("simulando el canje: %v", err)
	}
	if err := repo.Revoke(ctx, canjeada.ID, env.tenantID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("revocar una canjeada: err = %v, quiero ErrConflict", err)
	}
	var revokedAt *time.Time
	if err := env.db.QueryRowContext(ctx,
		`SELECT revoked_at FROM public.tenant_invitations WHERE id = $1`, canjeada.ID).Scan(&revokedAt); err != nil {
		t.Fatalf("releyendo la canjeada: %v", err)
	}
	if revokedAt != nil {
		t.Fatal("la invitación canjeada quedó además marcada como revocada: el UPDATE no llevaba su condición")
	}

	// (3) De OTRA empresa (o inexistente) ⇒ ErrNotFound, y no se toca nada.
	otra := newITEnv(t)
	ajena, err := repo.Create(ctx, invitacionDe(otra.tenantID, "ajena"))
	if err != nil {
		t.Fatalf("Create ajena: %v", err)
	}
	if err := repo.Revoke(ctx, ajena.ID, env.tenantID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revocar la de otra empresa: err = %v, quiero ErrNotFound", err)
	}
	if err := env.db.QueryRowContext(ctx,
		`SELECT revoked_at FROM public.tenant_invitations WHERE id = $1`, ajena.ID).Scan(&revokedAt); err != nil {
		t.Fatalf("releyendo la ajena: %v", err)
	}
	if revokedAt != nil {
		t.Fatal("se revocó la invitación de OTRA empresa: el tenant_id no estaba en el WHERE")
	}
	if err := repo.Revoke(ctx, uuid.NewString(), env.tenantID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("revocar un id inexistente debe ser ErrNotFound")
	}
}

// TestIntegration_Invitaciones_ElOrdenLoAplicaLaBase: (created_at DESC, id DESC).
// Es el ORDER BY que T-A2 fija y del que depende que el listado no cambie de
// orden entre dos recargas.
func TestIntegration_Invitaciones_ElOrdenLoAplicaLaBase(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	repo := iampostgres.NewInvitationRepo(env.db)

	ids := make([]string, 0, 5)
	for i := range 5 {
		inv, err := repo.Create(ctx, invitacionDe(env.tenantID, "orden"))
		if err != nil {
			t.Fatalf("Create nº %d: %v", i, err)
		}
		ids = append(ids, inv.ID)
	}

	filas, err := repo.ListByTenant(ctx, env.tenantID)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(filas) != len(ids) {
		t.Fatalf("listado de %d filas, quiero %d", len(filas), len(ids))
	}
	// Las más recientes primero: el orden inverso al de creación.
	for i, fila := range filas {
		quiero := ids[len(ids)-1-i]
		if fila.ID != quiero {
			t.Fatalf("posición %d = %s, quiero %s (las más recientes primero)", i, fila.ID, quiero)
		}
	}
	// Y es ESTABLE: dos lecturas seguidas dan lo mismo. Sin el desempate por id,
	// dos filas del mismo microsegundo podrían alternar entre plan y plan.
	otraVez, err := repo.ListByTenant(ctx, env.tenantID)
	if err != nil {
		t.Fatalf("segunda ListByTenant: %v", err)
	}
	for i := range filas {
		if filas[i].ID != otraVez[i].ID {
			t.Fatalf("el orden no es estable: posición %d dio %s y luego %s", i, filas[i].ID, otraVez[i].ID)
		}
	}
}

// TestIntegration_Invitaciones_ElListadoNoCruzaEmpresas: dos tenants, cada uno ve
// lo suyo.
func TestIntegration_Invitaciones_ElListadoNoCruzaEmpresas(t *testing.T) {
	t.Parallel()
	a := newITEnv(t)
	b := newITEnv(t)
	ctx := context.Background()
	repo := iampostgres.NewInvitationRepo(a.db)

	deA, err := repo.Create(ctx, invitacionDe(a.tenantID, "de-a"))
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}
	deB, err := repo.Create(ctx, invitacionDe(b.tenantID, "de-b"))
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}

	filasA, err := repo.ListByTenant(ctx, a.tenantID)
	if err != nil {
		t.Fatalf("ListByTenant(A): %v", err)
	}
	if len(filasA) != 1 || filasA[0].ID != deA.ID {
		t.Fatalf("la empresa A ve %+v, quiero solo %s", filasA, deA.ID)
	}
	filasB, err := repo.ListByTenant(ctx, b.tenantID)
	if err != nil {
		t.Fatalf("ListByTenant(B): %v", err)
	}
	if len(filasB) != 1 || filasB[0].ID != deB.ID {
		t.Fatalf("la empresa B ve %+v, quiero solo %s", filasB, deB.ID)
	}
}

// TestIntegration_Invitaciones_ElRolViajaYSobreviveAlBorradoDelRol.
//
// ON DELETE SET NULL y no CASCADE: si el rol se borra, la invitación sigue VIVA
// y dará de alta SIN rol. Borrar un rol no puede anular por sorpresa las
// incorporaciones en vuelo, y eso solo se ve contra la base real.
func TestIntegration_Invitaciones_ElRolViajaYSobreviveAlBorradoDelRol(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	repo := iampostgres.NewInvitationRepo(env.db)
	roles := iampostgres.NewRoleRepo(env.db)

	rol, err := roles.Create(ctx, domain.Role{TenantID: &env.tenantID, Name: "invitado-it"})
	if err != nil {
		t.Fatalf("crear rol: %v", err)
	}
	inv := invitacionDe(env.tenantID, "con-rol")
	inv.RoleID = &rol.ID
	creada, err := repo.Create(ctx, inv)
	if err != nil {
		t.Fatalf("Create con rol: %v", err)
	}
	if creada.RoleID == nil || *creada.RoleID != rol.ID {
		t.Fatalf("role_id devuelto = %v, quiero %s", creada.RoleID, rol.ID)
	}

	if _, err := env.db.ExecContext(ctx, `DELETE FROM public.iam_roles WHERE id = $1`, rol.ID); err != nil {
		t.Fatalf("borrando el rol: %v", err)
	}
	filas, err := repo.ListByTenant(ctx, env.tenantID)
	if err != nil {
		t.Fatalf("ListByTenant tras borrar el rol: %v", err)
	}
	if len(filas) != 1 {
		t.Fatalf("la invitación desapareció al borrar el rol: quedan %d filas (¿CASCADE en vez de SET NULL?)", len(filas))
	}
	if filas[0].RoleID != nil {
		t.Fatalf("role_id = %v tras borrar el rol, quiero NULL (ON DELETE SET NULL)", *filas[0].RoleID)
	}
}

// llamanteDe adapta un tenant fijo al puerto in.CallerResolver, igual que hace
// bootstrap.buildRolePlane con la Identity del token.
func llamanteDe(tenantID, userID string) in.CallerResolver {
	return in.CallerResolverFunc(func(_ context.Context) (in.Caller, bool) {
		return in.Caller{TenantID: tenantID, UserID: userID}, true
	})
}

// TestIntegration_Invitaciones_ElTTLLlegaHastaLaColumna — EL CÍRCULO COMPLETO
// del TTL: usecase real → adaptador real → columna TIMESTAMPTZ real.
//
// 🔴 POR QUÉ NO BASTA EL TEST DEL DOBLE. El default y el clamp los aplica el
// emisor, y lo que los tests de más arriba comprueban es el `expires_at` que el
// emisor CALCULA. Entre ese cálculo y lo que gobierna de verdad el canje queda un
// viaje —el parámetro $4, el tipo TIMESTAMPTZ, la zona horaria de la sesión— que
// solo este test recorre. Se lee la columna con SQL crudo y se compara contra
// `now()` DE LA PROPIA BASE, no contra el reloj de Go: comparar un timestamp de
// Postgres con uno de Go compara dos relojes distintos, y el desfase es
// silencioso y permanente.
//
// Los cinco casos son los mismos del contract test, y los tres con `ttl` distinto
// del default son los que caen si alguien renombra la clave del cable.
func TestIntegration_Invitaciones_ElTTLLlegaHastaLaColumna(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	svc, err := iamusecase.NewInvitationService(
		llamanteDe(env.tenantID, uuid.NewString()),
		iampostgres.NewInvitationRepo(env.db),
		iampostgres.NewRoleRepo(env.db),
	)
	if err != nil {
		t.Fatalf("NewInvitationService: %v", err)
	}

	casos := []struct {
		nombre string
		ttl    int
		quiero time.Duration
	}{
		{"ausente ⇒ 24 h por defecto", 0, 24 * time.Hour},
		{"negativo ⇒ se trata como ausente", -1, 24 * time.Hour},
		{"explícito, DISTINTO del default", 3600, time.Hour},
		{"por debajo del suelo ⇒ 60 s", 1, time.Minute},
		{"por encima del techo ⇒ 30 días", 99999999, 30 * 24 * time.Hour},
	}
	for _, c := range casos {
		emitida, err := svc.IssueInvitation(ctx, in.IssueInvitationInput{TTLSeconds: c.ttl})
		if err != nil {
			t.Fatalf("%s: IssueInvitation: %v", c.nombre, err)
		}
		// La vida la calcula POSTGRES sobre su propia columna y su propio reloj:
		// `expires_at - now()`. Así no hay dos relojes en la resta.
		var vida time.Duration
		var segundos float64
		if err := env.db.QueryRowContext(ctx, `
			SELECT EXTRACT(EPOCH FROM (expires_at - now()))
			FROM public.tenant_invitations WHERE id = $1
		`, emitida.Invitation.ID).Scan(&segundos); err != nil {
			t.Fatalf("%s: leyendo expires_at de la columna: %v", c.nombre, err)
		}
		vida = time.Duration(segundos * float64(time.Second))
		// El margen es de 5 s por los dos lados: entre el cálculo del emisor y el
		// `now()` de la base pasan el INSERT y esta consulta.
		if vida < c.quiero-5*time.Second || vida > c.quiero+5*time.Second {
			t.Errorf("%s: la columna expires_at deja una vida de %v, quiero ~%v", c.nombre, vida, c.quiero)
		}
	}
}

// TestIntegration_Invitaciones_LasCuatroNULLablesLleganALaEntidad — LA PARIDAD
// COLUMNA ↔ ENTIDAD de scanInvitation, las cuatro a la vez.
//
// 🔴 POR QUÉ EXISTE, Y QUÉ AGUJERO TAPA. Un campo NULLable que el SELECT trae y
// el scan NO traslada a la entidad no rompe nada visible: la fila llega, el
// listado responde 200 y el campo sale como «no ocurrió». Los tests que había
// tocaban `RedeemedBy`/`RedeemedAt` SOLO con asertos NEGATIVOS —«la invitación
// recién creada no está canjeada»—, y un aserto negativo es VACUO cuando ningún
// fixture recorre la rama por la que entraría el dato: quitando esas dos líneas
// del scan seguía todo verde. Medido: la mutación salía VERDE antes de este test
// y cae con él.
//
// Lo que costaría en producción no es un detalle interno: una invitación YA
// CANJEADA se listaría como `pending`, porque Status() decide por esos punteros.
// La dueña vería viva una puerta que alguien ya usó — y no la revocaría, porque
// parece disponible.
//
// Se siembra por SQL DIRECTO y no por el repositorio a propósito: lo que se está
// probando es el camino de LECTURA, y escribir con el mismo código que se lee
// dejaría pasar un par escritor/lector que se equivoque igual en los dos lados.
func TestIntegration_Invitaciones_LasCuatroNullablesLleganALaEntidad(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	repo := iampostgres.NewInvitationRepo(env.db)
	roles := iampostgres.NewRoleRepo(env.db)

	rol, err := roles.Create(ctx, domain.Role{TenantID: &env.tenantID, Name: "nullables-it"})
	if err != nil {
		t.Fatalf("crear rol: %v", err)
	}
	quienCanjeo := uuid.NewString()

	// Fila CANJEADA y con rol: cubre role_id, redeemed_by y redeemed_at.
	canjeada := invitacionDe(env.tenantID, "nullables-canjeada")
	if _, err := env.db.ExecContext(ctx, `
		INSERT INTO public.tenant_invitations
			(tenant_id, token_hash, role_id, expires_at, created_by, redeemed_by, redeemed_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
	`, canjeada.TenantID, canjeada.TokenHash, rol.ID, canjeada.ExpiresAt, canjeada.CreatedBy, quienCanjeo); err != nil {
		t.Fatalf("sembrando la canjeada: %v", err)
	}
	// Fila REVOCADA: cubre revoked_at.
	revocada := invitacionDe(env.tenantID, "nullables-revocada")
	if _, err := env.db.ExecContext(ctx, `
		INSERT INTO public.tenant_invitations
			(tenant_id, token_hash, expires_at, created_by, revoked_at)
		VALUES ($1, $2, $3, $4, now())
	`, revocada.TenantID, revocada.TokenHash, revocada.ExpiresAt, revocada.CreatedBy); err != nil {
		t.Fatalf("sembrando la revocada: %v", err)
	}

	filas, err := repo.ListByTenant(ctx, env.tenantID)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(filas) != 2 {
		t.Fatalf("listado de %d filas, quiero 2", len(filas))
	}

	var vistaCanjeada, vistaRevocada bool
	for _, fila := range filas {
		switch {
		case fila.RedeemedAt != nil || fila.RedeemedBy != nil:
			vistaCanjeada = true
			exigirParDelCanje(t, fila, quienCanjeo, rol.ID)
		case fila.RevokedAt != nil:
			vistaRevocada = true
			exigirRevocacion(t, fila)
		}
	}
	// 🚨 GUARDA ANTI-HUECO: sin esto, un scan que dejara las cuatro en nil pasaría
	// el bucle entero sin entrar en ninguna rama — y el test verde vigilaría una
	// pared. Es el mismo defecto que este fichero existe para cerrar.
	if !vistaCanjeada {
		t.Fatal("ninguna fila llegó con redeemed_by/redeemed_at informados: el scan NO cablea el par del canje, " +
			"y una invitación ya usada se listaría como pendiente")
	}
	if !vistaRevocada {
		t.Fatal("ninguna fila llegó con revoked_at informado: el scan NO cablea la revocación")
	}
}

// exigirParDelCanje comprueba que las tres columnas de una invitación consumida
// —el par (redeemed_by, redeemed_at) y el rol— llegaron a la entidad, y que
// Status la clasifica en consecuencia.
func exigirParDelCanje(t *testing.T, fila domain.Invitation, quienCanjeo, rolID string) {
	t.Helper()
	if fila.RedeemedBy == nil || *fila.RedeemedBy != quienCanjeo {
		t.Errorf("redeemed_by no llegó a la entidad: %v, quiero %s", fila.RedeemedBy, quienCanjeo)
	}
	if fila.RedeemedAt == nil || fila.RedeemedAt.IsZero() {
		t.Error("redeemed_at no llegó a la entidad")
	}
	if fila.RoleID == nil || *fila.RoleID != rolID {
		t.Errorf("role_id no llegó a la entidad: %v, quiero %s", fila.RoleID, rolID)
	}
	if got := fila.Status(time.Now()); got != domain.InvitationRedeemed {
		t.Errorf("una invitación canjeada se clasifica como %q: la dueña la vería viva", got)
	}
}

// exigirRevocacion comprueba que revoked_at llegó a la entidad y que Status la
// da por revocada.
func exigirRevocacion(t *testing.T, fila domain.Invitation) {
	t.Helper()
	if fila.RevokedAt == nil || fila.RevokedAt.IsZero() {
		t.Error("revoked_at no llegó a la entidad")
	}
	if got := fila.Status(time.Now()); got != domain.InvitationRevoked {
		t.Errorf("una invitación revocada se clasifica como %q", got)
	}
}
