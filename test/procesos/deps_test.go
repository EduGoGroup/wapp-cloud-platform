//go:build integracion

package procesos

// Fija testcontainers-go en go.mod (T9.2, decisión T-2) antes de que exista quien lo use:
// sin un importador, `go mod tidy` borraría el require y el commit de dependencias no pasaría
// el gate de tidy. T9.5 lo borra en cuanto main_test.go importa los dos paquetes de verdad.
import (
	_ "github.com/testcontainers/testcontainers-go"                  // contenedores de la corrida
	_ "github.com/testcontainers/testcontainers-go/modules/postgres" // el Postgres 17 del arnés
)
