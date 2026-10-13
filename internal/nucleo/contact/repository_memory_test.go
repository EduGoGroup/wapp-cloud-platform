package contact_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact/contacthelpertest"
	"github.com/google/uuid"
)

// Este test es EXTERNO (package contact_test): contacthelpertest importa contact, así que un test interno
// que importara contacthelpertest daría un ciclo de imports. Por eso prueba solo por los exportados.

// Aserciones de compilación de lo que MemoryResolver promete: es un Resolver (el puerto), el
// constructor recibe el migrador (nil se admite) y NO devuelve un error, y Resolve y Destino tienen
// las firmas del puerto. migradorStub, el doble de este fichero, es un StateMigrator.
var (
	_ contact.Resolver      = (*contact.MemoryResolver)(nil)
	_ contact.StateMigrator = (*migradorStub)(nil)

	_ func(contact.StateMigrator) *contact.MemoryResolver = contact.NewMemoryResolver

	_ func(*contact.MemoryResolver, context.Context, string, []contact.Ref, string) (string, error) = (*contact.MemoryResolver).Resolve
	_ func(*contact.MemoryResolver, context.Context, string, string) (contact.Ref, error)           = (*contact.MemoryResolver).Destino
)

// Los valores de las refs de estos tests: ya normalizados, válidos para contact.NewRef.
const (
	numeroAna  = "573001112233"
	lidAna     = "88887777"
	usuarioAna = "juanito"
)

// claveMarca y marcaResolve sirven para comprobar que el ctx de Resolve es el que le llega al
// migrador: el stub registra lo que cuelga de ese ctx bajo claveMarca.
type (
	claveMarca struct{}
	marcaCtx   string
)

const marcaResolve marcaCtx = "ctx de Resolve"

// errMigrador es el error con el que falla el migrador de los tests de la fusión.
var errMigrador = errors.New("el migrador de prueba falló")

// llamadaMigrador es una llamada registrada a MigrateContactID.
type llamadaMigrador struct {
	marca                  any
	tenantID, desde, hasta string
}

// migradorStub es el StateMigrator con el que estos tests cuentan y miran las llamadas de la
// fusión. Registra cada llamada, con lo que cuelga de su ctx bajo claveMarca, y devuelve err (nil
// si no falla). No tiene estado de flow_state: eso lo hace contacthelpertest.EstadoMemoria.
type migradorStub struct {
	err      error
	llamadas []llamadaMigrador
}

func (m *migradorStub) MigrateContactID(ctx context.Context, tenantID, fromContactID, toContactID string) error {
	m.llamadas = append(m.llamadas, llamadaMigrador{
		marca:    ctx.Value(claveMarca{}),
		tenantID: tenantID,
		desde:    fromContactID,
		hasta:    toContactID,
	})
	return m.err
}

// La suite de contrato del puerto contra MemoryResolver, sin BD y sin reloj: cada caso monta un
// resolver nuevo con un EstadoMemoria que es a la vez su migrador y lo que la suite observa. Con
// -race, porque la suite incluye la ráfaga concurrente (R-32).
func TestMemoryResolver_Contrato(t *testing.T) {
	contacthelpertest.Contrato(t, func(t *testing.T) contacthelpertest.Montaje {
		est := contacthelpertest.NuevoEstado()
		return contacthelpertest.Montaje{
			Resolver: contact.NewMemoryResolver(est),
			TenantA:  uuid.NewString(),
			TenantB:  uuid.NewString(),
			Estado:   est,
		}
	})
}

// Migrador nil: NewMemoryResolver lo admite y la fusión sigue sin estado que migrar. Resolve
// devuelve el contact_id canónico (el del contacto más antiguo), sin error y sin pánico; las dos refs
// quedan atadas a él y el huérfano deja de existir (N-03).
func TestMemoryResolver_Fusion_SinMigrador_Sigue(t *testing.T) {
	r := contact.NewMemoryResolver(nil)
	tenant := uuid.NewString()
	tel, lid := nuevaRef(t, contact.KindPhoneE164, numeroAna), nuevaRef(t, contact.KindWALID, lidAna)
	idTel := resolverOK(t, r, tenant, tel)
	idLid := resolverOK(t, r, tenant, lid)
	if idTel == idLid {
		t.Fatalf("precondición: el teléfono y el LID debían ser dos contactos y comparten %q", idTel)
	}

	canonico, err := r.Resolve(t.Context(), tenant, []contact.Ref{tel, lid}, "")

	if err != nil {
		t.Fatalf("Resolve de la fusión con migrador nil: %v; quiere nil", err)
	}
	if canonico != idTel {
		t.Errorf("canónico de la fusión = %q; quiere el contacto más antiguo, %q", canonico, idTel)
	}
	if got := resolverOK(t, r, tenant, lid); got != canonico {
		t.Errorf("el LID tras la fusión resuelve a %q; quiere el canónico %q", got, canonico)
	}
	if _, err := r.Destino(t.Context(), tenant, idLid); !errors.Is(err, contact.ErrContactNotFound) {
		t.Errorf("Destino del huérfano = %v; quiere un error que envuelva ErrContactNotFound", err)
	}
}

