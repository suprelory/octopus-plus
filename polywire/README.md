# Polywire

Polywire is an independent Go module for LLM protocol conversion, extracted from
Octopus. Module path: `github.com/suprelory/octopus-plus/polywire`; Go 1.25 or newer.
It parses client requests, builds provider HTTP requests, translates responses
and canonical stream events, and reports conversion losses. It never executes
HTTP requests or opens WebSocket connections.

## Use

The module is developed in this repository; it has not been published as a
separate version by this extraction. A local consumer can use:

```go
require github.com/suprelory/octopus-plus/polywire v0.0.0

replace github.com/suprelory/octopus-plus/polywire => /path/to/octopus-plus/polywire
```

```go
engine := polywire.New(polywire.Config{})
client := engine.Inbound(inbound.InboundTypeOpenAIChat)
request, err := client.TransformRequest(ctx, body)
if err != nil {
    return err
}
target := outbound.OutboundTypeAnthropic
decision, prepared := engine.PrepareRequestForModel(request, "claude-model", target, false)
if decision.Rejected() {
    return fmt.Errorf("conversion rejected: %s", decision.Summary())
}
// The host decides whether a degraded conversion is acceptable.
provider := engine.Outbound(target)
wire, report, err := prepared.Build(ctx, baseURL, apiKey)
```

Import `polywire`, `polywire/inbound`, and `polywire/outbound` under the module
path above. `wire` is an unsent `*http.Request`; `report` describes the actual
encoded request. The host executes it with its own HTTP client, calls
`provider.TransformResponse`, then `client.TransformResponse`. The host closes
request/response bodies. Unknown factory types return `nil`.

`PrepareRequestForModel` retains the validated body and conversion report.
`PreparedRequest.Build` creates a fresh HTTP request for the chosen context,
endpoint and key without converting the payload again. Treat a prepared request
as an immutable snapshot: changing the source request requires preparing again.
Keep the response adapter fresh for each attempt. Passthrough and rejected
decisions return no prepared body; custom adapters can opt in by implementing
`model.RequestTargeter`. `PlanRequestForModel` remains available when only a
decision is needed, and `outbound.BuildRequest` still supports direct conversion.

See the complete [conversion example](examples/convert/main.go), which uses a
synthetic response and runs without network I/O:

```sh
cd polywire
GOWORK=off go run ./examples/convert
GOWORK=off go test ./...
GOWORK=off go vet ./...
GOWORK=off go build ./...
```

In PowerShell, set `$env:GOWORK = 'off'` before running these Go commands.

## Dependencies and ownership

`polywire.Config` accepts `Logger`, `TokenCounter`, and `SignatureStore`.

- A nil logger is silent. Diagnostics retain their existing structured event
  names, including `transformer.reasoning.signature.passthrough`.
- A nil token counter disables estimates: `EstimatedInputTokens` and Anthropic's
  initial estimated input usage are zero until actual provider usage arrives.
  Inject `func(content, model string) int` to retain host tokenizer behavior.
- A nil signature store gives each Engine its own bounded memory store: 24-hour
  TTL, at most 4096 physical exact/fallback keys, lazy cleanup, no goroutines.
  Reuse the Engine across turns. Inject `compat.SignatureStore` to share a store,
  or `compat.NoopSignatureStore{}` to explicitly disable recovery.
- Use `compat.WithGeminiSignatureScope` on request and response contexts to set
  tenant, API key, session, model, channel and format isolation. Anthropic adapters
  fill a missing model and format from the client request. Opaque signature bytes
  are preserved; the memory store hashes scope identifiers in its keys.

Engines can be shared concurrently if injected services support concurrent use.
Every `Engine.Inbound` / `Engine.Outbound` call creates an adapter for one attempt.
Keep each stateful adapter and `model.StreamConverter` owned by a single stream;
serialize `Push` / `Finish`, and call `Finish` once with the actual termination
cause. A terminal frame may complete missing boundaries; a source error must not
be treated as clean EOF. `streamio.SSEDecoder` and `IncrementalSSEObserver` are also
single-owner. `streamio.SSESource.Close` closes the supplied reader; the host must
call it on early exit to unblock the reader goroutine.

For SSE, use `streamio.NewSSESource` (or feed `SSEDecoder` directly), pass each
`SourceEvent` to `model.NewStreamConverter(provider, policy).Push`, and encode the
returned events with `client.TransformStreamEvents`. Obtain `policy` from
`outbound.TerminalPolicy(target)`. Preserve event type, ID, sequence and transport
alongside `Data`; WebSocket hosts can construct the same `model.SourceEvent`.

On retries, use a fresh inbound and `model.RequestStateSeedable.SeedRequestState`
to reuse parsed request estimates. `GetInternalResponse` consumes/reset stream
aggregation on adapters that use `BuildAndReset`; retrieve the aggregate once.
Always use `request.Clone()` to prepare model mapping or overrides: JSON
roundtrips lose operation, raw recovery and other non-JSON fields.

