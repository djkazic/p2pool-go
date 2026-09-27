# Formal specification and model checking

TLA+ specifications of p2pool-go's consensus-critical algorithms, plus the
TLC model-checking configurations run against them.

These verify the **protocol design**, not the Go code. Where a counterexample
mattered, it was replayed against the real implementation — see
`internal/sharechain/probes_test.go`, `internal/stratum/vardiff_test.go` and
[`reproducer/`](reproducer), which double as regression checks.

## Running

```sh
curl -L -o tla2tools.jar \
  https://github.com/tlaplus/tlaplus/releases/latest/download/tla2tools.jar

cd specs
java -cp tla2tools.jar tlc2.TLC -workers 4 -config ForkChoice.cfg ForkChoice
```

```sh
go run ./specs/reproducer    # from the repo root; exits non-zero on regression
```

## Modules

| Module | Models | Source of truth |
|---|---|---|
| `ForkChoice.tla` | share DAG, `AssignWork`, `ChainWork`, `SelectTip`, gossip, pruning | `internal/sharechain/fork.go`, `chain.go` |
| `Difficulty.tla` | `NextTarget` retarget loop, compact round-trip, `clampToNetwork` | `internal/sharechain/difficulty.go`, `chain.go` |
| `PPLNS.tla` | `CalculatePayouts` incl. finder fee, remainder, dust consolidation | `internal/pplns/calculator.go`, `window.go` |
| `Sync.tla` | a fresh node bootstrapping from a peer that has pruned | `internal/node/node.go` sync/inv handlers, `chain.go` `PruneOldShares` |
| `PeerScore.tla` | misbehavior scoring under honest clock skew | `internal/sharechain/validation.go`, `internal/node/node.go` `recordPeerOutcome` |
| `Validation.tla` | agreeing on a share's consensus target across prune horizons | `internal/sharechain/validation.go` `historyIsComplete`, `difficulty.go` |
| `Payout.tla` | what validation enforces about a share's coinbase outputs | `internal/sharechain/validation.go` step 9, `internal/types/coinbase.go`, `internal/pplns/calculator.go` |
| `Reorg.tla` | the chain event pipeline and the job miners are working on | `internal/sharechain/chain.go` `emit`, `internal/node/node.go` `handleChainEvent`, `internal/work/generator.go` |

## Configurations and results

| Config | Checks | Result |
|---|---|---|
| `ForkChoice.cfg` | `TipIsHeaviest`, `CacheSound`, `ConsistentAnchor` (1 node) | passed, 4 283 states |
| `FC_Gossip.cfg` | `TipIsHeaviest`, `AgreeOnEqualKnowledge` (2 nodes gossiping) | passed, 8 412 states |
| `FC_Prune.cfg` | `ConsistentAnchor`, `AgreeOnEqualKnowledge` (2 nodes, pruning) | passed, 83 317 states |
| `FC_Orphan.cfg` | same, with `PruneOrphans` also live | passed, 2 714 740 states |
| `Difficulty.cfg` | `TargetAboveMin`, `TargetBelowMax`, `TargetPositive`, `CompactStable` | passed, 171 states |
| `Diff_Ratchet.cfg` | same, over 20 retargets of uninterrupted fast shares | passed, 471 states |
| `Diff_Clamped.cfg` | same, with `clampToNetwork` live | passed, 329 states |
| `PPLNS_Core.cfg` | `PayoutConservation`, `NoNonPositive` | passed, 2 520 inputs |
| `PPLNS_MainnetDust.cfg` | `NoDustOutputs`, `FinderGetsFee` at a 3.125 BTC subsidy | passed, 630 inputs |
| `PPLNS_Dust.cfg` | `NoDustOutputs` including 1-satoshi rewards | passed, 2 205 inputs |
| `Sync.cfg` | `ProgressPossible`, `Bootstraps` (seed prunes 6 shares to 3) | passed, 32 states |
| `PeerScore.cfg` | `NoProvableChargeAgainstHonestPeer` (clock skew 0-2 units) | passed, 99 states |
| `PS_Disconnect.cfg` | `NoHonestDisconnect` | passed, 99 states |
| `PS_Synced.cfg` | control: both nodes' clocks in sync | passed, 9 states |
| `PS_ProvableControl.cfg` | control: pre-fix, timestamp rejection charged to the peer | **violated** - what finding 7 fixed |
| `Validation.cfg` | `NoProvableRejection` across differing prune horizons | passed, 204 states |
| `Val_NoGate.cfg` | control: pre-fix, no `historyIsComplete` gate | **violated** — what finding 8 fixed |
| `Payout.cfg` | `PayoutsMatchWindow`, `NoMajorityTheft`, `SubmitterIsPaid` | passed, 80 states |
| `Payout_Unenforced.cfg` | control: pre-fix, outputs checked only for the submitter | **violated** — what finding 9 fixed |
| `Payout_Theft.cfg` | control: pre-fix, `NoMajorityTheft` alone | **violated** — what finding 9 fixed |
| `Reorg.cfg` | `Converges` with the job-refresh timer running | passed, 268 states |
| `Reorg_NoRefresh.cfg` | `Converges` with the refresh timer off | passed, 157 states |
| `Reorg_Tight.cfg` | same, one-slot channel so tip events are dropped | passed, 124 states |
| `Reorg_Dedupe.cfg` | control: pre-fix, with the `lastReorgTip` suppression | **violated** — what finding 10 fixed |

