package domain_test

// canje_test.go — EL VEREDICTO DEL CANJE (Plan 047 · Ola A · T-A3).
//
// EvaluarCanje es la mitad del anti-oráculo que se puede probar sin base de
// datos: que la ausencia y la caducidad salgan por la MISMA función, con la
// MISMA forma y sin trabajo asimétrico entre ellas. Aquí se fija su tabla de
// verdad; la otra mitad —que además cuesten lo mismo en la base— la vigila el
// candado sobre el AST del adaptador.

import (
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
)

func TestEvaluarCanje_LosCuatroVeredictos(t *testing.T) {
	t.Parallel()

	ahora := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	antes := ahora.Add(-time.Hour)
	despues := ahora.Add(time.Hour)

	viva := func() *domain.Invitation {
		return &domain.Invitation{TenantID: "t-1", ExpiresAt: despues}
	}

	casos := []struct {
		nombre string
		inv    *domain.Invitation
		quiero domain.ResultadoCanje
	}{
		{
			// El único que deja seguir.
			nombre: "viva",
			inv:    viva(),
			quiero: domain.CanjeProcede,
		},
		{
			// 🔴 nil NO es un error del llamante: es como el adaptador representa
			// «no había fila». Que entre por el mismo parámetro que la presencia es
			// lo que hace que los dos caminos compartan el resto del código.
			nombre: "no existe",
			inv:    nil,
			quiero: domain.CanjeAusente,
		},
		{
			nombre: "caducada",
			inv:    &domain.Invitation{TenantID: "t-1", ExpiresAt: antes},
			quiero: domain.CanjeCaducado,
		},
		{
			nombre: "ya canjeada",
			inv: func() *domain.Invitation {
				i := viva()
				quien := "usr-otro"
				i.RedeemedBy, i.RedeemedAt = &quien, &antes
				return i
			}(),
			quiero: domain.CanjeConsumido,
		},
		{
			// T-A8: revocada NO se canjea. Comparte veredicto con la canjeada a
			// propósito — ver el comentario de CanjeConsumido.
			nombre: "revocada",
			inv: func() *domain.Invitation {
				i := viva()
				i.RevokedAt = &antes
				return i
			}(),
			quiero: domain.CanjeConsumido,
		},
		{
			// La precedencia: una invitación canjeada hace un mes TAMBIÉN está
			// vencida, y lo que cuenta es lo que PASÓ, no lo que el reloj dice
			// después. Si esto saliera CanjeCaducado, un token ya usado devolvería
			// 410 en vez de 409 y la UI diría «pide otra» en vez de «esa ya se usó».
			nombre: "canjeada Y vencida gana el canje",
			inv: func() *domain.Invitation {
				quien := "usr-otro"
				return &domain.Invitation{
					TenantID:   "t-1",
					ExpiresAt:  antes,
					RedeemedBy: &quien,
					RedeemedAt: &antes,
				}
			}(),
			quiero: domain.CanjeConsumido,
		},
		{
			// El borde exacto: expires_at == ahora ya está vencida. Se fija porque
			// «<» y «<=» son la clase de detalle que se cambia sin querer al
			// reescribir la condición, y el borde no lo enseña ningún caso lejano.
			nombre: "justo en el instante de vencer",
			inv:    &domain.Invitation{TenantID: "t-1", ExpiresAt: ahora},
			quiero: domain.CanjeCaducado,
		},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			t.Parallel()
			if got := domain.EvaluarCanje(c.inv, ahora); got != c.quiero {
				t.Errorf("EvaluarCanje(%s) = %v, quiero %v", c.nombre, got, c.quiero)
			}
		})
	}
}
