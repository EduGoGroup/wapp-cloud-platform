#!/bin/bash
# Verifica la toolchain de la reconstrucción y AVISA. Nunca instala, nunca falla la sesión.
cd "${CLAUDE_PROJECT_DIR:-.}" || exit 0
ENT=local; [ "${CLAUDE_CODE_REMOTE:-}" = "true" ] && ENT=web
GO_WANT=go1.26.5; LINT_WANT=2.12.2; AVISOS=0
echo "== Verdad de campo ($ENT) =="
GOV=$(GOWORK=off go env GOVERSION 2>/dev/null)
[ "$GOV" = "$GO_WANT" ] || { echo "⚠️ Go es '$GOV', la fijada es $GO_WANT (GOTOOLCHAIN=$GO_WANT)"; AVISOS=1; }
LV=$(golangci-lint version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)
[ "$LV" = "$LINT_WANT" ] || { echo "⚠️ golangci-lint es '${LV:-ausente}', el fijado es v$LINT_WANT: NINGÚN gate es autoritativo"; AVISOS=1; }
echo "Go: ${GOV:-ausente} · golangci-lint: ${LV:+v}${LV:-ausente}"
if [ "$ENT" = web ]; then docker info >/dev/null 2>&1 && echo "Docker: responde" || echo "Docker: NO responde — el daemon no arranca solo: (nohup dockerd >/tmp/dockerd.log 2>&1 &)"; fi
timeout 20 git fetch -q origin 2>/dev/null
echo "Rama: $(git branch --show-current) · origin/dev: $(git log --oneline -1 origin/dev 2>/dev/null)"
echo "Pendientes: $(grep -rn --include='*.go' --exclude-dir=pendiente 'pendiente\.Implementar(' internal 2>/dev/null | grep -vc '_test\.go:')"
ls documentations/reorganizacion-modular/traspasos/*.md 2>/dev/null | while read -r f; do
  grep -q '^## CERRADO' "$f" || echo "Traspaso ABIERTO: $f"; done
[ "$AVISOS" = 0 ] && echo "Toolchain: OK" || echo "Toolchain: NO LISTA — dilo en el informe, no la sustituyas"
exit 0
