//go:build pendiente

package apipublica

// deadlines_test.go — cubre deadlines.go. Es un test INTERNO (package apipublica) porque, salvo
// SendBudgetFrom, lo que deadlines.go promete vive en auxiliares no exportados, que nacen con el
// verde (05 E-4, P6).

import (
	"testing"
	"time"
)

func TestSendBudgetFrom(t *testing.T) {
	cases := []struct {
		name         string
		writeTimeout time.Duration
		want         time.Duration
	}{
		{"default_write_timeout_leaves_nine_seconds", 10 * time.Second, 9 * time.Second},
		{"just_above_the_margin", time.Second + time.Millisecond, time.Millisecond},
		{"exactly_the_margin_is_no_budget", time.Second, 0},
		{"below_the_margin_is_no_budget", 500 * time.Millisecond, 0},
		{"zero_is_no_budget", 0, 0},
		{"negative_is_no_budget", -5 * time.Second, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SendBudgetFrom(tc.writeTimeout); got != tc.want {
				t.Errorf("SendBudgetFrom(%v) = %v, quiero %v", tc.writeTimeout, got, tc.want)
			}
		})
	}
}
