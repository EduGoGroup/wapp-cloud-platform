# `wapp-crm-v1` — contrato de integración con puentes CRM/POS

> **Congelado.** Esta carpeta es la fuente de verdad de la frontera entre wApp y cualquier
> puente CRM/POS. El schema vive junto al código que lo emite (`cloud/wapp-cloud-platform`) a
> propósito: una sola fuente de verdad, y un test de CI que impide que el contrato y el código
> diverjan en silencio (REQ-07).

**Audiencia:** quien escribe un **puente** — la pieza que traduce entre este contrato y un CRM,
ERP o POS concreto. El puente **no es parte de wApp** (ADR-0032): tiene su propio ciclo de vida y
su propio repositorio, aunque lo escribamos nosotros.

---

## 1 · Los tres verbos

| Verbo | Dirección | Estado en v1 | Documento |
|---|---|---|---|
| `intake.push` | wApp → puente | **Implementado** (webhook firmado, outbox durable) | [`intake.push.md`](./intake.push.md) |
| `intake.status` | puente → wApp | **Implementado** (`POST /api/v1/integrations/callback`) | [`intake.status.md`](./intake.status.md) |
| `catalog.pull` | wApp ← puente | **Documentado, implementación DIFERIDA** | [`catalog.pull.md`](./catalog.pull.md) |

Y nada más. Clientes, inventario, facturación, contabilidad, líneas de artículo, lotes, almacenes:
**fuera de v1**, explícitamente.

## 2 · Principios (léelos antes de escribir una línea de puente)

1. **La adaptación vive en el puente.** *Si el CRM necesita algo que wApp no modela, se resuelve
   EN el puente.* wApp es un punto de venta básico para nano empresas —producto, combo, categoría—
   y va a seguir siéndolo. No aceptamos campos, mapeos ni conceptos de ningún CRM concreto dentro
   de wApp (ADR-0032, INV-08).
2. **El contacto viaja OPACO.** `contact` es un identificador estable e inescrutable, nunca un
   teléfono ni un JID (ADR-0017, INV-01). Sirve para correlacionar entre pushes; no para llamar a
   nadie. Trátalo como una cadena opaca: no lo parsees.
3. **Sin moneda tipada.** No existe —ni existirá— un campo `currency` en ningún nivel de ningún
   schema (INV-09). Un monto es un número. Si tu CRM exige símbolo, tasa o código ISO, el tenant lo
   pone en sus **variables** y tú lo lees de `variables{}`.
4. **wApp no interpreta `variables{}`.** Es un diccionario del tenant que viaja tal cual. Sin lista
   blanca de claves, sin tipos, sin semántica.
5. **Pedido y presupuesto son EL MISMO objeto** (ADR-0031): la *solicitud* (`intake`). Por eso hay
   un solo verbo de empuje. Que en tu CRM eso entre como cotización, orden o factura lo decides tú.

## 3 · Política de versionado

- Todo payload lleva `contract_version: "1"` (cadena, no número).
- **`wapp-crm-v1` no muta.** Cualquier cambio de *forma* —un campo nuevo, un campo que desaparece,
  un tipo que cambia, un valor nuevo en un enum— es **`wapp-crm-v2`**, publicado en su propia
  carpeta y con **convivencia explícita**: v1 se sigue emitiendo hasta que los puentes migren, y el
  fin de vida de v1 se anuncia con fecha (INV-07).
- Los schemas declaran `additionalProperties: false` en la raíz de `intake.push` y de
  `intake.status`: no hay «campos extra tolerados» que después se conviertan en contrato de facto.
- Un consumidor **puede ignorar** campos que no le sirvan (`customer_note`, `customization`,
  `variables`, `buyer_data`): ignorar no es incumplir.

## 4 · Transporte y firma (D-042.5)

wApp entrega por **HTTP POST** al `endpoint_url` que el tenant configura, con `Content-Type:
application/json`.

**Cadena canónica** (exactamente esto, sin espacios ni saltos añadidos):

```
v1:<X-Wapp-Timestamp>:<cuerpo CRUDO tal como viaja en el POST>
```

**Firma:** `HMAC-SHA256(secreto_del_tenant, cadena_canónica)`, en hexadecimal minúscula.

**Headers salientes:**

