package modules

// Coerciones tolerantes de valores de un Payload de efecto (map[string]any). Toleran
// tanto el tipo NATIVO (efecto construido en proceso) como el round-trip JSON (los
// números vuelven como float64). Valor ausente o de otro tipo ⇒ cero. Las comparten
// los proyectores de módulo y el WebhookSink para parsear la MISMA forma de payload
// sin duplicar la lógica (Plan 027 · Ola 3 · T8).

// AsFloat coacciona un valor de payload a float64.
func AsFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

// AsInt coacciona un valor de payload a int.
func AsInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

// AsString coacciona un valor de payload a string ("" si no es string).
func AsString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
