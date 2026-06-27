// Package admin contendrá los handlers HTTP del motor de flujos:
// POST /admin/flows (publicar definición, validada y versionada) y
// POST /admin/flows/start (iniciar conversación + enviar el menú, decisión C).
//
// Modelan internal/platform/httpapi/admin.go (decode JSON, validación, códigos)
// y se registran con mux.Handle en cmd/server/main.go. Los handlers llegan en
// T3; en T0 el paquete solo reserva el espacio.
package admin
