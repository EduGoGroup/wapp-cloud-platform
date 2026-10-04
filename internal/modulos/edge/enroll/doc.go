// Porta internal/gateway/enroll/doc.go @ 8896f13

// Package enroll implementa el enrolamiento por tenant del Gateway CloudLink
// (Plan 005 · T3): valida el CSR del Edge, consume un código de activación de un
// solo uso y firma un certificado de Edge con la CA del entorno.
//
// Es copia-adaptación del paquete enroll de wapp-cloudlink (referencia), llevado
// al namespace de la Plataforma Cloud y extendido con persistencia PostgreSQL:
//   - CA firmante de CSRs (cert hoja con EKU ClientAuth, Organization=tenant).
//   - CodeStore de códigos de un solo uso: PostgresCodeStore (consumo atómico
//     en BD).
//   - EdgeCertRepository: persiste los metadatos del cert emitido.
//
// Los dobles en memoria de los dos puertos (antes MemoryStore y
// MemoryEdgeCertRepository, en este paquete) viven en enrollhelpertest con sus
// suites de contrato (D-F3-1).
//
//   - Service: orquesta verificar CSR → consumir código → firmar → persistir.
//   - Server: implementa cloudlinkv1.EnrollmentServer (EnrollEdge), mapeando los
//     errores de dominio a códigos gRPC.
//
// Frontera zero-knowledge (ADR-0009): por aquí solo viaja material PÚBLICO (CSR,
// cert hoja, cadena de CA). La clave privada del Edge se genera y se queda en el
// Edge; la DEK y el store cifrado NUNCA llegan a la nube.
//
// El código de activación se compara TAL CUAL: este paquete no lo normaliza
// (ni espacios, ni mayúsculas, ni rechazo del vacío). Un código con un espacio
// de más es, sencillamente, un código desconocido.
package enroll
