package iamidentity

// Parte de m2m.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): el cable (do, errores de identity) y el mapeo de códigos por operación.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
)

// ---------------------------------------------------------------------------
// Transporte y traducción de errores
// ---------------------------------------------------------------------------

// m2mError es el error de una respuesta no exitosa de identity en las rutas M2M: su código HTTP,
// el `code` estable de su cuerpo y los `details` por campo cuando los trae (identity
// dto/health_dto.go:11-17).
//
// Es un tipo APARTE de httpError —el de client.go— porque aquí los `details` SÍ deciden: son los
// que distinguen "la contraseña es corta" de cualquier otro 400 del signup. NO lleva el cuerpo
// completo ni nada que haya viajado en la petición.
type m2mError struct {
	status  int
	code    string
	details map[string]string
}

func (e *m2mError) Error() string {
	return fmt.Sprintf("identity respondió %d (%s)", e.status, e.code)
}

// isUnauthorized dice si el error es un 401 de identity, que es el único que justifica recanjear
// el Service Token.
func isUnauthorized(err error) bool {
	var me *m2mError
	return errors.As(err, &me) && me.status == http.StatusUnauthorized
}

// do ejecuta una petición contra identity y decodifica la respuesta en out (o la descarta si out
// es nil). bearer, si no está vacío, viaja como portador.
//
// Es hermana del post de client.go y no la misma función porque aquí hace falta el verbo (PUT,
// GET) y los `details` del error; fundirlas obligaría a tocar el cliente de personas.
func (c *M2MClient) do(ctx context.Context, method, path, bearer string, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("iam: serializando la petición a identity: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return fmt.Errorf("iam: construyendo la petición a identity: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// Un identity inalcanzable NO es una credencial rechazada.
		return fmt.Errorf("%w: %s", domain.ErrIdentityUnavailable, path)
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode >= http.StatusBadRequest {
		code, details := errorPayload(resp.Body)
		return &m2mError{status: resp.StatusCode, code: code, details: details}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("iam: respuesta de identity ilegible: %w", err)
	}
	return nil
}

// errorPayload extrae el `code` y los `details` del cuerpo de error de identity. Si no se puede
// leer, devuelve vacíos: el código HTTP ya basta para decidir.
func errorPayload(body io.Reader) (string, map[string]string) {
	var payload struct {
		Code    string            `json:"code"`
		Details map[string]string `json:"details"`
	}
	if err := json.NewDecoder(io.LimitReader(body, maxErrorBody)).Decode(&payload); err != nil {
		return "", nil
	}
	return payload.Code, payload.Details
}

// mapExchangeError traduce el fallo del canje de la API key.
//
// El 401 es UNO para tres causas —key desconocida, revocada o vencida—: identity no las distingue
// a propósito y wApp no puede inventarse la diferencia.
func mapExchangeError(err error) error {
	var me *m2mError
	if !errors.As(err, &me) {
		return err
	}
	switch me.status {
	case http.StatusUnauthorized:
		return domain.ErrMachineCredentialInvalid
	case http.StatusBadRequest:
		// api_key vacío. Con el constructor validando no debería ocurrir, así que si ocurre es de
		// la credencial, no de quien pidió el alta.
		return domain.ErrMachineCredentialInvalid
	case http.StatusTooManyRequests:
		return domain.ErrRateLimited
	case http.StatusInternalServerError, http.StatusServiceUnavailable:
		return domain.ErrIdentityUnavailable
	default:
		return err
	}
}

// mapEnsureError traduce el fallo del alta create-or-attach.
//
// No hay 404 ni 409 en esta ruta por diseño de identity: al volver sin error la cuenta existe
// siempre, y dos altas simultáneas del mismo correo acaban las dos en la MISMA cuenta
// (post_ensure_user_handler.go:66-70).
func mapEnsureError(err error) error {
	var me *m2mError
	if !errors.As(err, &me) {
		return err
	}
	switch me.status {
	case http.StatusBadRequest:
		return domain.ErrInvalidInput
	case http.StatusUnauthorized, http.StatusForbidden:
		// 401 tras el recanje y el reintento, o 403 por scope insuficiente: las dos son de la
		// credencial de wApp, no del correo que se aseguraba.
		return domain.ErrMachineCredentialInvalid
	case http.StatusTooManyRequests:
		return domain.ErrRateLimited
	case http.StatusInternalServerError, http.StatusServiceUnavailable:
		return domain.ErrIdentityUnavailable
	default:
		return err
	}
}

// mapGetUserSystemsError traduce el fallo de la LECTURA de accesos.
//
// Es una función APARTE de mapUserSystemsError y no la misma, aunque hoy sus tablas coincidan
// salvo en una línea: esa línea es el 403. En el PUT hay DOS 403 —la frontera de ecosistema y el
// scope insuficiente— y hay que separarlos por el `code`; en el GET solo cabe el segundo, y la
// razón está escrita en el propio identity (get_user_systems_handler.go): sin declaración no hay
// ninguna clave que el llamante haya nombrado, luego ninguna puede ser ajena. Compartir mapper
// haría que un SYSTEM_ACCESS_DENIED imposible en esta ruta saliera como ErrSystemNotAllowed —«esa
// aplicación no es tuya»— cuando lo que de verdad pasa es que a la credencial de wApp le falta
// `identity.users.systems.read`.
//
// El 404 es un desenlace de NEGOCIO aquí, no un fallo: dice que esa persona no está en el
// padrón, que es distinto de estar y no tener ningún acceso (200 con la lista vacía).
func mapGetUserSystemsError(err error) error {
	var me *m2mError
	if !errors.As(err, &me) {
		return err
	}
	switch me.status {
	case http.StatusBadRequest:
		// El identificador no tiene forma de UUID.
		return domain.ErrInvalidInput
	case http.StatusUnauthorized, http.StatusForbidden:
		// 401 tras el recanje y el reintento, o 403 por scope: las dos son de la credencial de
		// wApp, no de la persona que se consultaba.
		return domain.ErrMachineCredentialInvalid
	case http.StatusNotFound:
		return domain.ErrNotFound
	case http.StatusTooManyRequests:
		return domain.ErrRateLimited
	case http.StatusInternalServerError, http.StatusServiceUnavailable:
		return domain.ErrIdentityUnavailable
	default:
		return err
	}
}

// mapUserSystemsError traduce el fallo de la escritura de accesos.
//
// Los DOS 403 posibles significan cosas opuestas y se separan por el `code`:
// SYSTEM_ACCESS_DENIED es la frontera de ecosistema —lo arregla quien pidió el conjunto— y
// FORBIDDEN es scope insuficiente —lo arregla quien configuró la credencial—. Confundirlos haría
// que un error de configuración de wApp se le contara al administrador como "esa aplicación no es
// tuya".
func mapUserSystemsError(err error) error {
	var me *m2mError
	if !errors.As(err, &me) {
		return err
	}
	switch me.status {
	case http.StatusBadRequest:
		return domain.ErrInvalidInput
	case http.StatusForbidden:
		if me.code == codeSystemAccessDenied {
			return domain.ErrSystemNotAllowed
		}
		return domain.ErrMachineCredentialInvalid
	case http.StatusUnauthorized:
		return domain.ErrMachineCredentialInvalid
	case http.StatusNotFound:
		return domain.ErrNotFound
	case http.StatusTooManyRequests:
		return domain.ErrRateLimited
	case http.StatusInternalServerError, http.StatusServiceUnavailable:
		return domain.ErrIdentityUnavailable
	default:
		return err
	}
}

// mapSignupError traduce el fallo del registro público.
//
// El 400 se abre en dos por los `details`: si identity nombra el campo `password`, lo que falló
// es la POLÍTICA de contraseña —12 caracteres mínimo, 72 bytes máximo— y quien llame necesita
// poder decírselo a la persona sin adivinar. Cualquier otro 400 es entrada inválida a secas.
//
// El 409 tapa tres estados que identity NO distingue en el cable —correo con otra clave, cuenta
// bloqueada, cuenta inactiva—, así que wApp tampoco los distingue.
func mapSignupError(err error) error {
	var me *m2mError
	if !errors.As(err, &me) {
		return err
	}
	switch me.status {
	case http.StatusBadRequest:
		if reason, ok := me.details["password"]; ok {
			return fmt.Errorf("%w: %s", domain.ErrPasswordPolicy, reason)
		}
		return domain.ErrInvalidInput
	case http.StatusConflict:
		return domain.ErrEmailTaken
	case http.StatusTooManyRequests:
		// Sin Retry-After: identity no lo emite, así que no hay cuándo.
		return domain.ErrRateLimited
	case http.StatusInternalServerError, http.StatusServiceUnavailable:
		return domain.ErrIdentityUnavailable
	default:
		return err
	}
}
