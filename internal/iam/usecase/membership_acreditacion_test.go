package usecase_test

// membership_acreditacion_test.go — EL ALTA ACREDITA ANTES DE ESCRIBIR
// (Plan 047 · Ola B, T-B2/T-B3/T-B4).
//
// Lo que estos tests fijan no es «que se llame a identity», sino las tres
// propiedades por cuya ausencia una persona podía quedar de alta sin poder
// entrar, o entrar y perder otro acceso:
//
//	T-B2  el orden: si la acreditación falla, NO se escribe la membresía.
//	T-B3  la unión: al PUT viaja lo que ya tenía MÁS wapp.bff, no wapp.bff a secas.
//	T-B4  la parquedad: si ya la tenía, el PUT no se llama ni una vez.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/usecase"
)

// identidadDeMentira es el doble de out.UserSystemsClient. Guarda el conjunto
// vigente y lo ACTUALIZA cuando el PUT sale bien, que es lo que hace el identity
// de verdad: sin eso, un test de idempotencia no podría distinguir «no volvió a
// escribir porque ya estaba» de «no escribe nunca».
type identidadDeMentira struct {
	mu       sync.Mutex
	vigentes []string
	errGet   error
	errPut   error
	gets     int
	puts     [][]string
}

var _ out.UserSystemsClient = (*identidadDeMentira)(nil)

func (f *identidadDeMentira) GetUserSystems(_ context.Context, _ string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.errGet != nil {
		return nil, f.errGet
	}
	// Copia: el conjunto que devuelve el puerto no es del llamante, y si este
	// doble entregara su propio arreglo, un append del usecase podría escribir
	// dentro del estado del doble y tapar justo el defecto que se busca.
	return append([]string{}, f.vigentes...), nil
}

func (f *identidadDeMentira) ReplaceUserSystems(_ context.Context, _ string, systems []string) (domain.IdentitySystemsDiff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, append([]string{}, systems...))
	if f.errPut != nil {
		return domain.IdentitySystemsDiff{}, f.errPut
	}
	f.vigentes = append([]string{}, systems...)
	return domain.IdentitySystemsDiff{Systems: f.vigentes, Granted: []string{}, Revoked: []string{}}, nil
}

// escrituras devuelve los conjuntos que viajaron en cada PUT.
func (f *identidadDeMentira) escrituras() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.puts...)
}

// repoQueCuentaAltas envuelve el MembershipRepo real (el doble en memoria) y
// cuenta las llamadas a Add. Es lo que permite afirmar «CERO escrituras» sin
// depender de mirar la tabla después: una tabla vacía también la deja un Add que
// se llamó y falló, y lo que T-B2 exige es que no llegue a llamarse.
type repoQueCuentaAltas struct {
	out.MembershipRepo
	mu    sync.Mutex
	altas int
}

func (r *repoQueCuentaAltas) Add(ctx context.Context, userID, tenantID string) error {
	r.mu.Lock()
	r.altas++
	r.mu.Unlock()
	return r.MembershipRepo.Add(ctx, userID, tenantID)
}

func (r *repoQueCuentaAltas) llamadas() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.altas
}

