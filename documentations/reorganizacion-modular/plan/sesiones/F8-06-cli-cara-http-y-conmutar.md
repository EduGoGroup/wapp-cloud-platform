# F8-06 · F8 · la cara HTTP y conmutar · 💻 CLI

| | |
|---|---|
| Fase · bloque | [F8 · conversacion](../F8-conversacion/README.md) · 6 · la cara HTTP y conmutar |
| Entorno | 💻 solo local |
| Nivel (E-12) | medio (`admin`, `apipublica`); el re-cableado del arranque, complejo (identidad de instancias) |
| Duración objetivo | 45–90 min |
| Tareas | T8.13, T8.29–T8.35 |
| Depende de | F8-05 |
| Decisiones | D-F8-2 (`admin` sin `Register`), D-F8-5, D-F3-2 |
| Se para cuando | `admin` y los handlers I1–I19 verdes; un commit `conmutar(conversacion)`; huella igual (`go test -run Huella ./internal/arranque/` rc=0); `ls internal/arranque/bridge_*.go` vacío; lista de puentes (import) vacía; `Conmutados` completo; `go list -deps ./cmd/server-modular` sin paquetes viejos; `make ci-local` rc=0 con 0 SKIP; `dev` empujado |

## Antes de pegar el prompt (Jhoan)

- [ ] La sesión anterior (F8-05) está integrada y empujada en `dev`.
- [ ] Decisiones rellenas en [`../DECISIONES.md`](../DECISIONES.md): D-F8-2 (`admin` sin `Register`), D-F8-5, D-F3-2.
- [ ] `go1.26.5` y `golangci-lint v2.12.2` disponibles (`make toolchain`).
- [ ] Arrancar: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude`.

## Prompt

```text
Sesión F8-06 del plan de reconstrucción modular de wapp-cloud-platform · 💻 CLI.

Antes de nada, lee ENTERO y sigue al pie de la letra el protocolo:
documentations/reorganizacion-modular/plan/sesiones/PROTOCOLO-CLI.md

Tu encargo (y solo este):
- Fase: F8 · conversacion → documentations/reorganizacion-modular/plan/F8-conversacion/
- Bloque: 6 · la cara HTTP y conmutar
- Tareas: T8.13 y T8.29–T8.35 (T8.29 = TX.22–TX.23 y T8.34 = TX.24 de plan/FX-cara-http/tareas.md) de plan/F8-conversacion/tareas.md
- Te paras cuando: `admin` y los handlers I1–I19 están verdes; hay un commit `conmutar(conversacion)`; la huella es igual (`go test -run Huella ./internal/arranque/` rc=0); `ls internal/arranque/bridge_*.go` no devuelve nada; la lista de puentes (import) está vacía; `Conmutados` está completo; `go list -deps ./cmd/server-modular` no lista paquetes viejos; `make ci-local` rc=0 con 0 SKIP; `dev` empujado.
- Decisiones: D-F8-2 (`admin` sin `Register`), D-F8-5, D-F3-2 en plan/DECISIONES.md. Si falta alguna, PARA y dilo.
- Skills: reconstruir-modulo, contrato-tdd, validar-antes-de-cerrar.

No hay rama web que integrar ni traspaso que cerrar: trabajas sobre `dev`.
T8.31–T8.34 no compilan por separado: van en UN commit `conmutar(conversacion)`, tras el ensayo en seco de T8.30.
F8 no crea adaptadores: retira TODOS los `bridge_*.go` vivos y las dos segundas instancias viejas (T8.32). Con cada muerte, su módulo dueño entra en `Conmutados`; al acabar, cero adaptadores y `Conmutados` completo. Los «puentes» de `05` §4.1 son imports: esa lista también queda vacía.
El test de cableado afirma que el arranque construye lo NUEVO y que ninguna fase importa un paquete viejo (grep por ruta de import). Un solo `runtime.New`, un solo `entResolver`, un solo `kp`, un solo `gw`.

Nivel de ceremonia: el del inventario aprobado (`05` E-12). Sin umbral de cobertura: un test por promesa del contrato; mutantes en lo complejo.

Al terminar, las tres cosas: tareas [x] con SHA, bloque en ESTADO.md, hallazgos en el README de la fase. `git push origin dev` (rc sin pipe). No toques `main`.
No empieces la sesión siguiente.
```

## Al terminar debe existir

- En [`../F8-conversacion/tareas.md`](../F8-conversacion/tareas.md): T8.13, T8.29–T8.35 `[x]` con SHA.
- Un bloque de la sesión en `ESTADO.md` de la reorganización.
- Los hallazgos nuevos en el [README de la fase](../F8-conversacion/README.md).
- El commit `conmutar(conversacion)` en `dev`.
- `dev` empujado (`git push origin dev`, rc sin pipe).

## Si algo sale mal

- Una dependencia sin `[x]`, una decisión vacía o una contradicción con la spec, `05` o un ADR: la sesión **para y pregunta**; no se esquiva.
- Una limitación del entorno (lint distinto, sin Docker, red): se **anota** en `ESTADO.md`, no se adapta el proyecto a ella.
- Si se corta a medias: lo commiteado y empujado es la verdad; se relanza **la misma sesión** con el mismo prompt, y la verdad de campo del protocolo dirá por dónde seguir. Solo entonces se escribe un traspaso.
- Si el bloque no cabe en ~90 min: para en un punto limpio, cierra con las tres cosas y se relanza.
