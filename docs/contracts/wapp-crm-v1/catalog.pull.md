# `catalog.pull` — wApp ← puente

**Dirección:** wApp pide, el puente responde con el catálogo del tenant.
**Schema:** [`catalog.pull.schema.json`](./catalog.pull.schema.json) · **Ejemplo:**
[`examples/catalog.pull.json`](./examples/catalog.pull.json)

---

> # ⛔ IMPLEMENTACIÓN DIFERIDA
>
> **La forma está publicada; el código NO existe.**
> **Nada** en el Plan 042 implementa este verbo: wApp **no llama** a ningún puente para pedir
> catálogo, ni hoy ni al cerrar este plan. El catálogo que sirve el bot sigue saliendo del blob que
> el tenant carga en wApp.
>
> Está aquí porque **un contrato nace completo**: publicar la forma ahora impide que el día que se
> implemente aparezca inventada de nuevo, distinta y ya con puentes escritos encima. Un puente
> puede prepararla; no puede probarla contra wApp todavía.
>
> Configurar `catalog_adapter = 'http'` en la API de integraciones **se rechaza con error explícito
> («catalog.pull diferido»)**. El valor existe en el modelo de datos para nombrar el futuro, no
> para activarlo.

---

## Forma de la respuesta

Es **la misma forma del blob de catálogo que wApp ya consume**, más `contract_version`:

```json
{
  "contract_version": "1",
  "categories": [
    {
      "code": "empanadas",
      "label": "Empanadas",
      "items": [
        { "code": "emp-que", "sku": "EMP-QUE", "label": "Empanada de queso", "price": 10.5, "description": "Horneada, masa de trigo" },
        { "code": "emp-pol", "sku": "EMP-POL", "label": "Empanada de pollo", "price": 11 }
      ]
    }
  ]
}
```

| Campo | Tipo | Oblig. | Semántica |
|---|---|:--:|---|
| `contract_version` | `string` (const `"1"`) | sí | Versión del contrato. |
| `categories` | `array` | sí | Categorías, **en el orden en que se ofrecerán** al cliente. |
| `categories[].code` | `string` | sí | Código estable de la categoría. |
| `categories[].label` | `string` | sí | Etiqueta visible. |
| `categories[].items` | `array` | sí | Artículos de la categoría, en orden de presentación. |
| `…items[].code` | `string` | sí | Código estable del artículo. |
| `…items[].sku` | `string` | sí | SKU. Es el valor que volverá en `items[].sku` de cada `intake.push`. |
| `…items[].label` | `string` | sí | Etiqueta visible. |
| `…items[].price` | `number` ≥ 0 | sí | Precio unitario. **Sin moneda tipada** (INV-09): un monto es un número. |
| `…items[].description` | `string` | **no** | Descripción. Opcional. |

El schema admite **propiedades adicionales** dentro de categorías y artículos: el catálogo de wApp
tiene extensiones (subcategorías, etiquetas, atributos, variantes, componentes) que un puente puede
entregar y que este documento no congela. Lo que sí está congelado es el núcleo de la tabla.

**El puente entrega el catálogo YA en formato wApp.** Traducir desde el modelo del CRM —familias,
líneas de artículo, listas de precios, unidades de medida— es trabajo del puente (INV-08,
ADR-0032). wApp no va a crecer conceptos de catálogo de nadie.

## Condiciones para cuando se implemente (D-042.3)

Estas tres no son recomendaciones: son el precio de entrada. Sin ellas, este verbo **no se
implementa**.

1. **Caché + TTL obligatorios, sirviendo siempre el último catálogo bueno.**
   Un ERP caído **no puede colgar una conversación de WhatsApp**. Si el fetch falla, el bot sigue
   vendiendo con el último catálogo conocido; si nunca hubo uno, se degrada con un mensaje, no con
   un silencio.
2. **Timeout corto de fetch.** El presupuesto de latencia de una conversación es de segundos, no
   de la paciencia del ERP.
3. **Refresh FUERA del camino del mensaje.** El catálogo se refresca en segundo plano —igual que la
   entrega de webhooks va por outbox y worker—, jamás en línea con el mensaje que un cliente acaba
   de mandar. Es la misma regla sagrada de la ida (INV-02) aplicada a la vuelta del contenido.

Implementarlo significa además escribir el adaptador `http` del puerto de contenido del motor de
flujos, que hoy **está documentado pero no existe** y sería el primer cliente HTTP saliente de esa
ruta.

## Lo que NO está en v1

- **`catalog.push`** (wApp empujando su catálogo hacia el CRM): no existe. Se evaluará con demanda
  real.
- **Stock / disponibilidad**: fuera. wApp no modela inventario.
- **Listas de precios por cliente, promociones, escalados**: fuera. Si el CRM las tiene, el puente
  entrega el catálogo ya resuelto.
