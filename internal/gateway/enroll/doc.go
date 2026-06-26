// Package enroll implementa el enrolamiento por tenant del Gateway CloudLink
// (Plan 005 · T3): valida el CSR del Edge, consume un código de activación de un
// solo uso y firma un certificado de Edge con la CA del entorno.
//
// Es copia-adaptación del paquete enroll de wapp-cloudlink (referencia), llevado
// al namespace de la Plataforma Cloud y extendido con persistencia PostgreSQL:
//   - CA firmante de CSRs (cert hoja con EKU ClientAuth, Organization=tenant).
//   - CodeStore de códigos de un solo uso: MemoryStore (tests CI-safe) y
//     PostgresCodeStore (consumo atómico en BD).
//   - EdgeCertRepository: persiste los metadatos del cert emitido.
//   - Service: orquesta verificar CSR → consumir código → firmar → persistir.
//   - Server: implementa cloudlinkv1.EnrollmentServer (EnrollEdge), mapeando los
//     errores de dominio a códigos gRPC.
//
// Frontera zero-knowledge (ADR-0009): por aquí solo viaja material PÚBLICO (CSR,
// cert hoja, cadena de CA). La clave privada del Edge se genera y se queda en el
// Edge; la DEK y el store cifrado NUNCA llegan a la nube.
package enroll
