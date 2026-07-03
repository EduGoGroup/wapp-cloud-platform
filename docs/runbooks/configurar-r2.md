# Runbook — Configurar Cloudflare R2 (Plan 017 · almacén de media)

> Acción manual del usuario (estilo runbook Neon del MP-02). **Estado (2026-07-03):
> YA cableado** en el `.env` local. Este documento deja el rastro reproducible.

## Qué es

El Plan 017 envía archivos (PDF/imagen) por WhatsApp desde un flujo. El binario se
guarda en **Cloudflare R2** (S3-compatible; **NO** AWS, sin costo en alpha). La nube
genera una **URL prefirmada de corta vida** (15 min) y el Edge la descarga y la sube
a WhatsApp. La nube nunca entrega las credenciales de R2 al Edge (zero-knowledge,
ADR-0007/0009).

En **alpha** wApp comparte la cuenta de Cloudflare con EduGo y **reutiliza el bucket
`edugo-materials`** con un **prefijo propio `wapp/`** en las keys (en vez de crear un
bucket dedicado). El mismo R2 sirve dev y prod (sin MinIO local).

## Pasos

1. **Bucket.** En el panel R2 de Cloudflare (misma cuenta EduGo) el bucket
   **`edugo-materials`** ya existe. wApp escribe bajo el prefijo **`wapp/`** (p. ej.
   `wapp/media/errores-a-revisar.pdf`). Para un bucket propio en el futuro basta con
   cambiar `WAPP_STORAGE_S3_BUCKET`.

2. **API Token R2.** Crea (o reutiliza) un token R2 con acceso al bucket. Anota:
   - `Access Key ID`
   - `Secret Access Key`
   - Endpoint S3: `https://<ACCOUNT_ID>.r2.cloudflarestorage.com`

3. **`.env` (NO versionado).** Rellena las claves con los valores reales:

   ```sh
   WAPP_STORAGE_S3_REGION=us-east-1        # R2 la ignora, el SDK la exige
   WAPP_STORAGE_S3_BUCKET=edugo-materials
   WAPP_STORAGE_S3_ENDPOINT=https://<ACCOUNT_ID>.r2.cloudflarestorage.com
   WAPP_STORAGE_S3_ACCESS_KEY_ID=<token R2>
   WAPP_STORAGE_S3_SECRET_ACCESS_KEY=<token R2>
   WAPP_STORAGE_S3_PRESIGN_EXPIRY=15m
   ```

   La plantilla versionada (sin secretos) está en `.env.example`. **Nunca** commitees
   una credencial. Carga el `.env` en la shell antes de arrancar:

   ```sh
   set -a; . ./.env; set +a
   ```

4. **Fail-fast.** Al arrancar, el adaptador R2 valida el bucket con `HeadBucket`
   (`internal/platform/storage/objectstore/r2_factory.go`): si el bucket o las
   credenciales faltan, el proceso **no levanta**. Mismo R2 en dev y prod.

5. **Seed del e2e (T6).** El script de seed sube los archivos de prueba a
   `s3://edugo-materials/wapp/media/…` usando **estas mismas credenciales**; el nodo
   `media` del flujo referencia esas keys. (El upload por API es Plan 018.)

## Referencias

- Diseño: `../../../docs/plans/017-pdf-media/design.md` §3 y §8.
- Config: `internal/platform/config/config.go` (`StorageConfig`, claves `WAPP_STORAGE_S3_*`).
- Adaptador: `internal/platform/storage/objectstore/` (`PresignClient`, `NewR2PresignClient`).
