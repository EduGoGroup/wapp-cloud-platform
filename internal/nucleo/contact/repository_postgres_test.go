//go:build pendiente

package contact

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Aserciones de compilación de lo que PostgresResolver promete sin base de datos: es un Resolver
// (el puerto), el constructor recibe el pool, el cipher y el KeyProvider y NO devuelve un error (no
// valida sus argumentos), y Destino tiene la firma del puerto. Destino y el SQL de Resolve solo
// se ejercitan contra un Postgres real: la suite contacthelpertest.Contrato (T1.13, T1.18) y F9.
var _ Resolver = (*PostgresResolver)(nil)

var _ func(*sql.DB, *crypto.FieldCipher, crypto.KeyProvider) *PostgresResolver = NewPostgresResolver

var _ func(*PostgresResolver, context.Context, string, string) (Ref, error) = (*PostgresResolver).Destino

// R-18: con la lista vacía (nil o []Ref{}) Resolve devuelve ErrNoRefs, con el texto observable
// exacto, el contactID "" y SIN tocar la base de datos. El resolver se construye con db, cipher y kp
// nil: cualquier acceso a uno de los tres fallaría con un pánico, así que un resultado limpio prueba
// que ni la transacción, ni el cifrado, ni el índice ciego se tocaron.
func TestPostgresResolver_SinRefs_ErrNoRefs(t *testing.T) {
	const (
		tenant   = "6f0f3c2e-8d1b-4a57-9c64-2b7a1d5e9f10"
		pushName = "Ana Perez"
		texto    = "contact: se requiere al menos una contact_ref"
	)
	r := NewPostgresResolver(nil, nil, nil)
	for _, c := range []struct {
		caso string
		refs []Ref
	}{
		{"lista nil", nil},
		{"lista vacía", []Ref{}},
	} {
		t.Run(c.caso, func(t *testing.T) {
			id, err := r.Resolve(t.Context(), tenant, c.refs, pushName)
			if !errors.Is(err, ErrNoRefs) {
				t.Fatalf("Resolve sin refs = %q, %v; quiere un error que envuelva ErrNoRefs", id, err)
			}
			if err.Error() != texto {
				t.Errorf("texto observable = %q; quiere %q", err.Error(), texto)
			}
			if id != "" {
				t.Errorf("con error el contactID debe ser \"\"; dio %q", id)
			}
		})
	}
}

// R1.4.d: el error de Resolve sin refs no filtra PII. Con un pushName y un tenant en la mano, ni
// uno ni otro aparecen en el texto del error.
func TestPostgresResolver_SinRefs_NoFiltraPII(t *testing.T) {
	const (
		tenant   = "6f0f3c2e-8d1b-4a57-9c64-2b7a1d5e9f10"
		pushName = "Ana Perez"
	)
	_, err := NewPostgresResolver(nil, nil, nil).Resolve(t.Context(), tenant, nil, pushName)
	if err == nil {
		t.Fatal("Resolve sin refs no dio error")
	}
	for _, secreto := range []string{pushName, tenant} {
		if strings.Contains(err.Error(), secreto) {
			t.Errorf("el error contiene %q, que no debe salir en un mensaje: %v", secreto, err)
		}
	}
}

// El constructor no valida sus argumentos: con db, cipher y kp nil devuelve igualmente un
// resolver, sin pánico y sin error (su firma no tiene uno: ver las aserciones de compilación).
func TestNewPostgresResolver_NoValidaArgumentos(t *testing.T) {
	if r := NewPostgresResolver(nil, nil, nil); r == nil {
		t.Fatal("NewPostgresResolver(nil, nil, nil) = nil; quiere un *PostgresResolver: el constructor no valida")
	}
}
