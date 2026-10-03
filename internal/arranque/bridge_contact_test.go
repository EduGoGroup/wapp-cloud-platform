package arranque

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go/ast"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	viejo "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Los tests de este fichero se derivan del comentario de contactBridge (una aserción por promesa)
// y del corpus de equivalencia viejo ↔ nuevo de R1.4.e (trampa T-10): un Normalize nuevo que
// difiera en un byte rompería en silencio tres índices ciegos (contacts.value_bidx,
// fleet_sessions.self_pn_bidx y el anti-self-loop). Sin BD: el adaptador se prueba sobre un doble
// del puerto nuevo.

// ctxMarkerKey marca el ctx que se pasa al adaptador, para comprobar que llega el MISMO a next.
type ctxMarkerKey struct{}

// fakeNextResolver es el doble de contact.Resolver: captura lo que recibe y devuelve lo que se le
// dice.
type fakeNextResolver struct {
	gotCtx       context.Context
	gotTenantID  string
	gotRefs      []contact.Ref
	gotPushName  string
	gotContactID string
	calls        int

	contactID string
	destino   contact.Ref
	err       error
}

var _ contact.Resolver = (*fakeNextResolver)(nil)

func (f *fakeNextResolver) Resolve(ctx context.Context, tenantID string, refs []contact.Ref, pushName string) (string, error) {
	f.calls++
	f.gotCtx, f.gotTenantID, f.gotRefs, f.gotPushName = ctx, tenantID, refs, pushName
	if f.err != nil {
		return "", f.err
	}
	return f.contactID, nil
}

func (f *fakeNextResolver) Destino(ctx context.Context, tenantID, contactID string) (contact.Ref, error) {
	f.calls++
	f.gotCtx, f.gotTenantID, f.gotContactID = ctx, tenantID, contactID
	if f.err != nil {
		return contact.Ref{}, f.err
	}
	return f.destino, nil
}

const (
	bridgeTenantID  = "9b2f6a2e-0c1d-4f7e-9a51-3c2d1e0f4a5b"
	bridgeContactID = "4c8e2b1a-7d3f-4e6a-8b9c-0a1b2c3d4e5f"
)

func markedCtx(t *testing.T) context.Context {
	t.Helper()
	return context.WithValue(t.Context(), ctxMarkerKey{}, "marker")
}

// ctxMarker devuelve la marca de markedCtx que lleva ctx, o nil si no la lleva (o ctx es nil).
func ctxMarker(ctx context.Context) any {
	if ctx == nil {
		return nil
	}
	return ctx.Value(ctxMarkerKey{})
}

// Promesa: Resolve copia cada viejo.Ref en un contact.Ref campo a campo, en el mismo orden, sin
// quitar ni añadir ninguna y SIN re-normalizar; delega con el mismo ctx, tenantID y pushName y
// devuelve el contactID de next tal cual.
func TestContactBridge_Resolve_CopiesRefsVerbatim(t *testing.T) {
	cases := []struct {
		name string
		refs []viejo.Ref
	}{
		{"normalized phone and lid keep order", []viejo.Ref{
			{Kind: viejo.KindPhoneE164, Value: "573001112233"},
			{Kind: viejo.KindWALID, Value: "88887777"},
		}},
		{"lid before phone keeps order", []viejo.Ref{
			{Kind: viejo.KindWALID, Value: "88887777"},
			{Kind: viejo.KindPhoneE164, Value: "573001112233"},
		}},
		{"unnormalized phone is not renormalized", []viejo.Ref{
			{Kind: viejo.KindPhoneE164, Value: "+57 (300) 111-2233"},
		}},
		{"unnormalized lid is not renormalized", []viejo.Ref{
			{Kind: viejo.KindWALID, Value: "88887777_1:5@lid"},
		}},
		{"username with uppercase and blanks is not renormalized", []viejo.Ref{
			{Kind: viejo.KindWAUsername, Value: "  JuanPerez  "},
		}},
		{"empty ref is not dropped", []viejo.Ref{
			{},
		}},
		{"unknown kind is not validated", []viejo.Ref{
			{Kind: "email", Value: "x"},
		}},
		{"duplicates are not deduplicated", []viejo.Ref{
			{Kind: viejo.KindPhoneE164, Value: "573001112233"},
			{Kind: viejo.KindPhoneE164, Value: "573001112233"},
		}},
		{"empty list stays empty", []viejo.Ref{}},
		{"nil list stays empty", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			next := &fakeNextResolver{contactID: bridgeContactID}
			b := &contactBridge{next: next}

			id, err := b.Resolve(markedCtx(t), bridgeTenantID, c.refs, "Ana")
			if err != nil {
				t.Fatalf("Resolve = %q, %v; quiere el contactID de next sin error", id, err)
			}
			if id != bridgeContactID {
				t.Errorf("Resolve = %q; quiere el contactID de next tal cual, %q", id, bridgeContactID)
			}
			if next.calls != 1 {
				t.Fatalf("next.Resolve llamado %d veces; quiere 1", next.calls)
			}
			if ctxMarker(next.gotCtx) != "marker" {
				t.Errorf("next recibió otro ctx: quiere el mismo que se pasó al adaptador")
			}
			if next.gotTenantID != bridgeTenantID || next.gotPushName != "Ana" {
				t.Errorf("next recibió tenant %q y pushName %q; quiere %q y %q",
					next.gotTenantID, next.gotPushName, bridgeTenantID, "Ana")
			}
			if len(next.gotRefs) != len(c.refs) {
				t.Fatalf("next recibió %d refs (%+v); quiere %d, ni una más ni una menos",
					len(next.gotRefs), next.gotRefs, len(c.refs))
			}
			for i, in := range c.refs {
				got := next.gotRefs[i]
				if got.Kind != in.Kind || got.Value != in.Value {
					t.Errorf("ref %d: next recibió %+v; quiere %+v copiada campo a campo, sin re-normalizar", i, got, in)
				}
			}
		})
	}
}

