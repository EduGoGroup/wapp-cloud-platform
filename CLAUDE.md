# CLAUDE.md — wapp-cloud-platform (Piezas 03 y 05)

> Orientado a LLM. Lee esto antes de tocar cualquier archivo.
> Especificación pieza 03: `../../docs/piezas/03-plataforma-cloud.md`
> Especificación pieza 05: `../../docs/piezas/05-motor-flujos-modulos.md`
> CLAUDE.md raíz del ecosistema: `../../CLAUDE.md` (si existe)

---

## Qué es esta pieza

**Monolito modular Go** que aloja todo lo que gestiona el equipo de wApp
(plataforma SaaS). La nube **piensa**; el Edge despacha (ADR-0005).

Cuatro módulos de dominio cohesivos, cada uno con frontera, tablas y API:
1. **IAM** — autenticación (JWT) y autorización (RBAC) multi-tenant.
2. **Negocio** — contactos, segmentos, plantillas/catálogos, campañas.
3. **Motor de Flujos** — máquina de estados conversacional por contacto con módulos enchufables.
4. **Gateway CloudLink** — terminación gRPC de los Edges: streams, mTLS, leases, fleet.

---

## Responsabilidad en wApp

| Qué hace la Plataforma | Qué NO hace |
|---|---|
| Arma el payload completo (destino + contenido + media) | Tocar `whatsmeow` o el socket de WhatsApp |
| Empuja órdenes al Edge por CloudLink | Custodiar la DEK del cliente |
| Recibe eventos entrantes y los procesa (Motor de Flujos) | Depender de RabbitMQ, broker o `edugo-worker` |
| Emite y revoca leases (kill-switch anti-clon) | Guardar el store cifrado del Edge |
| Genera URLs prefirmadas de corta vida (S3/MinIO) | Ejecutar lógica en el Edge |
| Fan-out de campañas con goroutines + channels | Usar fan-out por broker/worker externo |

---

## Arquitectura hexagonal

```
internal/
  domain/
    iam/       → Tenant, User, Role, Permission
    business/  → Contact, Segment, Template, Campaign, CampaignItem
    flows/     → FlowDef, FlowInstance, Node, Transition
    gateway/   → EdgeRecord, Session, Lease
  app/
    iam/       → Register, Login, RefreshToken, CheckPermission
    business/  → UpsertContact, CreateCampaign, DispatchCampaign (fan-out goroutines)
    flows/     → ProcessIncomingEvent, AdvanceFlowStep, RenderNode (ProcessorRegistry)
    gateway/   → EnrollEdge, AcceptStream, EmitLease, RevokeLease
  adapters/
    postgres/  → repositorios relacionales (PG: tenants, contactos, campañas, fleet, leases)
    mongo/     → repositorios documentales (flow_defs, flow_state, flow_results)
    s3/        → almacenamiento de media + URLs prefirmadas (S3/MinIO)
    grpcserver/→ servidor CloudLink bidi-stream con mTLS (ver pieza 02)
    http/      → Gin: rutas Consola/BFF + health-checks
```

---

## Tecnología y decisiones clave (ADRs)

| ADR | Decisión | Impacto en código |
|---|---|---|
| ADR-0004 | Reutilización por copia-adaptación | Copiar IAM/RBAC de `edugo-api-identity`; patterns de `edugo-shared`; no importar como lib |
| ADR-0005 | Edge = despachador; nube arma payload completo | Siempre entregar payload completo al Edge; nunca dejar que el Edge llame endpoints de negocio |
| ADR-0007 | Zero-knowledge: nube emite lease, nunca la DEK | El Gateway emite y revoca leases; la DEK nunca llega aquí |
| ADR-0009 | Datos de negocio en la nube; DEK y store solo en el Edge | PostgreSQL/MongoDB/S3: solo metadatos y contenido de negocio |
| ADR-0010 | Monolito modular; extraer a servicio solo cuando duela | No partir módulos por adelantado; módulos con frontera limpia |
| ADR-0003 | Sin Redis ni broker; fan-out por goroutines | `DispatchCampaign` usa worker pool acotado, no RabbitMQ |

