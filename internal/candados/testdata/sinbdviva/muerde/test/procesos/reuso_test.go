//go:build integracion

package procesos

import "github.com/testcontainers/testcontainers-go"

var reuso = testcontainers.WithReuseByName("wapp")
