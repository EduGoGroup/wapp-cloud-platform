# `intake.push` — wApp → puente

**Dirección:** wApp entrega, el puente recibe.
**Transporte:** `POST` firmado al `endpoint_url` del tenant (firma, headers y ventana anti-replay
en el [README](./README.md) §4).
**Schema:** [`intake.push.schema.json`](./intake.push.schema.json) · **Ejemplo:**
[`examples/intake.push.json`](./examples/intake.push.json)

Es el **único verbo de empuje**. Pedido y presupuesto son el mismo objeto —la *solicitud*
(`intake`, ADR-0031)—, y el que decide cómo entra al CRM (cotización, orden, factura) es el puente.

---

## Ejemplo completo

```json
{
  "contract_version": "1",
  "verb": "intake.push",
  "tenant": "acme-panaderia",
  "contact": "c_7f3a9c41d8e24b6fa0517e93b2c4d5e6",
  "intake_id": "3f2a1c9e-5b7d-4a10-9c3e-8d16b4f2a077",
  "lifecycle_status": "confirmed",
  "revision_no": 1,
  "variables": {
    "moneda": "Bs",
    "tasa_dia": "36,50",
    "pie_de_nota": "Gracias por su compra"
  },
  "buyer_data": {
    "documento": "12.345.678-5",
    "direccion_entrega": "Av. Libertador 742, piso 3"
  },
  "customer_note": "dejarlo en portería",
  "items": [
    { "sku": "EMP-QUE",      "label": "Empanada de queso",   "customization": "sin cebolla", "qty": 2, "unit_price": 10.5 },
    { "sku": "BEB-COLA-350", "label": "Refresco cola 350 ml", "customization": "",            "qty": 1, "unit_price": 3 }
  ],
  "total": 24,
  "timestamp": "2026-08-07T13:45:12Z",
  "event_history_id": "evt-2026-08-07-1345"
}
```

## Campos

| Campo | Tipo | Oblig. | Semántica |
|---|---|:--:|---|
| `contract_version` | `string` (const `"1"`) | sí | Versión del contrato. Siempre la **cadena** `"1"`. |
| `verb` | `string` (const `"intake.push"`) | sí | Verbo. Permite multiplexar un solo endpoint receptor. |
| `tenant` | `string` | sí | Tenant emisor. Informativo para el puente: la identidad real de la conexión es el secreto con el que va firmada la entrega. |
| `contact` | `string` | sí | Identificador **OPACO** y estable del contacto. Correlaciona solicitudes del mismo cliente entre pushes. **Jamás** un teléfono ni un JID. No lo parsees: hoy es un identificador sin estructura pública y su forma no es parte del contrato. |
| `intake_id` | `string` (uuid) | sí | Identificador de la solicitud. **Es la clave de correlación del contrato**: la vuelta `intake.status` clavea por él. |
| `lifecycle_status` | `string` (enum) | sí | Punto del ciclo de vida **de wApp** en que está la solicitud. Ver tabla abajo. |
| `revision_no` | `integer` ≥ 1 | sí | Revisión de la negociación. Ver nota abajo. |
| `variables` | `object` | sí | Snapshot literal de las variables del tenant al momento del push. Ver nota abajo. Puede ser `{}`. |
| `buyer_data` | `object` de `string` | sí | Datos que el **comprador entregó voluntariamente** en el flujo (el checklist que configuró el tenant: documento, dirección de entrega, referencia…). Las claves las define el tenant; wApp **no valida su semántica** —no sabe si `12.345.678-5` es un RUT válido—, solo sanea el texto. Puede ser `{}` si el tenant no configuró checklist. |
| `customer_note` | `string` (≤ 280) | sí | Indicación del cliente final para **todo el pedido**. Ver nota abajo. Puede ser `""`. |
| `items` | `array` | sí | Líneas de la solicitud. |
| `items[].sku` | `string` | sí | SKU del artículo tal como lo publica el catálogo del tenant. |
| `items[].label` | `string` | sí | Etiqueta del artículo **congelada al agregar la línea**: un cambio posterior de catálogo no reescribe un pedido ya tomado. |
| `items[].customization` | `string` (≤ 280) | sí | Personalización **no facturable** de la línea. Ver nota abajo. Puede ser `""`. |
| `items[].qty` | `integer` ≥ 1 | sí | Unidades de la línea. |
| `items[].unit_price` | `number` ≥ 0 | sí | Precio unitario congelado al agregar la línea. **Sin moneda tipada.** |
| `total` | `number` ≥ 0 | sí | Total de la solicitud. **Sin moneda tipada.** |
| `timestamp` | `string` (RFC3339) | sí | Instante UTC en que wApp construyó este push. **No** es la hora de entrega: una entrega reintentada conserva el `timestamp` original. |
| `event_history_id` | `string` | no | Resuelto MD-042.1 (2026-08-07). Campo **informativo**, soporte humano únicamente. **No es único, no lo uses como clave.** Ausente si la solicitud no tiene evento conversacional asociado. |

Todos los campos de la tabla son **obligatorios y siempre presentes**, también cuando están
vacíos. Es deliberado: el receptor no debería tener que distinguir «el cliente no pidió nada
especial» de «esta plataforma no me lo cuenta» para decidir si imprime la comanda entera.

El schema declara `additionalProperties: false` en la raíz y en cada línea: un payload con un campo
extra **no es** un `intake.push` válido.

## `lifecycle_status` — el ciclo de vida de wApp

