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
go version && golangci-lint version        # go1.26.5 y v2.12.2; si no, el entorno NO está listo
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
5. Si `golangci-lint` no es la `v2.12.2`, **no declaras ningún gate pasado**: lo dices y sigues solo
   con lo que no dependa del lint.

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
- **Rojo**: el contrato lleva **solo exportados** (el linter `unused` rompe el gate con no
  exportados sin uso); cuerpos `panic(pendiente.Implementar("paq.Func"))`, nunca un valor cero; el
  test nace con `//go:build pendiente` y **una aserción por promesa** del comentario.
- **Verde**: un fichero por commit; cabecera `// Porta <ruta vieja> @ <sha>`; el porqué viaja con la
  lógica (E-10); se quita la etiqueta; `make cobertura-ficheros` ≥ 80 % (salvo adaptadores Postgres).
- **Conmutar**: el arranque nuevo cablea lo nuevo; `huella_test` idéntica; los tipos nuevos que aún
  consumen paquetes viejos se adaptan en `internal/arranque/puente_<x>.go`; las rutas que tocan, a
  `apipublica` según el mapa de FX, **patrón byte a byte**.
- **Idioma (E-11)**: nombres (ficheros, tipos, funciones, variables, tests) en **inglés**; solo los
  comentarios y la documentación en español. Lo ya escrito no se renombra; los textos observables se copian
  literales. Dilo en el prompt de cada sub-agente.
- **Puentes** de import al código viejo: solo los que declare la spec, y **declarados** en
  `internal/modulos/fronteras_test.go` en el mismo commit.

## 4 · Gates (skill `validar-antes-de-cerrar`)

```bash
make ci-local > /tmp/ci.log 2>&1; echo "rc=$?"          # el rc SIN pipe
go vet -tags pendiente ./... ; echo "rc=$?"
make test-pendiente                                       # cuenta lo que falta (existe desde F0-02/T0.4;
                                                          # antes, di «el target aún no existe»)
go test -v ./internal/<lo tuyo>/... 2>&1 | grep -c -- '--- SKIP'   # SKIP en código nuevo = 0
```

Más los que diga tu bloque (`make cobertura-ficheros`, `huella_test`, `go vet -tags integracion
./test/procesos/...`). Un gate corrido con otra versión de la toolchain **no es autoritativo**: dilo.

## 5 · Prohibiciones

🚫 Tocar el código viejo (salvo las excepciones **declaradas** en `DECISIONES.md`) · 🚫 `t.Skip` en
código nuevo · 🚫 conectar un test a un Postgres vivo: ni `WAPP_TEST_DB_DSN`, ni `localhost:5432`, ni
el **PostgreSQL 16 preinstalado en la VM**; solo testcontainers · 🚫 bajar `go 1.26.5` o generar
`go.sum` sin red real · 🚫 cambiar un texto observable (errores, literales de protocolo, nombres de
métricas y patrones de ruta) · 🚫 levantar `cmd/server` y `cmd/server-modular` a la vez contra la
misma BD o los mismos puertos · 🚫 empujar a `dev` o a `main` · 🚫 un nombre en español en lo nuevo, salvo las excepciones de `05` E-11 · 🚫 decidir rumbo en solitario: ante
un conflicto con la spec, `05` o un ADR, **para y pregunta**.

## 6 · Cerrar la sesión

1. `tareas.md`: `[x] … — cerrada en \`<sha>\`` (o `[~]` diciendo qué falta). Nunca `[x]` sin SHA.
2. `documentations/reorganizacion-modular/ESTADO.md`: fase, bloque, siguiente paso, SHA de
   `origin/dev` y de tu rama.
3. Si algo del bloque lo cierra la local (🌐→💻): **traspaso** con la skill `traspaso-web-local`
   (ocho secciones; la §7 con contenido real).
4. Lo que aprendiste y la norma no decía → «Contradicciones encontradas» o «Decisiones que necesita»
   del `README.md` de la fase.
5. `git push` de **tu** rama y **`gh pr create --base dev`** (la rama por defecto del remoto es
   `main`). Título: `<id de sesión> · <bloque>`. Cuerpo: el informe de gates, el enlace a la sesión
   (`https://claude.ai/code/${CLAUDE_CODE_REMOTE_SESSION_ID/#cse_/session_}`) y la frase
   **«Integrar SIN squash: rojo y verde son commits distintos»**.
6. **No sigas con el bloque siguiente** aunque te sobre tiempo: la sesión termina en su punto de
   parada.

## 7 · Si la sesión se corta a medias

Lo que no esté commiteado y empujado se pierde con la VM. Empuja **pronto y a menudo** a tu rama
(cada commit rojo/verde). La sesión que retome lee `tareas.md` (lo `[x]` con SHA es la verdad) y el
`git log` de la rama.
