// Package domain contiene los agregados y reglas de negocio de la Plataforma Cloud.
// Módulos de dominio previstos:
//   - iam/       → Tenant, User, Role, Permission (RBAC multi-tenant; copia edugo-api-identity).
//   - business/  → Contact, Segment, Template, Campaign, CampaignItem.
//   - flows/     → FlowDef, FlowInstance, Node, Transition (motor de flujos, pieza 05).
//   - gateway/   → EdgeRecord, Session, Lease (registro de fleet + emisión de leases).
// Sin dependencias de infraestructura.
package domain
