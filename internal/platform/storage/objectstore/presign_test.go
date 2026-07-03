package objectstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// fakePresigner es un doble de s3Presigner: no golpea R2/AWS, captura lo que se
// le pasa y devuelve una URL fija (o un error inyectado).
type fakePresigner struct {
	url        string
	err        error
	gotBucket  string
	gotKey     string
	gotExpires time.Duration
}

func (f *fakePresigner) capture(bucket, key string, optFns []func(*s3.PresignOptions)) {
	f.gotBucket = bucket
	f.gotKey = key
	o := &s3.PresignOptions{}
	for _, fn := range optFns {
		fn(o)
	}
	f.gotExpires = o.Expires
}

func (f *fakePresigner) PresignGetObject(_ context.Context, params *s3.GetObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	f.capture(aws.ToString(params.Bucket), aws.ToString(params.Key), optFns)
	if f.err != nil {
		return nil, f.err
	}
	return &v4.PresignedHTTPRequest{URL: f.url, Method: "GET"}, nil
}

func (f *fakePresigner) PresignPutObject(_ context.Context, params *s3.PutObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	f.capture(aws.ToString(params.Bucket), aws.ToString(params.Key), optFns)
	if f.err != nil {
		return nil, f.err
	}
	return &v4.PresignedHTTPRequest{URL: f.url, Method: "PUT"}, nil
}

func TestGenerateDownloadURL(t *testing.T) {
	const (
		bucket = "edugo-materials"
		key    = "wapp/media/errores-a-revisar.pdf"
		want   = "https://r2.example/presigned-get"
		expiry = 15 * time.Minute
	)
	fp := &fakePresigner{url: want}
	c := newPresignClient(fp, bucket, expiry)

	before := time.Now()
	url, expiresAt, err := c.GenerateDownloadURL(context.Background(), key)
	after := time.Now()
	if err != nil {
		t.Fatalf("GenerateDownloadURL error inesperado: %v", err)
	}
	if url != want {
		t.Errorf("url: got %q, want %q", url, want)
	}
	if fp.gotBucket != bucket || fp.gotKey != key {
		t.Errorf("presign recibió bucket=%q key=%q; want %q/%q", fp.gotBucket, fp.gotKey, bucket, key)
	}
	if fp.gotExpires != expiry {
		t.Errorf("expiry pasado al SDK: got %v, want %v", fp.gotExpires, expiry)
	}
	// expiresAt ≈ now+expiry (dentro de la ventana [before, after] de la llamada).
	if expiresAt.Before(before.Add(expiry)) || expiresAt.After(after.Add(expiry)) {
		t.Errorf("expiresAt %v fuera de [%v, %v]", expiresAt, before.Add(expiry), after.Add(expiry))
	}
}

func TestGenerateUploadURL(t *testing.T) {
	const want = "https://r2.example/presigned-put"
	fp := &fakePresigner{url: want}
	c := newPresignClient(fp, "edugo-materials", 15*time.Minute)

	url, expiresAt, err := c.GenerateUploadURL(context.Background(), "wapp/media/x.pdf")
	if err != nil {
		t.Fatalf("GenerateUploadURL error inesperado: %v", err)
	}
	if url != want {
		t.Errorf("url: got %q, want %q", url, want)
	}
	if expiresAt.IsZero() {
		t.Error("expiresAt no debería ser cero en éxito")
	}
}

func TestGenerateDownloadURL_PropagaError(t *testing.T) {
	wantErr := errors.New("boom")
	c := newPresignClient(&fakePresigner{err: wantErr}, "b", 15*time.Minute)

	_, _, err := c.GenerateDownloadURL(context.Background(), "k")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error: got %v, want envoltorio de %v", err, wantErr)
	}
}

func TestNewPresignClient_ExpiryDefault(t *testing.T) {
	c := newPresignClient(&fakePresigner{}, "b", 0)
	if c.expiry != defaultPresignExpiry {
		t.Errorf("expiry con 0: got %v, want default %v", c.expiry, defaultPresignExpiry)
	}
}

// fakeHeadBucket es un doble de headBucketAPI para probar la validación fail-fast.
type fakeHeadBucket struct {
	err error
}

func (f *fakeHeadBucket) HeadBucket(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &s3.HeadBucketOutput{}, nil
}

func TestValidateBucket_FailFast(t *testing.T) {
	wantErr := errors.New("403 forbidden")
	err := validateBucket(context.Background(), &fakeHeadBucket{err: wantErr}, "edugo-materials")
	if !errors.Is(err, wantErr) {
		t.Fatalf("validateBucket debería fallar envolviendo %v; got %v", wantErr, err)
	}
}

func TestValidateBucket_OK(t *testing.T) {
	if err := validateBucket(context.Background(), &fakeHeadBucket{}, "edugo-materials"); err != nil {
		t.Fatalf("validateBucket no debería fallar con bucket accesible: %v", err)
	}
}
