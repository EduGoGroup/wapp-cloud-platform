package runtime

import (
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Aserción de compilación: el adaptador ES el puerto que consume el runtime.
var _ SelfNumberChecker = (*PostgresSelfNumbers)(nil)

// Este fichero prueba, con un driver de mentira, la FORMA del adaptador: qué se decide antes de ir
// a la base, qué viaja en la consulta y cómo se leen su respuesta y sus errores. Que su SQL haga
// en un Postgres de verdad lo que el contrato promete —el perfil, el estado, la agregación por
// número, el aislamiento por tenant, el índice sembrado con otra clave— lo prueba
// runtimehelpertest.ContratoSelfNumbers con el arnés de test/procesos
// (runtime_self_numbers_contrato_test.go), y contra el gemelo en memoria en unitario.

const (
	snTenant = "5b7e0f0c-9d0e-4a51-8a55-0d4f6a3c1e10"
	snNumber = "573001112233"
)

// snKeyProvider es un KeyProvider de mentira: su índice «ciego» deja ver de qué tenant y de qué
// valor salió, para poder afirmar qué se le pidió. Lo demás no lo usa este adaptador.
type snKeyProvider struct{}

var _ crypto.KeyProvider = snKeyProvider{}

func (snKeyProvider) WrapDEK([]byte) ([]byte, string, error) {
	return nil, "", errors.New("snKeyProvider: sin WrapDEK")
}

func (snKeyProvider) UnwrapDEK([]byte, string) ([]byte, error) {
	return nil, errors.New("snKeyProvider: sin UnwrapDEK")
}
func (snKeyProvider) BlindIndex(tenantID, value string) string { return snBlindIndex(tenantID, value) }
func (snKeyProvider) CurrentKeyID() string                     { return "sn" }

// snBlindIndex es el índice de snKeyProvider. No contiene el valor tal cual (va al revés), para
// que «el número no viaja» se pueda afirmar sobre los argumentos.
func snBlindIndex(tenantID, value string) string {
	reversed := []rune(value)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	return "bidx[" + tenantID + "|" + string(reversed) + "]"
}

// snHarness son dos predicados sobre el MISMO driver de mentira —con KeyProvider y sin él— y el
// registro de lo que le llegó.
type snHarness struct {
	fake    *pgFake
	checker *PostgresSelfNumbers
	noKP    *PostgresSelfNumbers
}

// newSNHarness construye los dos predicados sobre un driver de mentira con ese guion.
func newSNHarness(t *testing.T, replies ...pgReply) snHarness {
	t.Helper()
	fake := &pgFake{replies: replies}
	db := fake.open(t)
	return snHarness{checker: NewPostgresSelfNumbers(db, snKeyProvider{}), noKP: NewPostgresSelfNumbers(db, nil), fake: fake}
}

// TestNewPostgresSelfNumbers_DoesNotTouchTheDatabase: construir no manda ni una sentencia ni
// valida nada: ni un db nil ni un KeyProvider nil fallan aquí.
func TestNewPostgresSelfNumbers_DoesNotTouchTheDatabase(t *testing.T) {
	h := newSNHarness(t)
	if h.checker == nil || h.noKP == nil {
		t.Fatal("NewPostgresSelfNumbers devolvió nil")
	}
	if NewPostgresSelfNumbers(nil, nil) == nil {
		t.Fatal("NewPostgresSelfNumbers(nil, nil) devolvió nil")
	}
	requirePgStatements(t, h.fake, 0)
}

// TestIsSelfNumber_EmptyNumber_FalseWithoutAsking: sin número no hay pregunta: (false, nil) sin ir
// a la base, y ANTES de mirar el KeyProvider (sin él tampoco es un error).
func TestIsSelfNumber_EmptyNumber_FalseWithoutAsking(t *testing.T) {
	h := newSNHarness(t, pgReply{rows: [][]driver.Value{{true}}})
	for name, checker := range map[string]*PostgresSelfNumbers{"with a key provider": h.checker, "without a key provider": h.noKP} {
		self, err := checker.IsSelfNumber(t.Context(), snTenant, "")
		if self || err != nil {
			t.Errorf("%s: IsSelfNumber(\"\") = (%v, %v), quería (false, nil)", name, self, err)
		}
	}
	requirePgStatements(t, h.fake, 0)
}

// TestIsSelfNumber_NoKeyProvider_ErrorNotASilentFalse: sin KeyProvider la pregunta no tiene
// respuesta: devuelve el centinela, no un false mudo, y no va a la base.
func TestIsSelfNumber_NoKeyProvider_ErrorNotASilentFalse(t *testing.T) {
	h := newSNHarness(t, pgReply{rows: [][]driver.Value{{true}}})

	self, err := h.noKP.IsSelfNumber(t.Context(), snTenant, snNumber)
	if !errors.Is(err, ErrSelfNumbersNoKeyProvider) {
		t.Fatalf("error = %v, quería ErrSelfNumbersNoKeyProvider", err)
	}
	if self {
		t.Error("con error devolvió true")
	}
	requirePgStatements(t, h.fake, 0)
}

// TestIsSelfNumber_AsksByBlindIndex_NeverByTheNumber: UNA consulta con dos argumentos, el tenant y
// el índice ciego del número TAL CUAL llegó (aquí no se normaliza: con adornos, el índice es el
// del número con adornos). El número en claro no viaja ni en los argumentos ni en el texto.
func TestIsSelfNumber_AsksByBlindIndex_NeverByTheNumber(t *testing.T) {
	for _, number := range []string{snNumber, "+57 (300) 111-2233"} {
		h := newSNHarness(t, pgReply{rows: [][]driver.Value{{true}}})

		if _, err := h.checker.IsSelfNumber(t.Context(), snTenant, number); err != nil {
			t.Fatalf("IsSelfNumber(%q): %v", number, err)
		}
		sent := requirePgStatements(t, h.fake, 1)
		want := []driver.Value{snTenant, snBlindIndex(snTenant, number)}
		if len(sent[0].args) != 2 || sent[0].args[0] != want[0] || sent[0].args[1] != want[1] {
			t.Errorf("argumentos = %v, quería %v", sent[0].args, want)
		}
		if strings.Contains(sent[0].query, number) {
			t.Errorf("el texto de la consulta lleva el número en claro: %s", sent[0].query)
		}
	}
}

// TestIsSelfNumber_ReadsTheAggregate: true solo si el agregado es verdadero; falso, NULL (ninguna
// fila casó) y «la consulta no devolvió fila» son los tres (false, nil).
func TestIsSelfNumber_ReadsTheAggregate(t *testing.T) {
	cases := []struct {
		name  string
		reply pgReply
		want  bool
	}{
		{"some live session is not passive", pgReply{rows: [][]driver.Value{{true}}}, true},
		{"every matching session is passive", pgReply{rows: [][]driver.Value{{false}}}, false},
		{"no session matches (NULL)", pgReply{rows: [][]driver.Value{{nil}}}, false},
		{"the query returns no row at all", pgReply{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newSNHarness(t, c.reply)

			self, err := h.checker.IsSelfNumber(t.Context(), snTenant, snNumber)
			if err != nil {
				t.Fatalf("IsSelfNumber: %v", err)
			}
			if self != c.want {
				t.Errorf("IsSelfNumber = %v, quería %v", self, c.want)
			}
		})
	}
}

// TestIsSelfNumber_DatabaseFailure_WrappedWithoutPII: un fallo de la base vuelve como (false,
// error) envuelto tras su prefijo, y el mensaje no lleva ni el número ni su índice.
func TestIsSelfNumber_DatabaseFailure_WrappedWithoutPII(t *testing.T) {
	boom := errors.New("la base se cayó")
	h := newSNHarness(t, pgReply{err: boom})

	self, err := h.checker.IsSelfNumber(t.Context(), snTenant, snNumber)
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, quería uno que envuelva %v", err, boom)
	}
	if self {
		t.Error("con error devolvió true")
	}
	if want := "self_numbers: consulta fleet_sessions: " + boom.Error(); err.Error() != want {
		t.Errorf("texto del error = %q, quería %q", err.Error(), want)
	}
	if errors.Is(err, ErrSelfNumbersNoKeyProvider) {
		t.Error("un fallo de la base casa con ErrSelfNumbersNoKeyProvider")
	}
	for _, leak := range []string{snNumber, snBlindIndex(snTenant, snNumber)} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("el mensaje de error lleva %q: %s", leak, err.Error())
		}
	}
}

// TestErrSelfNumbersNoKeyProvider_Literal: el texto del centinela es el del viejo, byte a byte.
func TestErrSelfNumbersNoKeyProvider_Literal(t *testing.T) {
	const want = "self_numbers: sin KeyProvider no se puede calcular el índice ciego"
	if got := ErrSelfNumbersNoKeyProvider.Error(); got != want {
		t.Errorf("ErrSelfNumbersNoKeyProvider = %q, quería %q", got, want)
	}
}