// Promesa: Destino delega con el mismo ctx, tenantID y contactID y copia la contact.Ref que recibe
// en una viejo.Ref campo a campo.
func TestContactBridge_Destino_CopiesRefBack(t *testing.T) {
	cases := []struct {
		name string
		ref  contact.Ref
	}{
		{"phone", contact.Ref{Kind: contact.KindPhoneE164, Value: "573001112233"}},
		{"lid", contact.Ref{Kind: contact.KindWALID, Value: "88887777"}},
		{"value copied verbatim", contact.Ref{Kind: contact.KindPhoneE164, Value: "+57 300"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			next := &fakeNextResolver{destino: c.ref}
			b := &contactBridge{next: next}

			got, err := b.Destino(markedCtx(t), bridgeTenantID, bridgeContactID)
			if err != nil {
				t.Fatalf("Destino = %+v, %v; quiere la ref de next sin error", got, err)
			}
			want := viejo.Ref{Kind: c.ref.Kind, Value: c.ref.Value}
			if got != want {
				t.Errorf("Destino = %+v; quiere %+v copiada campo a campo", got, want)
			}
			if next.calls != 1 {
				t.Fatalf("next.Destino llamado %d veces; quiere 1", next.calls)
			}
			if ctxMarker(next.gotCtx) != "marker" {
				t.Errorf("next recibió otro ctx: quiere el mismo que se pasó al adaptador")
			}
			if next.gotTenantID != bridgeTenantID || next.gotContactID != bridgeContactID {
				t.Errorf("next recibió tenant %q y contactID %q; quiere %q y %q",
					next.gotTenantID, next.gotContactID, bridgeTenantID, bridgeContactID)
			}
		})
	}
}

// bridgeCall ejecuta el método del adaptador que toca y dice si el valor devuelto junto al error es
// el vacío ("" para Resolve, viejo.Ref{} para Destino).
type bridgeCall func(t *testing.T, b *contactBridge) (zero bool, err error)

func callResolve(t *testing.T, b *contactBridge) (bool, error) {
	id, err := b.Resolve(t.Context(), bridgeTenantID,
		[]viejo.Ref{{Kind: viejo.KindPhoneE164, Value: "573001112233"}}, "")
	return id == "", err
}

func callDestino(t *testing.T, b *contactBridge) (bool, error) {
	ref, err := b.Destino(t.Context(), bridgeTenantID, bridgeContactID)
	return ref == (viejo.Ref{}), err
}

// oldSentinels son los tres centinelas viejos: un error ajeno no casa con ninguno, y cada error
// traducido casa solo con el suyo.
var oldSentinels = []error{viejo.ErrNoRefs, viejo.ErrNoDestino, viejo.ErrContactNotFound}

// Promesa: un error de next que casa con contact.ErrNoRefs, contact.ErrNoDestino o
// contact.ErrContactNotFound sale con EXACTAMENTE el mismo texto (el que daría el viejo, byte a
// byte, incluido el envoltorio «: "<contactID>"»), con un Unwrap() []error que contiene el error
// original y el centinela viejo, y errors.Is casa con el nuevo y con el viejo. Junto al error, el
// valor vacío.
func TestContactBridge_SentinelErrors_KeepTextAndMatchBoth(t *testing.T) {
	cases := []struct {
		name     string
		call     bridgeCall
		nextErr  error
		newSent  error
		oldSent  error
		wantText string
	}{
		{"resolve no refs", callResolve,
			contact.ErrNoRefs, contact.ErrNoRefs, viejo.ErrNoRefs,
			viejo.ErrNoRefs.Error()},
		{"destino no destination", callDestino,
			contact.ErrNoDestino, contact.ErrNoDestino, viejo.ErrNoDestino,
			viejo.ErrNoDestino.Error()},
		{"destino no destination wrapped with kind", callDestino,
			fmt.Errorf("%w: kind %q no direccionable", contact.ErrNoDestino, contact.KindWAUsername),
			contact.ErrNoDestino, viejo.ErrNoDestino,
			fmt.Errorf("%w: kind %q no direccionable", viejo.ErrNoDestino, viejo.KindWAUsername).Error()},
		{"destino contact not found bare", callDestino,
			contact.ErrContactNotFound, contact.ErrContactNotFound, viejo.ErrContactNotFound,
			viejo.ErrContactNotFound.Error()},
		{"destino contact not found wrapped with quoted id", callDestino,
			fmt.Errorf("%w: %q", contact.ErrContactNotFound, bridgeContactID),
			contact.ErrContactNotFound, viejo.ErrContactNotFound,
			fmt.Errorf("%w: %q", viejo.ErrContactNotFound, bridgeContactID).Error()},
		{"resolve contact not found is also translated", callResolve,
			contact.ErrContactNotFound, contact.ErrContactNotFound, viejo.ErrContactNotFound,
			viejo.ErrContactNotFound.Error()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := &contactBridge{next: &fakeNextResolver{err: c.nextErr}}

			zero, err := c.call(t, b)
			if err == nil {
				t.Fatalf("el adaptador no devolvió error; quiere el de next traducido")
			}
			if !zero {
				t.Errorf("junto al error el valor no es el vacío")
			}
			if err.Error() != c.wantText {
				t.Errorf("Error() = %q; quiere el texto exacto %q", err.Error(), c.wantText)
			}
			if err.Error() != c.nextErr.Error() {
				t.Errorf("Error() = %q; quiere el mismo texto que el error de next, %q", err.Error(), c.nextErr.Error())
			}
			if !errors.Is(err, c.newSent) {
				t.Errorf("errors.Is(err, centinela nuevo %q) = false; quiere true", c.newSent)
			}
			if !errors.Is(err, c.oldSent) {
				t.Errorf("errors.Is(err, centinela viejo %q) = false; quiere true", c.oldSent)
			}
			if !errors.Is(err, c.nextErr) {
				t.Errorf("errors.Is(err, error de next) = false; quiere que conserve el original")
			}
			for _, other := range oldSentinels {
				if !errors.Is(other, c.oldSent) && errors.Is(err, other) {
					t.Errorf("errors.Is(err, %q) = true; solo debe casar con su centinela viejo", other)
				}
			}
			assertUnwrapHolds(t, err, c.nextErr, c.oldSent)
		})
	}
}

