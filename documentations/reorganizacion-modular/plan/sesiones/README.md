# Sesiones — el orden de ejecución y el prompt de cada una

> Cada fichero de esta carpeta es **una sesión** de Claude Code: dice en qué entorno se hace, de qué
> depende, dónde se para, y trae el **prompt listo para pegar**. El *cómo* común de todas vive en
> dos protocolos que la propia sesión lee al empezar: [`PROTOCOLO-WEB.md`](PROTOCOLO-WEB.md) y
> [`PROTOCOLO-CLI.md`](PROTOCOLO-CLI.md). Así el prompt es corto y la regla no se duplica.
>
> **81 pasos**: 62 sesiones 🌐 web · 15 💻 CLI · 4 🧑 tuyas (sin prompt).

## Cómo se usa

1. **Una sola vez**: [`00-01`](00-01-jhoan-preparar-entorno-web.md) (el entorno web) y
   [`00-02`](00-02-jhoan-decisiones-iniciales.md) (las decisiones de [`../DECISIONES.md`](../DECISIONES.md) §1, §2 y §4).
2. **Sigue la tabla en orden.** Abre el fichero del paso, cumple su «Antes de pegar el prompt», y
   pega el bloque `Prompt`:
   - 🌐 **web**: claude.ai/code → repo `EduGoGroup/wapp-cloud-platform` → **rama base `dev`** → el
     entorno de 00-01 → pegar. (O desde la terminal: `claude --cloud "<prompt>"`, tras empujar.)
   - 💻 **CLI**: `cd /Volumes/Projects/source/wApp/cloud/wapp-cloud-platform && claude` → pegar.
   - 🧑 **tuyas**: no hay prompt; es una lista de lo que decides tú.
3. **Entre dos sesiones**: la web termina con un **PR hacia `dev`**. Antes de arrancar la siguiente,
   ese PR tiene que estar **integrado sin squash** (en GitHub, «Rebase and merge» o «Create a merge
   commit»; nunca «Squash and merge»). Si el bloque era 🌐→💻, lo integra la sesión 💻 que sigue.
4. **Marca el estado** en la columna de la tabla (`hecha`, con la fecha) cuando la sesión cierre. La
   verdad, de todos modos, son los `[x]` con SHA de cada `tareas.md`.

## Reglas del orden

- **El orden supone D-F9-1 = sí** (F9 adelantado). Si decides que no, los pasos de F9 que están
  antes de F2 (F9-01…F9-05) se saltan en su sitio y F9 entero se hace tras F8 (T9.34); las sesiones
  de cierre de cada módulo ignoran su pasada de procesos.
- **Nada tras F1-05 sin tu decisión** del piloto. Si esa decisión cambia el método, la sesión de
  recalibración de F1-05 ajusta las specs y estas sesiones antes de seguir.
- **En serie, no en paralelo.** Las sesiones de una fase comparten ficheros (`tareas.md`,
  `fronteras_test.go`, el arranque); dos a la vez chocarían. La única excepción razonable: una 🧑
  mientras corre otra.
- **Si una sesión se corta** (límite de uso, VM reciclada): se relanza **la misma** con el mismo
  prompt. Su verdad de campo lee lo commiteado y empujado y sigue desde ahí.
- **Si un bloque resulta demasiado grande** para una sesión, la sesión para en un punto limpio
  (commits empujados, `[~]` en la tarea) y se relanza con el mismo prompt.

## El orden

