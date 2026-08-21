package gatewaygrpc

import "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"

// 🔴 LA GUARDA QUE IMPIDE QUE EL SALUDO SE APAGUE EN SILENCIO.
//
// greetIfNeeded descubre la capacidad con una ASERCIÓN DE TIPO sobre s.fleet, que se
// declara fleet.Repository (ver el ⚠️ del docstring de sessionGreeter). Una aserción
// que falla no da error: entra por el camino del Debug y NO SALUDA A NADIE. O sea que
// el día que alguien le cambie la firma a PendingGreeting/MarkGreeted —o le ponga
// delante un decorador por delegación— nada se pondría rojo, y el defecto solo se
// vería en el teléfono de un cliente que no recibe su aviso.
//
// Esta línea convierte ese fallo silencioso en un fallo de COMPILACIÓN. Es lo único
// que ata el puerto al tipo que bootstrap.go:173 inyecta de verdad.
var _ sessionGreeter = (*fleet.PostgresRepository)(nil)

// ⚠️ Y lo que NO se afirma aquí, dicho en voz alta porque se verificó y es verdad:
// *fleet.MemoryRepository NO cumple sessionGreeter (le falta MarkGreeted). No es un
// olvido de esta guarda —añadir el assert lo pondría rojo—: es el estado real del
// código, y su consecuencia es que cualquier montaje sobre el repositorio EN MEMORIA
// no saluda. Hoy eso solo afecta a dobles de prueba. Si algún día el repo en memoria
// llegara a un camino que deba saludar, la salida es promover los dos métodos a
// fleet.Repository, no relajar esto.
