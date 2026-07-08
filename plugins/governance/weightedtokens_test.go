package governance

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func float64Ptr(v float64) *float64 { return &v }

// TestWeightedTokenUsage_DefaultDisabled_MatchesFlatTotal covers issue #3834's
// backward-compat requirement: a rate limit with no weights configured must
// behave identically to today's flat prompt+completion sum.
func TestWeightedTokenUsage_DefaultDisabled_MatchesFlatTotal(t *testing.T) {
	rl := buildRateLimit("rl-default", 1_000_000, 1000)
	got := weightedTokenUsage(rl, 100, 50)
	assert.Equal(t, int64(150), got, "nil weights must reduce to prompt+completion, unweighted")
}

// TestWeightedTokenUsage_CustomRatio verifies the issue's worked example:
// input_token_weight=0.2, output_token_weight=1.0 weights output 5x input.
func TestWeightedTokenUsage_CustomRatio(t *testing.T) {
	rl := buildRateLimit("rl-ratio", 1_000_000, 1000)
	rl.InputTokenWeight = float64Ptr(0.2)
	rl.OutputTokenWeight = float64Ptr(1.0)

	// 100 prompt tokens * 0.2 + 50 completion tokens * 1.0 = 20 + 50 = 70
	got := weightedTokenUsage(rl, 100, 50)
	assert.Equal(t, int64(70), got)
}

// TestWeightedTokenUsage_RatioOnlySemantics: per the issue spec, weights are
// not restricted to 0-1 — only the ratio between them matters. {0.2,1.0} and
// {1.0,5.0} must be equivalent in relative effect (scaled by 5x here since
// absolute magnitude differs, only the input:output ratio is preserved).
func TestWeightedTokenUsage_RatioOnlySemantics(t *testing.T) {
	rlSmall := buildRateLimit("rl-small", 1_000_000, 1000)
	rlSmall.InputTokenWeight = float64Ptr(0.2)
	rlSmall.OutputTokenWeight = float64Ptr(1.0)

	rlScaled := buildRateLimit("rl-scaled", 1_000_000, 1000)
	rlScaled.InputTokenWeight = float64Ptr(1.0)
	rlScaled.OutputTokenWeight = float64Ptr(5.0)

	small := weightedTokenUsage(rlSmall, 100, 50)
	scaled := weightedTokenUsage(rlScaled, 100, 50)
	assert.Equal(t, small*5, scaled, "5x-scaled weights must produce 5x the weighted usage (ratio preserved)")
}

// TestWeightedTokenUsage_OnlyOneWeightSet: an unset weight defaults to 1.0
// independently of whether the other weight is set.
func TestWeightedTokenUsage_OnlyOneWeightSet(t *testing.T) {
	rl := buildRateLimit("rl-partial", 1_000_000, 1000)
	rl.OutputTokenWeight = float64Ptr(5.0)
	// InputTokenWeight left nil -> defaults to 1.0

	// 100*1.0 + 50*5.0 = 100 + 250 = 350
	got := weightedTokenUsage(rl, 100, 50)
	assert.Equal(t, int64(350), got)
}

// TestWeightedTokenUsage_Rounding verifies fractional weighted sums round to
// the nearest integer for storage in the int64 counter.
func TestWeightedTokenUsage_Rounding(t *testing.T) {
	rl := buildRateLimit("rl-round", 1_000_000, 1000)
	rl.InputTokenWeight = float64Ptr(0.33)
	rl.OutputTokenWeight = float64Ptr(1.0)

	// 10 * 0.33 = 3.3 -> rounds to 3
	got := weightedTokenUsage(rl, 10, 0)
	assert.Equal(t, int64(3), got)

	// 15 * 0.33 = 4.95 -> rounds to 5
	got = weightedTokenUsage(rl, 15, 0)
	assert.Equal(t, int64(5), got)
}

