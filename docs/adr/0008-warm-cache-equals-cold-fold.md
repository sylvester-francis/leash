# ADR-0008: The warm cache must equal a cold fold

Status: accepted

## Context

Folding the whole journal on every request would make throughput O(journal) per
call, which degrades as a run grows. leash keeps a warm cache: the folded `State`
and the last journal sequence, so an ongoing run appends in O(1) and never
re-reads. A cache introduces the classic risk: it can drift from the source of
truth, and a spend total that has silently drifted is worse than a slow one.

## Decision

The warm cache is held to a hard invariant: **at every point, the cached `State`
is exactly equal to a cold fold of the journal.** Concretely:

- Both the warm path and the cold-load path fold through the *same*
  `Governor.Fold`. New accounting (cost, tokens, samples, pruning) is added there
  and nowhere else, so the two paths cannot diverge.
- The warm path advances only on a successful, idempotent append; a failed append
  does not advance the cache, so a later cold reload agrees.
- Eviction is always safe: an evicted run cold-reloads to the same state on its
  next call, which is why idle runs can be dropped to bound memory.

## Consequences

- O(1) warm appends with no correctness cost, and free eviction for memory bounds.
- A discipline on contributors: never mutate `State` totals outside the fold, and
  never add a warm-only side effect that a cold fold would not reproduce.
- The invariant is testable directly, and is asserted as a property: for a random
  sequence of calls and evictions, the warm state equals a fresh cold fold.

## Alternatives considered

- **Always cold-fold (no cache).** Correct by construction but O(journal) per call;
  a long run gets slower as it goes.
- **Cache the total independently of the fold.** Faster to write, but it is a second
  source of truth that will drift the first time someone adds accounting to one
  path and forgets the other. The single-fold rule removes that whole class of bug.