| Clave | Significado | Notas |
|---|---|---|
| `open` | Carrito en curso. | |
| `pending_approval` | Presupuesto esperando al dueño. | |
| `needs_info` | El dueño pidió datos; vuelve a `pending_approval`. | |
| `confirmed` | Pedido aceptado. | |
| `deposit_requested` | Seña solicitada. | |
| `deposit_paid` | Seña recibida. | |
| `settled` | Pagado completo. | terminal |
| `cancelled` | Alguien se pronunció sobre la solicitud y la canceló. | terminal |
| `rejected` | **El dueño rechaza el presupuesto.** | terminal |
| `abandoned` | La conversación que la sostenía se canceló, o el dueño descartó el pedido huérfano. | terminal |
| `expired` | **Terminal LEGADO.** | Ver aviso |

> **`expired` es legado.** Existen filas históricas con ese estado y por eso la clave puede
> emitirse, pero **nadie entra ya en él**: en wApp nada vence por tiempo. No construyas lógica
> nueva alrededor suyo; trátalo como un terminal más.

> **El contrato JAMÁS emite `closed`.** `closed` es la clave **legada** con la que el módulo de
> carrito cierra una solicitud y que sigue viva en la base de datos; se **normaliza a `confirmed`
> al leer**, en un único punto del sistema. Si tu puente ve `closed` en un `intake.push`, es un
> bug de wApp: repórtalo, no lo mapees.

> **Cuidado con `rejected`.** Aparece también en el vocabulario de `intake.status`, y **significa
> otra cosa**. Aquí es «el dueño del negocio rechazó el presupuesto»; allí es lo que tu puente haya
> decidido mapear desde su CRM. Son dos vocabularios **disjuntos** que comparten una palabra.

## `revision_no`

Número de revisión de la negociación sobre la misma solicitud. **Hoy wApp emite siempre `1`**:
solo existe la revisión que produce el carrito. El Plan 044 añadirá revisiones (lo que el pipeline
LLM interpretó, la corrección del dueño, la versión aprobada), y entonces un mismo `intake_id`
podrá empujarse varias veces con `revision_no` creciente.

**Consecuencia práctica para el puente:** la clave de idempotencia de negocio es
**`intake_id` + `revision_no`**. Dos entregas con el mismo par describen el mismo estado; una
entrega con `revision_no` mayor **sustituye** a la anterior sobre la misma solicitud.

## `variables{}` — snapshot sin interpretación

`variables` es una copia literal de las variables de empresa del tenant **en el instante de la
entrega** (decisión 2026-08-07: el builder lee `tenant_variables` dentro del worker que arma la
entrega, nunca en línea con el mensaje de WhatsApp — INV-02). Esto es **distinto** de `timestamp`,
que sí es fijo desde el push y no se mueve en un reintento: si las variables del tenant cambiaron
entre el primer intento y un reintento, `variables{}` puede diferir aunque `timestamp` sea el
mismo. wApp **no interpreta claves ni valores**: no hay lista blanca, no hay tipos, no hay
significado reservado. Si el tenant define `moneda = Bs` y `tasa_dia = 36,50`, eso es exactamente
lo que llega — incluida la coma decimal, porque es texto del tenant, no un número de wApp.

Es el mecanismo previsto para todo lo que un CRM exige y wApp no modela: símbolo de moneda, tasa
del día, código de sucursal, condición de pago. **No hay campo `currency` y no lo habrá**
(INV-09): un monto es un monto.

Un puente que necesite refrescar variables **entre** pushes puede consultarlas por API
(`GET /api/v1/tenant-variables`), con el token del tenant.

## Las dos indicaciones del cliente final

`customer_note` (para todo el pedido: «dejarlo en portería») y `items[].customization` (para una
línea: «sin cebolla») son **texto libre en claro**, saneado y acotado en origen (280 caracteres,
sin controles ni saltos de línea).

- **Nunca suman al `total`** ni alteran `unit_price`. No son un cargo: son una instrucción.
- **Viajan porque quien prepara el producto no es quien recibió el pedido.** Si el «sin cebolla» no
  cruza esta frontera, el cliente recibe el producto mal hecho.
- Ambas admiten `""`, y un consumidor que las ignore sigue siendo un consumidor válido del
  contrato.
- Son instrucción de producción/entrega, **no identidad**: por eso van en claro y no cifradas.

## Campos RESERVADOS — documentados, **NO emitidos**

| Campo | Estado |
|---|---|
| `contact_phone` | **RESERVADO. wApp v1 no lo emite.** |
| `contact_name` | **RESERVADO. wApp v1 no lo emite.** |

Están aquí para que nadie los invente con otro nombre el día que se decidan, y **no existen como
propiedades válidas en el schema**: un payload que los incluya es inválido hoy.

> **Decisión de privacidad explícita.** El contacto viaja **opaco** por diseño (ADR-0017): el
> puente correlaciona sin conocer el número. Emitir teléfono o nombre del cliente final es una
> decisión de privacidad del proyecto que **no está tomada**, y que además exigiría consentimiento
> del tenant bajo su responsabilidad. Mientras no exista esa decisión —y su implementación— estos
> dos campos no se emiten. Si tu CRM los exige, la respuesta correcta hoy es que el tenant capture
> el dato en su checklist de comprador, y entonces llega por `buyer_data` porque **el cliente lo
> entregó voluntariamente**, que es un hecho distinto.

## Qué NO lleva este verbo

- **`currency`**: no existe (INV-09).
- **Impuestos, descuentos, cargos de envío como líneas**: wApp no los modela en v1. Si tu CRM los
  exige, los compone el **puente** (INV-08).
- **Datos del canal**: número, JID, nombre de perfil, foto. Nada de eso sale de wApp.
