---
name: reconstruir-modulo
description: Use when starting or continuing a whole phase of the modular reconstruction of wapp-cloud-platform — F0 (scaffolding: parallel boot, pendiente, make targets, locks) or F1–F8 (one module: nucleo, acceso, edge, inferencia, catalogo, solicitudes, captacion, conversacion). The main agent orchestrates sub-agents per file/package and protects its context window, following documentations/reorganizacion-modular/05-metodo-contratos-y-tdd.md. Triggers — "arranca F0", "empecemos F1", "reconstruye el módulo X", "ola de <módulo>", "pasa a contratos el módulo X", "conmuta el módulo X", "sigue con la reconstrucción".
---

# Reconstruir un módulo: orquestar una fase

> **La norma vive en [`documentations/reorganizacion-modular/05-metodo-contratos-y-tdd.md`](../../../documentations/reorganizacion-modular/05-metodo-contratos-y-tdd.md)**
> (§6 las fases, §4 el ciclo, §4.1 los puentes, §5 los candados). El árbol destino, en
> `04-estructura-final.md` §3, y qué paquete viejo es la referencia de cada nuevo, en `04` §4.
> Esta skill es **cómo se conduce** una fase. Si choca con `05`, manda `05`.

Eres **orquestador, no implementador**. Un módulo son decenas de ficheros; si los lees todos en tu
contexto, divergirás. Regla madre: **delega por fichero o por paquete, quédate con las
conclusiones, valida lo que decide el rumbo.**

## Paso 0 · Verdad de campo (siempre, antes de nada)

```bash
git fetch -q origin && git status --short && git branch --show-current
git log --oneline -1 origin/dev
grep -rn 'pendiente.Implementar' --include='*.go' internal | wc -l     # lo que falta por implementar
ls internal/modulos internal/nucleo internal/arranque 2>/dev/null       # qué existe ya
```

- La sesión **local** trabaja sobre **`dev`**, al día con `origin/dev`; la **web**, en su rama
  partida de `origin/dev`, y abre PR `--base dev` (`plan/sesiones/PROTOCOLO-WEB.md`). Toda ola aterriza en `dev`; `main` solo lo
  mueve la sesión local cuando Jhoan lo pide.
- **¿En qué fase estás?** Lo dice lo que existe, no lo que recuerdes. Si `internal/arranque` no
  existe, estás en **F0**. La parada de F1 se resolvió el 2026-10-03 (`05` E-12): en cada fase, el
  inventario **clasifica cada archivo** en simple / medio / complejo y lista cuántos adaptadores `bridge_<x>.go` harán
  falta; Jhoan lo aprueba antes de escribir código.

## Paso 1 · Inventario del módulo (delegado)

Manda un sub-agente **Explore** con este contrato de salida, no más:

> Para el módulo `<m>`, según `04` §4: lista de paquetes viejos de referencia → paquete nuevo;
> por paquete, sus ficheros de producción con una línea de qué hace; sus ficheros de test viejos
> (solo nombres y número de `Test*`); qué importa de otros módulos (y si ese destino ya está
> reconstruido o sería **puente**); qué ficheros son puertos o adaptadores Postgres y si hay gemelo
> en memoria.

Con eso partes el trabajo en **tareas por paquete** (`TaskCreate`), en orden de dependencias
internas del módulo: primero las hojas.

## Paso 2 · Contratos y rojo de TODO el módulo

- Un sub-agente **por paquete** (no por módulo: satura), con la skill **`contrato-tdd`** y el
  inventario de su paquete. Devuelve: ficheros creados, exportados de cada uno, candados de
  invariante que tocó (`05` §3.2), puentes que necesitó, y la salida de `go vet -tags pendiente`.
- 🔤 **Idioma (`05` E-11): dilo en el prompt de CADA sub-agente**, en el rojo y en el verde. Nombres (ficheros, paquetes,
  tipos, funciones, variables, tests y casos) en **inglés**; solo los comentarios y la documentación en español. Lo ya escrito
  y lo ya decidido no se renombra; los textos observables se copian literales. Si la spec nombra en español algo que aún no
  existe, se escribe en inglés y la correspondencia se anota en el `tareas.md` de la fase.