// assertUnwrapHolds comprueba que err tiene Unwrap() []error y que la lista contiene, por
// identidad, el error original de next y el centinela viejo.
func assertUnwrapHolds(t *testing.T, err, original, oldSentinel error) {
	t.Helper()
	var multi interface{ Unwrap() []error }
	if !errors.As(err, &multi) {
		t.Fatalf("el error no tiene Unwrap() []error")
	}
	var hasOriginal, hasOld bool
	for _, e := range multi.Unwrap() {
		hasOriginal = hasOriginal || e == original //nolint:errorlint // identidad: el original, no uno que lo envuelva
		hasOld = hasOld || e == oldSentinel        //nolint:errorlint // identidad: el centinela viejo, sin envolver
	}
	if !hasOriginal || !hasOld {
		t.Errorf("Unwrap() = %v; quiere el error original (%v) y el centinela viejo (%v)",
			multi.Unwrap(), hasOriginal, hasOld)
	}
}

// Promesa: cualquier otro error de next pasa tal cual, sin envolver: el mismo valor, el mismo texto
// y sin casar con ningún centinela viejo. Junto al error, el valor vacío.
func TestContactBridge_ForeignErrors_PassThrough(t *testing.T) {
	storeErr := errors.New("contact: leer refs del contacto: conexión perdida")
	invalidRef := fmt.Errorf("%w: kind desconocido %q", contact.ErrInvalidRef, "email")
	cases := []struct {
		name    string
		call    bridgeCall
		nextErr error
	}{
		{"resolve store error", callResolve, storeErr},
		{"destino store error", callDestino, storeErr},
		{"resolve migration error wrapping", callResolve,
			fmt.Errorf("contact: migrar flow_state en fusión: %w", storeErr)},
		{"resolve context canceled", callResolve, context.Canceled},
		{"destino context deadline", callDestino, context.DeadlineExceeded},
		{"resolve invalid ref is not a resolver sentinel", callResolve, invalidRef},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := &contactBridge{next: &fakeNextResolver{err: c.nextErr}}

			zero, err := c.call(t, b)
			if err != c.nextErr { //nolint:errorlint // la promesa es identidad: pasa tal cual, sin envolver
				t.Errorf("err = %v (%T); quiere el error de next tal cual, %v (%T)", err, err, c.nextErr, c.nextErr)
			}
			if err != nil && err.Error() != c.nextErr.Error() {
				t.Errorf("Error() = %q; quiere %q", err.Error(), c.nextErr.Error())
			}
			if !zero {
				t.Errorf("junto al error el valor no es el vacío")
			}
			for _, old := range oldSentinels {
				if errors.Is(err, old) {
					t.Errorf("errors.Is(err, %q) = true; un error ajeno no casa con un centinela viejo", old)
				}
			}
		})
	}
}

// ── Corpus de equivalencia viejo ↔ nuevo (R1.4.e, trampa T-10) ────────────────────────────────

// equivalenceCase es una entrada (kind, value) del corpus; rule dice qué regla de diseno.md §4
// ejercita.
type equivalenceCase struct {
	rule, name, kind, value string
}