Low-level `inbound.Get` / `outbound.Get` and zero-valued provider adapters remain
usable with host services disabled. Prefer an Engine for application integration;
`config.Config` and the lower-level `New` factories provide explicit injection.
No application package, framework, tokenizer, logging backend or business metric
registry is required. The sole direct third-party dependency is `samber/lo`.

## Supported boundaries

Client inbounds: OpenAI Chat Completions, OpenAI Responses, Anthropic Messages,
and OpenAI Embeddings. Provider outbounds: those four plus Gemini Contents.
Gemini is currently an outbound adapter; its row in the [quality matrix](QUALITY_MATRIX.md)
describes canonical source semantics, not a registered Gemini client parser.
Images, rerank and auxiliary compact/WebSocket endpoints have model/descriptor
metadata where applicable but are not standalone cross-protocol HTTP operations.

`PlanRequestForModel` and `BuildRequest` report preserved, translated, repaired,
dropped, truncated and rejected semantics. Polywire does not pick channels or
enforce a strict/degradation policy. The host owns native fallback decisions,
model mapping, retries, replay/session persistence, connection pools, heartbeats,
timeouts, downstream commitment and billing. Raw request passthrough is exposed
through `model.PassthroughCapable` for Anthropic and OpenAI Responses.

## Protocol conversion contracts

Protocol fixtures and conversion behavior are retained from Octopus. Adapters
now live under this module's package paths.

- `model/`: canonical request, message, response, usage and tool types. Missing,
  explicit `null`, zero and empty values remain distinct protocol inputs.
- `inbound/`: client request parsing and response encoding, including wire item
  and content lifecycles.
- `outbound/`: provider request building, response parsing, cache/beta settings,
  signatures, passthrough and stream item tracking.
- `protocol/`: shared provider wire types. `compat/` contains scoped conversion
  helpers; `rawjson/` preserves raw fields during targeted edits.
- `streamio/`: generic SSE framing and observation; `httpio/`: bounded body reads.

Keep opaque signatures unchanged and scoped to their provider, kind and tool
call. Preserve field presence and native replay items. Avoid consuming an
aggregator twice. Run transformer tests for request fixtures, signatures and
stream ordering, and relay tests for retry isolation and replay. Golden fixtures
change only when protocol behavior changes intentionally.

Chat requests retain unknown top-level fields in their native recovery sidecar;
conversion to another protocol reports each omitted extension. Native canonical
builders preserve explicit null and empty values for declared wire fields, while
concrete prepared values take precedence. `EmptyFields` retains only those small
empty carriers. Use `SetFieldPresence(field, FieldAbsent)` when intentionally
removing one from a cloned request. Chat reports also account for empty fields
that the target cannot emit. Only a single completion choice is supported: `n:1`
and `n:null` are accepted, and other explicit counts fail request validation.

`InternalLLMRequest.Operation` is the authoritative endpoint payload. Outbound
builders and replay use `ChatPayload`, `ResponsesPayload`, `EmbeddingsPayload`,
`ImagesPayload`, `RerankPayload`, and `ConversationMessages`. Shared routing and
generation options remain on the request.

Legacy-only callers remain supported. `NormalizeOperation` lifts their payload
into an operation and populates deprecated fields for compatibility. Callers that
provide both representations must keep them consistent; builders and capability
planning reject conflicts before upstream submission. Use
`SetConversationMessages`, `SetOpenAIRawInputItems`, and
`SetOpenAIResponsesOptions` when modifying a cloned request for conversion or
replay. Empty authoritative fields never recover stale provider sidecars.

The deprecated byte and aggregate streaming methods remain compatibility APIs
for at least one release cycle. Relay uses canonical events and one per-stream
`Push`/`Finish` converter. Provider adapters parse provider state, the canonical
finalizer owns message and block completion, and inbound encoders serialize the
result. A terminal marker can repair missing canonical stops; a source error
cannot. `Finish` seals the stream for every termination cause.

The Anthropic encoder keeps an active tool's deltas streaming and defers other
interleaved content until that tool stops, preserving tool IDs and arguments in
serial content blocks. The legacy aggregate API flushes remaining tools at its
terminal marker; error events discard deferred output. Refusal deltas accumulate
across batches and are emitted as Anthropic text with a refusal stop reason when
the upstream supplies an ordinary completion reason.

`StreamAggregator` merges chunks incrementally and buffers text, reasoning,
refusal, audio and tool arguments without retaining the original chunks.
Indexed content blocks and tools keep their original order. `Response` returns
an independent snapshot; `BuildAndReset` transfers the completed aggregate and
releases its state. Input chunks may be reused after `Add` returns.

Gemini tool parameters and structured output share
`model.ConvertJSONSchemaToGemini`. Conversion preserves `anyOf`, nullable type
unions and resolvable local references, using the original schema bytes when
available. Unsupported keywords and recursive/unresolved references produce
`ErrSchemaLossy` with source paths, and both wire paths report the same losses.
Reference expansion is bounded to 64 levels and 4096 nodes. The schema profile
remains conservative: unsupported constraints such as `additionalProperties`,
`oneOf`, `allOf`, `pattern` and length bounds are reported for host policy review.

