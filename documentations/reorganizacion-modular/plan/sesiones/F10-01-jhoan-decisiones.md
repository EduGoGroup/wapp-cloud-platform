# F10-01 · Decisiones antes del relevo · 🧑 Jhoan

> No es un prompt. Antes de F10-02 hay que rellenar [`../DECISIONES.md`](../DECISIONES.md) §6
> (D-F10-1…7) y confirmar que **F9 está `CERRADO`** (F9-05): sin la suite de procesos en verde contra
> los dos binarios, el código nuevo no tiene ni una prueba contra Postgres y el relevo no procede.

- 🟡 **Decisión pendiente antes de F10-03 (deuda D-38 de [`deuda.md`](../../../deuda.md))**: para probar con el Edge
  **real** sin WhatsApp hay que modificar `wapp-edge-agent` (hacer permanente su inyector de entrantes). Elige:
  **(a)** se hace, en una sesión propia en el repo del Edge, antes de F10-03; o **(b)** no se hace y se renueva una sesión
  de WhatsApp para la prueba en UAT. Las sesiones de WhatsApp de local y del VPS estaban caducadas el 2026-10-10.
- **D-F10-1** (ventana de la prueba en UAT ≥ 24 h) y **D-F10-2** (dejarlo corriendo si el relevo
  llega en ≤ 7 días) fijan **cuándo** se hace F10-03: elige la fecha.
- **D-F10-6** (tag `v0.3.0`) y el paso a `main` (F10-06) solo ocurren **si lo pides expresamente** en
  esa sesión.
- **D-F10-7** (regla de conteo del ADR-0010) toca la documentación del ecosistema: la hace F10-05.
- Todo F10 es **local** (💻): sin sesión web y sin traspaso, pero con rama y PR a `dev` (regla 6 del `CLAUDE.md`), salvo que una sesión se corte.