---

## Motor de Flujos (Pieza 05) — resumen

El Motor de Flujos es el módulo `flows/` dentro de este monolito.

**Modelo:** máquina de estados por `(tenant, sesión, contacto)`, definiciones
en MongoDB (`flow_defs`), estado conversacional en MongoDB (`flow_state`).

**Módulos enchufables** (patrón `ProcessorRegistry` de `edugo-worker`, copiado y
adaptado — sin RabbitMQ):

| Módulo | Hace | Estado |
|---|---|---|
| Menú | Lista numerada → rama por elección | Base |
| Encuesta | Secuencia de preguntas, recolecta respuestas | Base |
| Pedido | Carrito sobre catálogo, emite orden | Base |
| PDF/Media | Entrega URL prefirmada al Edge | Base |
| Pago | Cobro y conciliación | Futuro |

Menús y encuestas se simulan en texto numerado si `whatsmeow` no soporta
botones nativos; el módulo decide el render según capacidades reportadas por
la sesión.

---

## Qué reutiliza de EduGo (por copia-adaptación)

| Origen (EduGo) | Qué se copia | Adaptación necesaria |
|---|---|---|
| `edugo-api-identity` | IAM completo: JWT, RBAC, multi-tenant | Adaptar a dominio wApp (tenants = clientes del negocio) |
| `edugo-shared` | Logger, auth middleware, repository base, health, métricas | Adaptar imports; sin dependencia de módulo EduGo |
| `edugo-worker` → `ProcessorRegistry` | Patrón de registro de procesadores intercambiables | Reimplementar sobre concurrencia Go, sin RabbitMQ |

**No** se importa ningún paquete de EduGo como dependencia go.mod.

---

## Datos (tres almacenes)

| Almacén | Qué guarda | Qué NUNCA guarda |
|---|---|---|
| PostgreSQL | Tenants, usuarios/RBAC, contactos, segmentos, plantillas, campañas, fleet de Edges, leases | DEK, store cifrado, llaves Signal |
| MongoDB | `flow_defs` (grafos de nodo), `flow_state` (estado por contacto), `flow_results` | DEK, store cifrado |
| S3/MinIO | PDFs, imágenes, media de plantillas; genera URLs prefirmadas de corta vida | DEK, store cifrado |

---

## Gateway CloudLink en la nube

- Punto de terminación gRPC: acepta conexiones entrantes de los Edges.
- Autentica por mTLS (cert por Edge/tenant) + token de plataforma.
- Mantiene el registro de fleet (`EdgeRecord`, estado online/offline, `last_seen`).
- Emite, renueva y revoca **leases** (kill-switch anti-clon, ADR-0007).
- Multiplexa por `session_id`; un Edge gestiona N sesiones (ADR-0008).

---

## Puntos abiertos (no implementar sin consenso)

- Cadencia de renovación del lease y operación offline con lease cacheado (ADR-0007).
- Corte exacto para extraer módulos a servicio aparte (ADR-0010).
- Fan-out: límite de paralelismo por tenant/Edge para no saturar ni provocar bloqueos.
- La nube no tiene cola durable propia; la durabilidad la da el `outbox` del Edge (ADR-0003).

---

## Referencias

- Pieza 03: `../../docs/piezas/03-plataforma-cloud.md`
- Pieza 05: `../../docs/piezas/05-motor-flujos-modulos.md`
- CloudLink (conducto edge↔cloud): `../../docs/piezas/02-cloudlink.md`
- Edge Agent: `../../docs/piezas/01-edge-agent.md`
- ADRs: `../../docs/adr/`
- CLAUDE.md raíz: `../../CLAUDE.md`
