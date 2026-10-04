package iamidentity

// Parte de m2m.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): el service token: caché, canje de la API key, recuerdo breve del fallo y reintento.

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// ---------------------------------------------------------------------------
// Service Token: canje y caché
// ---------------------------------------------------------------------------

// authorized ejecuta una llamada M2M con el Service Token vigente y, si identity lo rechaza con
// 401, hace UN recanje y UN reintento. Uno, no un bucle: si el token recién canjeado también se
// rechaza, el problema es la credencial y reintentar solo multiplica el tráfico contra una ruta
// que identity frena.
//
// El cuerpo se serializa dentro de do en cada intento, así que el reintento no arrastra un
// io.Reader ya consumido.
func (c *M2MClient) authorized(ctx context.Context, method, path string, body, out any) error {
	token, err := c.serviceToken(ctx, "")
	if err != nil {
		return err
	}
	err = c.do(ctx, method, path, token, body, out)
	if !isUnauthorized(err) {
		return err
	}
	// El token que se acaba de usar es el que identity rechazó: se pasa como `stale` para que un
	// recanje que otra goroutine ya hizo no se repita.
	fresh, cerr := c.serviceToken(ctx, token)
	if cerr != nil {
		return cerr
	}
	return c.do(ctx, method, path, fresh, body, out)
}

// serviceToken devuelve un Service Token vigente, canjeando la API key si hace falta.
//
// stale, cuando no está vacío, es el token que el llamante vio rechazado: solo se recanjea si la
// caché sigue teniendo ESE. Así, dos peticiones que se comen el mismo 401 a la vez producen UN
// canje y no dos.
//
// El HTTP se hace FUERA de cualquier candado y el turno se espera con select: mientras uno
// canjea, los demás o encuentran el resultado al despertar o se van con su propio ctx.Err().
// Ninguno se queda esperando un canje que ya no le sirve.
func (c *M2MClient) serviceToken(ctx context.Context, stale string) (string, error) {
	// 1) Camino feliz: la caché sirve y basta una lectura compartida.
	if token, ok := c.cachedToken(stale); ok {
		return token, nil
	}
	// Un contexto ya muerto no compite por el turno: si entrara en el select podría llevárselo
	// por sorteo y gastar un canje que nadie va a leer.
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// 2) El turno. Aquí manda el ctx del llamante: si se cancela o vence esperando, se vuelve con
	//    su error en vez de sumarse a la cola.
	select {
	case c.slot <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-c.slot }()

	// 3) Con el turno en la mano, releer: mientras se esperaba, otro pudo canjear —y entonces no
	//    hay nada que hacer— o fallar —y entonces tampoco, que repetir el intento contra un
	//    identity que se acaba de ver caído es la estampida que se quiere evitar—.
	if token, ok := c.cachedToken(stale); ok {
		return token, nil
	}
	if err := c.recentFailure(); err != nil {
		return "", err
	}

	token, lifetime, err := c.exchangeAPIKey(ctx)
	if err != nil {
		c.storeFailure(ctx, err, stale)
		return "", err
	}
	c.storeToken(token, lifetime)
	return token, nil
}

// cachedToken devuelve el token cacheado cuando sirve: existe, no es el que el llamante acaba de
// ver rechazado y aún no ha vencido.
func (c *M2MClient) cachedToken(stale string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.token == "" || c.token == stale || !c.now().Before(c.expiresAt) {
		return "", false
	}
	return c.token, true
}

// recentFailure devuelve el fallo del último canje mientras siga dentro de exchangeFailureTTL, y
// nil cuando ya caducó. Es lo que impide que una ráfaga entera se convierta en una ráfaga de
// canjes contra un identity caído.
func (c *M2MClient) recentFailure() error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.lastErr == nil || !c.now().Before(c.lastErrAt.Add(exchangeFailureTTL)) {
		return nil
	}
	return c.lastErr
}

// storeToken guarda el canje bueno y borra la caché negativa: identity contestó, así que lo que
// se supiera de su último fallo ya no describe la realidad.
func (c *M2MClient) storeToken(token string, lifetime time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.token, c.expiresAt = token, c.now().Add(lifetime)
	c.lastErr, c.lastErrAt = nil, time.Time{}
}

// storeFailure apunta el fallo del canje para que la ráfaga que viene detrás se lo lleve sin
// llamar, y vacía la caché si lo que quedaba dentro es justamente el token que identity acaba de
// rechazar: mejor "sin token" que un token malo.
//
// Un ctx cancelado NO se apunta: eso no dice nada de identity —dice que ESE llamante se fue— y
// cachearlo castigaría a los demás por una prisa ajena.
func (c *M2MClient) storeFailure(ctx context.Context, err error, stale string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if stale != "" && c.token == stale {
		c.token, c.expiresAt = "", time.Time{}
	}
	if ctx.Err() != nil {
		return
	}
	c.lastErr, c.lastErrAt = err, c.now()
}

// exchangeAPIKey hace el canje y NADA más: ni candados ni caché. Devuelve el token y la vida que
// la caché se permite darle.
func (c *M2MClient) exchangeAPIKey(ctx context.Context) (string, time.Duration, error) {
	var res serviceTokenResponse
	if err := c.do(ctx, http.MethodPost, pathServiceToken, "", serviceTokenRequest{APIKey: c.apiKey}, &res); err != nil {
		return "", 0, mapExchangeError(err)
	}
	if res.ServiceToken == "" {
		return "", 0, errors.New("iam: identity devolvió un canje sin service token")
	}
	if res.ExpiresIn <= 0 {
		return "", 0, errors.New("iam: identity devolvió un service token sin vigencia")
	}
	return res.ServiceToken, usableLifetime(res.ExpiresIn), nil
}

// nonNilStrings convierte el nil que produce un `null` del JSON en el arreglo vacío que el
// dominio promete. No copia: solo sustituye el nil.
func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// usableLifetime traduce `expires_in` en la vida que la caché se permite usar.
//
// Resta el margen de seguridad salvo cuando el token es tan corto que restarlo lo dejaría nacido
// muerto: ahí se usa la mitad. Un identity mal configurado con TTLs minúsculos degrada en más
// canjes, no en un token que nunca se reutiliza.
func usableLifetime(expiresIn int64) time.Duration {
	lifetime := time.Duration(expiresIn) * time.Second
	if lifetime > 2*tokenSafetyMargin {
		return lifetime - tokenSafetyMargin
	}
	return lifetime / 2
}