// TestAddMember_SiLaAcreditacionFallaNoSeEscribeLaMembresia — T-B2.
//
// El orden es la mitad del arreglo: acreditar primero significa que un identity
// caído deja el estado ANTERIOR (ni fila ni acceso) y que reintentar es
// idempotente. Al revés, cada fallo dejaría a una persona que es miembro y no
// puede entrar — el defecto exacto que esta ola cierra.
func TestAddMember_SiLaAcreditacionFallaNoSeEscribeLaMembresia(t *testing.T) {
	t.Parallel()

	// Las DOS mitades de la acreditación pueden fallar, y las dos tienen que
	// abortar el alta: leer el conjunto y declararlo.
	for nombre, romper := range map[string]func(*identidadDeMentira){
		"identity no contesta la lectura": func(f *identidadDeMentira) { f.errGet = domain.ErrIdentityUnavailable },
		"identity rechaza la escritura":   func(f *identidadDeMentira) { f.errPut = domain.ErrSystemNotAllowed },
	} {
		t.Run(nombre, func(t *testing.T) {
			t.Parallel()
			f := newMembershipFixture(t)
			romper(f.identity)
			userID := uuid.NewString()

			err := f.svc.AddMember(ctxOf(testTenant), in.MembershipInput{UserID: userID})
			if err == nil {
				t.Fatal("AddMember devolvió nil: una acreditación fallida no puede dar de alta a nadie")
			}
			if n := f.altas.llamadas(); n != 0 {
				t.Errorf("members.Add se llamó %d veces; quiero CERO: la membresía no puede escribirse "+
					"antes de que identity acredite la aplicación", n)
			}
			if tenants := f.tenantsDe(t, userID); len(tenants) != 0 {
				t.Errorf("quedó membresía tras el fallo: %v", tenants)
			}
		})
	}
}

// TestAddMember_ElConjuntoQueViajaEsLaUNION — T-B3.
//
// ReplaceUserSystems es DECLARATIVO: lo que no viaja queda revocado. Mandar
// `["wapp.bff"]` a secas le quitaría a esa persona cualquier otra aplicación que
// tuviera, y el síntoma no aparecería en el alta sino en su siguiente login
// contra la otra. Por eso el assert es sobre el conjunto EXACTO y su orden, no
// sobre «contiene wapp.bff».
//
// Este es el test que no existía y por cuya ausencia pudo nacer el patrón de
// aproximar la unión con una tabla local de wApp.
func TestAddMember_ElConjuntoQueViajaEsLaUNION(t *testing.T) {
	t.Parallel()

	casos := []struct {
		nombre   string
		vigentes []string
		quiero   []string
	}{
		// El caso literal del criterio: lo que ya tenía se conserva ENTERO.
		{"con una aplicación previa", []string{"edugo.web"}, []string{"edugo.web", "wapp.bff"}},
		// El caso realista dentro del ecosistema: identity solo devuelve claves de
		// wApp (ADR-0016), y `wapp.edge` es la que de verdad está en juego — es la
		// del relé del Edge, y perderla deja al operador fuera de su consola local.
		{"con la del Edge", []string{"wapp.edge"}, []string{"wapp.edge", "wapp.bff"}},
		{"con varias", []string{"edugo.web", "wapp.edge"}, []string{"edugo.web", "wapp.edge", "wapp.bff"}},
		// Y el alta de alguien que no tenía ninguna: el conjunto es solo la nueva.
		{"sin ninguna previa", nil, []string{"wapp.bff"}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			t.Parallel()
			f := newMembershipFixture(t)
			f.identity.vigentes = c.vigentes

			if err := f.svc.AddMember(ctxOf(testTenant), in.MembershipInput{UserID: uuid.NewString()}); err != nil {
				t.Fatalf("AddMember: %v", err)
			}
			puts := f.identity.escrituras()
			if len(puts) != 1 {
				t.Fatalf("escrituras en identity = %d, quiero exactamente 1", len(puts))
			}
			if !iguales(puts[0], c.quiero) {
				t.Errorf("al PUT viajó %v; quiero %v (el conjunto ENTERO, no solo la clave nueva: "+
					"lo que no viaja queda revocado)", puts[0], c.quiero)
			}
		})
	}
}