Every finding below is **fixed**. Regression tests live in
`internal/sharechain/probes_test.go` and `internal/stratum/vardiff_test.go`
and run as part of the normal suite; the `*Control.cfg`, `Val_NoGate.cfg`,
`Payout_Unenforced.cfg`, `Payout_Theft.cfg` and `Reorg_Dedupe.cfg`
configurations keep the original counterexamples reproducible.


## Findings

### 1. `ChainWork` returned a depth-truncated sum — fixed

`ForkChoice.ChainWork(tip, maxDepth)` walked back at most `maxDepth` shares.
When the walk ran out of budget before reaching the zero hash, a prune edge
or a cached ancestor, it returned the sum over only those `maxDepth` shares,
and `SelectTip` compared two such values as though they were cumulative
chain work.

`maxDepth` was `windowSize`, threaded from `config.PPLNSWindowSize` (default
8640), and `PruneOldShares(PPLNSWindowSize)` keeps the chain at about that
same length — so on a mature node the budget was routinely the binding
constraint. A 6-share chain of difficulty 1 reported work 3 at
`windowSize=3`; a 4-share chain tied with a 3-share fork.

**Fix.** Cumulative work is now maintained incrementally. `AssignWork`
records a share's work as it is added — its parent's work plus its own
difficulty — which validation makes safe, since a share whose parent is
missing is rejected. `ChainWork` is an O(1) lookup in the steady state and
falls back to an uncapped walk only for shares restored from disk, where it
fills in every share it passes. `ShareChain.RebuildWork` performs that walk
once at startup so the whole store is anchored at a single point.
`shareDifficulty` now floors at 1, so a child always strictly outweighs its
parent.

### 2. Nodes holding identical shares disagreed on the tip — fixed

`FC_Prune.cfg` previously reached a state where `knows[n1] = knows[n2]` but
`tip[n1] ≠ tip[n2]`, needing neither pruning nor a dishonest peer — only a
different arrival order.

**Fix.** With whole-chain work, a share that extends the current tip always
outweighs it, so `SelectTip`'s "candidate directly extends the current tip"
shortcut became redundant and was removed. Tip selection is now a plain
maximum over known shares: a function of the share set, not of arrival
order. `AgreeOnEqualKnowledge` holds across 8 412 gossip states and 57 419
states with pruning enabled.

Pruning moves a node's anchor, so `CacheSound` — every value equals true
work back to the zero hash — no longer applies once shares are dropped. The
property that does survive, and the one comparisons actually need, is
`ConsistentAnchor`: within a node, every recorded value sits the same
distance from true work. Fork choice only ever compares two of its own
node's tips, never a number from a peer.

