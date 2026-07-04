package bedrock

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestToBedrockConverseStreamResponse_OutputItemDoneEmitsContentBlockStop is a regression
// test for #4262: the Bedrock ConverseStream egress never emitted a contentBlockStop event,
// violating AWS's Converse contract (every contentBlockStart must be closed by a
// contentBlockStop before messageStop). SDKs that finalize a block on this event (e.g.
// strands) ended up with an empty assembled message.
func TestToBedrockConverseStreamResponse_OutputItemDoneEmitsContentBlockStop(t *testing.T) {
	contentIndex := 2
	resp := &schemas.BifrostResponsesStreamResponse{
		Type:         schemas.ResponsesStreamResponseTypeOutputItemDone,
		ContentIndex: &contentIndex,
	}

	event, err := ToBedrockConverseStreamResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event == nil {
		t.Fatal("expected a non-nil event")
	}
	if !event.Stop {
		t.Fatal("expected event.Stop to be true for output_item.done")
	}
	if event.ContentBlockIndex == nil || *event.ContentBlockIndex != contentIndex {
		t.Errorf("ContentBlockIndex = %v, want %d", event.ContentBlockIndex, contentIndex)
	}

	encoded := event.ToEncodedEvents()
	var sawStop bool
	for _, e := range encoded {
		if e.EventType == "contentBlockStop" {
			sawStop = true
			payload, ok := e.Payload.(struct {
				ContentBlockIndex *int `json:"contentBlockIndex"`
			})
			if !ok {
				t.Fatalf("unexpected contentBlockStop payload type: %T", e.Payload)
			}
			if payload.ContentBlockIndex == nil || *payload.ContentBlockIndex != contentIndex {
				t.Errorf("encoded contentBlockStop index = %v, want %d", payload.ContentBlockIndex, contentIndex)
			}
		}
	}
	if !sawStop {
		t.Fatal("expected a contentBlockStop event in the encoded output, got none")
	}
}

// TestToEncodedEvents_FullSequenceIncludesContentBlockStop verifies the full per-block
// event order: contentBlockStart -> contentBlockDelta -> contentBlockStop -> messageStop,
// confirming contentBlockStop is ordered before messageStop when both are present.
func TestToEncodedEvents_FullSequenceIncludesContentBlockStop(t *testing.T) {
	idx := 0
	stopReason := "end_turn"
	event := &BedrockStreamEvent{
		ContentBlockIndex: &idx,
		Stop:              true,
		StopReason:        &stopReason,
	}

	encoded := event.ToEncodedEvents()
	var stopIdx, messageStopIdx = -1, -1
	for i, e := range encoded {
		switch e.EventType {
		case "contentBlockStop":
			stopIdx = i
		case "messageStop":
			messageStopIdx = i
		}
	}
	if stopIdx == -1 {
		t.Fatal("expected a contentBlockStop event")
	}
	if messageStopIdx == -1 {
		t.Fatal("expected a messageStop event")
	}
	if stopIdx > messageStopIdx {
		t.Errorf("contentBlockStop (index %d) must come before messageStop (index %d)", stopIdx, messageStopIdx)
	}
}