// equivalenceCorpus cubre R-01 (kinds), R-02…R-07 y N-05, válidos e inválidos, y los casos
// adversarios ADV. Para cada entrada, el viejo y el nuevo deben dar la MISMA salida y el MISMO
// texto de error.
var equivalenceCorpus = []equivalenceCase{
	// R-01 · R-08: kinds fuera de los tres soportados.
	{"R-01", "empty kind", "", "573001112233"},
	{"R-01", "kind phone", "phone", "573001112233"},
	{"R-01", "kind uppercase", "PHONE_E164", "573001112233"},
	{"R-01", "kind email", "email", "ana@example.com"},
	// R-02: phone, solo dígitos.
	{"R-02", "phone with plus", contact.KindPhoneE164, "+14155552671"},
	{"R-02", "phone with spaces and dashes", contact.KindPhoneE164, "+1 415-555-2671"},
	{"R-02", "phone with parentheses", contact.KindPhoneE164, "+1 (415) 555 2671"},
	{"R-02", "phone already normalized", contact.KindPhoneE164, "573001112233"},
	{"R-02", "phone with dots", contact.KindPhoneE164, "44.20.7946.0018"},
	{"R-02", "phone with letters mixed in", contact.KindPhoneE164, "57-300-abc-1112233"},
	{"R-02", "phone with fullwidth digits only", contact.KindPhoneE164, "５７３００"},
	// R-03: dos formatos del mismo número.
	{"R-03", "same number formatted", contact.KindPhoneE164, "+1 (415) 555-2671"},
	{"R-03", "same number bare", contact.KindPhoneE164, "14155552671"},
	// R-04: phone inválido y el borde de 15 dígitos.
	{"R-04", "phone empty", contact.KindPhoneE164, ""},
	{"R-04", "phone without digits", contact.KindPhoneE164, "abc"},
	{"R-04", "phone only separators", contact.KindPhoneE164, "+ - ()"},
	{"R-04", "phone with 15 digits is valid", contact.KindPhoneE164, "123456789012345"},
	{"R-04", "phone with 16 digits", contact.KindPhoneE164, "1234567890123456"},
	{"R-04", "phone with 20 digits", contact.KindPhoneE164, "12345678901234567890"},
	// R-05: LID canónico.
	{"R-05", "lid with server", contact.KindWALID, "123456789012345@lid"},
	{"R-05", "lid bare user", contact.KindWALID, "123456789012345"},
	{"R-05", "lid with device", contact.KindWALID, "123456789012345:2@lid"},
	{"R-05", "lid with agent and device", contact.KindWALID, "123456789012345_1:2@lid"},
	{"R-05", "lid with agent only", contact.KindWALID, "88887777_1@lid"},
	{"R-05", "lid with edge blanks", contact.KindWALID, "  981054321@lid  "},
	{"R-05", "lid with inner blank before server", contact.KindWALID, "981054321 @lid"},
	// R-06: LID inválido.
	{"R-06", "lid empty", contact.KindWALID, ""},
	{"R-06", "lid only server", contact.KindWALID, "@lid"},
	{"R-06", "lid letters", contact.KindWALID, "abc@lid"},
	{"R-06", "lid mixed", contact.KindWALID, "12ab34@lid"},
	{"R-06", "lid only agent", contact.KindWALID, "_1@lid"},
	{"R-06", "lid only device", contact.KindWALID, ":2@lid"},
	{"R-06", "lid with plus", contact.KindWALID, "+88887777@lid"},
	// R-07: username.
	{"R-07", "username uppercase with blanks", contact.KindWAUsername, "  JuanPerez  "},
	{"R-07", "username inner blank kept", contact.KindWAUsername, "Juan Perez"},
	{"R-07", "username non ascii uppercase", contact.KindWAUsername, "ÁNGEL"},
	{"R-07", "username only blanks", contact.KindWAUsername, "   "},
	{"R-07", "username empty", contact.KindWAUsername, ""},
	// N-05: JID de dispositivo tratado como teléfono conserva el dígito del dispositivo.
	{"N-05", "phone device jid keeps device digit", contact.KindPhoneE164, "573001112233:5@s.whatsapp.net"},
	{"N-05", "phone plain jid", contact.KindPhoneE164, "573001112233@s.whatsapp.net"},
	{"N-05", "lid device jid drops device", contact.KindWALID, "88887777:5@lid"},
	// ADV · casos adversarios (hallazgo 40 de F1): el corpus de arriba no distinguía un
	// normalizeLID que cortara por el ÚLTIMO «@» en vez de por el primero. Tres familias, en los
	// tres kinds: «@» repetidos, dígitos que no son ASCII y espacios Unicode. Van con escapes
	// \u para que se vean: U+0660–U+0669 son los dígitos arábigo-índicos, U+FF10–U+FF19 los de
	// ancho completo, U+00A0 es NBSP, U+2003 EM SPACE y U+3000 IDEOGRAPHIC SPACE.
	{"ADV", "lid with doubled at", contact.KindWALID, "123@@lid"},
	{"ADV", "lid letters with doubled at", contact.KindWALID, "a@@b"},
	{"ADV", "lid with two servers", contact.KindWALID, "123@x@lid"},
	{"ADV", "lid with repeated lid server", contact.KindWALID, "123@lid@lid"},
	{"ADV", "lid with device after a second at", contact.KindWALID, "123@lid:2@lid"},
	{"ADV", "lid with trailing at", contact.KindWALID, "123@"},
	{"ADV", "lid only ats", contact.KindWALID, "@@"},
	{"ADV", "lid arabic-indic digits", contact.KindWALID, "١٢٣٤٥@lid"},
	{"ADV", "lid fullwidth digits", contact.KindWALID, "１２３４５@lid"},
	{"ADV", "lid ascii mixed with fullwidth digits", contact.KindWALID, "123４５"},
	{"ADV", "lid with nbsp edges", contact.KindWALID, " 981054321@lid "},
	{"ADV", "lid with em space edges", contact.KindWALID, " 981054321 "},
	{"ADV", "lid with ideographic space edges", contact.KindWALID, "　981054321@lid　"},
	{"ADV", "lid with nbsp before server", contact.KindWALID, "981054321 @lid"},
	{"ADV", "lid with em space in the middle", contact.KindWALID, "9810 54321@lid"},
	{"ADV", "lid with ideographic space before device", contact.KindWALID, "981054321　:2@lid"},
	{"ADV", "lid only unicode spaces", contact.KindWALID, "  　"},
	{"ADV", "phone with doubled at", contact.KindPhoneE164, "573001112233@@s.whatsapp.net"},
	{"ADV", "phone with two servers", contact.KindPhoneE164, "573001112233@1@s.whatsapp.net"},
	{"ADV", "phone arabic-indic digits only", contact.KindPhoneE164, "٥٧٣٠٠"},
	{"ADV", "phone ascii mixed with arabic-indic digits", contact.KindPhoneE164, "57٣٠٠1112233"},
	{"ADV", "phone ascii mixed with fullwidth digits", contact.KindPhoneE164, "57３００1112233"},
	{"ADV", "phone with nbsp edges", contact.KindPhoneE164, " 573001112233 "},
	{"ADV", "phone with em space in the middle", contact.KindPhoneE164, "57 300 1112233"},
	{"ADV", "phone with ideographic space in the middle", contact.KindPhoneE164, "57　300　1112233"},
	{"ADV", "phone only unicode spaces", contact.KindPhoneE164, "  　"},
	{"ADV", "username with doubled at", contact.KindWAUsername, "Juan@@Perez"},
	{"ADV", "username with at and server", contact.KindWAUsername, "JuanPerez@lid"},
	{"ADV", "username with nbsp edges", contact.KindWAUsername, " JuanPerez "},
	{"ADV", "username with em space edges", contact.KindWAUsername, " JuanPerez "},
	{"ADV", "username with ideographic space edges", contact.KindWAUsername, "　JuanPerez　"},
	{"ADV", "username with nbsp in the middle", contact.KindWAUsername, "Juan Perez"},
	{"ADV", "username only unicode spaces", contact.KindWAUsername, "  　"},
	{"ADV", "username fullwidth uppercase", contact.KindWAUsername, "ＪＵＡＮ"},
	{"ADV", "username with arabic-indic digits", contact.KindWAUsername, "Juan١٢٣"},
	{"ADV", "kind with trailing nbsp", contact.KindWALID + " ", "123@lid"},
	{"ADV", "kind with fullwidth underscore", "wa＿lid", "123@lid"},
}

