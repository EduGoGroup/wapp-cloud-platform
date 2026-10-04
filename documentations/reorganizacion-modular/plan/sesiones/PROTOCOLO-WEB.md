# Protocolo de una sesión WEB (claude.ai/code)

> Lo lee **entero**, antes de nada, toda sesión web del plan. El prompt que la arrancó solo dice
> **qué bloque** le toca; el **cómo** está aquí. Si algo de aquí choca con
> [`05-metodo-contratos-y-tdd.md`](../../05-metodo-contratos-y-tdd.md), manda `05`; si choca con la
> spec de la fase, **para y pregunta**.

## 0 · Qué es esto, en tres líneas

`internal/` de `wapp-cloud-platform` se **reconstruye** (no se mueve) en `internal/modulos/<m>/`,
`internal/nucleo/`, `internal/arranque/` e `internal/apipublica/`: cada fichero nace con su
**contrato sin lógica** y un **test escrito desde ese contrato**, en rojo, y después recibe la lógica
portada del paquete viejo. El código viejo **no se toca**: es la referencia y lo que corre en UAT.

## 1 · Verdad de campo (antes de leer la spec)

```bash
git fetch -q origin && git status --short && git branch --show-current
git log --oneline -1 origin/dev
make toolchain; echo "rc=$?"               # TOOLCHAIN=OK y rc=0; si no, el entorno NO está listo
```

1. Tu rama de trabajo debe partir de **`origin/dev` actual**. Si no, `git rebase origin/dev` (o
   `git merge origin/dev`) antes de tocar nada.
2. En el `tareas.md` de tu fase, las tareas de las que depende tu bloque están `[x]` **con SHA**, y
   cada SHA existe: `git cat-file -e <sha>`. Si no → **para** y dilo.
3. En [`../DECISIONES.md`](../DECISIONES.md), las decisiones que bloquean tu bloque (columna
   «Bloquea») tienen la columna «Decisión» **rellena**. Si alguna está vacía → **para** y dilo. Si
   alguna se decidió **distinta** de la recomendación, aplícalo y anótalo en tu `tareas.md`.
4. Si hay un traspaso abierto de tu fase en `documentations/reorganizacion-modular/traspasos/`,
   léelo entero (skill `traspaso-web-local`).
5. Si `make toolchain` no termina en `TOOLCHAIN=OK` con `rc=0`, **no declaras ningún gate pasado**:
   lo dices, con su salida, y sigues solo con lo que no dependa de lo que falte (si es el
   `golangci-lint`, lo que no dependa del lint). En la web el lint fijado viene en el `PATH`;
   `make tools` es para la máquina local y aquí **no está probado**
   ([`../../06-entorno-web.md`](../../06-entorno-web.md) §6). No lo sustituyas por otro.

## 2 · Qué leer, y nada más (protege tu contexto)

1. [`../00-marco/README.md`](../00-marco/README.md) y lo que él mande leer para tu caso.
2. De tu fase: `README.md` → `requisitos.md` → `arquitectura.md` → `diseno.md` → `reglas.md` → **tu
   bloque** de `tareas.md`.
3. Si tu bloque tiene tareas **TX** (mudar rutas a `apipublica`):
   [`../FX-cara-http/mapa-de-rutas.md`](../FX-cara-http/mapa-de-rutas.md) y
   [`../FX-cara-http/diseno.md`](../FX-cara-http/diseno.md) son la autoridad.
4. El código viejo **de referencia** de cada fichero que vayas a crear (tabla de `04` §4 y el
   `diseno.md` de tu fase) y sus **tests viejos**, que se **leen** antes de escribir el contrato (E-8)
   y **no se portan**.

## 3 · Cómo trabajar

- **Eres orquestador**: skill **`reconstruir-modulo`**. Delega **por paquete** (contratos) o **por
  fichero** (verde) en sub-agentes con la skill **`contrato-tdd`**; quédate con conclusiones y
  evidencia (`fichero:línea`, `rc`, SHA). Paquetes independientes, en paralelo.
- **Nivel de ceremonia (`05` E-12)**: el del **inventario de la fase, aprobado por Jhoan**. *Simple*: contrato,
  test y lógica en una pasada, varios archivos por sesión. *Medio*: rojo y verde por archivo, agrupados por
  paquete. *Complejo*: el esquema completo, con mutantes. Si un archivo sale peor de lo previsto, **sube de
  nivel** y lo dices. La primera sesión de una fase **empieza por el inventario y para** hasta que Jhoan lo
  apruebe. No se relaja en ningún nivel: equivalencia viejo ↔ nuevo, `ci-local` con 0 SKIP, procesos de F9.
- **Rojo**: el contrato lleva **solo exportados** (el linter `unused` rompe el gate con no
  exportados sin uso); cuerpos `panic(pendiente.Implementar("paq.Func"))`, nunca un valor cero; el
  test nace con `//go:build pendiente` y **una aserción por promesa** del comentario.
- **Verde**: un fichero por commit; cabecera `// Porta <ruta vieja> @ <sha>`; el porqué viaja con la
  lógica (E-10); se quita la etiqueta. **Sin umbral de cobertura** (`05` E-9): un test por promesa del contrato, mutantes en
  el nivel complejo; `make cobertura-ficheros` es un **informe** que va al PR. El test de un auxiliar no
  exportado nace aquí, y solo si lleva regla de negocio o ramas no triviales (`05` E-4). Todo puerto con BD
  lleva su suite `Contrato(t, func(t) Montaje)` en memoria **y** en Postgres.
