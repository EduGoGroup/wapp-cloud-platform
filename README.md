# wapp-cloud-platform (Piezas 03 y 05)

Plataforma cloud modular en Go: todo lo que aloja el equipo de wApp. Arranca
como **monolito modular** y se extrae a servicios solo cuando duela. Aquí
viven los contactos, plantillas, campañas, el motor de flujos conversacionales
y el gateway que habla con cada Edge.

## Rol en wApp

La nube **piensa**; el Edge despacha. Esta plataforma arma el payload completo
y lo empuja al Edge por CloudLink. También recibe los eventos entrantes del
Edge, los procesa en el Motor de Flujos (máquina de estados por contacto con
módulos enchufables) y devuelve la respuesta completa.

## Tecnología

| Decisión | Detalle |
|---|---|
| Lenguaje | Go 1.23 |
| Arquitectura | Monolito modular hexagonal (IAM / Negocio / Motor de Flujos / Gateway) |
| IAM | RBAC multi-tenant; copia/adapta `edugo-api-identity` |
| HTTP | Gin (rutas de Consola/BFF) |
| gRPC | Servidor CloudLink: `Enrollment.EnrollEdge` + `CloudLink.Connect` bidi-stream con mTLS |
| PostgreSQL | Tenants, usuarios, contactos, campañas, fleet, leases |
| MongoDB | Definiciones de flujo (`flow_defs`) y estado conversacional (`flow_state`) |
| S3 / MinIO | Media y PDFs; URLs prefirmadas de corta vida para el Edge |
| Concurrencia | Goroutines + channels (sin RabbitMQ ni `edugo-worker`) |
| Motor de Flujos | `ProcessorRegistry` (idea copiada de `edugo-worker`) + módulos enchufables (Menú, Encuesta, Pedido, PDF/Media) |

## Cómo correrá (placeholder)

```bash
# Compilar (placeholder)
go build -o bin/platform ./cmd/platform

# Ejecutar (placeholder)
./bin/platform --config ./config/config.yaml
```

## Estado

**Greenfield.** Solo scaffold inicial. Ver `CLAUDE.md` para contexto
arquitectónico y:
- `../../docs/piezas/03-plataforma-cloud.md`
- `../../docs/piezas/05-motor-flujos-modulos.md`

> El module path `github.com/wApp/wapp-cloud-platform` es un placeholder
> ajustable al repositorio Git real cuando se publique.
