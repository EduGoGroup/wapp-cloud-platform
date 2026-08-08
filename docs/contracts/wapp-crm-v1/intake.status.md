# `intake.status` — puente → wApp

**Dirección:** el puente entrega, wApp recibe.
**Endpoint:** `POST /api/v1/integrations/callback`
**Autenticación:** HMAC (mismo esquema y mismo secreto que la ida, [README](./README.md) §4) más
el header `X-Wapp-Tenant: <tenant_id>`. **Sin JWT.**
**Schema:** [`intake.status.schema.json`](./intake.status.schema.json) · **Ejemplo:**
[`examples/intake.status.json`](./examples/intake.status.json)

Es la vuelta: el puente le cuenta a wApp qué pasó con una solicitud **dentro del CRM**.

---

## Ejemplo completo

```json
{
  "contract_version": "1",
  "verb": "intake.status",
  "intake_id": "3f2a1c9e-5b7d-4a10-9c3e-8d16b4f2a077",
  "status": "preparing",
  "external_ref": "OC-2026-004512",
  "occurred_at": "2026-08-07T13:52:03Z"
}
```

Con estos headers:

```
X-Wapp-Tenant:    acme-panaderia
X-Wapp-Timestamp: 1786139523
X-Wapp-Signature: v1=<hex de HMAC-SHA256(secreto, "v1:1786139523:<cuerpo crudo>")>
```

## Campos

| Campo | Tipo | Oblig. | Semántica |
|---|---|:--:|---|
| `contract_version` | `string` (const `"1"`) | sí | Versión del contrato. |
| `verb` | `string` (const `"intake.status"`) | sí | Verbo. |
| `intake_id` | `string` (uuid) | sí | La solicitud, tal como llegó en el `intake.push`. |
| `status` | `string` (enum de 4) | sí | Estado canónico del CRM. Ver abajo. |
| `external_ref` | `string` | **no** | Referencia del CRM (folio, número de orden, lo que sea). **Opaca para wApp**: se guarda y se muestra, jamás se interpreta ni se usa como clave. |
| `occurred_at` | `string` (RFC3339) | sí | Instante en que el hecho ocurrió **en el CRM** — no cuando se envió el callback. |

**El `tenant` NO viaja en el cuerpo.** El tenant efectivo es el del header `X-Wapp-Tenant`
**autenticado por la firma HMAC**. Un campo `tenant` en el cuerpo **no se ignora: se rechaza**. El
schema declara `additionalProperties: false`, así que cualquier propiedad que no esté en la tabla
de arriba —`tenant` incluido— responde **422**. Es deliberado y vale para todo el contrato: un
error de integración tiene que doler en el primer intento, no pasar desapercibido durante semanas
mientras el puente cree que está mandando un campo que wApp nunca leyó.

---

## ⚠️ Los cuatro estados canónicos NO son el ciclo de vida de wApp

> **Este vocabulario es DISJUNTO del de `lifecycle_status`.**
> Los cuatro valores de abajo **no son** estados del ciclo de vida de wApp y **no se mapean sobre
> `intakes.status`**. Un `intake.status` **nunca** cambia el `lifecycle_status` de una solicitud:
> se refleja en **columnas propias** (`crm_status`, `crm_synced_at`, `crm_external_ref`), separadas
> a propósito.
>
> **Por qué.** Los dos vocabularios describen cosas distintas: uno es la negociación tal como wApp
> la conoce (¿aprobado?, ¿señado?, ¿saldado?), el otro es lo que le pasa al pedido dentro de un
> sistema que wApp no conoce. Mapear el segundo sobre el primero obligaría a wApp a **inventar
> semántica de CRM** —decidir que `paid` «es» `settled`, que `delivered` «es» algo— y eso es
> exactamente lo que el contrato prohíbe (INV-08, ADR-0032): la adaptación vive en el puente.

| `status` | Significado en esta frontera |
|---|---|
| `paid` | El CRM considera la solicitud pagada. |
| `preparing` | El CRM la tiene en preparación. |
| `delivered` | El CRM la considera entregada. |
| `rejected` | El CRM la rechazó. |

> **`rejected` existe en AMBOS vocabularios y significa cosas distintas.**
> En `lifecycle_status` (la ida) `rejected` es **«el dueño del negocio rechazó el presupuesto»**: un
> hecho de la negociación en wApp. Aquí, en la vuelta, `rejected` es **lo que el puente haya
> decidido mapear desde su CRM** —pedido anulado, crédito denegado, stock imposible—. **No son el
> mismo hecho** y no se pisan: viven en columnas distintas. Que compartan palabra es una
> coincidencia del castellano técnico, no un puente semántico.

## La regla: cada puente mapea; wApp no conoce el vocabulario de ningún CRM

Tu CRM dirá `FACTURADO`, `EN_RUTA`, `PICKING`, `ANULADO` o lo que sea. **Ese mapeo es tuyo.** wApp
no incorpora tablas de equivalencias por CRM, ni ahora ni nunca: sería el parche-sobre-parche que
ADR-0032 corta de raíz. Si ninguno de los cuatro te sirve para un hecho concreto, la respuesta no
es un quinto estado: es que ese hecho **no cruza** esta frontera en v1.

## La regla: cuando HAY CRM, el CRM manda (ADR-0031)

Mientras el tenant tenga la integración de eventos activa:

- wApp **guarda el reflejo** del estado del CRM y el timestamp del último sync.
- wApp **jamás pisa** un estado reflejado con lógica local: ni un automatismo ni el cambio manual
  del dueño. La consola muestra el estado como **solo-lectura, con origen «CRM»**.
- Sin integración activa, el dueño cambia estados a mano y wApp manda. Es lo contrario, y es
  correcto: no hay dos verdades a la vez, hay **una** y se sabe cuál.

## Idempotencia

Aplicar **el mismo `intake.status` repetido es un no-op**. Reenvía sin miedo: es la contracara de
la semántica at-least-once de la ida. Deduplicamos por el contenido del reflejo, no por un
identificador de entrega, así que no necesitas llevar registro de lo que ya mandaste.

Un `occurred_at` **anterior** al último sync aplicado se considera información vieja y no
retrocede el reflejo.

## Códigos de respuesta

| Código | Cuándo | Qué hacer |
|---|---|---|
| **2xx** | Aceptado y aplicado (o aplicado antes: idempotente). | Nada. |
| **401** | Firma inválida, header ausente, o `X-Wapp-Timestamp` fuera de la ventana de ±300 s. | Revisa el secreto, que firmes el **cuerpo crudo**, y el reloj de tu servidor. |
| **403** | El tenant no tiene la capacidad `crm_bridge` activa. | Es comercial, no técnico: el tenant debe activar la integración. Reintentar no ayuda. |
| **404** | El `intake_id` no existe **o** no pertenece a este tenant. | **Los dos casos responden igual, a propósito**: distinguirlos permitiría sondear identificadores de otros tenants. |
| **422** | El cuerpo no es un `intake.status` válido: `status` fuera de los cuatro canónicos, campo obligatorio ausente, tipo incorrecto. | Corrige el mapeo. Reintentar el mismo cuerpo dará siempre 422. |

Un **5xx** es un fallo de wApp: reintenta con backoff.