### 3. `MinShareTarget` was declared as a safety bound but never enforced — fixed

`difficulty.go` documented `MinShareTarget` as the bound that "prevents the
difficulty from going too high (target too low)", but `NextTarget` clamped
only against `MaxShareTarget`; `MinShareTarget` was used solely as a
difficulty denominator for display in `internal/node/node.go`. The only real
lower bound was `clampToNetwork`, guarded by `sc.networkTarget != nil` and
therefore inert until the first `SetNetworkTarget` call (`node.go:315`).
While inert, each retarget could divide the target by 4 with no floor:
`1024 → 256 → 64 → 16 → 4 → 1 → 0`. A target of 0 is unmineable.

**Fix.** The constant's two roles are now separate. `DifficultyOneTarget`
(`0x1d00ffff`) is the difficulty-1 denominator, which is what `node.go` was
actually using. `MinShareTarget` is now a genuine floor at `0x1500ffff` — a
target of ~2^160, i.e. a share difficulty of ~2^64 — and `NextTarget` clamps
to it. Bitcoin's own difficulty has never exceeded ~2^47, so this cannot
bind on a live pool; it exists only to keep the retarget away from zero. The
value is exactly representable in compact form, so the round-trip at the end
of `NextTarget` cannot push a clamped result back below it.

### 4. Payout conservation holds (no defect)

`CalculatePayouts` distributes exactly `totalReward` across every input in
the bounded domain checked: 2 520 combinations of per-miner weights, reward
values from 1 satoshi to a full 3.125 BTC subsidy, and finder addresses that
are in the window, outside it, or absent. The truncating division, the
remainder sweep and the dust consolidation together neither create nor
destroy satoshis. `FinderGetsFee` and `NoNonPositive` also hold throughout.

### 5. Below-dust outputs could reach the coinbase — fixed

Two outputs could sit below the dust threshold: the finder, exempt from the
dust sweep by design, and every output at once when consolidation was skipped
because they were all dust. Either makes the coinbase non-standard, so the
block would not relay and the whole reward would be lost.

**Fix.** `CalculatePayouts` ends with a standardness pass that folds each
remaining dust output into the largest other one, smallest first, ties broken
by address. Nothing is discarded — only the recipient moves — and each pass
drops one entry, so it terminates. If a single output is left and it is still
below the threshold, the reward itself is smaller than the dust limit and
there is nothing better to do.

One deliberate trade-off: when the finder's own payout is below dust it now
gets folded away, so `FinderGetsFee` no longer holds at rewards near the dust
limit. Standardness wins, because an unrelayable coinbase costs everyone the
entire reward. At any realistic subsidy the 0.5% fee is orders of magnitude
above dust and the finder is never touched — `PPLNS_MainnetDust.cfg` asserts
both invariants together at a 3.125 BTC subsidy and passes.

### 6. A node could not join a pool whose peers had pruned — fixed

`PruneOldShares` runs on a ticker on every node, leaving each node's oldest
stored share with a `PrevShareHash` pointing at a share nobody has any more.
`ValidateShare` rejected any share whose parent was absent, and the oldest
share a peer serves is exactly such a share — so it was rejected, its child
was rejected, and the whole sync added nothing. At a 30-second share target
the pool closed to new members after about three days.

**Fix.** `ShareChain.AddShareAsRoot` accepts a share whose parent is unknown
as the root of a chain. The sync driver calls it for the **oldest share of a
batch only**, so a failed download mid-batch is still rejected rather than
quietly spawning a disconnected fragment. Gossip never takes this path.

Rooting is safe because a rooted chain starts from zero accumulated work: to
become this node's tip it has to out-work the chain already held, which a
fabricated chain cannot do without real hashrate.

