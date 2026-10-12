#!/bin/bash
# Comprueba que lo que la documentación de la pieza nombra entre comillas invertidas EXISTE en el árbol.
# Uso: check-docs-tree.sh [fichero.md …]   (sin argumentos: toda la documentación de la pieza)
# Sale con rc=1 si falta algo. Se corre desde la raíz del repo.
# Cuatro comprobaciones, cada una con su regla:
#   RUTA     un token que empieza por un directorio del repo (o es un fichero de la raíz) → `ls -d` (admite globs)
#   LINEA    `fichero:N` → el fichero existe (por ruta, o por nombre si va sin directorio) y tiene ≥ N líneas
#   SIMBOLO  `paquete.Simbolo`, `Simbolo()` o `TestAlgo` → aparece como palabra en algún .go
#   ENTORNO  `WAPP_X` → `X` o `WAPP_X` aparece en el código, el Makefile, los scripts o deploy/ (regla 7: el prefijo lo compone el loader)
#   RUTA_VIEJA  el patrón de paquetes borrados de diseno.md §4
FILES=("$@")
[ ${#FILES[@]} -eq 0 ] && FILES=(CLAUDE.md README.md documentations/*.md .claude/skills/*/SKILL.md)
SRC=(cmd internal test scripts deploy Makefile .github .golangci.yml .env.example docker-compose.yml)
OLD='internal/(bootstrap|casebank|catalogimport|contracts|degradation|diagnostics|entitlements|evidence|filtercfg|flujos|gateway|iam|inferstats|ingest|intake|intakeahead|intakes|integrations|intentcfg|llmvia|platformadmin|prompts|publicapi|reanalisis|receipts|tenantllm|tenantvars|turnoacotado)\b'
RC=0; OUT=$(mktemp)
for f in "${FILES[@]}"; do
  case "$f" in documentations/literal-aviso-sesion-pasiva.md) continue ;; esac
  n=$(grep -oE "$OLD" "$f" | wc -l | tr -d ' ')
  [ "$n" -gt 0 ] && { echo "RUTA_VIEJA $f: $n"; RC=1; }
  grep -oE '`[^`]+`' "$f" | tr -d '`' | sort -u | while IFS= read -r t; do
    # LINEA: fichero:N (y fichero:N-M)
    if printf '%s' "$t" | grep -qE '^[A-Za-z0-9_./-]+\.(go|sql|sh|yml|yaml|proto|md|mod|json):[0-9]+'; then
      p=${t%%:*}; l=$(printf '%s' "$t" | sed -E 's/^[^:]+:([0-9]+).*/\1/')
      if [ -f "$p" ]; then r=$p; else r=$(find cmd internal test scripts deploy .github .claude documentations -path "*$p" -not -path '*/worktrees/*' 2>/dev/null | head -1); fi
      if [ -z "$r" ]; then echo "LINEA $f: $t (no existe el fichero)"
      elif [ "$(wc -l < "$r")" -lt "$l" ]; then echo "LINEA $f: $t ($r tiene $(wc -l < "$r" | tr -d ' ') líneas)"; fi
      continue
    fi
    # RUTA
    if printf '%s' "$t" | grep -qE '^(\./)?(cmd|internal|test|scripts|deploy|documentations|\.github|\.claude)/[^ ]*$'; then
      p=$(printf '%s' "$t" | sed -E 's/[:#].*$//; s/\.\.\.$//; s/…$//; s/<[^>]*>/*/g; s/\{[^}]*\}/*/g')
      # shellcheck disable=SC2086
      ls -d $p >/dev/null 2>&1 || echo "RUTA $f: $t"
      continue
    fi
    # ENTORNO
    if printf '%s' "$t" | grep -qE '^WAPP_[A-Z0-9_]+$'; then
      s=${t#WAPP_}
      grep -rqE "\b(WAPP_)?$s\b" "${SRC[@]}" 2>/dev/null || echo "ENTORNO $f: $t"
      continue
    fi
    # SIMBOLO
    s=''
    if printf '%s' "$t" | grep -qE '^[a-z][a-z0-9_]*\.[A-Z][A-Za-z0-9_]*(\(\))?$'; then s=$(printf '%s' "$t" | sed -E 's/^[^.]+\.//; s/\(\)$//')
    elif printf '%s' "$t" | grep -qE '^[A-Za-z_][A-Za-z0-9_]*\(\)$'; then s=${t%()}
    elif printf '%s' "$t" | grep -qE '^Test[A-Z][A-Za-z0-9_]*$'; then s=$t; fi
    if [ -n "$s" ]; then grep -rqw --include='*.go' -- "$s" cmd internal test || echo "SIMBOLO $f: $t"; fi
  done
done > "$OUT" 2>&1
cat "$OUT"
[ -s "$OUT" ] && RC=1
echo "FALTAN=$(wc -l < "$OUT" | tr -d ' ')"
rm -f "$OUT"
exit $RC
