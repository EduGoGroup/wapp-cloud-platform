// Package app contiene los casos de uso de la Plataforma Cloud.
// Casos de uso previstos por módulo:
//   - iam/       → Register, Login, RefreshToken, CheckPermission.
//   - business/  → UpsertContact, CreateCampaign, DispatchCampaign (fan-out goroutines+channels, sin RabbitMQ).
//   - flows/     → ProcessIncomingEvent, AdvanceFlowStep, RenderNode (ProcessorRegistry adaptado de edugo-worker).
//   - gateway/   → EnrollEdge, AcceptStream, EmitLease, RevokeLease (kill-switch anti-clon).
// Puertos de salida: EdgeStreamPort, StoragePort, ObjectStorePort, NotificationPort.
package app
