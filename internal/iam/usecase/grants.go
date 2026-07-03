package usecase

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
	"github.com/EduGoGroup/wapp-shared/auth"
)

// grantsToAuth convierte la lista de grants de dominio (pattern+effect) al wire
// format de wapp-shared/auth (Allow[]/Deny[]) que consume el matcher glob.
func grantsToAuth(gs []domain.Grant) auth.Grants {
	out := auth.Grants{Allow: []string{}, Deny: []string{}}
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
// (auth.ResolveRoleChain sobre parent_role_id), agrega los grants de todos los
// roles de todas las cadenas y, por último, funde los overrides del usuario. El
// aplanado usa auth.MergeGrantChain (une allow/deny y deduplica; la precedencia
// deny-sobre-allow la aplica el matcher por request, no aquí). Devuelve además
// los NOMBRES de los roles asignados directamente (snapshot informativo del
// token).
func resolveEffectiveGrants(
	ctx context.Context,
	roles out.RoleRepo,
	grants out.GrantRepo,
	userID string,
) (auth.Grants, []string, error) {
	assigned, err := roles.RolesOfUser(ctx, userID)
	if err != nil {
		return auth.Grants{}, nil, err
	}

	chain := make([]auth.Grants, 0, len(assigned)+1)
	roleNames := make([]string, 0, len(assigned))
	seenRole := make(map[string]struct{}, len(assigned))

	for _, r := range assigned {
		roleNames = append(roleNames, r.Name)
		// Cadena de herencia del rol (rol + ancestros por parent_role_id).
		ids, cerr := auth.ResolveRoleChain(r.ID, func(id string) (string, bool, error) {
			return roles.ParentOf(ctx, id)
		})
		if cerr != nil {
			return auth.Grants{}, nil, cerr
		}
		for _, id := range ids {
			if _, dup := seenRole[id]; dup {
				continue // un mismo rol compartido por dos cadenas se agrega una vez
			}
			seenRole[id] = struct{}{}
			gs, gerr := roles.GrantsOf(ctx, id)
			if gerr != nil {
				return auth.Grants{}, nil, gerr
			}
			chain = append(chain, grantsToAuth(gs))
		}
	}

	// Overrides del usuario (se mergean por encima de los del rol).
	userGrants, err := grants.GrantsOfUser(ctx, userID)
	if err != nil {
		return auth.Grants{}, nil, err
	}
	chain = append(chain, grantsToAuth(userGrants))

	return auth.MergeGrantChain(chain), roleNames, nil
}