// TestBumpRateLimitUsage_WeightedAccounting_EndToEnd exercises the actual
// store path (not just the pure helper): a VK-level rate limit configured
// with weights must have its TokenCurrentUsage incremented by the weighted
// amount when BumpRateLimitUsage is called with a raw prompt/completion pair.
func TestBumpRateLimitUsage_WeightedAccounting_EndToEnd(t *testing.T) {
	logger := NewMockLogger()

	rl := buildRateLimit("rl-e2e", 1_000_000, 1000)
	rl.InputTokenWeight = float64Ptr(0.2)
	rl.OutputTokenWeight = float64Ptr(1.0)
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-weighted", "Weighted VK", rl)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rl},
	}, nil)
	require.NoError(t, err)

	// 1000 prompt tokens * 0.2 + 200 completion tokens * 1.0 = 200 + 200 = 400
	require.NoError(t, store.BumpRateLimitUsage(context.Background(), "rl-e2e", 1000, 200, true, true))

	updated := store.LoadRateLimit(context.Background(), "rl-e2e")
	require.NotNil(t, updated)
	assert.Equal(t, int64(400), updated.TokenCurrentUsage, "weighted usage must reflect the rate limit's own weights")
	assert.Equal(t, int64(1), updated.RequestCurrentUsage)
}

// TestBumpRateLimitUsage_DefaultDisabled_EndToEnd confirms that a rate limit
// with no weights configured accumulates the flat prompt+completion total,
// preserving today's behavior for every existing config.
func TestBumpRateLimitUsage_DefaultDisabled_EndToEnd(t *testing.T) {
	logger := NewMockLogger()

	rl := buildRateLimit("rl-flat", 1_000_000, 1000)
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-flat", "Flat VK", rl)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rl},
	}, nil)
	require.NoError(t, err)

	require.NoError(t, store.BumpRateLimitUsage(context.Background(), "rl-flat", 1000, 200, true, true))

	updated := store.LoadRateLimit(context.Background(), "rl-flat")
	require.NotNil(t, updated)
	assert.Equal(t, int64(1200), updated.TokenCurrentUsage, "unweighted rate limit must sum prompt+completion exactly like before")
}

// TestPostHookWorker_ExtractsPromptAndCompletionTokens_ChatResponse verifies
// the extraction fix in main.go: postHookWorker must read PromptTokens and
// CompletionTokens separately (not just TotalTokens) so weighting has data to
// work with end-to-end.
func TestPostHookWorker_ExtractsPromptAndCompletionTokens_ChatResponse(t *testing.T) {
	logger := NewMockLogger()

	rl := buildRateLimit("rl-post", 1_000_000, 1000)
	rl.InputTokenWeight = float64Ptr(0.2)
	rl.OutputTokenWeight = float64Ptr(1.0)
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-post", "Post VK", rl)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rl},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	plugin := &GovernancePlugin{
		ctx:     context.Background(),
		tracker: tracker,
		logger:  logger,
	}

	response := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     1000,
				CompletionTokens: 200,
				TotalTokens:      1200,
			},
		},
	}

	plugin.postHookWorker(response, nil, schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, "sk-bf-post", "req-1", "", true, 0, nil)
	time.Sleep(250 * time.Millisecond)

	updated := store.LoadRateLimit(context.Background(), "rl-post")
	require.NotNil(t, updated)
	// 1000*0.2 + 200*1.0 = 200 + 200 = 400, NOT the flat 1200 total.
	assert.Equal(t, int64(400), updated.TokenCurrentUsage, "postHookWorker must apply weighted accounting, not flat TotalTokens")
}

// TestPostHookWorker_ReconcilesTotalWhenSplitFieldsAreNil covers a regression
// found during review: some providers report only TotalTokens (e.g.
// transcription responses that omit InputTokens/OutputTokens), which would
// otherwise leave promptTokens=completionTokens=0 and silently zero out
// rate-limit consumption. postHookWorker must attribute the unaccounted total
// to promptTokens so an unweighted (default) rate limit still matches
// TotalTokens exactly, preserving the backward-compat guarantee.
func TestPostHookWorker_ReconcilesTotalWhenSplitFieldsAreNil(t *testing.T) {
	logger := NewMockLogger()

	rl := buildRateLimit("rl-nilsplit", 1_000_000, 1000)
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-nilsplit", "NilSplit VK", rl)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rl},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	plugin := &GovernancePlugin{
		ctx:     context.Background(),
		tracker: tracker,
		logger:  logger,
	}

	totalTokens := 500
	response := &schemas.BifrostResponse{
		TranscriptionResponse: &schemas.BifrostTranscriptionResponse{
			Usage: &schemas.TranscriptionUsage{
				TotalTokens: &totalTokens,
				// InputTokens/OutputTokens intentionally nil - this provider only reports a total.
			},
		},
	}

	plugin.postHookWorker(response, nil, schemas.OpenAI, "whisper-1", schemas.TranscriptionRequest, "sk-bf-nilsplit", "req-1", "", true, 0, nil)
	time.Sleep(250 * time.Millisecond)

	updated := store.LoadRateLimit(context.Background(), "rl-nilsplit")
	require.NotNil(t, updated)
	assert.Equal(t, int64(500), updated.TokenCurrentUsage, "unweighted rate limit must count the full TotalTokens even when the provider omits the prompt/completion split")
}

