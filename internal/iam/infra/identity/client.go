// Package iamidentity implementa out.IdentityClient: el adaptador HTTP hacia
// identity-api, el SSO del grupo (identity Plan 003 · Ola 3).
//
// Es la única pieza del IAM que sale del proceso hacia otro servicio. Traduce
// el contrato de identity (`identity_token`, `expires_in`, sus códigos de error)
// a los tipos y errores tipados del dominio de wApp, de modo que ni los usecases
// ni el gateway conozcan su forma en el cable.
//
// HIGIENE: aquí viajan credenciales y tokens. Este paquete NO loguea NADA —ni
// cuerpos, ni tokens, ni la URL con parámetros—: los errores que devuelve
// nombran la operación y el código HTTP, nunca el material.
package iamidentity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// Rutas de identity-api (prefijo /api/v1, contrato verificado contra su router).
const (
	pathLogin     = "/api/v1/auth/login"
	pathRefresh   = "/api/v1/auth/refresh"
	pathLogout    = "/api/v1/auth/logout"
	pathLogoutAll = "/api/v1/auth/logout-all"
)

// defaultTimeout acota cada llamada a identity. Es corto a propósito: estas
// llamadas están en el camino de un login de usuario, y un identity lento tiene
// que degradar en un error, no en una espera indefinida del operador.
const defaultTimeout = 10 * time.Second

// maxErrorBody acota lo que se lee del cuerpo de una respuesta de error.
const maxErrorBody = 4 << 10

// Client habla con identity-api por HTTP.
type Client struct {
	baseURL string
	http    *http.Client
}

var _ out.IdentityClient = (*Client)(nil)

// New construye el cliente contra la URL base de identity-api (sin barra final;
// se normaliza). La URL SIEMPRE viene de configuración: no hay default, porque
// no existe un identity "por defecto" al que sea seguro mandar contraseñas.
func New(baseURL string, timeout time.Duration) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, errors.New("iam: la URL de identity-api no puede estar vacía")
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return nil, fmt.Errorf("iam: la URL de identity-api debe ser http(s): %q", base)
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{baseURL: base, http: &http.Client{Timeout: timeout}}, nil
}

