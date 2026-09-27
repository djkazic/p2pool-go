---------------------------- MODULE Difficulty ----------------------------
(***************************************************************************)
(* A TLA+ model of p2pool-go's sharechain difficulty retarget.             *)
(*                                                                         *)
(* Mirrors internal/sharechain/difficulty.go NextTarget and                *)
(* internal/sharechain/chain.go clampToNetwork.                            *)
(*                                                                         *)
(* Targets are modelled as integers on a scaled-down axis: MaxT stands     *)
(* for MaxShareTarget (0x207fffff) and MinT for MinShareTarget             *)
(* (0x1d00ffff).  Only the ORDER and the RATIOS between targets matter to  *)
(* the algorithm, and both are preserved.                                  *)
(*                                                                         *)
(* Quantize models the CompactToTarget(TargetToCompact(t)) round-trip that *)
(* NextTarget applies to its result.  The real round-trip keeps 23 bits of *)
(* mantissa and truncates the rest, so it can only ever move a target      *)
(* DOWN; Quantize keeps MantissaBits bits and truncates likewise.          *)
(*                                                                         *)
(* The retarget is driven by an adversary who picks the observed timing    *)
(* within bounds, which is exactly the freedom a majority of hashrate has: *)
(* actualTime is a span between two median share timestamps.               *)
(***************************************************************************)
EXTENDS Integers, Sequences

CONSTANTS
    MaxT,          \* MaxShareTarget
    MinT,          \* MinShareTarget -- the bound difficulty.go DECLARES
    MantissaBits,  \* significant bits kept by the compact round-trip
    MaxRatioNum,   \* adversary's timing lever: actualTime  \in 1..MaxRatioNum
    ExpectedTime,  \* targetTime * intervals
    NetworkTarget, \* clampToNetwork floor; Nil means SetNetworkTarget not yet called
    Nil,
    MaxSteps

VARIABLES target, steps
vars == <<target, steps>>

-----------------------------------------------------------------------------
(* Number of significant bits of a positive integer. *)
RECURSIVE BitLen(_)
BitLen(x) == IF x <= 0 THEN 0 ELSE 1 + BitLen(x \div 2)

RECURSIVE Pow2(_)
Pow2(k) == IF k <= 0 THEN 1 ELSE 2 * Pow2(k - 1)

(* CompactToTarget(TargetToCompact(t)): keep the top MantissaBits bits,
   truncate the rest.  Truncation, never rounding, so Quantize(t) <= t. *)
Quantize(t) ==
    IF t <= 0 THEN 0
    ELSE IF BitLen(t) <= MantissaBits THEN t
    ELSE LET g == Pow2(BitLen(t) - MantissaBits) IN (t \div g) * g

-----------------------------------------------------------------------------
(***************************************************************************)
(* difficulty.go NextTarget, from the point where currentTarget and the    *)
(* timing span are in hand.  Reproduced in the original order:             *)
(*                                                                         *)
(*   newTarget = currentTarget * actualTime / expectedTime                 *)
(*   clamp to [currentTarget/4, currentTarget*4]                           *)
(*   clamp to [MinShareTarget, MaxShareTarget]                             *)
(*   compact round-trip                                                    *)
(*                                                                         *)
(* The lower clamp is what stops the 4x-per-step reduction from ratcheting *)
(* the target to zero.  MinShareTarget is exactly representable in compact *)
(* form, so the round-trip that follows cannot push a clamped value back   *)
(* below it.                                                               *)
(***************************************************************************)
NextTarget(cur, actualTime) ==
    LET expected  == IF ExpectedTime <= 0 THEN 1 ELSE ExpectedTime
        actual    == IF actualTime  <= 0 THEN 1 ELSE actualTime
        raw       == (cur * actual) \div expected
        maxAdjust == cur * 4
        minAdjust == cur \div 4
        a         == IF raw > maxAdjust THEN maxAdjust ELSE raw
        b         == IF a   < minAdjust THEN minAdjust ELSE a
        c         == IF b   > MaxT      THEN MaxT      ELSE b
        e         == IF c   < MinT      THEN MinT      ELSE c
    IN Quantize(e)

(* chain.go clampToNetwork: applied by getExpectedTarget*, and a no-op
   until SetNetworkTarget has been called (networkTarget starts nil). *)
ClampToNetwork(t) ==
    IF NetworkTarget = Nil THEN t
    ELSE IF t < NetworkTarget THEN Quantize(NetworkTarget)
    ELSE t

Retarget(cur, actualTime) == ClampToNetwork(NextTarget(cur, actualTime))

-----------------------------------------------------------------------------
Init == target = MaxT /\ steps = 0

Step ==
    /\ steps < MaxSteps
    /\ \E a \in 1..MaxRatioNum :
          target' = Retarget(target, a)
    /\ steps' = steps + 1

Done == steps >= MaxSteps /\ UNCHANGED vars

Next == Step \/ Done
Spec == Init /\ [][Next]_vars

-----------------------------------------------------------------------------
(***************************************************************************)
(*                              INVARIANTS                                 *)
(***************************************************************************)

TypeOK == target \in Int /\ steps \in 0..MaxSteps

\* A target of zero is unmineable: no hash satisfies hash <= 0, so the
\* sharechain would stop accepting shares entirely.
TargetPositive == target > 0

\* The floor MinShareTarget enforces: the target never drops below the
\* hardest share the chain is willing to ask for.
TargetAboveMin == target >= MinT

\* The target must stay at or below the easiest allowed value.
TargetBelowMax == target <= MaxT

\* Every value the retarget can produce must survive the compact round-trip
\* unchanged, or two nodes -- one that mined the share locally, one that
\* received it as a compact uint32 over P2P -- would disagree about its
\* target and reject each other's shares.
CompactStable == Quantize(target) = target

=============================================================================