// TestPostHookWorker_ReconcilesTotalWhenSplitExceedsTotal covers the opposite
// divergence: a provider (e.g. Cohere embeddings) that populates PromptTokens
// but leaves TotalTokens at its zero value. Unweighted accounting must match
// TotalTokens (0), not silently over-count using the split.
func TestPostHookWorker_ReconcilesTotalWhenSplitExceedsTotal(t *testing.T) {
	logger := NewMockLogger()

	rl := buildRateLimit("rl-splitexceeds", 1_000_000, 1000)
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-splitexceeds", "SplitExceeds VK", rl)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rl},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	plugin := &GovernancePlugin{
		ctx:     context.Background(),
		tracker: tracker,
		logger:  logger,
	}

	response := &schemas.BifrostResponse{
		EmbeddingResponse: &schemas.BifrostEmbeddingResponse{
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens: 200,
				TotalTokens:  0, // provider left TotalTokens unset
			},
		},
	}

	plugin.postHookWorker(response, nil, schemas.Cohere, "embed-v3", schemas.EmbeddingRequest, "sk-bf-splitexceeds", "req-1", "", true, 0, nil)
	time.Sleep(250 * time.Millisecond)

	updated := store.LoadRateLimit(context.Background(), "rl-splitexceeds")
	require.NotNil(t, updated)
	assert.Equal(t, int64(0), updated.TokenCurrentUsage, "unweighted rate limit must match TotalTokens (0), not over-count from a stale PromptTokens field")
}

// TestPostHookWorker_ReconcilesWhenCompletionAloneExceedsTotal is a regression
// test for a bug found in an earlier version of the reconciliation fix: the
// original clamp only protected promptTokens, so when completionTokens alone
// exceeded tokensUsed (e.g. a malformed/inconsistent usage report), the
// result was promptTokens=0 with completionTokens left untouched - summing to
// MORE than tokensUsed and over-counting for an unweighted rate limit, the
// exact class of bug the reconciliation was meant to prevent. The fix clamps
// completionTokens into [0, tokensUsed] and derives promptTokens as the
// remainder, so the sum always equals tokensUsed exactly.
func TestPostHookWorker_ReconcilesWhenCompletionAloneExceedsTotal(t *testing.T) {
	logger := NewMockLogger()

	rl := buildRateLimit("rl-completionexceeds", 1_000_000, 1000)
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-completionexceeds", "CompletionExceeds VK", rl)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rl},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	tracker := NewUsageTracker(context.Background(), store, resolver, nil, logger)
	defer tracker.Cleanup()

	plugin := &GovernancePlugin{
		ctx:     context.Background(),
		tracker: tracker,
		logger:  logger,
	}

	// billedUsage reports completionTokens (100) that alone exceeds tokensUsed (50).
	response := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     5,
				CompletionTokens: 100,
				TotalTokens:      50,
			},
		},
	}

	plugin.postHookWorker(response, nil, schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, "sk-bf-completionexceeds", "req-1", "", true, 0, nil)
	time.Sleep(250 * time.Millisecond)

	updated := store.LoadRateLimit(context.Background(), "rl-completionexceeds")
	require.NotNil(t, updated)
	assert.Equal(t, int64(50), updated.TokenCurrentUsage, "unweighted rate limit must match TotalTokens (50) exactly, not over-count to 100 from an inconsistent completionTokens")
}
