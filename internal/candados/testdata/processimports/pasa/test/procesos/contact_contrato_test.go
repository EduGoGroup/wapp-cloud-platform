//go:build integracion

package procesos

import (
	"database/sql"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact/contacthelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// La suite, el constructor del puerto (cualquier New…) y crypto. Los comentarios pueden nombrar
// contact.Resolver y contact.PostgresResolver: se mira el AST, no el texto.
func TestContactContrato(t *testing.T) {
	var db *sql.DB
	kp, _ := crypto.NewEnvKeyProvider(crypto.KeyringConfig{})
	_ = contacthelpertest.Montaje{Resolver: contact.NewPostgresResolver(db, crypto.NewFieldCipher(kp), kp)}
	_ = contact.NewMemoryResolver(nil)
	_ = pgx.ErrNoRows
}
