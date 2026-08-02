package usecase

import (
	"context"

	identityrbac "github.com/EduGoGroup/identity-shared/auth/rbac"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// grantsToAuth convierte la lista de grants de dominio (pattern+effect) al wire
// format de identity-shared/auth (Allow[]/Deny[]) que consume el matcher glob.
func grantsToAuth(gs []domain.Grant) identityrbac.Grants {
	out := identityrbac.Grants{Allow: []string{}, Deny: []string{}}
	for _, g := range gs {
		if g.Effect == domain.EffectDeny {
			out.Deny = append(out.Deny, g.Pattern)
		} else {
			out.Allow = append(out.Allow, g.Pattern)
		}
	}
	return out
}

// resolveEffectiveGrants calcula los grants EFECTIVOS de un usuario AL EMITIR el
// token (design.md §5): por cada rol asignado resuelve su cadena de herencia
// (identityrbac.ResolveRoleChain sobre parent_role_id), agrega los grants de todos los
// roles de todas las cadenas y, por último, funde los overrides del usuario. El
// aplanado usa identityrbac.MergeGrantChain (une allow/deny y deduplica; la precedencia
// deny-sobre-allow la aplica el matcher por request, no aquí). Devuelve además
// los NOMBRES de los roles asignados directamente (snapshot informativo del
// token).
func resolveEffectiveGrants(
	ctx context.Context,
	roles out.RoleRepo,
	grants out.GrantRepo,
	userID string,
) (identityrbac.Grants, []string, error) {
	assigned, err := roles.RolesOfUser(ctx, userID)
	if err != nil {
		return identityrbac.Grants{}, nil, err
	}

	chain := make([]identityrbac.Grants, 0, len(assigned)+1)
	roleNames := make([]string, 0, len(assigned))
	seenRole := make(map[string]struct{}, len(assigned))

	for _, r := range assigned {
		roleNames = append(roleNames, r.Name)
		// Cadena de herencia del rol (rol + ancestros por parent_role_id).
		ids, cerr := identityrbac.ResolveRoleChain(r.ID, func(id string) (string, bool, error) {
			return roles.ParentOf(ctx, id)
		})
		if cerr != nil {
			return identityrbac.Grants{}, nil, cerr
		}
		for _, id := range ids {
			if _, dup := seenRole[id]; dup {
				continue // un mismo rol compartido por dos cadenas se agrega una vez
			}
			seenRole[id] = struct{}{}
			gs, gerr := roles.GrantsOf(ctx, id)
			if gerr != nil {
				return identityrbac.Grants{}, nil, gerr
			}
			chain = append(chain, grantsToAuth(gs))
		}
	}

	// Overrides del usuario (se mergean por encima de los del rol).
	userGrants, err := grants.GrantsOfUser(ctx, userID)
	if err != nil {
		return identityrbac.Grants{}, nil, err
	}
	chain = append(chain, grantsToAuth(userGrants))

	return identityrbac.MergeGrantChain(chain), roleNames, nil
}
