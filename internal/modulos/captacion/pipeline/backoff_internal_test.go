package pipeline

// backoff_internal_test.go — los dos auxiliares no exportados de backoff.go que llevan
// regla de negocio (E-4): la clasificación del fallo y la curva del castigo. La política
// vista desde el worker —techos, desenlaces, marcas— está en backoff_test.go; aquí queda
// lo que desde allí no se alcanza (un intento menor que 1) o se diagnosticaría mal.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
)

// TestCauseOf_ClassifiesByFamilyAndQualityGoesFirst: una tabla con cada familia y con las
// mezclas que deciden el ORDEN de las ramas.
func TestCauseOf_ClassifiesByFamilyAndQualityGoesFirst(t *testing.T) {
	integrity := fmt.Errorf("store: upsert: %w", &pgconn.PgError{Code: "23505"})
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"bare quality", llm.ErrLLMQuality, CauseQuality},
		{"quality wrapped twice", fmt.Errorf("p3: %w", fmt.Errorf("campo vacío: %w", llm.ErrLLMQuality)), CauseQuality},
		{"quality next to an integrity violation", fmt.Errorf("%w y %w", integrity, llm.ErrLLMQuality), CauseQuality},
		{"quality next to a missing literal", fmt.Errorf("%w y %w", stages.ErrNoLiteral, llm.ErrLLMQuality), CauseQuality},
		{"missing literal", fmt.Errorf("p2: %w", stages.ErrNoLiteral), CauseInvalidJob},
		{"unique violation", integrity, CauseInvalidJob},
		{"not null violation", &pgconn.PgError{Code: "23502"}, CauseInvalidJob},
		{"foreign key violation", &pgconn.PgError{Code: "23503"}, CauseInvalidJob},
		{"check violation", &pgconn.PgError{Code: "23514"}, CauseInvalidJob},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, CauseInfra},
		{"serialization failure", &pgconn.PgError{Code: "40001"}, CauseInfra},
		{"deadline", context.DeadlineExceeded, CauseInfra},
		{"cancelled", context.Canceled, CauseInfra},
		{"job no longer processing", stages.ErrJobNotProcessing, CauseInfra},
		{"anything else", errors.New("dial tcp: connection refused"), CauseInfra},
	}
	for _, c := range cases {
		if got := causeOf(c.err); got != c.want {
			t.Errorf("%s: causeOf = %q, se esperaba %q", c.name, got, c.want)
		}
	}
}

// bounds muestrea la curva de UN intento y devuelve el mínimo y el máximo vistos.
func bounds(attempt int, base, ceiling time.Duration) (low, high time.Duration) {
	low = backoffFor(attempt, base, ceiling)
	for range 256 {
		d := backoffFor(attempt, base, ceiling)
		low, high = min(low, d), max(high, d)
	}
	return low, high
}

// TestBackoffFor_Curve: `base × 2^(N−1)` topado en el techo, con el exponente acotado a 12
// y un intento menor que 1 tratado como el primero. Cada muestra cae en `[0,8·d; 1,2·d)`.
func TestBackoffFor_Curve(t *testing.T) {
	const base = 30 * time.Second
	cases := []struct {
		attempt int
		ceiling time.Duration
		want    time.Duration
	}{
		{-3, 5 * time.Minute, base},
		{0, 5 * time.Minute, base},
		{1, 5 * time.Minute, base},
		{2, 5 * time.Minute, 2 * base},
		{3, 5 * time.Minute, 4 * base},
		{4, 5 * time.Minute, 8 * base},
		{5, 5 * time.Minute, 5 * time.Minute},
		{50, 5 * time.Minute, 5 * time.Minute},
		{12, 10000 * time.Hour, base << 11},
		{13, 10000 * time.Hour, base << 12},
		{14, 10000 * time.Hour, base << 12},
		{200, 10000 * time.Hour, base << 12},
	}
	for _, c := range cases {
		low, high := bounds(c.attempt, base, c.ceiling)
		floor, roof := time.Duration(float64(c.want)*0.8), time.Duration(float64(c.want)*1.2)
		if low < floor || high >= roof {
			t.Errorf("intento %d: muestras en [%s, %s], se esperaban dentro de [%s, %s)", c.attempt, low, high, floor, roof)
		}
		// El jitter usa el rango ENTERO: con 257 muestras las cotas vistas se acercan a las
		// teóricas. Un jitter más estrecho (o ausente) no llegaría.
		if spread := float64(high-low) / float64(c.want); spread < 0.3 {
			t.Errorf("intento %d: el jitter solo dispersa un %.0f %% del castigo, se esperaba cerca del 40 %%", c.attempt, spread*100)
		}
	}
}
