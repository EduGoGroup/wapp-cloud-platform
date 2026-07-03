package objectstore

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// R2Config son los parámetros de arranque del cliente Cloudflare R2. Los rellena
// el main desde config.StorageConfig (env WAPP_STORAGE_S3_*). Endpoint es el S3
// de R2 (https://<accountid>.r2.cloudflarestorage.com); Region la exige el SDK
// aunque R2 la ignore.
type R2Config struct {
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	Endpoint        string
	PresignExpiry   time.Duration
}

// headBucketAPI abstrae HeadBucket para poder probar la validación fail-fast sin
// golpear R2 real. El *s3.Client real la satisface.
type headBucketAPI interface {
	HeadBucket(ctx context.Context, params *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
}

// NewR2PresignClient construye el cliente S3 apuntado a Cloudflare R2 y valida el
// bucket con HeadBucket (fail-fast al arrancar: si el bucket o las credenciales
// faltan, el proceso no levanta). Devuelve el puerto PresignClient. Copia-
// adaptación de edugo-shared/bootstrap/s3/factory.go (namespace wApp; R2 es
// virtual-hosted ⇒ UsePathStyle=false).
func NewR2PresignClient(ctx context.Context, cfg R2Config) (PresignClient, error) {
	creds := credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(creds),
	)
	if err != nil {
		return nil, fmt.Errorf("objectstore: load AWS config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint) // endpoint R2 (no AWS)
		o.UsePathStyle = false                    // R2 = virtual-hosted, NO path-style
	})

	if err := validateBucket(ctx, client, cfg.Bucket); err != nil {
		return nil, err
	}

	return newPresignClient(s3.NewPresignClient(client), cfg.Bucket, cfg.PresignExpiry), nil
}

// validateBucket comprueba que el bucket existe y es accesible (fail-fast). Se
// aísla del constructor para poder inyectar un doble en pruebas.
func validateBucket(ctx context.Context, api headBucketAPI, bucket string) error {
	if _, err := api.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)}); err != nil {
		return fmt.Errorf("objectstore: bucket %q no accesible: %w", bucket, err)
	}
	return nil
}
