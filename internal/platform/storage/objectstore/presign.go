// Package objectstore provee el puerto de almacén de objetos de la Plataforma
// Cloud (Plan 017) y su adaptador sobre Cloudflare R2 (S3-compatible vía
// aws-sdk-go-v2 apuntado por BaseEndpoint; NO es AWS). El puerto entrega URLs
// prefirmadas de corta vida para que el Edge descargue (GET) o suba (PUT, Plan
// 018) un binario sin recibir jamás las credenciales de R2 (zero-knowledge,
// ADR-0007/0009). Es hermano de storage/postgres y copia-adaptación del patrón
// de edugo-shared/storage/s3 (renombrado al namespace wApp; sin importar edugo).
package objectstore

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// defaultPresignExpiry es la vigencia por defecto de las URLs prefirmadas cuando
// no se configura WAPP_STORAGE_S3_PRESIGN_EXPIRY. R2/S3 imponen un tope de 7
// días; 15 minutos es el capability token de corta vida del despacho de media.
const defaultPresignExpiry = 15 * time.Minute

// PresignClient es el puerto (hexagonal) de generación de URLs prefirmadas. El
// runtime lo consume al despachar un nodo media (§4); el módulo media NO lo
// conoce. Vive en cloud-platform (único consumidor hoy); se promueve a
// wapp-shared cuando duela (ADR-0010).
type PresignClient interface {
	// GenerateDownloadURL devuelve una URL prefirmada GET de corta vida para que
	// el Edge descargue el archivo (uso principal del Plan 017), junto con el
	// instante de expiración.
	GenerateDownloadURL(ctx context.Context, key string) (url string, expiresAt time.Time, err error)
	// GenerateUploadURL devuelve una URL prefirmada PUT — presente para el Plan
	// 018 (upload por API); NO se usa en 017.
	GenerateUploadURL(ctx context.Context, key string) (url string, expiresAt time.Time, err error)
}

// s3Presigner abstrae las operaciones de firma del *s3.PresignClient para poder
// inyectar un doble en pruebas (así los tests no golpean R2/AWS real). El
// *s3.PresignClient real la satisface.
type s3Presigner interface {
	PresignGetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	PresignPutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

// s3PresignClient es la implementación del puerto sobre aws-sdk-go-v2 apuntado a
// R2 por BaseEndpoint (ver r2_factory.go).
type s3PresignClient struct {
	presigner s3Presigner
	bucket    string
	expiry    time.Duration
}

// Verificación en compilación de que el adaptador satisface el puerto.
var _ PresignClient = (*s3PresignClient)(nil)

// newPresignClient envuelve un s3Presigner con el bucket y la vigencia. Si expiry
// es <= 0 cae al default de 15 minutos. Se mantiene privado: el arranque usa
// NewR2PresignClient (r2_factory.go), que construye el cliente S3 real.
func newPresignClient(presigner s3Presigner, bucket string, expiry time.Duration) *s3PresignClient {
	if expiry <= 0 {
		expiry = defaultPresignExpiry
	}
	return &s3PresignClient{presigner: presigner, bucket: bucket, expiry: expiry}
}

// GenerateDownloadURL firma un GET del objeto key. No fija ResponseContentDisposition
// a propósito (§3.2): el nombre visible lo pone el Edge desde SendMedia.filename.
func (c *s3PresignClient) GenerateDownloadURL(ctx context.Context, key string) (string, time.Time, error) {
	res, err := c.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(c.expiry))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("objectstore: presign GET %s: %w", key, err)
	}
	return res.URL, time.Now().Add(c.expiry), nil
}

// GenerateUploadURL firma un PUT del objeto key (Plan 018; sin uso en 017).
func (c *s3PresignClient) GenerateUploadURL(ctx context.Context, key string) (string, time.Time, error) {
	res, err := c.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(c.expiry))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("objectstore: presign PUT %s: %w", key, err)
	}
	return res.URL, time.Now().Add(c.expiry), nil
}
