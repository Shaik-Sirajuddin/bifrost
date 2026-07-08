package tables

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func float64Ptr(v float64) *float64 { return &v }

func TestRateLimitBeforeSave_WeightsOmitted_DefaultsToBackwardCompatible(t *testing.T) {
	limit := int64(1000)
	duration := "1h"
	rl := &TableRateLimit{
		ID:                 "rl-1",
		TokenMaxLimit:      &limit,
		TokenResetDuration: &duration,
	}

	require.NoError(t, rl.BeforeSave(nil))
	assert.Nil(t, rl.InputTokenWeight, "omitted weight must stay nil, not default to a stored 1.0")
	assert.Nil(t, rl.OutputTokenWeight)
}

func TestRateLimitBeforeSave_PositiveWeights_Valid(t *testing.T) {
	limit := int64(1000)
	duration := "1h"
	rl := &TableRateLimit{
		ID:                 "rl-2",
		TokenMaxLimit:      &limit,
		TokenResetDuration: &duration,
		InputTokenWeight:   float64Ptr(0.2),
		OutputTokenWeight:  float64Ptr(1.0),
	}

	require.NoError(t, rl.BeforeSave(nil))
}

func TestRateLimitBeforeSave_WeightsGreaterThanOne_Valid(t *testing.T) {
	// Per issue #3834: weights are ratio-only, no upper bound.
	limit := int64(1000)
	duration := "1h"
	rl := &TableRateLimit{
		ID:                 "rl-3",
		TokenMaxLimit:      &limit,
		TokenResetDuration: &duration,
		InputTokenWeight:   float64Ptr(1.0),
		OutputTokenWeight:  float64Ptr(5.0),
	}

	require.NoError(t, rl.BeforeSave(nil))
}

func TestRateLimitBeforeSave_ZeroOrNegativeWeights_Rejected(t *testing.T) {
	limit := int64(1000)
	duration := "1h"

	tests := []struct {
		name   string
		input  *float64
		output *float64
	}{
		{"zero input weight", float64Ptr(0), float64Ptr(1.0)},
		{"negative input weight", float64Ptr(-0.5), float64Ptr(1.0)},
		{"zero output weight", float64Ptr(1.0), float64Ptr(0)},
		{"negative output weight", float64Ptr(1.0), float64Ptr(-2)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rl := &TableRateLimit{
				ID:                 "rl-invalid",
				TokenMaxLimit:      &limit,
				TokenResetDuration: &duration,
				InputTokenWeight:   tt.input,
				OutputTokenWeight:  tt.output,
			}
			assert.Error(t, rl.BeforeSave(nil))
		})
	}
}