// sameErrText dice si dos errores son ambos nil o ambos no nil con el mismo texto.
func sameErrText(a, b error) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Error() == b.Error()
}

// R1.4.e: Normalize nuevo da la misma salida y el mismo texto de error que el viejo, y sus errores
// casan con el ErrInvalidRef de cada paquete.
func TestContactEquivalence_Normalize(t *testing.T) {
	for _, c := range equivalenceCorpus {
		t.Run(c.rule+" "+c.name, func(t *testing.T) {
			oldOut, oldErr := viejo.Normalize(c.kind, c.value)
			newOut, newErr := contact.Normalize(c.kind, c.value)
			if newOut != oldOut || !sameErrText(newErr, oldErr) {
				t.Errorf("Normalize(%q, %q): nuevo = %q, %v; viejo = %q, %v", c.kind, c.value, newOut, newErr, oldOut, oldErr)
			}
			if (oldErr != nil) != errors.Is(oldErr, viejo.ErrInvalidRef) ||
				(newErr != nil) != errors.Is(newErr, contact.ErrInvalidRef) {
				t.Errorf("Normalize(%q, %q): los errores deben envolver el ErrInvalidRef de su paquete: nuevo %v, viejo %v",
					c.kind, c.value, newErr, oldErr)
			}
		})
	}
}

// R-03 sobre los dos paquetes a la vez: dos formatos del mismo número dan el mismo value en el
// viejo y en el nuevo (la base del índice ciego).
func TestContactEquivalence_SameNumberSameValue(t *testing.T) {
	formats := []string{"+1 (415) 555-2671", "14155552671", "+1 415-555-2671", "1.415.555.2671"}
	for _, f := range formats {
		oldOut, oldErr := viejo.Normalize(viejo.KindPhoneE164, f)
		newOut, newErr := contact.Normalize(contact.KindPhoneE164, f)
		if oldErr != nil || newErr != nil || oldOut != "14155552671" || newOut != oldOut {
			t.Errorf("Normalize(phone, %q): nuevo = %q, %v; viejo = %q, %v; quieren los dos %q",
				f, newOut, newErr, oldOut, oldErr, "14155552671")
		}
	}
}

// R1.4.e: NewRef nuevo da la misma Ref (kind y value) y el mismo texto de error que el viejo.
func TestContactEquivalence_NewRef(t *testing.T) {
	for _, c := range equivalenceCorpus {
		t.Run(c.rule+" "+c.name, func(t *testing.T) {
			oldRef, oldErr := viejo.NewRef(c.kind, c.value)
			newRef, newErr := contact.NewRef(c.kind, c.value)
			if newRef.Kind != oldRef.Kind || newRef.Value != oldRef.Value || !sameErrText(newErr, oldErr) {
				t.Errorf("NewRef(%q, %q): nuevo = %+v, %v; viejo = %+v, %v", c.kind, c.value, newRef, newErr, oldRef, oldErr)
			}
		})
	}
}