// Migrador que falla: Resolve devuelve contactID "" y un error que envuelve el del migrador (%w) con
// el texto exacto «contact: migrar flow_state en fusión: » seguido del texto de ese error, el mismo
// que usa PostgresResolver. No se afirma qué estado queda después (la memoria no es atómica).
func TestMemoryResolver_Fusion_MigradorFalla_EnvuelveElError(t *testing.T) {
	mig := &migradorStub{err: errMigrador}
	r := contact.NewMemoryResolver(mig)
	tenant := uuid.NewString()
	tel, lid := nuevaRef(t, contact.KindPhoneE164, numeroAna), nuevaRef(t, contact.KindWALID, lidAna)
	resolverOK(t, r, tenant, tel)
	resolverOK(t, r, tenant, lid)

	id, err := r.Resolve(t.Context(), tenant, []contact.Ref{tel, lid}, "")

	if err == nil {
		t.Fatalf("Resolve de la fusión con un migrador que falla = %q, nil; quiere un error", id)
	}
	if !errors.Is(err, errMigrador) {
		t.Errorf("el error %q no envuelve el del migrador (errors.Is)", err)
	}
	const prefijo = "contact: migrar flow_state en fusión:"
	if !strings.HasPrefix(err.Error(), prefijo) {
		t.Errorf("el texto %q no empieza por %q", err.Error(), prefijo)
	}
	if want := prefijo + " " + errMigrador.Error(); err.Error() != want {
		t.Errorf("texto observable = %q; quiere exactamente %q", err.Error(), want)
	}
	if id != "" {
		t.Errorf("con error el contactID debe ser \"\"; dio %q", id)
	}
	if len(mig.llamadas) != 1 {
		t.Errorf("el migrador recibió %d llamadas; quiere 1, la de la fusión", len(mig.llamadas))
	}
}

// Una llamada por huérfano: fundir tres contactos en uno son exactamente dos llamadas a
// MigrateContactID, cada una con el tenant de la llamada, un huérfano distinto del canónico, el
// canónico como destino y el ctx de Resolve; y los dos huérfanos aparecen una vez cada uno. Volver a
// resolver las tres refs, ya fundidas, no vuelve a llamar al migrador.
func TestMemoryResolver_Fusion_UnaLlamadaPorHuerfano(t *testing.T) {
	mig := &migradorStub{}
	r := contact.NewMemoryResolver(mig)
	tenant := uuid.NewString()
	ctx := context.WithValue(t.Context(), claveMarca{}, marcaResolve)
	tel := nuevaRef(t, contact.KindPhoneE164, numeroAna)
	lid := nuevaRef(t, contact.KindWALID, lidAna)
	usuario := nuevaRef(t, contact.KindWAUsername, usuarioAna)
	idTel := resolverOK(t, r, tenant, tel)
	idLid := resolverOK(t, r, tenant, lid)
	idUsuario := resolverOK(t, r, tenant, usuario)
	if len(mig.llamadas) != 0 {
		t.Fatalf("precondición: crear tres contactos por separado no funde nada y hubo %d llamadas", len(mig.llamadas))
	}

	canonico, err := r.Resolve(ctx, tenant, []contact.Ref{usuario, lid, tel}, "")

	if err != nil {
		t.Fatalf("Resolve de la fusión de tres: %v", err)
	}
	if canonico != idTel {
		t.Errorf("canónico = %q; quiere el contacto más antiguo, %q", canonico, idTel)
	}
	if len(mig.llamadas) != 2 {
		t.Fatalf("llamadas al migrador = %d (%+v); quiere 2, una por huérfano", len(mig.llamadas), mig.llamadas)
	}
	porHuerfano := make(map[string]int)
	for i, l := range mig.llamadas {
		if l.tenantID != tenant {
			t.Errorf("llamada %d: tenant %q; quiere el de la llamada, %q", i, l.tenantID, tenant)
		}
		if l.desde == canonico {
			t.Errorf("llamada %d: el huérfano %q es el canónico; quiere uno distinto", i, l.desde)
		}
		if l.hasta != canonico {
			t.Errorf("llamada %d: destino %q; quiere el canónico %q", i, l.hasta, canonico)
		}
		if l.marca != marcaResolve {
			t.Errorf("llamada %d: el ctx del migrador no es el de Resolve (marca %v)", i, l.marca)
		}
		porHuerfano[l.desde]++
	}
	for _, huerfano := range []string{idLid, idUsuario} {
		if porHuerfano[huerfano] != 1 {
			t.Errorf("el huérfano %q se migró %d veces; quiere 1", huerfano, porHuerfano[huerfano])
		}
	}

	resolverOK(t, r, tenant, tel, lid, usuario)
	if len(mig.llamadas) != 2 {
		t.Errorf("tras resolver de nuevo las tres refs ya fundidas hay %d llamadas; quiere las mismas 2", len(mig.llamadas))
	}
}