| Paso | Sesión | | Fase | Bloque | Tareas | Estado |
|---:|---|:-:|---|---|---|---|
| 1 | [`00-01-jhoan-preparar-entorno-web`](00-01-jhoan-preparar-entorno-web.md) | 🧑 | 00 | — | — | pendiente |
| 2 | [`00-02-jhoan-decisiones-iniciales`](00-02-jhoan-decisiones-iniciales.md) | 🧑 | 00 | — | — | hecha 2026-09-30 |
| 3 | [`F0-01-web-entorno`](F0-01-web-entorno.md) | 🌐 | F0 | A | T0.0–T0.1 | hecha 2026-09-30 |
| 4 | [`F0-02-web-pendiente-y-make`](F0-02-web-pendiente-y-make.md) | 🌐 | F0 | B | T0.2–T0.4, T0.26 | hecha 2026-09-30 (PR #14, fusionado en `dev` sin squash) |
| 5 | [`F0-03-web-candados`](F0-03-web-candados.md) | 🌐 | F0 | C | T0.5–T0.9 | hecha 2026-09-30 (PR #15, fusionado en `dev` sin squash) |
| 6 | [`F0-04-web-arranque-y-huella`](F0-04-web-arranque-y-huella.md) | 🌐 | F0 | D | T0.10–T0.15 | hecha 2026-09-30 (PR #16, fusionado en `dev` sin squash) |
| 7 | [`F0-05-web-cara-vacia-platform-deriva`](F0-05-web-cara-vacia-platform-deriva.md) | 🌐 | F0 | E | T0.16–T0.21, T0.27 | hecha 2026-09-30 (PR #17, fusionado en `dev` sin squash) |
| 8 | [`F0-06-cli-cierre`](F0-06-cli-cierre.md) | 💻 | F0 | E/F | T0.16–T0.21, T0.27 + T0.22–T0.25 | hecha 2026-09-30 (F0 cerrada; `dev` empujado) |
| 9 | [`F9-01-web-arnes`](F9-01-web-arnes.md) | 🌐 | F9 | A | T9.1–T9.12 🕐 | pendiente |
| 10 | [`F9-02-cli-arnes-cierre`](F9-02-cli-arnes-cierre.md) | 💻 | F9 | A | T9.1–T9.12 🕐 | pendiente |
| 11 | [`F1-01-web-contratos-y-rojo`](F1-01-web-contratos-y-rojo.md) | 🌐 | F1 | A | T1.1–T1.7 | pendiente |
| 12 | [`F1-02-web-verde`](F1-02-web-verde.md) | 🌐 | F1 | B | T1.8–T1.13 | pendiente |
| 13 | [`F1-03-web-adaptador-y-conmutacion`](F1-03-web-adaptador-y-conmutacion.md) | 🌐 | F1 | C | T1.14–T1.16 | pendiente |
| 14 | [`F1-04-cli-cierre-e-informe`](F1-04-cli-cierre-e-informe.md) | 💻 | F1 | D | T1.17–T1.19 | pendiente |
| 15 | [`F1-05-jhoan-parada`](F1-05-jhoan-parada.md) | 🧑 | F1 | — | — | pendiente |
| 16 | [`F9-03-web-procesos-plataforma-y-acceso`](F9-03-web-procesos-plataforma-y-acceso.md) | 🌐 | F9 | B1 | T9.13–T9.16 🕐 | pendiente |
| 17 | [`F9-04-web-procesos-de-negocio`](F9-04-web-procesos-de-negocio.md) | 🌐 | F9 | B2 | T9.17–T9.21, T9.35 🕐 | pendiente |
| 18 | [`F9-05-cli-procesos-cierre`](F9-05-cli-procesos-cierre.md) | 💻 | F9 | B1/B2 | T9.13–T9.16 🕐 + T9.17–T9.21, T9.35 🕐 | pendiente |
| 19 | [`F2-01-web-inventario-y-rojo-hojas`](F2-01-web-inventario-y-rojo-hojas.md) | 🌐 | F2 | A/B | T2.1 + T2.34 + T2.2–T2.8 | pendiente |
| 20 | [`F2-02-web-rojo-resto`](F2-02-web-rojo-resto.md) | 🌐 | F2 | C | T2.9–T2.16 | pendiente |
| 21 | [`F2-03-web-verde-hojas`](F2-03-web-verde-hojas.md) | 🌐 | F2 | D | T2.17–T2.21 | pendiente |
| 22 | [`F2-04-web-verde-usecase-identity`](F2-04-web-verde-usecase-identity.md) | 🌐 | F2 | E | T2.22–T2.23 | pendiente |
| 23 | [`F2-05-web-verde-postgres-http-platformadmin`](F2-05-web-verde-postgres-http-platformadmin.md) | 🌐 | F2 | F | T2.24–T2.27 | pendiente |
| 24 | [`F2-06-web-conmutar-y-rutas`](F2-06-web-conmutar-y-rutas.md) | 🌐 | F2 | G | T2.28–T2.31 | pendiente |
| 25 | [`F2-07-cli-cierre`](F2-07-cli-cierre.md) | 💻 | F2 | H | T2.32–T2.33 | pendiente |
| 26 | [`F3-01-web-inventario-y-rojo-hojas`](F3-01-web-inventario-y-rojo-hojas.md) | 🌐 | F3 | A/B | T3.1 + T3.2–T3.9 | pendiente |
| 27 | [`F3-02-web-rojo-fleet-filtercfg-grpc`](F3-02-web-rojo-fleet-filtercfg-grpc.md) | 🌐 | F3 | C | T3.10–T3.14 | pendiente |
| 28 | [`F3-03-web-verde-hojas`](F3-03-web-verde-hojas.md) | 🌐 | F3 | D | T3.15–T3.18 | pendiente |
| 29 | [`F3-04-web-verde-fleet-filtercfg`](F3-04-web-verde-fleet-filtercfg.md) | 🌐 | F3 | E | T3.19–T3.20 | pendiente |
| 30 | [`F3-05-web-verde-grpc`](F3-05-web-verde-grpc.md) | 🌐 | F3 | F | T3.21–T3.23 | pendiente |
| 31 | [`F3-06-web-conmutar-y-rutas`](F3-06-web-conmutar-y-rutas.md) | 🌐 | F3 | G | T3.24–T3.28 | pendiente |
| 32 | [`F3-07-cli-cierre-mtls`](F3-07-cli-cierre-mtls.md) | 💻 | F3 | G/H | T3.24–T3.28 + T3.29–T3.30 | pendiente |
| 33 | [`F4-01-web-inventario`](F4-01-web-inventario.md) | 🌐 | F4 | A | T4.1–T4.2 | pendiente |
| 34 | [`F4-02-web-rojo`](F4-02-web-rojo.md) | 🌐 | F4 | B | T4.3–T4.10 | pendiente |
| 35 | [`F4-03-web-verde-hojas`](F4-03-web-verde-hojas.md) | 🌐 | F4 | C | T4.11–T4.18 | pendiente |
| 36 | [`F4-04-web-verde-llmvia-c2`](F4-04-web-verde-llmvia-c2.md) | 🌐 | F4 | D | T4.19–T4.23 | pendiente |
| 37 | [`F4-05-web-conmutar-y-rutas`](F4-05-web-conmutar-y-rutas.md) | 🌐 | F4 | E | T4.24–T4.28 (+ TX.12–TX.14) | pendiente |
| 38 | [`F4-06-web-cierre`](F4-06-web-cierre.md) | 🌐 | F4 | F | T4.29–T4.31 | pendiente |
| 39 | [`F4-07-cli-cierre`](F4-07-cli-cierre.md) | 💻 | F4 | F | T4.29–T4.31 | pendiente |
| 40 | [`F5-01-web-inventario-y-rojo-model-indice`](F5-01-web-inventario-y-rojo-model-indice.md) | 🌐 | F5 | A/B1 | T5.1 + T5.2–T5.6 | pendiente |
| 41 | [`F5-02-web-rojo-catalogimport`](F5-02-web-rojo-catalogimport.md) | 🌐 | F5 | B2 | T5.7–T5.8 | pendiente |
| 42 | [`F5-03-web-verde`](F5-03-web-verde.md) | 🌐 | F5 | C | T5.9–T5.18 | pendiente |
| 43 | [`F5-04-web-cierre`](F5-04-web-cierre.md) | 🌐 | F5 | D | T5.19–T5.21 | pendiente |
| 44 | [`F6-01-web-inventario-y-hojas`](F6-01-web-inventario-y-hojas.md) | 🌐 | F6 | A | T6.1–T6.5 | pendiente |
| 45 | [`F6-02-web-rojo-intakes`](F6-02-web-rojo-intakes.md) | 🌐 | F6 | B | T6.6–T6.9 | pendiente |
| 46 | [`F6-03-web-rojo-quotetext-crm`](F6-03-web-rojo-quotetext-crm.md) | 🌐 | F6 | C | T6.10–T6.13 | pendiente |
| 47 | [`F6-04-web-verde-intakes-1`](F6-04-web-verde-intakes-1.md) | 🌐 | F6 | D | T6.14–T6.16 | pendiente |
| 48 | [`F6-05-web-verde-intakes-2`](F6-05-web-verde-intakes-2.md) | 🌐 | F6 | E | T6.17–T6.18 | pendiente |
| 49 | [`F6-06-web-verde-resto`](F6-06-web-verde-resto.md) | 🌐 | F6 | F | T6.19–T6.21 | pendiente |
| 50 | [`F6-07-web-cara-http`](F6-07-web-cara-http.md) | 🌐 | F6 | G | T6.22–T6.23 (= TX.16–TX.17 de FX) | pendiente |
| 51 | [`F6-08-web-conmutar-y-rutas`](F6-08-web-conmutar-y-rutas.md) | 🌐 | F6 | H | T6.24–T6.26 (incluye TX.18) | pendiente |
| 52 | [`F6-09-cli-cierre`](F6-09-cli-cierre.md) | 💻 | F6 | I | T6.27–T6.29 | pendiente |
| 53 | [`F7-01-web-inventario-y-hojas`](F7-01-web-inventario-y-hojas.md) | 🌐 | F7 | A | T7.1–T7.6 | pendiente |
| 54 | [`F7-02-web-rojo-stages`](F7-02-web-rojo-stages.md) | 🌐 | F7 | B | T7.7–T7.9 | pendiente |
| 55 | [`F7-03-web-rojo-pipeline-reanalisis`](F7-03-web-rojo-pipeline-reanalisis.md) | 🌐 | F7 | C | T7.10–T7.13 | pendiente |
| 56 | [`F7-04-web-verde-hojas`](F7-04-web-verde-hojas.md) | 🌐 | F7 | D | T7.14–T7.15 | pendiente |
| 57 | [`F7-05-web-verde-stages`](F7-05-web-verde-stages.md) | 🌐 | F7 | E | T7.16–T7.17 | pendiente |
| 58 | [`F7-06-web-verde-pipeline-reanalisis`](F7-06-web-verde-pipeline-reanalisis.md) | 🌐 | F7 | F | T7.18–T7.20 | pendiente |
| 59 | [`F7-07-web-cara-http`](F7-07-web-cara-http.md) | 🌐 | F7 | G | T7.21–T7.22 (= TX.19–TX.20 + intenciones) | pendiente |
| 60 | [`F7-08-web-conmutar`](F7-08-web-conmutar.md) | 🌐 | F7 | H | T7.23–T7.26 (incluye TX.21) | pendiente |
| 61 | [`F7-09-cli-cierre`](F7-09-cli-cierre.md) | 💻 | F7 | I | T7.27–T7.29 | pendiente |
| 62 | [`F8-01-web-inventario`](F8-01-web-inventario.md) | 🌐 | F8 | A | T8.1–T8.2 | pendiente |
| 63 | [`F8-02-web-rojo-hojas`](F8-02-web-rojo-hojas.md) | 🌐 | F8 | B | T8.3–T8.8 | pendiente |
| 64 | [`F8-03-web-rojo-motor`](F8-03-web-rojo-motor.md) | 🌐 | F8 | C | T8.9–T8.14 | pendiente |
| 65 | [`F8-04-web-rojo-carrito`](F8-04-web-rojo-carrito.md) | 🌐 | F8 | D | T8.15–T8.17 | pendiente |
| 66 | [`F8-05-web-rojo-runtime`](F8-05-web-rojo-runtime.md) | 🌐 | F8 | E | T8.18–T8.21 | pendiente |
| 67 | [`F8-06-web-verde-hojas`](F8-06-web-verde-hojas.md) | 🌐 | F8 | F | T8.22–T8.23 | pendiente |
| 68 | [`F8-07-web-verde-events-admin-cart`](F8-07-web-verde-events-admin-cart.md) | 🌐 | F8 | G | T8.24–T8.25 | pendiente |
| 69 | [`F8-08-web-verde-runtime-1`](F8-08-web-verde-runtime-1.md) | 🌐 | F8 | H | T8.26 | pendiente |
| 70 | [`F8-09-web-verde-runtime-2`](F8-09-web-verde-runtime-2.md) | 🌐 | F8 | I | T8.27–T8.28 | pendiente |
| 71 | [`F8-10-web-cara-http`](F8-10-web-cara-http.md) | 🌐 | F8 | J | T8.29 | pendiente |
| 72 | [`F8-11-web-conmutar`](F8-11-web-conmutar.md) | 🌐 | F8 | K | T8.30–T8.35 | pendiente |
| 73 | [`F8-12-cli-cierre`](F8-12-cli-cierre.md) | 💻 | F8 | K/L | T8.30–T8.35 + T8.36–T8.38 | pendiente |
| 74 | [`F9-06-cli-cierre`](F9-06-cli-cierre.md) | 💻 | F9 | D | T9.30–T9.33 | pendiente |
| 75 | [`F10-01-jhoan-decisiones`](F10-01-jhoan-decisiones.md) | 🧑 | F10 | — | — | pendiente |
| 76 | [`F10-02-web-dorada`](F10-02-web-dorada.md) | 🌐 | F10 | A | T10.1–T10.3 | pendiente |
| 77 | [`F10-03-cli-prueba-uat`](F10-03-cli-prueba-uat.md) | 💻 | F10 | B | T10.4–T10.6 | pendiente |
| 78 | [`F10-04-web-relevo`](F10-04-web-relevo.md) | 🌐 | F10 | C | T10.7–T10.14 | pendiente |
| 79 | [`F10-05-cli-cierre`](F10-05-cli-cierre.md) | 💻 | F10 | D | T10.15–T10.17 | pendiente |
| 80 | [`F10-06-cli-fuera-del-repo`](F10-06-cli-fuera-del-repo.md) | 💻 | F10 | E | T10.18–T10.21 | pendiente |
| 81 | [`F10-07-cli-main`](F10-07-cli-main.md) | 💻 | F10 | F | T10.22 — solo a petición de Jhoan | pendiente |
