package crypto

import (
	"context"
	"fmt"

	kmsapi "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
)

// Este archivo es el ÚNICO que importa el SDK de GCP. Todo lo demás del paquete
// habla con la interfaz KMSDecrypter, así que el provider se prueba con un doble y
// el SDK no se filtra al resto del código (ADR-0004: adaptador en el borde).

// gcpKMSDecrypter adapta el cliente de KMS de GCP a KMSDecrypter.
type gcpKMSDecrypter struct {
	client *kmsapi.KeyManagementClient
}

// closableDecrypter es un KMSDecrypter con vida propia: NewKMSKeyProvider lo abre,
// desenvuelve el keyring y lo CIERRA en el mismo arranque. No queda conexión viva
// contra el KMS: cero llamadas (y cero gasto) después de arrancar.
type closableDecrypter interface {
	KMSDecrypter
	Close() error
}

// newGCPKMSDecrypter abre el cliente de KMS con las credenciales por defecto de la
// aplicación (ADC: metadata del servicio en GCP, o GOOGLE_APPLICATION_CREDENTIALS
// fuera). No valida la llave todavía: el primer Decrypt es el que falla claro si
// falta el permiso cloudkms.cryptoKeyVersions.useToDecrypt o la llave no existe.
func newGCPKMSDecrypter(ctx context.Context) (closableDecrypter, error) {
	client, err := kmsapi.NewKeyManagementClient(ctx)
	if err != nil {
		return nil, err
	}
	return &gcpKMSDecrypter{client: client}, nil
}

// Decrypt desenvuelve ciphertext con la CryptoKey keyName. El KMS elige la versión
// de llave a partir del propio ciphertext, así que rotar la llave del KMS no
// invalida los keyring ya cifrados con versiones anteriores.
func (d *gcpKMSDecrypter) Decrypt(ctx context.Context, keyName string, ciphertext []byte) ([]byte, error) {
	resp, err := d.client.Decrypt(ctx, &kmspb.DecryptRequest{
		Name:       keyName,
		Ciphertext: ciphertext,
	})
	if err != nil {
		return nil, err
	}
	if len(resp.GetPlaintext()) == 0 {
		return nil, fmt.Errorf("el KMS devolvió un plaintext vacío para la llave %s", keyName)
	}
	return resp.GetPlaintext(), nil
}

// Close cierra la conexión gRPC con el KMS.
func (d *gcpKMSDecrypter) Close() error { return d.client.Close() }
