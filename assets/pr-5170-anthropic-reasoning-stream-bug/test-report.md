---
circuit: gen
version: v3
summary: E2E repro + fix report for the Anthropic-compat streaming reasoning-block crash.
---

# Anthropic-compat streaming reasoning-block bug — E2E test report

- Issue: https://github.com/maximhq/bifrost/issues/5169
- PR: https://github.com/maximhq/bifrost/pull/5170
- Branch: `fix/anthropic-reasoning-stream-item-added` (pushed to `Shaik-Sirajuddin/bifrost`)
- Worktree: `.claude/worktrees/anthropic-thinking-item-added` (off `dev` @ `0cb488798`)

## Root cause

`core/schemas/mux.go`'s `BifrostChatResponse.ToBifrostResponsesStreamResponse` (the
Chat→Responses stream converter used by every provider whose `ResponsesStream` falls
back to `ChatCompletionStream`) emitted `response.reasoning_summary_text.delta`
without ever emitting a matching `response.output_item.added`, and without setting
`Item`/`ItemID` on the delta.

`core/providers/anthropic/responses.go`'s `reverseStreamItemKey` resolves the
Anthropic content-block index via `Item.ID` → `ItemID` → `"oi:<OutputIndex>"`. The
reasoning delta's key fell back to `"oi:0"`, which never matched the text item's
`Item.ID` key registered at its own `output_item.added`. `blockIndexFor` therefore
missed and silently allocated a fresh block index with no matching
`content_block_start` ever emitted — a protocol violation that crashes the official
`anthropic-python` SDK's streaming accumulator
(`anthropic/lib/streaming/_messages.py:465`, `content = current_snapshot.content[event.index]`,
unchecked list index) with `IndexError: list index out of range`.

## Environment

- Local Ollama 0.31.1, model `qwen3:0.6b` (thinking-capable; confirmed it emits an
  OpenAI-compatible `reasoning` delta field over `/v1/chat/completions`).
- Bifrost built from the worktree, run with `-app-dir` pointing at a minimal config
  with an `ollama` provider (`ollama_key_config.url = http://localhost:11434`).
- Client: official `anthropic` Python SDK 0.116.0, `client.messages.stream(...)`
  context-manager helper, against Bifrost's `/anthropic/v1/messages`.
- Visual capture: Xvfb `:99` (1920x1080x24) + x11vnc (port 5999) + alacritty
  (software GL) + `scrot`, per request.

## Repro script

`repro_anthropic_sdk.py` in this directory — drives `client.messages.stream()` and
reports either a clean completion or the crash with full traceback, logging every
SSE event to a raw log file.

## Before fix

- Raw log: `before_fix_raw.log`
- Screenshot (full-screen, `:99`): `before_fix_repro.png`
- Result: **crashed after 4 events** with `IndexError: list index out of range` at
  `anthropic/lib/streaming/_messages.py:465` in `accumulate_event`.

## After fix

- Raw log: `after_fix_raw.log`
- Screenshot (full-screen, `:99`): `after_fix_repro.png`
- Result: **completed cleanly, 759 events**, `message_stop`'s accumulated snapshot
  correctly carries both the `text` content block and a `ThinkingBlock` with the
  full reasoning text and matching `content_block_stop` events for both indices.

## Fix

`core/schemas/mux.go`:

- Reasoning delta now gets its own `output_item.added` (type `reasoning`, its own
  output index reserved via the same `CurrentOutputIndex` counter tool calls use,
  stable `Item.ID`) before the first delta, and every subsequent delta/`.done` event
  carries the matching `ItemID` — mirrors the existing text-item lifecycle a few
  lines above, and the native Anthropic→Responses converter's `thinking` handling.
- Reasoning item is closed on `finish_reason` (`reasoning_summary_text.done` +
  `output_item.done`) and included in `response.completed`'s `Output` array —
  closes the remaining streaming-side gap noted in #1977 (non-streaming path was
  already fixed there).

## Test coverage

- New regression test: `core/providers/anthropic/muxreasoningstream_test.go`
  (`TestMuxReasoningStream_ReproducesCrashWithoutFix`). Drives the mux path with
  reasoning-then-text deltas through `ToAnthropicResponsesStreamResponse` and
  asserts `blockIndexMisses` stays empty and block framing is well-formed.
  - Verified it **fails** against pre-fix `mux.go` (`git stash` sanity check) and
    **passes** with the fix.
- `go test ./core/schemas/... ./core/providers/anthropic/...` — all green, no
  regressions.
- `go vet ./schemas/... ./providers/anthropic/...` — clean.
- Pre-existing unrelated failure noted and confirmed NOT caused by this change:
  `TestToOpenAIResponsesRequest_GPTOSS_SummaryToContentBlocks/gpt-oss_variant_model_converts_Summary_to_ContentBlocks`
  in `core/providers/openai` — fails identically on unmodified `dev`.

## Affected providers (unconditional Chat-Completions fallback)

Ollama, Groq, Cerebras, DeepSeek, Mistral, Nebius, Parasail, SGL, VLLM.
Perplexity is conditionally affected (only for `sonar-*` models not on
`/v1/responses`).