`ProtocolDescriptor.FieldRules` records each source semantic, target wire field,
action, condition, and reason. `outbound.BuildRequest` returns the HTTP request
and its conversion report without consuming the body or sending any bytes.
Capability planning runs this same local build with the effective upstream model.
Scalar rules inspect emitted JSON; tools are checked against emitted definitions
and schemas; provider preparation helpers report structured repairs and losses.
Octopus HTTP and transformed WebSocket submission persist and enforce the returned
report. Its strict policy rejects ordinary known losses but permits availability
fallbacks involving unknown top-level fields or identified native semantics.
These are host decisions; Polywire returns evidence for either policy.
Passthrough is planned separately because its preserved raw fields do not use
canonical builders.

Octopus reuses prepared bodies across candidate selection and HTTP/WebSocket
attempts within one relay request. The cache uses the effective model, outbound
protocol, passthrough decision and override fingerprint; it retains at most eight
bodies totaling 16 MiB. Requests outside that budget use ordinary construction.
Every attempt reapplies its endpoint, credentials, context and wire overrides,
and enforces the conversion policy. See [conversion benchmarks](CONVERSION_BENCHMARKS.md)
for the measured aggregation and preparation costs.

Each attempt records `conversion.mode` (`lossless_canonical`, `raw_sidecar`, or
`lossy_canonical`), `replay_available`, `raw_input_preserved`, and `exact_replay`.
Replay availability describes intact input for the selected target; it does not
promise that a remote conversation ID remains live. Lossy conversions retain the
raw-preservation flag but cannot claim intact replay. `Operation.Recovery` stores
unknown top-level native fields separately from their required-field list.
Routing prefers channels that preserve these fields, then falls back to other
protocols by omitting them and recording each dropped field in the conversion
report. The original request and recovery sidecar remain intact for retries.
All builders still validate required sidecars and native input/tool semantics
before encoding. Missing required sidecars or native semantics without a target
recovery path remain hard rejections, independent of the degradation policy.

The shared contract matrix loads `testdata/contracts/<descriptor name>.json` for
every registered adapter. Registration requires request/response wire fixtures,
a normal stream with usage and a terminal event, malformed JSON, an abrupt-stream
prefix, and declarations for text, thinking, signatures, tools, citations, and
audio. Unsupported semantics require an explicit reason. The matrix exercises
every supported inbound/outbound pair and rejects unsupported operation pairs.
Source contracts also exercise split frames, CRLF, multiline data, empty terminal
data, concurrent readers, concurrent close, and data followed by a source error.

Canonical events retain immutable `StreamProvenance`: API format, wire event
type/ID, source sequence/transport, provider event type/sequence, and raw bytes.
The wire event type remains separate from an inferred JSON type. Every event has
semantic importance; finalizer repairs also carry their triggering provenance
and are marked synthesized. `StreamReplay.Push` reconstructs original source
frames once, excludes synthesized boundaries, and rejects missing provenance,
cross-protocol replay, and conflicting or regressing source sequences.

Response and output-item boundaries, MCP calls, computer actions, server tools,
grounding, annotations, and native audio remain explicit events. Their raw fields
are not flattened into client function calls or text. Unknown events are opaque.
The aggregate response is a compatibility/metrics projection; native replay uses
the canonical events. Ordinary wire encoders return `StreamConversionLoss` when
a native semantic has no target representation, and attempts record the loss in
stream diagnostics. Native passthrough observes the canonical lifecycle without
encoding and discarding a lossy projection. OpenAI URL/file/container citations
keep native annotations; Gemini grounding retains unknown retrieval metadata.

SSE source and passthrough observation share one bounded framing decoder. A
stateless `SourceEventInspector` can preview complete data lines to release
semantic precommit while retaining trailing event types, IDs and multiline data
for final conversion. `SourceEvent.Decoded` is an immutable, optional provider
DTO cache; changing `Data` requires clearing it. The cache keeps payload fields
separate from mutable SSE envelope metadata. Native WebSocket observation and
canonical conversion reuse the same DTO, including raw output, usage and retry
information. See [decoder benchmarks](streamio/DECODER_BENCHMARKS.md) for
reproduction commands, before/after measurements and their scope.

Wire references: [Responses streaming events](https://developers.openai.com/api/reference/resources/responses/streaming-events),
[Anthropic messages](https://platform.claude.com/docs/en/api/beta/messages), and
[Gemini generate content](https://ai.google.dev/api/generate-content).

## License

Extraction does not relicense the code. Octopus modifications and additions
remain AGPL-3.0; see [LICENSE](LICENSE). MIT-derived AxonHub notices remain in
[NOTICE](NOTICE) and the Data URL helper's [NOTICE](internal/dataurl/NOTICE).
