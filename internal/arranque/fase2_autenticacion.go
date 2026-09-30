// Copia de internal/bootstrap/arranque/fase2_autenticacion.go @ 80807ba (F0 · 05 §6): cablea paquetes VIEJOS.
package arranque

import "context"

// faseAutenticacion arma el plano de auth de usuario del IAM y la config JWKS que el
// Edge necesita para verificar tokens sin llamar a nadie.
//
// 🔴 VA ANTES QUE EL GATEWAY Y QUE EL SERVIDOR PÚBLICO, y no es una preferencia de
// orden: el gateway CloudLink consume este mismo stack para las RPCs
// UserLogin/Refresh/Logout del Edge (Plan 033 · T2.2, ADR-0025), y el servidor :8103
// lo reusa tal cual. Un segundo stack aquí sería un segundo verificador, y entonces el
// :8103 podría aceptar tokens que el relé del Edge rechaza.
//
// El detalle de qué construye vive en auth.go, que es donde está buildAuthStack: esta
// fase solo dice CUÁNDO.
type faseAutenticacion struct{}

func (faseAutenticacion) nombre() string { return "autenticación" }

// requiere la base: buildAuthStack persiste y lee sobre el mismo pool.
func (faseAutenticacion) requiere() []string { return []string{"db"} }

func (faseAutenticacion) ejecutar(_ context.Context, c *contenedor) error {
	// --- Plano de auth de usuario del IAM (Plan 018 · T3, ADR-0019). ---
	authStk, err := buildAuthStack(c.cfg, c.db, c.log)
	if err != nil {
		return err
	}
	c.authStk = authStk

	// Config kind:"jwks" (ADR-0025 dec.2): la pública ES256 del emisor de usuario,
	// que el Edge verifica offline. Es GLOBAL del emisor ⇒ se entrega a todo Edge
	// que conecta (jwksConfigProvider), delegando el kind "intents" al provider
	// existente. La rotación reusa gw.PushConfig(ctx, tenant, "jwks", version, payload).
	jwksCfg, err := buildJWKSConfig(c.authStk.jwtBundle.esPub, c.authStk.jwtBundle.kid)
	if err != nil {
		return err
	}
	c.jwksCfg = jwksCfg

	c.marca("auth")
	return nil
}