// R1.4.e con R-22 y N-05: RefsFrom nuevo da las mismas refs, en el mismo orden, que el viejo.
func TestContactEquivalence_RefsFrom(t *testing.T) {
	cases := []struct {
		rule, name, fromPn, fromLid, from string
	}{
		{"R-22", "pn and lid", "573001112233", "88887777", "88887777@lid"},
		{"R-22", "pn and lid with raw formats", "+57 300 111-2233", "88887777_1:2@lid", ""},
		{"R-22", "only pn", "573001112233", "", ""},
		{"R-22", "only lid", "", "88887777@lid", ""},
		{"R-22", "raw lid jid infers wa_lid", "", "", "88887777@lid"},
		{"R-22", "raw phone jid infers phone", "", "", "573001112233@s.whatsapp.net"},
		{"R-22", "raw lid jid with trailing text", "", "", "88887777@lid.extra"},
		{"R-22", "raw jid ignored when pn is usable", "573001112233", "", "99990000@lid"},
		{"R-22", "raw jid ignored when lid is usable", "", "88887777", "573009998877@s.whatsapp.net"},
		{"R-22", "invalid pn dropped, lid kept", "abc", "88887777", ""},
		{"R-22", "invalid pn and lid fall back to raw", "abc", "xyz@lid", "573001112233@s.whatsapp.net"},
		{"R-22", "pn over 15 digits dropped", "1234567890123456", "", ""},
		{"R-22", "raw jid over 15 digits dropped", "", "", "1234567890123456@s.whatsapp.net"},
		{"R-22", "invalid raw lid dropped", "", "", "abc@lid"},
		{"R-22", "nothing", "", "", ""},
		{"N-05", "raw phone device jid keeps device digit", "", "", "573001112233:5@s.whatsapp.net"},
		{"N-05", "raw lid device jid drops device", "", "", "88887777:5@lid"},
		{"N-05", "raw lid agent and device jid", "", "", "88887777_1:5@lid"},
		// ADV · los mismos casos adversarios del corpus (hallazgo 40 de F1), por las tres entradas.
		{"ADV", "raw lid jid with doubled at", "", "", "88887777@@lid"},
		{"ADV", "raw lid jid with two servers", "", "", "88887777@x@lid"},
		{"ADV", "raw lid jid with repeated lid server", "", "", "88887777@lid@lid"},
		{"ADV", "lid with two servers", "", "88887777@x@lid", ""},
		{"ADV", "lid with repeated lid server and pn", "573001112233", "88887777@lid@lid", ""},
		{"ADV", "raw phone jid with doubled at", "", "", "573001112233@@s.whatsapp.net"},
		{"ADV", "raw phone jid followed by lid server", "", "", "573001112233@s.whatsapp.net@lid"},
		{"ADV", "pn arabic-indic digits falls back to raw", "٥٧٣٠٠", "", "573001112233@s.whatsapp.net"},
		{"ADV", "pn ascii mixed with fullwidth digits", "57３００1112233", "", ""},
		{"ADV", "lid fullwidth digits dropped, pn kept", "573001112233", "８８８８@lid", ""},
		{"ADV", "raw lid jid arabic-indic digits", "", "", "٨٨٨٨@lid"},
		{"ADV", "lid with nbsp edges", "", " 88887777@lid ", ""},
		{"ADV", "raw lid jid with em space before server", "", "", "88887777 @lid"},
		{"ADV", "raw lid jid with ideographic space edges", "", "", "　88887777@lid　"},
		{"ADV", "pn with ideographic spaces", "57　300　1112233", "", ""},
		{"ADV", "pn and lid only unicode spaces fall back to raw", " ", " ", "88887777@lid"},
	}
	for _, c := range cases {
		t.Run(c.rule+" "+c.name, func(t *testing.T) {
			oldRefs := viejo.RefsFrom(c.fromPn, c.fromLid, c.from)
			newRefs := contact.RefsFrom(c.fromPn, c.fromLid, c.from)
			if (oldRefs == nil) != (newRefs == nil) || len(newRefs) != len(oldRefs) {
				t.Fatalf("RefsFrom(%q, %q, %q): nuevo = %#v; viejo = %#v", c.fromPn, c.fromLid, c.from, newRefs, oldRefs)
			}
			for i := range oldRefs {
				if newRefs[i].Kind != oldRefs[i].Kind || newRefs[i].Value != oldRefs[i].Value {
					t.Errorf("RefsFrom(%q, %q, %q)[%d]: nuevo = %+v; viejo = %+v",
						c.fromPn, c.fromLid, c.from, i, newRefs[i], oldRefs[i])
				}
			}
		})
	}
}

// R1.4.e: Ref.Sendable nuevo da el mismo destino y el mismo texto de error que el viejo, y los
// errores casan con el ErrNoDestino de cada paquete.
func TestContactEquivalence_Sendable(t *testing.T) {
	cases := []struct {
		name        string
		kind, value string
	}{
		{"phone", contact.KindPhoneE164, "573001112233"},
		{"phone value not renormalized", contact.KindPhoneE164, "+57 300"},
		{"phone empty value", contact.KindPhoneE164, ""},
		{"lid", contact.KindWALID, "88887777"},
		{"lid value not renormalized", contact.KindWALID, "88887777:5"},
		{"lid empty value", contact.KindWALID, ""},
		{"username not addressable", contact.KindWAUsername, "juanperez"},
		{"empty kind not addressable", "", "573001112233"},
		{"unknown kind not addressable", "email", "x"},
		// Adversarios (hallazgo 40 de F1): Sendable no re-normaliza, tampoco esto.
		{"lid value with doubled at not renormalized", contact.KindWALID, "123@@lid"},
		{"lid value with fullwidth digits not renormalized", contact.KindWALID, "１２３"},
		{"phone value with nbsp edges not renormalized", contact.KindPhoneE164, " 573001112233 "},
		{"phone value with arabic-indic digits not renormalized", contact.KindPhoneE164, "٥٧٣"},
		{"kind with trailing nbsp not addressable", contact.KindPhoneE164 + " ", "573001112233"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			oldOut, oldErr := viejo.Ref{Kind: c.kind, Value: c.value}.Sendable()
			newOut, newErr := contact.Ref{Kind: c.kind, Value: c.value}.Sendable()
			if newOut != oldOut || !sameErrText(newErr, oldErr) {
				t.Errorf("Sendable(%q, %q): nuevo = %q, %v; viejo = %q, %v", c.kind, c.value, newOut, newErr, oldOut, oldErr)
			}
			if (oldErr != nil) != errors.Is(oldErr, viejo.ErrNoDestino) ||
				(newErr != nil) != errors.Is(newErr, contact.ErrNoDestino) {
				t.Errorf("Sendable(%q, %q): los errores deben envolver el ErrNoDestino de su paquete: nuevo %v, viejo %v",
					c.kind, c.value, newErr, oldErr)
			}
		})
	}
}

