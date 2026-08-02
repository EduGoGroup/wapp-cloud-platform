#!/usr/bin/env bash
#
# migrate-to-identity.sh — migra usuarios y credenciales M2M de wApp a identity.
#
# Ejecuta las casillas T2.1 (usuarios) y T2.3 (API keys) del Plan 003 de identity
# («Migración de wApp a identity»). El vínculo usuario↔tenant NO se migra aquí:
# se queda en wApp, en `tenant_members`, que crea la migración 0037 (T2.2).
#
# ES REPRODUCIBLE E IDEMPOTENTE a propósito: recrear identity desde cero (seeds
# limpios) y volver a lanzar este script debe dejar la misma foto, sin fricción y
# sin borrar nada. Todos los INSERT llevan `ON CONFLICT DO NOTHING`, así que una
# segunda pasada no escribe ni rompe.
#
# CERO credenciales aquí dentro: las dos conexiones salen de variables de entorno.
#
#   WAPP_SOURCE_DB_URL   URI de la BD de wApp (ORIGEN, solo lectura).
#   IDENTITY_DB_URL      URI de la BD de identity (DESTINO, schema iam).
#   OUT_DIR              (opcional) dónde dejar el SQL generado y los logs.
#                        Por defecto: un directorio temporal nuevo.
#   DRY_RUN=1            (opcional) genera el SQL y corre las comprobaciones,
#                        pero NO escribe en el destino.
#
# Uso:
#   export WAPP_SOURCE_DB_URL='postgres://…'
#   export IDENTITY_DB_URL='postgres://…'
#   ./scripts/migrate-to-identity/migrate-to-identity.sh
#
# HIGIENE DE SECRETOS (bug 0009 del grupo): psql escupe la URI COMPLETA —password
# incluida— cuando la conexión falla. Todo stderr pasa por un filtro antes de
# llegar a la terminal. No quites `redact`: es lo que separa un error de un
# incidente de seguridad.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SQL_DIR="${SCRIPT_DIR}/sql"

# ── Higiene de secretos ──────────────────────────────────────────────────────
# Tapa passwords de Neon (npg_…) y cualquier `user:password@host` de una URI.
redact() {
    sed -E 's/npg_[A-Za-z0-9_]+/***/g; s#(postgres(ql)?://[^:/@]+:)[^@]+@#\1***@#g'
}

# psql contra el ORIGEN / el DESTINO, siempre con stderr filtrado.
psql_src() { psql "${WAPP_SOURCE_DB_URL}" -v ON_ERROR_STOP=1 "$@" 2> >(redact >&2); }
psql_dst() { psql "${IDENTITY_DB_URL}"    -v ON_ERROR_STOP=1 "$@" 2> >(redact >&2); }

