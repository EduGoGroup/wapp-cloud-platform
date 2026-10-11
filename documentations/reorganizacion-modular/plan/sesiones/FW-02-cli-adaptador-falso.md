# FW-02 · FW · B · el adaptador falso · 💻 CLI

| | |
|---|---|
| Fase · bloque | [FW · WhatsApp enchufable](../FW-whatsapp-enchufable/README.md) · B · el adaptador falso |
| Entorno | 💻 local; trabaja en `edge/wapp-edge-agent` |
| Nivel (E-12) | No aplica: FW no reconstruye un módulo de este repo; el código vive en `edge/wapp-edge-agent` |
| Duración objetivo | 45–90 min |
| Tareas | TW.4–TW.6 |
| Depende de | FW-01, con el diseño y el ADR aprobados por Jhoan |
| Decisiones | D-FW-1 … D-FW-6 ([`../DECISIONES.md`](../DECISIONES.md) §8) |
| Se para cuando | se cumplen los puntos 1–4 de `reglas.md` §4 con su evidencia. |

## Antes de pegar el prompt (Jhoan)

- [ ] La dependencia de arriba cerró con sus tres cosas y su PR está integrado.
- [ ] Las decisiones que esta sesión necesita están rellenas en [`../DECISIONES.md`](../DECISIONES.md) §8.
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/edge/wapp-edge-agent && claude`.

## Prompt

```text
Sesión FW-02 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI · fase FW (WhatsApp enchufable).

Antes de nada, lee ENTERO y sigue el protocolo:
/Volumes/Projects/source/wApp/cloud/wapp-cloud-platform/documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md
Y la spec de la fase, entera (es corta): …/plan/FW-whatsapp-enchufable/ (README, requisitos, arquitectura, diseno, reglas, tareas).
Si vas a tocar o leer código del Edge, carga ANTES documentations/constitucion.md de edge/wapp-edge-agent: mandan sus reglas, no `05`.
Fuera de este repo, lee el ADR-0041 (documentations/adrs/ de la raíz de wApp) con su tabla de alternativas descartadas.

Tu encargo (y solo este):
- Bloque: B · el adaptador falso
- Tareas: TW.4–TW.6 de plan/FW-whatsapp-enchufable/tareas.md
- Te paras cuando: se cumplen los puntos 1–4 de `reglas.md` §4 con su evidencia.
- Decisiones: si falta una de las que tu bloque necesita, PARA y dilo.
- Con la palanca baja el Edge tiene que quedar IGUAL que hoy: es lo primero que se prueba y lo último que se vuelve a probar.
Innegociable: la doble llave (la DEK no se mueve; el lease lo emite y revoca el Cloud, también con WhatsApp falso), sin Redis ni broker en el Edge, ni un campo nuevo en el proto de CloudLink, ni un secreto en la documentación.
Cada repo, su rama desde `dev` y su PR sin squash; nunca directo a `dev` ni a `main`. Gates locales, rc leído sin pipe, SKIP contados.
Al terminar, las tres cosas: tareas [x] con SHA (y de qué repo), bloque en ESTADO.md, hallazgos en el README de la fase.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../FW-whatsapp-enchufable/tareas.md`](../FW-whatsapp-enchufable/tareas.md): TW.4–TW.6 `[x]` con SHA (o `[~]` con lo que falta).
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../FW-whatsapp-enchufable/README.md).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec o con un ADR: la sesión **para y pregunta**.
- Una limitación del entorno (sin Docker, sin el repo del Edge al lado): se **anota**, no se adapta el proyecto a ella.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
