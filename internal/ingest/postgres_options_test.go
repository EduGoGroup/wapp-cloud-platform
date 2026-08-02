package ingest

import (
	"testing"
	"time"
)

func TestPostgresDeduper_Options_Table(t *testing.T) {
	tests := []struct {
		name           string
		opts           []Option
		wantRetention  time.Duration
		wantSweepEvery uint64
		wantSweepBatch int
	}{
		{
			name: "custom valid options",
			opts: []Option{
				WithRetention(24 * time.Hour),
				WithSweep(100, 50),
			},
			wantRetention:  24 * time.Hour,
			wantSweepEvery: 100,
			wantSweepBatch: 50,
		},
		{
			name: "zero values retain default settings",
			opts: []Option{
				WithRetention(0),
				WithSweep(0, 0),
			},
			wantRetention:  defaultRetention,
			wantSweepEvery: defaultSweepEvery,
			wantSweepBatch: defaultSweepBatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deduper := NewPostgresDeduper(nil, tt.opts...)
			if deduper.retention != tt.wantRetention {
				t.Errorf("retention = %v, quiero %v", deduper.retention, tt.wantRetention)
			}
			if deduper.sweepEvery != tt.wantSweepEvery {
				t.Errorf("sweepEvery = %d, quiero %d", deduper.sweepEvery, tt.wantSweepEvery)
			}
			if deduper.sweepBatch != tt.wantSweepBatch {
				t.Errorf("sweepBatch = %d, quiero %d", deduper.sweepBatch, tt.wantSweepBatch)
			}
		})
	}
}
