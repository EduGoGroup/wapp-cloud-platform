#!/bin/bash
# Verifica la toolchain de la reconstrucción y AVISA. Nunca instala, nunca falla la sesión.
#
# Dice la verdad de lo que usará `make`, no la del PATH: se la pregunta a `make toolchain`, así
# que ni las versiones fijadas (GO_VERSION, LINT_VERSION) ni la regla que elige el golangci-lint
# (.bin/ de `make tools` primero, luego el PATH) están escritas otra vez aquí. Va con GOPROXY=off:
# si go<fijado> no está ya en la caché de Go, `go` falla en el acto en vez de descargarlo
# (verificar no es instalar) y sin esperar a la red.
cd "${CLAUDE_PROJECT_DIR:-.}" || exit 0
ENT=local; [ "${CLAUDE_CODE_REMOTE:-}" = "true" ] && ENT=web
AVISOS=0

# limit <segundos> <comando…> — rc 124 si se agota, como `timeout`. `timeout` no viene en todos
# los macOS: sin él se prueba gtimeout (coreutils) y luego perl; sin ninguno, el comando corre
# sin límite propio y lo acota el `timeout` del hook en .claude/settings.json.
limit() {
  local s=$1; shift
  if command -v timeout >/dev/null 2>&1; then timeout "$s" "$@"
  elif command -v gtimeout >/dev/null 2>&1; then gtimeout "$s" "$@"
  elif command -v perl >/dev/null 2>&1; then
    perl -e 'my $s = shift; my $p = fork; if (!$p) { exec @ARGV or exit 127 }
             $SIG{ALRM} = sub { kill "TERM", $p; exit 124 }; alarm $s; waitpid $p, 0; exit($? >> 8)' "$s" "$@"
  else "$@"; fi
}
# field <CLAVE> <n> — campo n (1 = versión, 2 = ruta) de la línea `CLAVE=versión ruta` de $TC.
field() { printf '%s\n' "$TC" | sed -n "s/^$1=//p" | head -1 | cut -d' ' -f"$2"; }

echo "== Verdad de campo ($ENT) =="
TC=$(GOPROXY=off limit 30 make --no-print-directory toolchain 2>&1); TC_RC=$?
GO_PIN=$(field GO_PINNED 1); GO_SYS=$(field GO_SYSTEM 1); GO_EFF=$(field GO_EFFECTIVE 1)
FMT_EFF=$(field GOFMT_EFFECTIVE 1)
LINT_PIN=$(field LINT_PINNED 1); LINT_EFF=$(field LINT_EFFECTIVE 1); LINT_AT=$(field LINT_EFFECTIVE 2)
if [ -z "$GO_PIN" ] || [ -z "$LINT_PIN" ]; then
  echo "⚠️ \`make toolchain\` no contestó (rc=$TC_RC; 124 = se agotaron los 30 s): no sé qué toolchain usará make"
  printf '%s\n' "$TC" | head -5
  AVISOS=1
else
  [ "$LINT_EFF" = missing ] && LINT_EFF=ausente
  if [ "$GO_EFF" = missing ]; then
    GO_EFF="NO disponible"
    echo "⚠️ $GO_PIN no está en la caché de Go (en el sistema hay $GO_SYS) y este hook no descarga: el primer \`make\` lo baja solo si hay red. Compruébalo con \`make toolchain\`"
    AVISOS=1
  elif [ "$GO_EFF" != "$GO_PIN" ]; then
    echo "⚠️ Go bajo make es '$GO_EFF', el fijado es $GO_PIN: mira \`make toolchain\`"
    AVISOS=1
  elif [ "$FMT_EFF" != "$GO_PIN" ]; then
    echo "⚠️ el gofmt de la toolchain fijada es '$FMT_EFF', no $GO_PIN: \`make fmt-check\` no es autoritativo"
    AVISOS=1
  fi
  if [ "$LINT_EFF" != "$LINT_PIN" ]; then
    echo "⚠️ golangci-lint: $LINT_EFF${LINT_AT:+ ($LINT_AT)}; ni .bin/ ni el PATH traen el fijado, $LINT_PIN: corre \`make tools\` (lo deja en .bin/). Hasta entonces NINGÚN gate es autoritativo"
    AVISOS=1
  fi
  echo "Go: $GO_EFF bajo make (GOTOOLCHAIN=$GO_PIN) · en el sistema: $GO_SYS"
  echo "golangci-lint: $LINT_EFF${LINT_AT:+ · $LINT_AT}"
  # Lo que da un `go` suelto en esta sesión (fuera de make no manda el Makefile): informa, no avisa.
  GO_BARE=$(GOPROXY=off GOWORK=off limit 10 go env GOVERSION 2>/dev/null)
  [ "$GO_BARE" = "$GO_PIN" ] || echo "Ojo: un \`go\` suelto, fuera de make, es '${GO_BARE:-ausente}': los gates van por \`make\` (o con GOTOOLCHAIN=$GO_PIN delante)"
fi
if [ "$ENT" = web ]; then docker info >/dev/null 2>&1 && echo "Docker: responde" || echo "Docker: NO responde — el daemon no arranca solo: (nohup dockerd >/tmp/dockerd.log 2>&1 &)"; fi
limit 20 git fetch -q origin 2>/dev/null
echo "Rama: $(git branch --show-current) · origin/dev: $(git log --oneline -1 origin/dev 2>/dev/null)"
echo "Pendientes: $(grep -rn --include='*.go' --exclude-dir=pendiente 'pendiente\.Implementar(' internal 2>/dev/null | grep -vc '_test\.go:')"
ls documentations/reorganizacion-modular/traspasos/*.md 2>/dev/null | while read -r f; do
  grep -q '^## CERRADO' "$f" || echo "Traspaso ABIERTO: $f"; done
[ "$AVISOS" = 0 ] && echo "Toolchain: OK" || echo "Toolchain: NO LISTA — dilo en el informe, no la sustituyas"
exit 0