// Sin fusión no hay llamada al migrador: ni al crear un contacto, ni con una sola ref existente, ni
// con varias del mismo contacto, ni con una lista repetida, ni con las mismas refs en otro tenant.
func TestMemoryResolver_SinFusion_NoLlamaAlMigrador(t *testing.T) {
	mig := &migradorStub{}
	r := contact.NewMemoryResolver(mig)
	tenant, otroTenant := uuid.NewString(), uuid.NewString()
	tel, lid := nuevaRef(t, contact.KindPhoneE164, numeroAna), nuevaRef(t, contact.KindWALID, lidAna)

	for _, p := range []struct {
		paso   string
		tenant string
		refs   []contact.Ref
	}{
		{"ninguna ref existente: crea el contacto", tenant, []contact.Ref{tel}},
		{"una sola ref existente", tenant, []contact.Ref{tel}},
		{"una ref existente y una nueva: se ata", tenant, []contact.Ref{tel, lid}},
		{"varias refs que ya son del mismo contacto", tenant, []contact.Ref{lid, tel}},
		{"una ref repetida en la entrada", tenant, []contact.Ref{tel, tel, tel}},
		{"las mismas refs en otro tenant", otroTenant, []contact.Ref{tel, lid}},
	} {
		resolverOK(t, r, p.tenant, p.refs...)
		if len(mig.llamadas) != 0 {
			t.Fatalf("%s: el migrador recibió %d llamadas (%+v); quiere 0", p.paso, len(mig.llamadas), mig.llamadas)
		}
	}
}

// Una Ref que NO viene de NewRef cuenta como una ref más: Resolve no la valida, no la re-normaliza y
// no la descarta. Una lista con solo la Ref cero, o con solo una ref que NewRef rechazaría, NO es una
// lista vacía: no da ErrNoRefs, crea un contacto y la ref queda atada a él como cualquier otra (la
// misma ref vuelve a dar el mismo contact_id, y una ref válida que llega con ella se ata a ese
// contacto en vez de crear otro).
//
// Va aquí y no en contacthelpertest.Contrato, a propósito: la suite solo usa refs de NewRef, que es la
// precondición del puerto. Postgres tampoco las filtra antes de contar, pero qué hace después con
// una ref así (cifrar un value vacío, insertar un kind que no es de los tres) solo se ve contra un
// Postgres real y su contrato no lo promete.
func TestMemoryResolver_Resolve_RefNotFromNewRefCountsAsARef(t *testing.T) {
	for _, c := range []struct {
		name string
		ref  contact.Ref
	}{
		{"zero Ref", contact.Ref{}},
		{"value that does not normalize", contact.Ref{Kind: contact.KindPhoneE164, Value: "not-a-number"}},
		{"unknown kind", contact.Ref{Kind: "email", Value: "ana@example.com"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := contact.NewMemoryResolver(nil)
			tenant := uuid.NewString()
			if _, err := contact.NewRef(c.ref.Kind, c.ref.Value); err == nil {
				t.Fatalf("precondición: contact.NewRef(%q, %q) debía rechazar esta ref", c.ref.Kind, c.ref.Value)
			}

			id, err := r.Resolve(t.Context(), tenant, []contact.Ref{c.ref}, "")

			if errors.Is(err, contact.ErrNoRefs) {
				t.Fatalf("Resolve([]Ref{%+v}) = %q, ErrNoRefs; quiere un contact_id: la ref cuenta, no se descarta", c.ref, id)
			}
			if err != nil {
				t.Fatalf("Resolve([]Ref{%+v}): %v; quiere nil", c.ref, err)
			}
			if _, perr := uuid.Parse(id); perr != nil {
				t.Fatalf("Resolve([]Ref{%+v}) devolvió %q, que no es un UUID: %v", c.ref, id, perr)
			}
			if again := resolverOK(t, r, tenant, c.ref); again != id {
				t.Errorf("la misma ref otra vez resuelve a %q; quiere %q: la ref quedó atada al contacto", again, id)
			}
			phone := nuevaRef(t, contact.KindPhoneE164, numeroAna)
			if got := resolverOK(t, r, tenant, c.ref, phone); got != id {
				t.Errorf("la ref junto a un teléfono nuevo resuelve a %q; quiere %q: es una ref existente y el teléfono se ata a su contacto", got, id)
			}
			if got := resolverOK(t, r, tenant, phone); got != id {
				t.Errorf("el teléfono, ya atado, resuelve a %q; quiere %q", got, id)
			}
		})
	}
}

