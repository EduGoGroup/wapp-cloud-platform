// Package procesos reúne los tests de integración por proceso de negocio de F9 (05 §7): caja
// negra contra un Postgres real levantado con testcontainers-go, un solo contenedor por corrida
// y una base clonada por proceso, ejecutados contra los dos binarios (cmd/server y
// cmd/server-modular). Nunca contra un Postgres vivo.
//
// Todos llevan la etiqueta integracion (corren con make test-procesos, en local) salvo el
// candado sin_bd_viva_test.go, que va sin etiqueta para morder en ci-local (F0 T0.8,
// plan/F0-andamiaje/reglas.md §3). Este fichero tampoco la lleva.
package procesos