- **Conmutar**: el arranque nuevo cablea lo nuevo; `huella_test` idéntica; los tipos nuevos que aún
  consumen paquetes viejos se adaptan en `internal/arranque/bridge_<x>.go` (`05` §4.2: nivel simple, con
  **test de cableado** que afirma que el arranque construye lo nuevo **y** que nadie importa lo viejo fuera
  del adaptador); un módulo entra en `Conmutados` cuando **muere su último adaptador**; las rutas que tocan,
  a `apipublica` según el mapa de FX, **patrón byte a byte**.
- **Idioma (E-11)**: nombres (ficheros, tipos, funciones, variables, tests) en **inglés**; solo los
  comentarios y la documentación en español. Lo ya escrito no se renombra; los textos observables se copian
  literales. Dilo en el prompt de cada sub-agente.
- **Puentes** de import al código viejo: solo los que declare la spec, y **declarados** en
  `internal/modulos/fronteras_test.go` en el mismo commit.

## 4 · Gates (skill `validar-antes-de-cerrar`)

```bash
make toolchain; echo "rc=$?"                             # paso 0: TOOLCHAIN=OK y rc=0
make ci-local > /tmp/ci.log 2>&1; echo "rc=$?"          # el rc SIN pipe
make vet-pendiente; echo "rc=$?"                         # go vet -tags pendiente ./...
make test-pendiente                                       # cuenta lo que falta (existe desde F0-02/T0.4;
                                                          # antes, di «el target aún no existe»)
go test -v ./internal/<lo tuyo>/... 2>&1 | grep -c -- '--- SKIP'   # SKIP en código nuevo = 0
```

Más los que diga tu bloque (`huella_test`, `make vet-integracion`; `make cobertura-ficheros` como informe,
no como gate). Un
gate corrido con otra versión de la toolchain **no es autoritativo**: dilo. El `go test` suelto de
arriba corre con el `go` de la sesión, que en la web es el fijado (variable `GOTOOLCHAIN` del
entorno); si no lo fuera, el hook lo dice en su línea `Ojo:` y el comando lleva
`GOTOOLCHAIN=go1.26.5` delante.

## 5 · Prohibiciones

🚫 Tocar el código viejo (salvo las excepciones **declaradas** en `DECISIONES.md`) · 🚫 `t.Skip` en
código nuevo · 🚫 conectar un test a un Postgres vivo: ni `WAPP_TEST_DB_DSN`, ni `localhost:5432`, ni
el **PostgreSQL 16 preinstalado en la VM**; solo testcontainers · 🚫 bajar `go 1.26.5` o generar
`go.sum` sin red real · 🚫 cambiar un texto observable (errores, literales de protocolo, nombres de
métricas y patrones de ruta) · 🚫 levantar `cmd/server` y `cmd/server-modular` a la vez contra la
misma BD o los mismos puertos · 🚫 empujar a `dev` o a `main` · 🚫 un nombre en español en lo nuevo, salvo las excepciones de `05` E-11 · 🚫 decidir rumbo en solitario: ante
un conflicto con la spec, `05` o un ADR, **para y pregunta**.

## 6 · Cerrar la sesión

**Siempre las mismas tres cosas** (`05` E-12), y nada más de documentación:

1. `tareas.md`: `[x] … — cerrada en \`<sha>\`` (o `[~]` diciendo qué falta). Nunca `[x]` sin SHA.
2. Un bloque en `documentations/reorganizacion-modular/ESTADO.md`: fase, sesión, siguiente paso, SHA de
   `origin/dev` y de tu rama.
3. Los hallazgos nuevos (lo que aprendiste y la norma no decía) → «Contradicciones encontradas» o
   «Decisiones que necesita» del `README.md` de la fase.

Y para entregar:

4. **Traspaso** (skill `traspaso-web-local`) **solo si** algo de tu sesión lo tiene que cerrar la local, o si
   la sesión se corta a medias. Si no, no se escribe: la sesión 💻 de cierre de la fase lee `tareas.md` y el PR.
5. `git push` de **tu** rama y **`gh pr create --base dev`** (la rama por defecto del remoto es
   `main`). Título: `<id de sesión> · <bloque>`. Cuerpo: con la skill **`describir-pr`** (Jhoan, 2026-10-04):
   objetivo explicado, dónde encaja en la fase y en el plan, resultado con desvíos y pendientes, y después el
   informe de gates, el enlace a la sesión (`https://claude.ai/code/${CLAUDE_CODE_REMOTE_SESSION_ID/#cse_/session_}`)
   y la frase **«Integrar SIN squash: rojo y verde son commits distintos»**. En la web no hay `gh`: la herramienta
   MCP de GitHub (`create_pull_request`).
6. **No sigas con la sesión siguiente** aunque te sobre tiempo: la sesión termina en su punto de
   parada. Una sesión es un bloque coherente de **45–90 min**; si no cabe, para en un punto limpio (commits
   empujados, `[~]` en la tarea), cierra con las tres cosas y se relanza.

### Si esta ficha 🌐 se corre en local

Cuando se acabe la promoción web, las fichas 🌐 y 🌐❓ se corren en la máquina de Jhoan con el **mismo
encargo**: se trabaja en una **rama partida de `dev`, con PR hacia `dev`** (regla innegociable 6 del `CLAUDE.md`, 2026-10-03; antes: sobre `dev`, sin PR) y sin traspaso, se cierran los gates con la toolchain local
([`PROTOCOLO-CLI.md`](PROTOCOLO-CLI.md) §1 y §3) y se termina con `git push origin <rama>` leyendo el rc sin
pipe, y el PR hacia `dev`.

## 7 · Si la sesión se corta a medias

Lo que no esté commiteado y empujado se pierde con la VM. Empuja **pronto y a menudo** a tu rama
(cada commit rojo/verde). La sesión que retome lee `tareas.md` (lo `[x]` con SHA es la verdad) y el
`git log` de la rama.