log()  { printf '  %s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
die()  { printf '\nABORTA: %s\n' "$*" >&2; exit 1; }

# ── Comprobación de entorno ──────────────────────────────────────────────────
: "${WAPP_SOURCE_DB_URL:?falta WAPP_SOURCE_DB_URL (URI de la BD de wApp, origen)}"
: "${IDENTITY_DB_URL:?falta IDENTITY_DB_URL (URI de la BD de identity, destino)}"
command -v psql >/dev/null || die "psql no está en el PATH"

OUT_DIR="${OUT_DIR:-$(mktemp -d -t migrate-to-identity)}"
mkdir -p "${OUT_DIR}"
log "SQL generado y logs en: ${OUT_DIR}"
[ "${DRY_RUN:-0}" = "1" ] && log "DRY_RUN=1 — no se escribirá en el destino"

# ── 1. Pre-check del origen: la condición que aborta ─────────────────────────
step "1/6 · Pre-check del ORIGEN (duplicados de email)"
src_counts="$(psql_src -At -F'|' -f "${SQL_DIR}/00-preflight-source.sql")"
IFS='|' read -r src_total src_vivos src_borrados src_dups <<<"${src_counts}"
log "usuarios: ${src_total} totales · ${src_vivos} vivos · ${src_borrados} soft-deleted"

[ "${src_dups}" -eq 0 ] || die "hay ${src_dups} emails duplicados entre usuarios vivos del origen.
  Es un error de datos, no un caso de negocio: resuélvelo en wApp antes de migrar.
  (design.md §Migración de datos punto 2 del Plan 003 de identity.)"

src_keys="$(psql_src -At -c 'SELECT count(*) FROM public.iam_api_keys;')"
log "API keys en el origen: ${src_keys}"

# ── 2. Pre-check cruzado: ¿algún email ya está en el destino con OTRO UUID? ──
# Es el caso del ADR-0011 §3 de identity (canónico = el UUID de EduGo). Si
# aparece, la migración PARA: la fila hay que crearla con el UUID canónico y
# dejar constancia en `iam.legacy_user_map` (T2.7), y eso es una decisión
# humana, no algo que este script pueda resolver solo.
step "2/6 · Pre-check CRUZADO (colisiones de email contra el destino)"
psql_src -At -F'|' -c \
    "SELECT format('(%L::uuid,%L)', id, email) FROM public.iam_users WHERE deleted_at IS NULL ORDER BY id;" \
    > "${OUT_DIR}/src-pairs.txt"

if [ -s "${OUT_DIR}/src-pairs.txt" ]; then
    {
        printf 'WITH src(id, email) AS (VALUES\n'
        paste -sd, - < "${OUT_DIR}/src-pairs.txt"
        printf ')\nSELECT s.id, u.id, u.email FROM iam.users u JOIN src s ON s.email = u.email WHERE u.id <> s.id;\n'
    } > "${OUT_DIR}/preflight-collisions.sql"

    colisiones="$(psql_dst -At -F'|' -f "${OUT_DIR}/preflight-collisions.sql")"
    if [ -n "${colisiones}" ]; then
        printf '%s\n' "${colisiones}" >&2
        die "el email de arriba ya existe en identity con OTRO UUID.
  Aplica el ADR-0011 §3 (el UUID canónico es el de EduGo), puebla iam.legacy_user_map
  y remapea las filas de wApp. NO lo resuelve este script."
    fi
    log "0 colisiones — los UUID de wApp se conservan tal cual"
else
    log "el origen no tiene usuarios vivos: nada que cruzar"
fi

# ── 3. Generación del SQL ────────────────────────────────────────────────────
step "3/6 · Generando los INSERT"
# -At: sin cabecera, sin pie y sin alinear — la salida ES el SQL, no un informe.
psql_src -At -f "${SQL_DIR}/01-gen-users.sql"    > "${OUT_DIR}/users.sql"
psql_src -At -f "${SQL_DIR}/02-gen-api-keys.sql" > "${OUT_DIR}/api-keys.sql"
gen_users="$(grep -c '^INSERT' "${OUT_DIR}/users.sql"    || true)"
gen_keys="$(grep -c  '^INSERT' "${OUT_DIR}/api-keys.sql" || true)"
log "generados: ${gen_users} INSERT de usuarios · ${gen_keys} de API keys"

[ "${gen_users}" -eq "${src_vivos}" ] || die "se generaron ${gen_users} INSERT para ${src_vivos} usuarios vivos.
  La diferencia suele ser un usuario cuyo tenant no existe en public.tenants (el JOIN lo descarta)."
[ "${gen_keys}" -eq "${src_keys}" ] || die "se generaron ${gen_keys} INSERT para ${src_keys} API keys."

if [ "${DRY_RUN:-0}" = "1" ]; then
    step "DRY_RUN · no se escribe en el destino"
    log "revisa ${OUT_DIR}/users.sql y ${OUT_DIR}/api-keys.sql"
    exit 0
fi

# ── 4. Aplicación al destino ─────────────────────────────────────────────────
# En UNA transacción por archivo: o entran todos o no entra ninguno. Un CHECK
# del destino que rechace una fila (email en minúsculas, forma del client_id,
# hash SHA-256 de 64 hex) tumba la transacción entera, que es lo correcto:
# media migración es peor que ninguna.
step "4/6 · Aplicando al DESTINO (identity)"
psql_dst --single-transaction -q -f "${OUT_DIR}/users.sql"
log "usuarios aplicados"
psql_dst --single-transaction -q -f "${OUT_DIR}/api-keys.sql"
log "API keys aplicadas"

# ── 5. Verificación de conteos ───────────────────────────────────────────────
step "5/6 · Verificación"
dst_counts="$(psql_dst -At -F'|' -f "${SQL_DIR}/03-verify-target.sql")"
IFS='|' read -r dst_users dst_keys_wapp dst_keys_total dst_legacy <<<"${dst_counts}"
log "identity · iam.users = ${dst_users} (origen vivos: ${src_vivos})"
log "identity · iam.api_keys ecosystem_key='wapp' = ${dst_keys_wapp} (origen: ${src_keys}); total en la tabla: ${dst_keys_total}"
log "identity · iam.legacy_user_map ecosystem='wapp' = ${dst_legacy}"

[ "${dst_users}" -ge "${src_vivos}" ]     || die "faltan usuarios en el destino: ${dst_users} < ${src_vivos}"
[ "${dst_keys_wapp}" -ge "${src_keys}" ]  || die "faltan API keys en el destino: ${dst_keys_wapp} < ${src_keys}"

# ── 6. Verificación fila a fila de lo que no puede cambiar ───────────────────
# Los conteos no ven un hash corrupto. Esto compara ORIGEN contra DESTINO en lo
# que la migración promete dejar INTACTO: el UUID, el email y el hash bcrypt —
# si el hash cambiara, la contraseña del usuario dejaría de funcionar.
step "6/6 · Verificación fila a fila (UUID · email · hash bcrypt)"
psql_src -At -F'|' -c \
    "SELECT id, email, md5(password_hash) FROM public.iam_users WHERE deleted_at IS NULL ORDER BY id;" \
    > "${OUT_DIR}/fingerprint-src.txt"
psql_dst -At -F'|' -c \
    "SELECT id, email, md5(password_hash) FROM iam.users ORDER BY id;" \
    > "${OUT_DIR}/fingerprint-dst.txt"

if diff -q "${OUT_DIR}/fingerprint-src.txt" "${OUT_DIR}/fingerprint-dst.txt" >/dev/null; then
    log "idénticos: los ${src_vivos} usuarios llegaron con su UUID, su email y su hash intactos"
else
    # El destino puede tener MÁS usuarios que wApp (EduGo, cuentas cross): eso no
    # es un fallo. Lo que sí lo es: que un usuario del origen no esté, o esté distinto.
    faltan="$(comm -23 "${OUT_DIR}/fingerprint-src.txt" "${OUT_DIR}/fingerprint-dst.txt")"
    [ -z "${faltan}" ] || { printf '%s\n' "${faltan}" >&2; die "los usuarios de arriba no están en el destino, o llegaron modificados"; }
    log "los ${src_vivos} usuarios de wApp están intactos; el destino tiene además otros (EduGo o cuentas cross)"
fi

printf '\nOK — migración completa. Artefactos en %s\n' "${OUT_DIR}"