// stubKeyProvider es un crypto.KeyProvider que nadie llama: el test de la costura solo mira QUÉ
// instancia acaba dentro del resolver, no la usa.
type stubKeyProvider struct{ crypto.KeyProvider }

// postgresResolverField lee por reflexión el campo no exportado name del *contact.PostgresResolver
// que hay detrás de b (falla el test si no es ese tipo). Es la única forma de afirmar, sin BD, que
// el resolver recibió EXACTAMENTE las instancias de la fase: el puerto no las expone.
func postgresResolverField(t *testing.T, b *contactBridge, name string) reflect.Value {
	t.Helper()
	pr, ok := b.next.(*contact.PostgresResolver)
	if !ok {
		t.Fatalf("contactBridge.next = %T; quiero *contact.PostgresResolver (el resolver NUEVO)", b.next)
	}
	f := reflect.ValueOf(pr).Elem().FieldByName(name)
	if !f.IsValid() {
		t.Fatalf("*contact.PostgresResolver no tiene el campo %q: el test de cableado se quedó atrás", name)
	}
	return f
}

// sameInstance dice si el valor de un campo (puntero, o interfaz que guarda un puntero) es la misma
// instancia que want.
func sameInstance(field reflect.Value, want any) bool {
	if field.Kind() == reflect.Interface {
		if field.IsNil() {
			return want == nil
		}
		field = field.Elem()
	}
	w := reflect.ValueOf(want)
	if !w.IsValid() || field.Kind() != reflect.Pointer || w.Kind() != reflect.Pointer {
		return false
	}
	return field.Type() == w.Type() && field.Pointer() == w.Pointer()
}

// TestNewContactResolver_WrapsNewPostgresResolverWithSameInstances fija la costura de T1.16: un
// contactBridge sobre el *contact.PostgresResolver NUEVO, construido con el db, el cipher y el kp
// recibidos, no con copias (R1.4.b, R1.5.b).
func TestNewContactResolver_WrapsNewPostgresResolverWithSameInstances(t *testing.T) {
	db := new(sql.DB)
	kp := &stubKeyProvider{}
	cipher := crypto.NewFieldCipher(kp)

	b := newContactResolver(db, cipher, kp)

	if b == nil {
		t.Fatal("newContactResolver devolvió nil")
	}
	for _, f := range []struct {
		name string
		want any
	}{{"db", db}, {"cipher", cipher}, {"kp", kp}} {
		if !sameInstance(postgresResolverField(t, b, f.name), f.want) {
			t.Errorf("PostgresResolver.%s no es la instancia recibida por newContactResolver", f.name)
		}
	}
}

// TestBuildFlowRuntimeDeps_WiresContactBridgeWithPhaseKeys es la aserción de cableado del arranque
// nuevo (T1.16): tras la fase 3 real (el contenedor de la huella), el resolver que reciben
// flowruntime.New (fase 7) e intakes.NewNotifier (fase 6) es un contactBridge sobre el resolver
// NUEVO, y ese resolver lleva EL MISMO cipher y EL MISMO KeyProvider que la fase guarda en
// flowDeps y reparte al resto de almacenes. 🔴 Otro KeyProvider duplicaría contactos en silencio.
func TestBuildFlowRuntimeDeps_WiresContactBridgeWithPhaseKeys(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")

	b, ok := c.flowDeps.contacts.(*contactBridge)
	if !ok {
		t.Fatalf("flowDeps.contacts = %T; quiero *contactBridge (el resolver nuevo de nucleo/contact)", c.flowDeps.contacts)
	}
	if !sameInstance(postgresResolverField(t, b, "db"), c.db) {
		t.Error("el resolver de contactos no usa el pool de la fase 1")
	}
	if !sameInstance(postgresResolverField(t, b, "cipher"), c.flowDeps.cipher) {
		t.Error("el resolver de contactos no usa el FieldCipher de la fase 3 (flowDeps.cipher)")
	}
	if !sameInstance(postgresResolverField(t, b, "kp"), c.flowDeps.kp) {
		t.Error("el resolver de contactos no usa el KeyProvider de la fase 3 (flowDeps.kp): otro value_bidx")
	}
}

// ── Cableado completo: ninguna fase usa el resolver viejo (05 §4.2, hallazgo 39 de F1) ───────

// oldContactImportPath es la ruta de import del paquete VIEJO de contactos.
const oldContactImportPath = "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"

