package logging

import (
	"log/slog"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":       slog.LevelDebug,
		"info":        slog.LevelInfo,
		"warn":        slog.LevelWarn,
		"warning":     slog.LevelWarn,
		"error":       slog.LevelError,
		"":            slog.LevelInfo,
		"desconocido": slog.LevelInfo,
		"  DEBUG  ":   slog.LevelDebug,
	}

	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q): got %v, want %v", in, got, want)
		}
	}
}

func TestNew_NoPanic(t *testing.T) {
	log := New(config.AppConfig{LogLevel: "debug", LogJSON: true})
	if log == nil {
		t.Fatal("New devolvió un logger nil")
	}
	// No debe panickear al emitir.
	log.Info("prueba", "k", "v")
	log.With("ctx", "test").Debug("hijo")
}
