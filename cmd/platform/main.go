package main

import (
	"fmt"
	"log"
)

func main() {
	// TODO: Inicializar la Plataforma Cloud (monolito modular)
	// - Cargar configuración (PostgreSQL, MongoDB, S3/MinIO)
	// - Inicializar módulo IAM (JWT + RBAC multi-tenant, copia de edugo-api-identity)
	// - Inicializar módulo Negocio (contactos, segmentos, plantillas, campañas)
	// - Inicializar Motor de Flujos (máquina de estados + ProcessorRegistry)
	// - Inicializar Gateway CloudLink gRPC server (mTLS, streams, leases, fleet)
	// - Registrar rutas HTTP/Gin para la Consola/BFF
	// - Arrancar servidor HTTP + servidor gRPC
	// - Bloquear en loop de señales del SO (SIGTERM/SIGINT → graceful shutdown)

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	fmt.Println("wapp-cloud-platform: placeholder — sin lógica aún")
	log.Println("TODO: implementar arranque real del monolito modular")
}