// ---------------------------------------------------------------------------
// Wire format de identity-api (sus nombres, no los nuestros)
// ---------------------------------------------------------------------------

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	System   string `json:"system"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type logoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// sessionResponse cubre la respuesta de login y la de refresh: el campo del
// token de usuario se llama `identity_token` (NO `access_token`) y la vigencia
// viaja como `expires_in` en segundos.
type sessionResponse struct {
	SessionID     string `json:"session_id"`
	IdentityToken string `json:"identity_token"`
	RefreshToken  string `json:"refresh_token"`
	ExpiresIn     int64  `json:"expires_in"`
}

// Login implementa out.IdentityClient.
func (c *Client) Login(ctx context.Context, email, password, system string) (domain.IdentitySession, error) {
	if email == "" || password == "" || system == "" {
		return domain.IdentitySession{}, domain.ErrInvalidInput
	}
	var res sessionResponse
	err := c.post(ctx, pathLogin, "", loginRequest{Email: email, Password: password, System: system}, &res)
	if err != nil {
		return domain.IdentitySession{}, mapLoginError(err)
	}
	return toSession(res)
}

// Refresh implementa out.IdentityClient.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (domain.IdentitySession, error) {
	if refreshToken == "" {
		return domain.IdentitySession{}, domain.ErrInvalidInput
	}
	var res sessionResponse
	if err := c.post(ctx, pathRefresh, "", refreshRequest{RefreshToken: refreshToken}, &res); err != nil {
		return domain.IdentitySession{}, mapRefreshError(err)
	}
	return toSession(res)
}

// Logout implementa out.IdentityClient. identity contesta 204 tanto si revocó
// como si no había nada que revocar (anti-oráculo), así que este método es
// idempotente por construcción.
func (c *Client) Logout(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return domain.ErrInvalidInput
	}
	return mapRefreshError(c.post(ctx, pathLogout, "", logoutRequest{RefreshToken: refreshToken}, nil))
}

// LogoutAll implementa out.IdentityClient. El titular sale del Identity Token
// que se presenta como portador, NUNCA de un cuerpo: es lo que hace que la
// revocación global no necesite que nadie transporte un user_id.
func (c *Client) LogoutAll(ctx context.Context, identityToken string) error {
	if identityToken == "" {
		return domain.ErrInvalidInput
	}
	return mapRefreshError(c.post(ctx, pathLogoutAll, identityToken, nil, nil))
}

// toSession traduce la respuesta de identity al tipo de dominio, convirtiendo
// `expires_in` (segundos) en el instante absoluto con el que se compara aquí.
func toSession(res sessionResponse) (domain.IdentitySession, error) {
	if res.IdentityToken == "" || res.RefreshToken == "" {
		return domain.IdentitySession{}, errors.New("iam: identity devolvió una sesión sin tokens")
	}
	return domain.IdentitySession{
		SessionID:     res.SessionID,
		IdentityToken: res.IdentityToken,
		RefreshToken:  res.RefreshToken,
		ExpiresAt:     time.Now().Add(time.Duration(res.ExpiresIn) * time.Second),
	}, nil
}

// httpError es el error de una respuesta no exitosa de identity: su código HTTP
// y el `code` tipado de su cuerpo. NO lleva el cuerpo completo ni nada que haya
// viajado en la petición.
type httpError struct {
	status int
	code   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("identity respondió %d (%s)", e.status, e.code)
}

// post ejecuta una petición contra identity y decodifica la respuesta en out (o
// la descarta si out es nil). bearer, si no está vacío, viaja como portador.
func (c *Client) post(ctx context.Context, path, bearer string, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("iam: serializando la petición a identity: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, payload)
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
	defer func() {
		// Drenar lo que quede antes de cerrar permite reutilizar la conexión.
		if _, derr := io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody)); derr != nil {
			_ = derr
		}
		if cerr := resp.Body.Close(); cerr != nil {
			_ = cerr
		}
	}()

	if resp.StatusCode >= http.StatusBadRequest {
		return &httpError{status: resp.StatusCode, code: errorCode(resp.Body)}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("iam: respuesta de identity ilegible: %w", err)
	}
	return nil
}

// errorCode extrae el `code` del cuerpo de error de identity. Si no se puede
// leer, devuelve "" — el código HTTP ya basta para decidir.
func errorCode(body io.Reader) string {
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(body, maxErrorBody)).Decode(&payload); err != nil {
		return ""
	}
	return payload.Code
}

// mapLoginError traduce el fallo de un login en identity al error tipado del
// dominio de wApp.
//
// El 403 del System Gate —credenciales correctas, pero esta persona no tiene
// concedida ESTA aplicación— se traduce a ErrUserInactive y no a credenciales
// inválidas: es la traducción más fiel de las que el contrato con el Edge
// admite, y evita decirle "contraseña incorrecta" a quien la escribió bien.
func mapLoginError(err error) error {
	var he *httpError
	if !errors.As(err, &he) {
		return err
	}
	switch he.status {
	case http.StatusUnauthorized:
		return domain.ErrInvalidCredentials
	case http.StatusForbidden:
		return domain.ErrUserInactive
	case http.StatusBadRequest:
		return domain.ErrInvalidInput
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return domain.ErrIdentityUnavailable
	default:
		return err
	}
}

// mapRefreshError traduce el fallo de un refresh/logout: ahí el 401 no habla de
// credenciales sino del refresh presentado.
func mapRefreshError(err error) error {
	var he *httpError
	if !errors.As(err, &he) {
		return err
	}
	switch he.status {
	case http.StatusUnauthorized:
		return domain.ErrRefreshInvalid
	case http.StatusBadRequest:
		return domain.ErrInvalidInput
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return domain.ErrIdentityUnavailable
	default:
		return err
	}
}
