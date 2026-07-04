package schemas

import (
	"encoding/json"
	"testing"
)

// TestChatPromptTokensDetails_MarshalJSON_CachedTokensReadOnly is a regression test for
// #4816: cached_tokens previously folded cache-write tokens into the standard OpenAI-spec
// field (CachedReadTokens + CachedWriteTokens). Per the OpenAI spec, cached_tokens is prompt
// tokens read from cache and must not include writes — folding writes in made every
// standard OpenAI-spec consumer (cost calculators, dashboards) bill cache writes as if they
// were cheap cache reads, a large under-count since writes are billed higher than reads.
func TestChatPromptTokensDetails_MarshalJSON_CachedTokensReadOnly(t *testing.T) {
	tests := []struct {
		name               string
		details            ChatPromptTokensDetails
		wantCachedTokens   int
		wantCachedReadOut  int
		wantCachedWriteOut int
	}{
		{
			name:               "write-only turn (fresh cache write, no reads)",
			details:            ChatPromptTokensDetails{CachedReadTokens: 0, CachedWriteTokens: 500},
			wantCachedTokens:   0,
			wantCachedReadOut:  0,
			wantCachedWriteOut: 500,
		},
		{
			name:               "read-only turn (cache hit)",
			details:            ChatPromptTokensDetails{CachedReadTokens: 300, CachedWriteTokens: 0},
			wantCachedTokens:   300,
			wantCachedReadOut:  300,
			wantCachedWriteOut: 0,
		},
		{
			name:               "mixed read and write in same turn",
			details:            ChatPromptTokensDetails{CachedReadTokens: 200, CachedWriteTokens: 100},
			wantCachedTokens:   200,
			wantCachedReadOut:  200,
			wantCachedWriteOut: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := json.Marshal(tt.details)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got struct {
				CachedTokens      int `json:"cached_tokens"`
				CachedReadTokens  int `json:"cached_read_tokens"`
				CachedWriteTokens int `json:"cached_write_tokens"`
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}
			if got.CachedTokens != tt.wantCachedTokens {
				t.Errorf("cached_tokens = %d, want %d (reads only, not reads+writes): %s", got.CachedTokens, tt.wantCachedTokens, out)
			}
			if got.CachedReadTokens != tt.wantCachedReadOut {
				t.Errorf("cached_read_tokens = %d, want %d", got.CachedReadTokens, tt.wantCachedReadOut)
			}
			if got.CachedWriteTokens != tt.wantCachedWriteOut {
				t.Errorf("cached_write_tokens = %d, want %d", got.CachedWriteTokens, tt.wantCachedWriteOut)
			}
		})
	}
}

// TestChatPromptTokensDetails_UnmarshalJSON_PlainCachedTokensStillMapsToRead verifies the
// inbound direction is unaffected by the fix: an OpenAI-spec provider that sends only
// cached_tokens (no split fields) still maps it to CachedReadTokens.
func TestChatPromptTokensDetails_UnmarshalJSON_PlainCachedTokensStillMapsToRead(t *testing.T) {
	var d ChatPromptTokensDetails
	if err := json.Unmarshal([]byte(`{"cached_tokens": 128}`), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.CachedReadTokens != 128 {
		t.Errorf("CachedReadTokens = %d, want 128", d.CachedReadTokens)
	}
	if d.CachedWriteTokens != 0 {
		t.Errorf("CachedWriteTokens = %d, want 0", d.CachedWriteTokens)
	}
}