// TestAddMember_SiYaTeniaLaAplicacionNoSeEscribeEnIdentity — T-B4.
//
// El alta es idempotente y la consola la reintenta, así que «volver a declarar
// lo mismo» no es gratis: le cuesta a identity una escritura y una línea de
// registro por cada repetición. Y el 204 no cambia — desde fuera, «no había nada
// que cambiar» y «se escribió» son indistinguibles a propósito: el 204 promete
// un ESTADO, no un número de escrituras.
func TestAddMember_SiYaTeniaLaAplicacionNoSeEscribeEnIdentity(t *testing.T) {
	t.Parallel()
	f := newMembershipFixture(t)
	f.identity.vigentes = []string{"wapp.bff"}
	userID := uuid.NewString()

	if err := f.svc.AddMember(ctxOf(testTenant), in.MembershipInput{UserID: userID}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if puts := f.identity.escrituras(); len(puts) != 0 {
		t.Errorf("se llamó al PUT %d veces con la aplicación ya concedida: %v", len(puts), puts)
	}
	// Y el alta sí ocurrió: sin esto, un `return nil` prematuro pasaría el test.
	if tenants := f.tenantsDe(t, userID); len(tenants) != 1 || tenants[0] != testTenant {
		t.Errorf("membresías = %v, quiero [%s]", tenants, testTenant)
	}

	// La segunda alta de la MISMA persona tampoco escribe: tras el primer PUT el
	// conjunto ya contiene la clave. Es el camino que recorre la consola cuando
	// alguien pulsa dos veces.
	otro := uuid.NewString()
	f.identity.vigentes = nil
	if err := f.svc.AddMember(ctxOf(testTenant), in.MembershipInput{UserID: otro}); err != nil {
		t.Fatalf("AddMember (alguien sin accesos): %v", err)
	}
	if err := f.svc.AddMember(ctxOf(testTenant), in.MembershipInput{UserID: otro}); err != nil {
		t.Fatalf("AddMember repetida: %v", err)
	}
	if puts := f.identity.escrituras(); len(puts) != 1 {
		t.Errorf("escrituras = %d (%v), quiero 1: la repetición no debe volver a declarar lo mismo", len(puts), puts)
	}
}

// TestAddMember_SinClienteM2MNoSeEscribeNada cubre el desenlace de despliegue
// sin WAPP_IDENTITY_API_KEY visto desde el usecase: ErrIdentityNotConfigured y
// CERO escrituras. El código HTTP que le corresponde (503) se fija en el plano
// público; aquí lo que importa es que no quede una fila huérfana.
func TestAddMember_SinClienteM2MNoSeEscribeNada(t *testing.T) {
	t.Parallel()
	store := memory.NewStore()
	altas := &repoQueCuentaAltas{MembershipRepo: store.Memberships}
	svc, err := usecase.NewMembershipService(testResolver, altas, nil, quietLogger())
	if err != nil {
		t.Fatalf("NewMembershipService sin cliente M2M debe construirse (la lectura de miembros sigue sirviendo): %v", err)
	}

	err = svc.AddMember(ctxOf(testTenant), in.MembershipInput{UserID: uuid.NewString()})
	if !errors.Is(err, domain.ErrIdentityNotConfigured) {
		t.Fatalf("err = %v, quiero ErrIdentityNotConfigured", err)
	}
	if n := altas.llamadas(); n != 0 {
		t.Errorf("members.Add se llamó %d veces sin poder acreditar; quiero CERO", n)
	}
}

// iguales compara dos conjuntos POSICIÓN A POSICIÓN. No es un `sort` + compara:
// el orden que viaja al PUT es estable por construcción (el de identity más la
// clave nueva al final) y afirmarlo así deja el contrato escrito.
func iguales(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestAcreditacion_ElLogDISTINGUELaCredencialDelRestoDeFallos.
//
// 🔴 Al llamante se le contesta lo MISMO —un 500 genérico— cuando identity
// rechaza la credencial M2M de wApp: es un fallo del servidor, y contarle a un
// administrador que a wApp le falta un scope no le sirve y cuenta de más. Cuando
// la respuesta funde dos causas a propósito, el LOG es el único sitio donde vive
// la diferencia; si ahí también se funden, quien diagnostica se queda ciego.
//
// Es la misma lección que costó una tarde el 2026-08-28 con el 401/403 del login
// de la consola, y por eso este test está escrito como el suyo: cada caso exige
// su marcador Y PROHÍBE el del otro. Con una sola dirección, fundir las ramas en
// el mensaje de la credencial pasaría verde.
func TestAcreditacion_ElLogDISTINGUELaCredencialDelRestoDeFallos(t *testing.T) {
	t.Parallel()

	const marcadorCredencial = "identity.users.systems.read"
	const marcadorGenerico = "acreditacion_fallida"

	casos := []struct {
		nombre     string
		fallo      error
		esperado   string
		noEsperado string
	}{
		// A la credencial de wApp le falta el scope de LECTURA (403 FORBIDDEN de
		// identity). La línea tiene que llevar a «reemite la API key con
		// identity.users.systems.read» sin abrir el código.
		{"credencial sin scope", domain.ErrMachineCredentialInvalid, marcadorCredencial, marcadorGenerico},
		// Todo lo demás: identity caído, la persona que no existe, el conjunto
		// rechazado. Aquí el error sí describe el problema y NO se puede insinuar
		// que haya que tocar la credencial — mandaría a reemitir una key que está bien.
		{"identity caído", domain.ErrIdentityUnavailable, marcadorGenerico, marcadorCredencial},
		{"la persona no existe", domain.ErrNotFound, marcadorGenerico, marcadorCredencial},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			store := memory.NewStore()
			svc, err := usecase.NewMembershipService(testResolver, store.Memberships,
				&identidadDeMentira{errGet: c.fallo}, sharedlogger.New(sharedlogger.WithWriter(&buf)))
			if err != nil {
				t.Fatalf("NewMembershipService: %v", err)
			}

			userID := uuid.NewString()
			if err := svc.AddMember(ctxOf(testTenant), in.MembershipInput{UserID: userID}); !errors.Is(err, c.fallo) {
				t.Fatalf("err = %v, quiero %v", err, c.fallo)
			}

			escrito := buf.String()
			if !strings.Contains(escrito, c.esperado) {
				t.Fatalf("el rastro tiene que decir %q y dice: %s", c.esperado, escrito)
			}
			if strings.Contains(escrito, c.noEsperado) {
				t.Fatalf("el rastro NO puede decir %q: %s", c.noEsperado, escrito)
			}
			// El user_id SÍ va: es un id opaco de identity y es lo único que permite
			// seguir un alta concreta entre varias.
			if !strings.Contains(escrito, userID) {
				t.Errorf("el rastro no lleva el user_id (%s): sin él no se puede seguir un alta concreta: %s", userID, escrito)
			}
		})
	}
}

