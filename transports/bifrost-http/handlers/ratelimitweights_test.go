package handlers

import (
	"context"
	"testing"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// TestRateLimitFromRequestFields_PassesThroughTokenWeights is a regression test
// for a gap found during manual e2e verification of issue #3834: an earlier
// version of rateLimitFromRequestFields (used by VK/team/customer/model-limit
// create+update paths) dropped InputTokenWeight/OutputTokenWeight entirely,
// so weights set via the API were silently discarded even though the
// standalone CreateRateLimitRequest/UpdateRateLimitRequest DTOs carried them.
func TestRateLimitFromRequestFields_PassesThroughTokenWeights(t *testing.T) {
	tokenMax := int64(1000)
	tokenDur := "1h"
	reqMax := int64(100)
	reqDur := "1h"
	inputWeight := 0.2
	outputWeight := 1.0

	rl := rateLimitFromRequestFields(&tokenMax, &tokenDur, &reqMax, &reqDur, &inputWeight, &outputWeight)

	assert.Equal(t, &tokenMax, rl.TokenMaxLimit)
	assert.Equal(t, &tokenDur, rl.TokenResetDuration)
	assert.Equal(t, &reqMax, rl.RequestMaxLimit)
	assert.Equal(t, &reqDur, rl.RequestResetDuration)
	require.NotNil(t, rl.InputTokenWeight)
	require.NotNil(t, rl.OutputTokenWeight)
	assert.Equal(t, 0.2, *rl.InputTokenWeight)
	assert.Equal(t, 1.0, *rl.OutputTokenWeight)
}

// TestRateLimitFromRequestFields_NilWeights_StayNil covers the backward-compat
// case: omitted weights must remain nil (not silently default to 1.0 in the
// stored struct — the 1.0 default is applied at consumption time, not here).
func TestRateLimitFromRequestFields_NilWeights_StayNil(t *testing.T) {
	tokenMax := int64(1000)
	tokenDur := "1h"

	rl := rateLimitFromRequestFields(&tokenMax, &tokenDur, nil, nil, nil, nil)

	assert.Nil(t, rl.InputTokenWeight)
	assert.Nil(t, rl.OutputTokenWeight)
}

// TestValidateRateLimit_RejectsNonPositiveWeights is a regression test for a
// gap found during review: validateRateLimit (the HTTP-layer check that
// returns a clean 400) validated every rate-limit field except the new
// InputTokenWeight/OutputTokenWeight, so an invalid weight passed the handler
// check and only failed later inside the DB transaction's BeforeSave hook —
// surfacing as a 500 instead of a 400.
func TestValidateRateLimit_RejectsNonPositiveWeights(t *testing.T) {
	zero := 0.0
	negative := -1.5
	valid := 1.0

	tests := []struct {
		name         string
		inputWeight  *float64
		outputWeight *float64
		wantErr      bool
	}{
		{"nil weights valid", nil, nil, false},
		{"positive weights valid", &valid, &valid, false},
		{"zero input weight rejected", &zero, &valid, true},
		{"negative input weight rejected", &negative, &valid, true},
		{"zero output weight rejected", &valid, &zero, true},
		{"negative output weight rejected", &valid, &negative, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rl := &configstoreTables.TableRateLimit{
				InputTokenWeight:  tt.inputWeight,
				OutputTokenWeight: tt.outputWeight,
			}
			err := validateRateLimit(rl)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestIsRateLimitRemovalRequest_WeightOnlyUpdateIsNotRemoval is a regression
// test for a critical bug found during review: isRateLimitRemovalRequest only
// checked TokenMaxLimit/RequestMaxLimit/TokenResetDuration/RequestResetDuration
// for nil-ness, so an update payload that set ONLY a weight (e.g.
// {"input_token_weight": 2.5} on an entity that already has token_max_limit
// configured) was misclassified as "remove the rate limit" — silently
// deleting the existing limits, the opposite of the caller's intent.
func TestIsRateLimitRemovalRequest_WeightOnlyUpdateIsNotRemoval(t *testing.T) {
	weight := 2.5

	tests := []struct {
		name        string
		req         *UpdateRateLimitRequest
		wantRemoval bool
	}{
		{"nil request is not removal (callers guard req.RateLimit != nil first)", nil, false},
		{"all fields nil is removal", &UpdateRateLimitRequest{}, true},
		{"weight-only update is NOT removal", &UpdateRateLimitRequest{InputTokenWeight: &weight}, false},
		{"output-weight-only update is NOT removal", &UpdateRateLimitRequest{OutputTokenWeight: &weight}, false},
		{"limit set is not removal", &UpdateRateLimitRequest{TokenMaxLimit: int64Ptr(1000)}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRateLimitRemovalRequest(tt.req)
			assert.Equal(t, tt.wantRemoval, got)
		})
	}
}

func int64Ptr(v int64) *int64 { return &v }

// TestUpdateTeam_WeightOnlyUpdatePreservesExistingLimits is an end-to-end
// handler-level regression test for a critical bug found during a second
// review round: the removal-detection fix (isRateLimitRemovalRequest /
// rateLimitIsEmpty) correctly stopped classifying a weight-only update as a
// full removal, but the "update existing rate limit" branch right after that
// check still blindly overwrote TokenMaxLimit/RequestMaxLimit with nil when
// the request omitted them — silently disabling enforcement while the rate
// limit row stayed "configured" (worse than the original bug: a delete is at
// least visible, this masks the limit's disappearance). A PUT that sets ONLY
// input_token_weight on a team with pre-existing token/request limits must
// preserve those limits exactly.
func TestUpdateTeam_WeightOnlyUpdatePreservesExistingLimits(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	handler := &GovernanceHandler{
		configStore:       store,
		governanceManager: pricingOverrideTestGovernanceManager{},
	}
	ctx := context.Background()

	tokenMax := int64(100000)
	tokenDur := "1h"
	requestMax := int64(1000)
	requestDur := "1h"
	rateLimit := &configstoreTables.TableRateLimit{
		ID:                   "rl-weight-only-update",
		TokenMaxLimit:        &tokenMax,
		TokenResetDuration:   &tokenDur,
		RequestMaxLimit:      &requestMax,
		RequestResetDuration: &requestDur,
	}
	require.NoError(t, store.CreateRateLimit(ctx, rateLimit))

	team := &configstoreTables.TableTeam{
		ID:          "team-weight-only-update",
		Name:        "weight-only-update-team",
		RateLimitID: &rateLimit.ID,
	}
	require.NoError(t, store.CreateTeam(ctx, team))

	// PUT with ONLY input_token_weight set - no token_max_limit/request_max_limit/durations.
	putCtx := newGovernanceTeamIDCtx(team.ID, `{"rate_limit":{"input_token_weight":5.0}}`)
	handler.updateTeam(putCtx)
	require.Equal(t, fasthttp.StatusOK, putCtx.Response.StatusCode(), "body=%s", putCtx.Response.Body())

	updatedTeam, err := store.GetTeam(ctx, team.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedTeam.RateLimitID, "rate limit must not have been removed by a weight-only update")

	updatedRateLimit, err := store.GetRateLimit(ctx, *updatedTeam.RateLimitID)
	require.NoError(t, err)
	require.NotNil(t, updatedRateLimit.TokenMaxLimit, "token_max_limit must be preserved, not wiped, by an update that omits it")
	assert.Equal(t, tokenMax, *updatedRateLimit.TokenMaxLimit)
	require.NotNil(t, updatedRateLimit.RequestMaxLimit, "request_max_limit must be preserved, not wiped, by an update that omits it")
	assert.Equal(t, requestMax, *updatedRateLimit.RequestMaxLimit)
	require.NotNil(t, updatedRateLimit.InputTokenWeight)
	assert.Equal(t, 5.0, *updatedRateLimit.InputTokenWeight, "the weight that was actually sent must be applied")
}
