# FW · Reglas

## 1 · Lo que no se toca

- **El contrato de CloudLink.** Ni un campo nuevo en el proto.
- **La doble llave.** No se propone mover la DEK, ni persistirla, ni un lease «de pruebas» que el servidor no emita.
- **Redis o un broker en el Edge**: prohibido (ADR-0003).
- **El código de producción de este repo.** Aquí solo `test/procesos`, el `Makefile` y documentación.
- **El Edge falso de `test/procesos`.** No se retira ni se debilita.
- **`documentations/literal-aviso-sesion-pasiva.md`** (en los dos repos): contrato congelado.

## 2 · Cómo se trabaja

1. **Cada repo, sus reglas.** En `edge/wapp-edge-agent` manda su `documentations/` (se carga `constitucion.md` antes de
   tocar nada) y su flujo de ramas; en este repo, la regla 6 del `CLAUDE.md`: rama desde `dev` y PR, sin squash.
2. **Los gates son locales y son dos por repo** (`ci-local` y `ci-docker`). Un PR no valida nada.
3. **El `rc` se lee sin pipe y los SKIP se cuentan.**
4. **Las variables se nombran por su nombre efectivo** (`WAPP_AGENT_…`): el loader compone el prefijo.
5. **Antes de proponer una alternativa, la tabla de descartadas del ADR-0041.** Varias ideas obvias ya están rechazadas
   ahí: entrar por debajo del listener, el binario aparte, registrar la ruta y devolver `403`, el `200` que no midió nada.
6. **Ni un secreto en la documentación**: de un certificado o una clave se dice dónde vive.

## 3 · Trampas conocidas

- **«Verde» contra el binario equivocado.** Un e2e puede parecer verde contra otro proceso: el caso de TW.8 afirma qué
  binario del Edge corre (ruta y revisión) antes de afirmar nada más. Es la razón de las «tres puertas» del runbook
  `e2e-con-whatsapp-real.md` del ecosistema.
- **El sintético llega a la nube.** Es deliberado (ADR-0041). En UAT ensucia datos reales del tenant de prueba: la receta
  de TW.9 dice con qué tenant y cómo se distinguen (`SINTETICO-`).
- **El inyector contesta `409` sin escucha viva.** No es un fallo del inyector: es la señal de que el falso todavía no
  publica lo que el camino real espera.

## 4 · Definición de hecho

1. Con la palanca baja, `make ci-local` del Edge rc=0 y el guardarraíl en verde: nada cambió.
2. Con la palanca echada, el Edge arranca sin device y sin red hacia WhatsApp (RW.1.a).
3. Entrada y salida probadas, cada promesa con su test (RW.2).
4. Grita, marca y respeta el lease revocado (RW.3).
5. El comando de TW.8: `RC=0`, 0 SKIP, 0 FAIL; `make test-procesos` con el mismo `PASS` de antes.
6. La receta de UAT, ejecutada en local de principio a fin, palanca bajada al final.
7. El ADR aprobado; D-38 cerrada en [`deuda.md`](../../../deuda.md); `documentations/` del Edge y de este repo al día.
8. Bloque de la fase en `ESTADO.md` y README de la fase en «cerrada».
