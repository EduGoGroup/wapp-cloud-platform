#!/usr/bin/env bash
#
# gen-dev-certs.sh — PKI de DESARROLLO para el Gateway CloudLink de la Plataforma.
#
# Genera en certs/ (fuera de git): una CA de dev y el cert del servidor
# (SAN localhost / 127.0.0.1), firmado por la CA. La misma CA firma los certs de
# Edge vía el flujo de enrolamiento (EnrollEdge), de modo que un Edge enrolado
# completa el handshake mTLS contra este servidor (T4).
#
# Copia-adaptación de wapp-cloudlink/scripts/gen-dev-certs.sh, recortada a lo que
# necesita la Plataforma (CA + cert de servidor). Idempotente: regenera al
# re-ejecutar.
#
# §8 del Plan 005: UNA sola CA de dev (multi-CA real se endurece después).
#
# SOLO para desarrollo local. NUNCA committear claves ni certs (.gitignore
# excluye certs/). En producción los certs los emite la CA de la plataforma/tenant.
#
# Uso:
#   ./scripts/gen-dev-certs.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CERT_DIR="${REPO_ROOT}/certs"
DAYS="${DAYS:-825}"

mkdir -p "${CERT_DIR}"
cd "${CERT_DIR}"

echo "==> Generando PKI de dev en ${CERT_DIR}"

# --- CA de dev (autofirmada, EC P-256) ---
openssl ecparam -name prime256v1 -genkey -noout -out ca.key
openssl req -x509 -new -key ca.key -sha256 -days "${DAYS}" \
  -subj "/CN=wapp-dev-ca" -out ca.crt

# --- cert de servidor (SAN localhost / 127.0.0.1, serverAuth) ---
cat > server.ext <<'EOF'
basicConstraints = CA:FALSE
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = DNS:localhost, IP:127.0.0.1
EOF

openssl ecparam -name prime256v1 -genkey -noout -out server.key
openssl req -new -key server.key -subj "/CN=localhost" -out server.csr
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -sha256 -days "${DAYS}" -extfile server.ext -out server.crt

rm -f server.ext server.csr ca.srl

echo "==> Listo:"
echo "    CA       : ${CERT_DIR}/ca.crt  (+ ca.key)   firma certs de Edge (enrolamiento) y del servidor"
echo "    servidor : ${CERT_DIR}/server.crt  (+ server.key)  SAN localhost,127.0.0.1"
echo
echo "Recordatorio: certs/ está fuera de git. No committear claves ni certs."
