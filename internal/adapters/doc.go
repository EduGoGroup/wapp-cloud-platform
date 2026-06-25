// Package adapters contiene las implementaciones concretas de los puertos de la Plataforma Cloud.
// Adaptadores previstos:
//   - postgres/   → repositorios relacionales (tenants, usuarios, contactos, campañas, fleet, leases).
//   - mongo/      → repositorios de documentos (flow_defs, flow_state, flow_results).
//   - s3/         → almacenamiento de media + generación de URLs prefirmadas (S3/MinIO).
//   - grpcserver/ → servidor gRPC CloudLink: Enrollment.EnrollEdge + CloudLink.Connect (bidi-stream, mTLS).
//   - http/       → Gin: rutas de la Consola/BFF y health-checks.
package adapters
