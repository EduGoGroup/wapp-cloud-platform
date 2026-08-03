package usecase_test

// Constantes y utilidades compartidas por los tests del paquete usecase. Vivían
// en auth_test.go, que murió con el IAM propio (identity Plan 003 · Ola 5).

const (
	testSigningKey = "hs256-material-de-firma-para-los-tests"
	testIssuer     = "wapp-iam-test"
	testTenant     = "11111111-1111-1111-1111-111111111111"
	testTenantB    = "22222222-2222-2222-2222-222222222222"
	testEmail      = "op@tenant.example"
	// testLoginPhrase es la contraseña que los tests del relé presentan al
	// doble de identity. wApp ya no la comprueba —eso es de identity desde la
	// Ola 3—; aquí solo viaja de paso.
	testLoginPhrase = "una-frase-de-acceso-larga"
)

func ptr(s string) *string { return &s }