// oldContactImporters es la lista blanca, por nombre de fichero, de los ficheros de producción de
// internal/arranque que pueden importar el paquete viejo de contactos, con los ÚNICOS símbolos
// que cada uno puede usar de él. Ninguno es un constructor: así el arranque nuevo no puede
// levantar un resolver viejo, ni en una fase ni en el propio adaptador.
//
//   - bridge_contact.go: es el adaptador (05 §4.2). Implementa viejo.Resolver, copia viejo.Ref
//     campo a campo y empareja los tres centinelas viejos.
//   - flows.go: solo el TIPO del campo flowRuntimeDeps.contacts, viejo.Resolver, que es lo que
//     piden flowruntime.New (fase 7) e intakes.NewNotifier (fase 6).
//
// Los dos salen de aquí en F8, cuando muere el adaptador (y entonces nucleo entra en Conmutados).
var oldContactImporters = map[string][]string{
	"bridge_contact.go": {"ErrContactNotFound", "ErrNoDestino", "ErrNoRefs", "Ref", "Resolver"},
	"flows.go":          {"Resolver"},
}

// oldContactUse es lo que un fichero de producción del arranque hace con el paquete viejo de
// contactos: con qué nombre lo importa y qué símbolos usa de él.
type oldContactUse struct {
	localName string   // nombre local del import: el alias, o "contact" si no lleva alias
	selectors []string // símbolos usados sobre localName, sin repetir y ordenados
}

// oldContactUses recorre los ficheros de producción de internal/arranque y devuelve, por nombre de
// fichero, los que importan el paquete viejo de contactos (o un subpaquete suyo) y lo que usan.
func oldContactUses(t *testing.T) map[string]oldContactUse {
	t.Helper()
	fset, files := astDelArranque(t)
	uses := make(map[string]oldContactUse)
	for _, f := range files {
		name := fset.Position(f.Pos()).Filename
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: import %s ilegible: %v", name, imp.Path.Value, err)
			}
			if path != oldContactImportPath && !strings.HasPrefix(path, oldContactImportPath+"/") {
				continue
			}
			local := "contact"
			if imp.Name != nil {
				local = imp.Name.Name
			}
			uses[name] = oldContactUse{localName: local, selectors: selectorsOn(f, local)}
		}
	}
	return uses
}

// selectorsOn devuelve los símbolos que f usa como <local>.<símbolo>, sin repetir y ordenados.
func selectorsOn(f *ast.File, local string) []string {
	seen := make(map[string]bool)
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == local {
			seen[sel.Sel.Name] = true
		}
		return true
	})
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// TestBootWiring_OnlyBridgeAndFlowsImportOldContact es la mitad «ninguna fase importa el viejo
// fuera del adaptador» del test de cableado obligatorio (05 §4.2). Sin ella, una fase puede
// construir el resolver VIEJO y pasárselo a su consumidor sin tocar flowDeps.contacts: con las
// mismas claves el comportamiento es idéntico y nucleo/contact deja de usarse sin que nadie lo
// note (hallazgo 39 de F1: el mutante de fase7_flujos.go sobrevivía a go vet y a todo el paquete).
func TestBootWiring_OnlyBridgeAndFlowsImportOldContact(t *testing.T) {
	uses := oldContactUses(t)
	for name := range uses {
		if _, ok := oldContactImporters[name]; !ok {
			t.Errorf("%s importa %s: en el arranque nuevo solo pueden importarlo bridge_contact.go (el adaptador) "+
				"y flows.go (el tipo del campo contacts); una fase recibe el resolver por flowDeps.contacts",
				name, oldContactImportPath)
		}
	}
	// La lista blanca no puede quedarse atrás: una entrada que ya no importa el viejo (o cuyo
	// fichero se borró) es un permiso que nadie usa, y se retira en el mismo commit.
	for name := range oldContactImporters {
		if _, ok := uses[name]; !ok {
			t.Errorf("%s está en la lista blanca oldContactImporters pero ya no importa %s (o el fichero ya no existe): "+
				"quítalo de la lista", name, oldContactImportPath)
		}
	}
}

// TestBootWiring_OldContactUsedOnlyAsTypes cierra la otra puerta: los dos ficheros de la lista
// blanca usan del paquete viejo SOLO los símbolos declarados —tipos y centinelas, ningún
// constructor—. flows.go, en particular, solo viejo.Resolver: no puede construir nada viejo.
func TestBootWiring_OldContactUsedOnlyAsTypes(t *testing.T) {
	uses := oldContactUses(t)
	for name, allowed := range oldContactImporters {
		use, ok := uses[name]
		if !ok {
			continue // lo dice TestBootWiring_OnlyBridgeAndFlowsImportOldContact, con su mensaje
		}
		if use.localName == "." || use.localName == "_" {
			t.Errorf("%s importa %s como %q: con ese import no se puede ver qué símbolos usa; impórtalo con nombre",
				name, oldContactImportPath, use.localName)
			continue
		}
		if len(use.selectors) == 0 {
			t.Errorf("%s importa %s como %q pero no se ve ningún uso %s.<símbolo>: el test se quedó ciego",
				name, oldContactImportPath, use.localName, use.localName)
		}
		for _, sel := range use.selectors {
			if !slices.Contains(allowed, sel) {
				t.Errorf("%s usa %s.%s del paquete viejo de contactos; solo puede usar %v "+
					"(el resolver se construye con newContactResolver, el de nucleo/contact)",
					name, use.localName, sel, allowed)
			}
		}
	}
}
