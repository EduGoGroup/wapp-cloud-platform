# La reconstrucción modular de `internal/` — ficha

> **Estado: hecha.** El relevo se cerró el 2026-10-11 y su expediente salió de este repo el
> 2026-10-12 (T10.25): **PR #87 abierto, en revisión**. Hasta que entre, `dev` sigue teniendo la carpeta.

## Qué se hizo

Entre el **2026-09-27** y el **2026-10-11**, `internal/` se **reconstruyó** por módulos de negocio:
cada paquete se volvió a escribir en su sitio nuevo, con su contrato y su test delante, y el código
anterior se **borró** cuando el nuevo ya corría en UAT. No fue un movimiento de carpetas.

| Antes | Después |
|---|---|
| Paquetes sueltos bajo `internal/`, uno por tema, sin frontera escrita | Siete módulos en `internal/modulos/` (`acceso`, `edge`, `conversacion`, `catalogo`, `captacion`, `inferencia`, `solicitudes`), con una lista blanca de quién importa a quién |
| Tests de integración contra un Postgres vivo, que se saltaban solos sin él | Procesos de negocio en `test/procesos/`, contra el binario y un Postgres de testcontainers, con cero SKIP |

Además quedó una sola cara HTTP pública (`internal/apipublica`) y un solo arranque, en nueve fases
(`internal/arranque`).

Hacia fuera no cambió nada a propósito: ni una ruta, ni un rpc, ni una tabla, ni una variable de
entorno. Lo que sí cambió de conducta visible está inventariado y es la deuda D-36 de
[`deuda.md`](deuda.md).

Se hizo en once fases (F0 andamiaje · F1 el piloto · F2–F8 un módulo cada una · F9 los procesos ·
F10 el relevo) más dos transversales (la cara HTTP y el WhatsApp enchufable del Edge). UAT corre el resultado desde el 2026-10-11.

## Qué dejó en este repo

- **Las normas del código**: [`constitucion.md`](constitucion.md) §5.1 (un fichero un test, ni un
  `t.Skip`, suites de contrato, exportados cubiertos, idioma, tamaño, las tres listas cerradas) y
  §5.2 (cómo se añade un módulo). Conservan su identificador `E-n` porque los comentarios del
  código las citan así.
- **Los candados** que las vigilan: [`arquitectura.md`](arquitectura.md) §4.3.
- **La deuda** que abrió y no cerró: [`deuda.md`](deuda.md), con el hallazgo o la decisión de
  origen de cada entrada.

## Dónde está el expediente

El método, los análisis, las decisiones, la spec de cada fase y las sesiones con sus hallazgos
(173 ficheros) están archivados en la documentación de wApp, fuera de este repo:

`wApp/documentations/archive/cloud-platform-modular-reorganization/`, en el repo de documentación del
ecosistema
([abrir](../../../documentations/archive/cloud-platform-modular-reorganization/README.md), si tienes
el ecosistema clonado con su disposición habitual).

En este repo vivió entero hasta el commit **`827b4ca0`** de `dev` (2026-10-12):
`git show 827b4ca0:documentations/reorganizacion-modular/README.md` lo enseña tal como era.

🔴 **El expediente cuenta cómo se hizo y describe código que ya no existe**: está lleno de rutas
borradas. No es fuente para saber qué hay hoy; para eso están `arquitectura.md` y el código.

## Cómo leer una cita vieja

Los comentarios del código y `deuda.md` citan el expediente sin su ruta. La clave:

| La cita | Es |
|---|---|
| «05 E-13», «E-9», «05 §7» | El método (`05-metodo-contratos-y-tdd.md`). Lo que sigue vigente está en `constitucion.md` §5.1 |
| `D-5`, `D-F7-13`, `D-R-7`, `P2` | Una decisión de `plan/DECISIONES.md` |
| `F8`, `F9-D`, `FX`, `FW` | Una fase: su carpeta es `plan/F8-conversacion/`, `plan/F9-procesos/`… |
| `T9.15`, `TX.4` | Una tarea del `tareas.md` de esa fase |
| «hallazgo 47 de F10» | Una línea del `README.md` de esa fase |
| `diseno.md §4`, `reglas.md §3` | Un fichero de la spec de la fase que el comentario nombra |

No confundir con los `D-nn` de [`deuda.md`](deuda.md) (`D-36`, `D-50`), que son deuda viva y sí
están aquí.
