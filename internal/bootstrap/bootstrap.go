// Package bootstrap es la FACHADA del arranque de la Plataforma Cloud: los dos
// únicos símbolos que el resto del proceso necesita para levantar el servidor.
//
// El cableado de verdad —las nueve fases que construyen los ~60 objetos del
// monolito— vive en el subpaquete internal/bootstrap/arranque, y vive ahí por una
// razón concreta: todas las fases comparten un contenedor con campos PRIVADOS, así
// que ninguna de las piezas internas del arranque tiene que exportarse para que la
// fase siguiente la vea. Este fichero es el puente entre ese interior privado y el
// exterior público, y por eso mide lo que mide.
//
// Para saber QUÉ EXISTE en la plataforma se lee arranque/orquestador.go, que tiene
// las nueve fases en orden y en una sola pantalla. Hasta el 2026-09-04 la respuesta
// a esa pregunta eran las 990 líneas de una función llamada Run.
package bootstrap

import (
	"context"
	"crypto/tls"

	"google.golang.org/grpc/credentials"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/bootstrap/arranque"
)

// Run ejecuta el ciclo de vida completo del servidor: carga de config,
// construcción de dependencias, arranque de listeners y espera de parada.
// Devuelve nil en shutdown limpio o el error del primer fallo fatal.
func Run(ctx context.Context) error {
	return arranque.Ejecutar(ctx)
}

// EnrollServerCreds construye credentials de TLS de servidor SOLAMENTE (sin
// exigir cert de cliente): el Edge enrola aquí antes de tener cert. NO se puede
// usar mtls.ServerCreds porque exige RequireAndVerifyClientCert.
//
// Se re-expone desde aquí porque su otro llamante vive fuera del proceso de
// arranque: cmd/server/integration_test.go levanta su propio servidor de
// enrolamiento y necesita EXACTAMENTE las mismas credenciales que producción, no
// una copia a mano que solo se prueba a sí misma.
func EnrollServerCreds(serverCert tls.Certificate) credentials.TransportCredentials {
	return arranque.EnrollServerCreds(serverCert)
}
