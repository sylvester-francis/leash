# Known issues and roadmap

leash states its own limits. This page lists behavior that is bounded by design,
and capabilities deliberately deferred, so nothing here is a surprise in
production. The [architecture](how-leash-works.md) and [durability](durability.md)
docs cover the guarantees that *do* hold.

## Limitations (by design or bounded)

- **The budget can overshoot by one in-flight call.** Metering is
  post-response - leash learns a call's cost only after the upstream has served
  it - so the call that crosses a `--max-cost` budget still completes.
  `--max-cost-per-call` bounds how large that final call can be, but does not
  eliminate the overshoot. Size the budget with one max-cost call of headroom.
- **Wrapper mode governs only base-URL-respecting SDKs.** Tier 1 works by
  injecting `OPENAI_BASE_URL` / `ANTHROPIC_BASE_URL` into the child. An SDK that
  ignores those (Amazon Bedrock's SigV4 client, Vertex's gRPC client, Azure
  OpenAI, or any client with a hard-coded base URL) sends traffic straight past
  leash. Those calls are simply not governed - run such agents through gateway
  mode with an explicit `--upstream`, or govern what you can reach.
- **Durable reactions have a bounded gap at the enqueue seam.** With
  `--reactions-db`, escalations are durable once enqueued, but the enqueue is
  asynchronous so the enforcement path never waits on it. A crash in the small
  window between a stop and the reaction's durable write loses that one reaction,
  and it does not re-fire (a stop is a live-only transition). This is deliberate
  (the meter is never blocked) and documented in
  [ADR-0009](adr/0009-durable-governance-reactions.md); a boot-reconciliation
  sweep can close it if the window ever matters.
- **Stall detection is byte-exact.** The stall boundary trips on verbatim
  repeated responses. A loop that varies by a timestamp or an id each turn is
  semantically stuck but not byte-identical, and will not trip it; lean on
  `--max-calls` and `--deadline` for those.
- **Deadlines and rate windows use the wall clock.** A large NTP step can make a
  `--deadline` or `--rate` window measure slightly early or late. leash does not
  discipline the system clock.

## Roadmap (deferred, not yet built)

These are real capabilities leash does not have. They are scoped out of the
current releases, not overlooked. Each is tracked as a GitHub issue under the
[`roadmap`](https://github.com/sylvester-francis/leash/labels/roadmap) label.

- **Horizontal scale** ([#20](https://github.com/sylvester-francis/leash/issues/20)).
  One active governor owns a ledger at a time; throughput is one process. Per-run
  sharding across governors (so a fleet can share one logical budget) is future
  work. `--standby` is failover, not scale-out.
- **Hierarchical multi-tenant quotas** ([#21](https://github.com/sylvester-francis/leash/issues/21)).
  Budgets are per run (per credential when auth is on). A quota that nests
  call < run < team < org, with limits at each level, is not modeled yet.
- **Native Ollama metering** ([#59](https://github.com/sylvester-francis/leash/issues/59)).
  Ollama is governed via its OpenAI-compatible `/v1` endpoint; its native
  `/api/chat` usage shape is not parsed.
- **OpenAI server-side tool pricing** ([#60](https://github.com/sylvester-francis/leash/issues/60)).
  Per-request tool charges are read and priced for Anthropic; OpenAI's are not yet.
- **Durable-reaction enqueue-seam sweep** ([#61](https://github.com/sylvester-francis/leash/issues/61)).
  A crash between a stop and the reaction's durable write loses that one reaction;
  a boot-reconciliation sweep can close it.
- **Vertex AI Gemini semantics** ([#62](https://github.com/sylvester-francis/leash/issues/62)).
  leash meters the Gemini API; Vertex reports `candidatesTokenCount` differently.
- **Gemini API stability watch** ([#68](https://github.com/sylvester-francis/leash/issues/68)).
  Gemini's native support depends on the `usageMetadata` field names and the
  candidates-includes-thoughts semantics, which Google may change without notice;
  tracked so a silent metering regression is caught, not shipped.

## Shipped

Closed and released. See the [CHANGELOG](../CHANGELOG.md) for the version each
landed in, and [security-model.md](security-model.md) for verifying a release.

- **Durable governance reactions** ([#54](https://github.com/sylvester-francis/leash/issues/54), closed).
- **Native Gemini metering** ([#55](https://github.com/sylvester-francis/leash/issues/55), closed).
- **Anthropic thinking tokens; fail closed on unpriceable tool spend** ([#56](https://github.com/sylvester-francis/leash/issues/56), closed).
- **Opt-in priced dimensions** (audio, cache-TTL, per-request tools, service tiers) ([#57](https://github.com/sylvester-francis/leash/issues/57), closed).
- **Soft limits and backpressure** ([#22](https://github.com/sylvester-francis/leash/issues/22), closed).
- **Richer observability** ([#23](https://github.com/sylvester-francis/leash/issues/23), closed).
- **Signed release supply chain** ([#24](https://github.com/sylvester-francis/leash/issues/24), closed).
- **Windows SQLite governor lock** ([#25](https://github.com/sylvester-francis/leash/issues/25), closed).
- **Idempotent ledger appends** ([#26](https://github.com/sylvester-francis/leash/issues/26), closed).

Have a use case that one of these blocks? Comment on the issue - it helps
prioritize.