Modelling the fix surfaced a second stuck state the original report missed.
Keying root acceptance off an empty store — the first thing tried — leaves a
node that partially synced, or that was offline for longer than the window,
holding a chain entirely below every peer's horizon with no way to re-anchor.
`AddShareAsRoot` is therefore explicit rather than conditional on the store
being empty, and `TestProbe_StaleNodeCanResync` covers it.

Two supporting changes: `syncFromAllPeers` merges peer inventories in
descending inventory length instead of ranging over a map in Go's randomized
order, so the merged list really is oldest-first and the root lands on the
oldest share; and `PruneKeep` retains `windowSize + DifficultyAdjustmentWindow
+ 1` shares so the payout window sits entirely inside the strictly validated
part of the chain.

### 7. Honest peers disconnected each other over clock skew — fixed

The `MaxTimeFuture` rejection returned a `ValidationError` with no `Category`,
and `CategoryProvable` is the zero value — so `handleP2PShare` charged it to
the relaying peer. The check is against the validating node's own clock, so it
proved nothing: a peer whose clock ran ahead applied the identical rule and
accepted. Five such shares disconnected an honest peer, and TLC reached that
with 15 minutes of skew.

**Fix.** That rejection now carries `CategoryIndeterminate`, so
`handleP2PShare` returns without touching the peer's counters — the same
treatment a parent-not-found rejection already received.

### 8. Expected share target jumped 4x at the prune horizon — fixed

`getExpectedTargetForParent` calls `GetAncestors(parentHash, 72)`, which stops
early at whatever the store is missing. For a parent at a node's prune horizon
only the parent came back, and `NextTarget` returns `MaxShareTarget` outright
for a window shorter than two shares — so the node rejected the child for
"share target mismatch", which *is* legitimately `CategoryProvable` and fed
finding 7's disconnect path.

**Fix.** `Validator.historyIsComplete` reports whether the walk back from a
parent reaches the start of the chain, or a full difficulty window, without
running into a share this node no longer holds. The consensus-target checks
are applied only when it does. Where the history is incomplete the node does
not claim to know the consensus target: it checks the share against the target
it declares, bounded to `[MinShareTarget, MaxShareTarget]`, and still requires
the share to meet it. `ValidateLoaded` always skipped the oldest shares for
this reason; this applies the same rule on the live path.

The relaxation is confined to history. Any share whose parent has a full
window behind it — every share at the tip of a chain longer than the window -
takes the strict path, so the tip is always fully validated.

Worth being precise about what this does and does not achieve. The two nodes
still *compute* different expected targets; the measured 4x gap is unchanged:

```
expected target, full history : 1ffff0...
expected target, at horizon   : 7fffff...   (MaxShareTarget)
```

What changed is that the difference no longer becomes a rejection or a
misbehavior charge. A node at its horizon is now more *permissive* than its
peers over a 72-share historical band, rather than wrongly stricter. That is
the same trust assumption `ValidateLoaded` already made, and `SameVerdict` is
documented in `Validation.tla` as deliberately not asserted.

---

### 9. The sharechain did not enforce payouts — fixed

The project's premise is that payouts need no trusted operator:

> **Trustless** — Payouts are enforced by sharechain consensus.

They were not. `ValidateShare` step 9 was the only check on coinbase outputs,
and it called `ValidateMinerInOutputs`, which scans for an output paying the
share's own miner and returns `nil` on the first match. No amount was read, no
other miner was looked for, and `CalculatePayouts` was never called on the
validation path at all. A miner could build a coinbase paying themselves the
entire block reward and every node accepted the share.

**Fix.** Validation now recomputes the PPLNS split from the share's own window
and compares it against the coinbase, via `types.ValidatePayoutsInOutputs`.

Three things had to change for that comparison to be agreeable between nodes:

- **The total comes from the share, not from a block template.** A template
  depends on each node's mempool, so peers cannot agree on an absolute reward.
  They can agree on how a given total must be divided, so the total is summed
  from the share's own value-carrying outputs and the split is checked against
  it. Zero-value outputs are skipped — the witness commitment is an OP_RETURN
  and is not a payout.