// TestAcreditacion_SinLoggerNoRevienta: el logger es OPCIONAL (nil no loguea),
// como en DelegatedAuthService. Sin este caso, un despliegue que lo construyera
// sin rastro moriría de un nil dereference en el primer fallo de identity —el
// peor momento posible.
//
// ⚠️ Lo que este fichero NO puede demostrar es «cero credenciales en el rastro»:
// por este paquete no pasan ni la API key ni ningún correo. La key vive dentro
// del adaptador M2M, que no loguea NADA por decisión escrita en su cabecera, y
// los errores que devuelve nombran la operación y el código HTTP. Un assert de
// «no contiene la key» aquí sería vacuo: no hay rama por la que pudiera entrar.
func TestAcreditacion_SinLoggerNoRevienta(t *testing.T) {
	t.Parallel()
	store := memory.NewStore()
	svc, err := usecase.NewMembershipService(testResolver, store.Memberships,
		&identidadDeMentira{errGet: domain.ErrMachineCredentialInvalid}, nil)
	if err != nil {
		t.Fatalf("NewMembershipService: %v", err)
	}
	if err := svc.AddMember(ctxOf(testTenant), in.MembershipInput{UserID: uuid.NewString()}); !errors.Is(err, domain.ErrMachineCredentialInvalid) {
		t.Fatalf("err = %v, quiero ErrMachineCredentialInvalid", err)
	}
}