// Destino devuelve la Ref cero con cada error, y el error es el que promete el comentario:
// ErrContactNotFound, con el texto exacto «contact: contact_id no encontrado: "<contactID>"» (el
// contactID recibido, entre comillas con %q), si el contacto no existe, es de otro tenant o el id o
// el tenant vienen mal formados (la memoria no parsea nada: Postgres daría un error de parseo); y
// ErrNoDestino sin envolver —su texto exacto, sin ErrContactNotFound— si el contacto solo tiene un
// wa_username.
func TestMemoryResolver_Destino_Errores(t *testing.T) {
	r := contact.NewMemoryResolver(nil)
	tenant, otroTenant := uuid.NewString(), uuid.NewString()
	idAna := resolverOK(t, r, tenant, nuevaRef(t, contact.KindPhoneE164, numeroAna))
	idSoloUsuario := resolverOK(t, r, tenant, nuevaRef(t, contact.KindWAUsername, usuarioAna))

	for _, c := range []struct {
		caso      string
		tenant    string
		contactID string
		quiere    error
	}{
		{"id inexistente", tenant, uuid.NewString(), contact.ErrContactNotFound},
		{"contacto de otro tenant", otroTenant, idAna, contact.ErrContactNotFound},
		{"contactID mal formado", tenant, "no-existe", contact.ErrContactNotFound},
		{"contactID vacío", tenant, "", contact.ErrContactNotFound},
		{"tenantID mal formado", "no-es-un-uuid", idAna, contact.ErrContactNotFound},
		{"solo un wa_username", tenant, idSoloUsuario, contact.ErrNoDestino},
	} {
		t.Run(c.caso, func(t *testing.T) {
			got, err := r.Destino(t.Context(), c.tenant, c.contactID)

			if !errors.Is(err, c.quiere) {
				t.Fatalf("Destino(%q, %q) = %+v, %v; quiere un error que envuelva %v", c.tenant, c.contactID, got, err, c.quiere)
			}
			if got != (contact.Ref{}) {
				t.Errorf("con error la Ref debe ser la cero; dio %+v", got)
			}
			wantText := "contact: sin destino enviable para el contact_id"
			if errors.Is(c.quiere, contact.ErrContactNotFound) {
				wantText = "contact: contact_id no encontrado: " + strconv.Quote(c.contactID)
			}
			if err.Error() != wantText {
				t.Errorf("texto observable = %q; quiere exactamente %q", err.Error(), wantText)
			}
			if errors.Is(c.quiere, contact.ErrNoDestino) && errors.Is(err, contact.ErrContactNotFound) {
				t.Errorf("Destino de un contacto que existe dio también ErrContactNotFound: %v", err)
			}
		})
	}
}

// nuevaRef construye la Ref de (kind, valor) con contact.NewRef, el único constructor que el puerto
// admite.
func nuevaRef(t *testing.T, kind, valor string) contact.Ref {
	t.Helper()
	ref, err := contact.NewRef(kind, valor)
	if err != nil {
		t.Fatalf("contact.NewRef(%q, %q): %v", kind, valor, err)
	}
	return ref
}

// resolverOK llama a Resolve sin push_name y exige que salga bien: sin error y con un contact_id
// que sea un UUID.
func resolverOK(t *testing.T, r *contact.MemoryResolver, tenantID string, refs ...contact.Ref) string {
	t.Helper()
	id, err := r.Resolve(t.Context(), tenantID, refs, "")
	if err != nil {
		t.Fatalf("Resolve(tenant %s, refs %+v): %v", tenantID, refs, err)
	}
	if _, perr := uuid.Parse(id); perr != nil {
		t.Fatalf("Resolve(tenant %s, refs %+v) devolvió %q, que no es un UUID: %v", tenantID, refs, id, perr)
	}
	return id
}