| Header | Ejemplo | Para qué |
|---|---|---|
| `X-Wapp-Signature` | `v1=9f86d081884c7d65…` | Firma. El prefijo `v1=` identifica el esquema de firma, **no** la versión del contrato. |
| `X-Wapp-Timestamp` | `1786139112` | Unix **segundos**. Entra en la cadena canónica. |
| `X-Wapp-Delivery` | `48127` | Identificador único de ESTA entrega (fila del outbox). Sirve para deduplicar. |

**Lo que el puente DEBE hacer al recibir:**

1. Leer el **cuerpo crudo** (bytes tal cual). No re-serialices el JSON antes de firmar: un
   `json.Marshal` de ida y vuelta reordena claves y cambia el HMAC.
2. Rechazar si `|now - X-Wapp-Timestamp| > 300 s` (ventana anti-replay).
3. Recalcular el HMAC y compararlo en **tiempo constante** (`hmac.Equal`, `crypto.timingSafeEqual`,
   equivalente). Nunca con `==` sobre cadenas.
4. Responder **2xx** cuando el mensaje quede aceptado. Cualquier otra cosa se considera fallo y
   wApp reintenta.

El **callback de vuelta usa exactamente el mismo esquema y el mismo secreto**: una sola credencial
por tenant en v1. El puente firma igual que verifica.

## 5 · Entrega: at-least-once y dedupe

- wApp **encola** cada push en un outbox durable y lo entrega desde un worker en proceso. **Jamás**
  se hace un POST en línea con el mensaje de WhatsApp (INV-02): una conversación no se cuelga
  porque un CRM esté caído.
- Reintentos con backoff exponencial y jitter; agotado el tope, la entrega queda marcada `dead` y
  visible en logs y métricas.
- **Semántica at-least-once:** un mismo push puede llegarte **más de una vez** (un 2xx que se
  perdió en la red, un reinicio a mitad de entrega). **El receptor debe ser idempotente.**
  - Deduplica por **`X-Wapp-Delivery`**: si ya procesaste ese identificador, responde 2xx y no
    hagas nada más.
  - A nivel de negocio, la clave de idempotencia es **`intake_id` + `revision_no`**: dos entregas
    con el mismo par describen el mismo estado de la misma solicitud.
- Al revés también: la aplicación de un mismo `intake.status` repetido es un **no-op** en wApp
  (REQ-11).

## 6 · Ficheros de esta carpeta

```
README.md                   ← este documento
intake.push.md              ← verbo de salida, campo a campo
intake.status.md            ← verbo de vuelta, estados canónicos y códigos de respuesta
catalog.pull.md             ← verbo DIFERIDO: forma publicada, sin implementación
intake.push.schema.json     ← JSON Schema draft 2020-12
intake.status.schema.json
catalog.pull.schema.json
examples/                   ← un ejemplo válido por verbo (verificados contra su schema)
```

Los `$id` de los schemas son URNs estables (`urn:wapp:contracts:wapp-crm-v1:<verbo>`): identifican
el schema sin prometer una URL que alguien tenga que mantener viva.

## 7 · Punto abierto — RESUELTO

> ### ✅ MD-042.1 — resuelto 2026-08-07 (decisión de Jhoan)
>
> **`event_history_id` entra en el contrato como campo informativo opcional.** El **ADR-0029 §E-3**
> (`docs/adr/0029-eventos-conversacionales-con-id-y-menu-despachador.md`) lista «el contrato CRM»
> entre los públicos del ID legible de evento (`<tipo>-YYYY-MM-DD-HHMM») — con esta decisión, el ADR
> queda correcto tal como está, sin necesidad de corregirlo.
>
> `intake.push` emite `event_history_id` (string, opcional, ausente si la solicitud no tiene evento
> conversacional asociado) con el aviso explícito **«no es único, no lo uses como clave»** (INV-16
> del Plan 043): es soporte humano («el carrito de las 13:45»), jamás identificador ni clave de
> dedupe — la clave de correlación sigue siendo `intake_id` + `revision_no`.
>
> Con esto, **`wapp-crm-v1` queda congelado del todo**: no quedan puntos abiertos. Cualquier cambio
> de forma futuro es `wapp-crm-v2` (INV-07).

## 8 · Lo que este contrato NO hace

- No expone clientes, inventario, facturación ni contabilidad.
- No define adaptadores para ningún CRM concreto: eso vive en el puente, fuera de wApp.
- No emite PII del canal (teléfono, JID, nombre de perfil). Ver `intake.push.md` §Campos
  reservados.
- No entrega catálogo todavía: ver `catalog.pull.md`.
