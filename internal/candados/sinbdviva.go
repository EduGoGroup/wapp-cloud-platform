package candados

import "github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"

// SinBDViva prohíbe apuntar un test de proceso a un Postgres vivo (05 §7.2): la única cadena
// de conexión válida es la que devuelve el contenedor de testcontainers
// (ctr.ConnectionString(ctx, "sslmode=disable")). fuentes son TODOS los .go de test/procesos
// (con o sin etiqueta integracion; las etiquetas se ignoran).
//
// Se salta el fichero cuyo nombre base es "sin_bd_viva_test.go" (el propio candado, que
// nombra los patrones para perseguirlos). En los demás, cada aparición de uno de estos
// patrones es una violación:
//   - un literal de cadena (interpretado o crudo) que contiene "WAPP_TEST_DB_DSN";
//   - un literal de cadena que contiene ":5432" (cubre "localhost:5432" y "127.0.0.1:5432");
//   - un literal de cadena que empieza por "postgres://" o por "postgresql://";
//   - el identificador WithReuseByName (suelto o como Sel de un selector).
//
// Los comentarios no cuentan: se mira el AST, no el texto. Fichero es la Ruta y Motivo
// contiene el patrón que la dispara, literal: "WAPP_TEST_DB_DSN", ":5432", "postgres://",
// "postgresql://" o "WithReuseByName". Un literal que dispara dos patrones da dos violaciones.
func SinBDViva(fuentes []Fuente) []Violacion {
	panic(pendiente.Implementar("candados.SinBDViva"))
}