- **The split is exact integer arithmetic.** The finder fee was
  `int64(float64(totalReward) * percent / 100.0)`; it is now basis points and
  integer division. Float arithmetic would almost certainly have been
  deterministic, but there is no reason for consensus to depend on that.
- **The fee and dust threshold are protocol constants.** They were per-node
  configuration; two nodes set differently would now reject each other's every
  share. `ConsensusFinderFeeBasisPoints` and `ConsensusDustThresholdSats` are
  fixed in `sharechain`, the node builds its own coinbase from them through
  `ShareChain.ExpectedPayouts`, and `config.Validate` rejects a config that
  tries to change either.

As with findings 6 and 8, the check only applies where the node holds the
history it is derived from — `historyIsComplete(parent, windowSize)`. This is
a wider band than the target check: a freshly bootstrapped node cannot enforce
payouts until it has a full PPLNS window above its root, roughly three days at
a 30-second share target. That band shrinks as the chain grows, and there is
no way around it — a node cannot recompute a split over shares it does not
have.

`PruneKeep` already retains `windowSize + DifficultyAdjustmentWindow + 1`, so
a share at the tip always has the full window behind it.

### 10. A stale `lastReorgTip` left miners on the wrong tip — fixed

`AddShare` emits `EventReorg` then `EventNewTip`, and `handleChainEvent` used
a `lastReorgTip` flag to swallow the second so a reorg did not generate two
jobs. But `emit` drops events when the subscriber channel is full. If the
`EventNewTip` the flag was armed for was dropped, the flag stayed armed and
swallowed a *later*, genuine tip event — leaving miners on a stale parent.

**Fix.** The suppression is gone. A reorg now generates two jobs for the same
tip, which costs one redundant `mining.notify` on a rare event; the flag could
cost 30 seconds of the pool's hashrate mining a dead parent. `Reorg_Tight.cfg`
— the configuration that produced the counterexample — now converges, and
`Reorg_Dedupe.cfg` keeps the old behaviour for comparison.

### 11. The vardiff grace window could close on work in flight — fixed

`handleSubmission` accepted a share meeting either `Difficulty` or
`PrevDifficulty`, giving one change of grace. `RecordShare` cannot retarget
more often than `VardiffRetargetTime`, but `SetDifficulty` — reached from
`mining.suggest_difficulty` with a miner-supplied value — had no minimum
interval, so two changes could land back to back and strand shares already in
flight.

**Fix.** `Vardiff` keeps the difficulties it has issued, newest first, dropping
them after `VardiffGraceWindow` (60s) and capping the list at 8.
`AcceptableDifficulties` returns the current one plus everything still inside
the window, the submission carries that set, and `handleSubmission` credits a
share meeting any of them.

## Checked and clean

Verified in this round, no defect found:

- **Orphan pruning** (`FC_Orphan.cfg`, 2 714 740 states) — adding
  `PruneOrphans` alongside `PruneOldShares` breaks neither the work anchor nor
  agreement between nodes holding the same shares.
- **Dropped chain events** (`Reorg.cfg`, `Reorg_NoRefresh.cfg`) — `emit`
  dropping events on a full channel is self-healing, because `GenerateJob`
  reads the live chain tip rather than the hash carried by the event. The
  pipeline converges even with the refresh timer disabled; only the stale-flag
  path in finding 10 does not.
- **Vardiff bounds** — difficulty stays within
  `[VardiffMinDifficulty, VardiffMaxDifficulty]` under every suggestion and
  share-count combination tested, including negative, zero and 1e300.
- **Stratum submit flooding** — `rate.NewLimiter(100, 20)` on the submit path
  bounds it, and the submit channel drops rather than blocking.

## Scope

Model checking is exhaustive over the bounded configurations listed above,
not over all inputs. A passing run means no counterexample exists within
those bounds. The models cover fork choice, difficulty retargeting and the
payout split; they do not model the libp2p transport, the Stratum server,
bbolt persistence, or Go-level concurrency.
