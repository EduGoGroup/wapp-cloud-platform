# FW-04 · FW · D · cierre y receta de UAT · 💻 CLI

| | |
|---|---|
| Fase · bloque | [FW · WhatsApp enchufable](../FW-whatsapp-enchufable/README.md) · D · cierre y receta de UAT |
| Entorno | 💻 local; necesita Docker, el repo del Edge y la raíz de wApp |
| Nivel (E-12) | No aplica: FW no reconstruye un módulo de este repo; el código vive en `edge/wapp-edge-agent` |
| Duración objetivo | 45–90 min |
| Tareas | TW.9–TW.10 |
| Depende de | FW-03 |
| Decisiones | D-FW-1 … D-FW-6 ([`../DECISIONES.md`](../DECISIONES.md) §8) |
| Se para cuando | los 8 puntos de `reglas.md` §4 tienen evidencia y D-38 está cerrada. |

## Antes de pegar el prompt (Jhoan)

- [ ] La dependencia de arriba cerró con sus tres cosas y su PR está integrado.
- [ ] Las decisiones que esta sesión necesita están rellenas en [`../DECISIONES.md`](../DECISIONES.md) §8.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión FW-04 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI · fase FW (WhatsApp enchufable).

Antes de nada, lee ENTERO y sigue el protocolo:
/Volumes/Projects/source/wApp/cloud/wapp-cloud-platform/documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md
Y la spec de la fase, entera (es corta): …/plan/FW-whatsapp-enchufable/ (README, requisitos, arquitectura, diseno, reglas, tareas).
Si vas a tocar o leer código del Edge, carga ANTES documentations/constitucion.md de edge/wapp-edge-agent: mandan sus reglas, no `05`.
Fuera de este repo, lee el ADR-0041 (documentations/adrs/ de la raíz de wApp) con su tabla de alternativas descartadas.

Tu encargo (y solo este):
- Bloque: D · cierre y receta de UAT
- Tareas: TW.9–TW.10 de plan/FW-whatsapp-enchufable/tareas.md
- Te paras cuando: los 8 puntos de `reglas.md` §4 tienen evidencia y D-38 está cerrada.
- Decisiones: si falta una de las que tu bloque necesita, PARA y dilo.
- Skills: validar-antes-de-cerrar, describir-pr. La receta de UAT se ejecuta en LOCAL; nada contra UAT en esta sesión (eso es F10-03).
Innegociable: la doble llave (la DEK no se mueve; el lease lo emite y revoca el Cloud, también con WhatsApp falso), sin Redis ni broker en el Edge, ni un campo nuevo en el proto de CloudLink, ni un secreto en la documentación.
Cada repo, su rama desde `dev` y su PR sin squash; nunca directo a `dev` ni a `main`. Gates locales, rc leído sin pipe, SKIP contados.
Al terminar, las tres cosas: tareas [x] con SHA (y de qué repo), bloque en ESTADO.md, hallazgos en el README de la fase.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../FW-whatsapp-enchufable/tareas.md`](../FW-whatsapp-enchufable/tareas.md): TW.9–TW.10 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../FW-whatsapp-enchufable/README.md).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec o con un ADR: la sesión **para y pregunta**.
- Una limitación del entorno (sin Docker, sin el repo del Edge al lado): se **anota**, no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
