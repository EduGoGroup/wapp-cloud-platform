# FW-03 · FW · C · la conversación con Edge real · 💻 CLI

| | |
|---|---|
| Fase · bloque | [FW · WhatsApp enchufable](../FW-whatsapp-enchufable/README.md) · C · la conversación con Edge real |
| Entorno | 💻 local; necesita **Docker** y el repo del Edge al lado |
| Nivel (E-12) | No aplica: FW no reconstruye un módulo de este repo; el código vive en `edge/wapp-edge-agent` |
| Duración objetivo | 45–90 min |
| Tareas | TW.7–TW.8 |
| Depende de | FW-02 integrada en el `dev` del Edge |
| Decisiones | D-FW-1 … D-FW-6 ([`../DECISIONES.md`](../DECISIONES.md) §8) |
| Se para cuando | el comando del carrito con Edge real da `RC=0`, 0 SKIP, 0 FAIL y `make test-procesos` conserva su `PASS`. |

## Antes de pegar el prompt (Jhoan)

- [ ] La dependencia de arriba cerró con sus tres cosas y su PR está integrado.
- [ ] Las decisiones que esta sesión necesita están rellenas en [`../DECISIONES.md`](../DECISIONES.md) §8.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión FW-03 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI · fase FW (WhatsApp enchufable).

Antes de nada, lee ENTERO y sigue el protocolo:
/Volumes/Projects/source/wApp/cloud/wapp-cloud-platform/documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md
Y la spec de la fase, entera (es corta): …/plan/FW-whatsapp-enchufable/ (README, requisitos, arquitectura, diseno, reglas, tareas).
Si vas a tocar o leer código del Edge, carga ANTES documentations/constitucion.md de edge/wapp-edge-agent: mandan sus reglas, no `05`.
Fuera de este repo, lee el ADR-0041 (documentations/adrs/ de la raíz de wApp) con su tabla de alternativas descartadas.

Tu encargo (y solo este):
- Bloque: C · la conversación con Edge real
- Tareas: TW.7–TW.8 de plan/FW-whatsapp-enchufable/tareas.md
- Te paras cuando: el comando del carrito con Edge real da `RC=0`, 0 SKIP, 0 FAIL y `make test-procesos` conserva su `PASS`.
- Decisiones: si falta una de las que tu bloque necesita, PARA y dilo.
- Skills: procesos-testcontainers, validar-antes-de-cerrar. El Edge falso del arnés NO se retira. El caso afirma qué binario del Edge corre antes de afirmar nada más.
Innegociable: la doble llave (la DEK no se mueve; el lease lo emite y revoca el Cloud, también con WhatsApp falso), sin Redis ni broker en el Edge, ni un campo nuevo en el proto de CloudLink, ni un secreto en la documentación.
Cada repo, su rama desde `dev` y su PR sin squash; nunca directo a `dev` ni a `main`. Gates locales, rc leído sin pipe, SKIP contados.
Al terminar, las tres cosas: tareas [x] con SHA (y de qué repo), bloque en ESTADO.md, hallazgos en el README de la fase.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../FW-whatsapp-enchufable/tareas.md`](../FW-whatsapp-enchufable/tareas.md): TW.7–TW.8 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../FW-whatsapp-enchufable/README.md).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec o con un ADR: la sesión **para y pregunta**.
- Una limitación del entorno (sin Docker, sin el repo del Edge al lado): se **anota**, no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