- Paquetes independientes → **en paralelo** (varias llamadas en un mismo mensaje).
- Los **puentes** al código viejo se declaran en `internal/modulos/fronteras_test.go` en el mismo
  commit que los introduce. Un puente no declarado rompe el gate: es la señal buscada, no un
  estorbo.
- Al cerrar la pasada: `dev` verde (`validar-antes-de-cerrar`), `make test-pendiente` cuenta lo que
  falta. Commits `rojo(<m>): …`.

## Paso 3 · Verde, fichero a fichero

- **Un fichero por commit**, `verde(<m>): <fichero>`. Puedes delegar varios ficheros en paralelo si
  son de paquetes distintos; dentro de un paquete, en serie (comparten `_test` y compilación).
- Cada sub-agente vuelve con: el `rc` del test del paquete **leído sin pipe**, la cobertura del
  fichero (informe, no bloquea), y cualquier regla del fichero viejo que decidió no portar (con motivo).
- **Valida tú** lo que decide el rumbo: que el test salió del contrato (no un test viejo copiado) y
  que ningún texto observable cambió.

## Paso 4 · Conmutar

Cuando todos los ficheros del módulo están en verde:

1. En `internal/arranque/`, la fase del módulo (`fase<N>_<modulo>.go`) pasa a cablear **los
   paquetes nuevos** en lugar de los viejos. El arranque viejo (`internal/bootstrap/arranque`) **no
   se toca**: es el oráculo.
2. `internal/arranque/huella_test.go` debe dar **la misma huella** que el arranque viejo para este
   módulo: rutas, rpc, métricas, goroutines.
3. Si el módulo retira puentes de otros (p. ej. al cerrar `conversacion`, F8), se retiran en esta
   misma ola y se re-tocan los paquetes que los usaban.
4. Commit `conmutar(<m>): el arranque nuevo cablea <m>`.

🔴 **Nunca** levantes `cmd/server` y `cmd/server-modular` a la vez contra la misma BD o los mismos
puertos: los dos migran, los dos escuchan `:8100-8103`, y dos agregadores con ventanas en memoria
partirían las ráfagas (`04` §2.2).

## Paso 5 · Cerrar la fase

- `validar-antes-de-cerrar` completo.
- Actualiza `documentations/reorganizacion-modular/` si la fase enseñó algo que la norma no decía
  (un coste, una excepción, un candado que estorba). En F1, **escribe el informe del piloto**:
  coste real por fichero y por nivel, qué candado estorbó. (El informe de F1 ya está: `plan/F1-nucleo-contact/informe-piloto.md`.)
  Cierra la sesión con las tres cosas fijas de `05` E-12: tareas `[x]` con SHA, un bloque en `ESTADO.md` y los hallazgos
  en el README de la fase.
- Si queda trabajo que solo puede hacer la sesión local (Docker, UAT, `main`), escribe el traspaso
  con **`traspaso-web-local`**.

## F0 · Andamiaje (no crea ningún módulo)

Qué produce, según `05` §6: `cmd/server-modular` + `internal/arranque` como **copia exacta** del
arranque viejo (cableando paquetes **viejos**) · `internal/pendiente` · la etiqueta `pendiente` y
los targets `test-pendiente` y `cobertura-ficheros` · los candados de §5 · los tres ✎ de
`platform` corregidos **en su sitio**. Criterio de cierre: huellas idénticas y `dev` verde.

## Antipatrones

- Un sub-agente para el módulo entero. Divergirá.
- Pasar a verde antes de tener los contratos del módulo completos: los contratos se hablan entre
  sí, y es en esa pasada donde aparecen los puentes.
- Conmutar un módulo con ficheros aún en rojo.
- Lanzar un sub-agente sin la regla de idioma (E-11) en su prompt.
- Seguir más allá de F1 sin la decisión de Jhoan.
- Commitear o decidir sobre un claim de un sub-agente sin evidencia (`fichero:línea`, `rc`, SHA).

## Relacionadas

`contrato-tdd` · `validar-antes-de-cerrar` · `traspaso-web-local` · `procesos-testcontainers` (F9)
