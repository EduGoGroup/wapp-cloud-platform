package iamidentity

// Parte de los tests de m2m.go (F2-05): el timeout por defecto. Va en el paquete, no en
// iamidentity_test, porque desde fuera solo se vería esperando los 10 s (mutante vivo de F2-05).

import (
	"testing"
	"time"
)

// TestNewM2M_NonPositiveTimeoutUsesDefault: timeout <= 0 usa el de por defecto, y uno positivo se
// respeta. Con el 0 exacto sin cubrir, el cliente quedaría sin plazo frente a un identity colgado.
func TestNewM2M_NonPositiveTimeoutUsesDefault(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{"zero", 0, defaultTimeout},
		{"negative", -time.Second, defaultTimeout},
		{"positive", 3 * time.Second, 3 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			m, err := NewM2M("https://identity.invalid", "api-key-de-prueba", c.timeout)
			if err != nil {
				t.Fatalf("NewM2M: %v", err)
			}
			if got := m.http.Timeout; got != c.want {
				t.Errorf("timeout del cliente HTTP = %v, quería %v", got, c.want)
			}
		})
	}
}
