------------------------------- MODULE PPLNS -------------------------------
(***************************************************************************)
(* A TLA+ model of p2pool-go's PPLNS payout split.                         *)
(*                                                                         *)
(* Mirrors internal/pplns/calculator.go CalculatePayouts and               *)
(* internal/pplns/window.go MinerWeights / TotalWeight.                    *)
(*                                                                         *)
(* Addresses are integers so that "sort.Strings(addresses)" becomes "<".   *)
(* Miner addresses are 1..NumMiners; ExternalFinder is an address that     *)
(* has no shares in the window (a finder whose window entry has expired);  *)
(* NoFinder models the empty finderAddress string.                         *)
(*                                                                         *)
(* This is a pure function, so the model has no transitions: Init ranges   *)
(* over every input in the bounded domain and TLC checks the invariants    *)
(* against each one exhaustively.                                          *)
(***************************************************************************)
EXTENDS Integers, FiniteSets

CONSTANTS
    NumMiners,       \* miner addresses are 1..NumMiners
    Weights,         \* per-miner weight values to explore (0 = no shares)
    Rewards,         \* totalReward values to explore, in satoshis
    FeeNum, FeeDen,  \* finderFeePercent as the exact fraction FeeNum/FeeDen
    Dust             \* dustThresholdSats

Miners         == 1..NumMiners
ExternalFinder == NumMiners + 1
NoFinder       == 0
Addrs          == 1..(NumMiners + 1)
Finders        == {NoFinder} \cup Addrs

SumOver(f, S) == LET g[T \in SUBSET S] ==
                     IF T = {} THEN 0
                     ELSE LET x == CHOOSE y \in T : TRUE
                          IN f[x] + g[T \ {x}]
                 IN g[S]

Min(S) == CHOOSE x \in S : \A y \in S : x <= y

(***************************************************************************)
(* calculator.go, the final standardness pass.                             *)
(*                                                                         *)
(* Two outputs can still sit below the dust threshold after the sweep: the  *)
(* finder, who is exempt from it, and every output at once when            *)
(* consolidation was skipped because they were all dust. Either makes the   *)
(* coinbase non-standard, so the block would not relay and the whole reward *)
(* would be lost.                                                          *)
(*                                                                         *)
(* Fold each remaining dust output into the largest other one, smallest     *)
(* first, ties broken by address. Nothing is discarded -- only the          *)
(* recipient moves -- and each pass drops one entry, so it terminates.      *)
(***************************************************************************)
RECURSIVE DustFold(_)
DustFold(f) ==
    LET present == {a \in Addrs : f[a] > 0}
        dusty   == {a \in present : f[a] < Dust}
    IN IF Cardinality(present) <= 1 \/ dusty = {}
       THEN f
       ELSE LET small == CHOOSE x \in dusty :
                             \A y \in dusty : f[x] <= f[y] /\ (f[x] = f[y] => x <= y)
                rest  == present \ {small}
                large == CHOOSE x \in rest :
                             \A y \in rest : f[x] >= f[y] /\ (f[x] = f[y] => x <= y)
            IN DustFold([f EXCEPT ![small] = 0, ![large] = f[large] + f[small]])

-----------------------------------------------------------------------------
(***************************************************************************)
(* calculator.go CalculatePayouts.  Returned as a total function over      *)
(* Addrs; 0 means "no entry in the payouts map", which is exactly how the  *)
(* Go code behaves, since it only ever stores strictly positive amounts.   *)
(***************************************************************************)
Payouts(w, total, finder) ==
    LET W    == SumOver(w, Miners)
        fee  == (total * FeeNum) \div FeeDen       \* finderFeePercent of total
        dist == total - fee                        \* distributableReward

        \* payout = distributableReward * weight / totalWeight, truncated.
        \* Only strictly positive amounts are stored.
        base == [a \in Addrs |-> IF a \in Miners /\ w[a] > 0
                                 THEN (dist * w[a]) \div W
                                 ELSE 0]

        \* payouts[finderAddress] += finderFee
        wFee == IF finder # NoFinder /\ fee > 0
                THEN [base EXCEPT ![finder] = base[finder] + fee]
                ELSE base

        distributed == SumOver(wFee, Addrs)
        rem         == total - distributed

        \* Rounding remainder goes to the finder, else to addresses[0] --
        \* the lexicographically first miner, whether or not it already
        \* has an entry.
        wRem == IF rem > 0
                THEN IF finder # NoFinder
                     THEN [wFee EXCEPT ![finder] = wFee[finder] + rem]
                     ELSE [wFee EXCEPT ![Min(Miners)] = wFee[Min(Miners)] + rem]
                ELSE wFee

        present == {a \in Addrs : wRem[a] > 0}
        \* The finder is deliberately exempt from dust consolidation.
        dustA   == {a \in present : wRem[a] < Dust /\ a # finder}
        dustTot == SumOver(wRem, dustA)

        \* "If ALL payouts are below dust, skip consolidation entirely."
        doCons  == Cardinality(dustA) < Cardinality(present) /\ dustTot > 0
        cleared == [a \in Addrs |-> IF a \in dustA THEN 0 ELSE wRem[a]]
        \* Sink: the finder, else the first miner that still has an entry.
        survivors == {a \in Miners : cleared[a] > 0}
        sink    == IF finder # NoFinder THEN finder
                   ELSE IF survivors # {} THEN Min(survivors)
                   ELSE NoFinder
        swept == IF ~doCons THEN wRem
                 ELSE IF sink = NoFinder THEN cleared
                 ELSE [cleared EXCEPT ![sink] = cleared[sink] + dustTot]
    IN DustFold(swept)

-----------------------------------------------------------------------------
VARIABLES w, total, finder, out
vars == <<w, total, finder, out>>

Init ==
    /\ w      \in [Miners -> Weights]
    /\ total  \in Rewards
    /\ finder \in Finders
    \* CalculatePayouts bails out early on an empty window or a
    \* non-positive reward; those inputs return nil and are out of scope.
    /\ SumOver(w, Miners) > 0
    /\ total > 0
    /\ out = Payouts(w, total, finder)

Next == UNCHANGED vars
Spec == Init /\ [][Next]_vars

-----------------------------------------------------------------------------
(***************************************************************************)
(*                              INVARIANTS                                 *)
(***************************************************************************)

\* VALUE CONSERVATION: the coinbase pays out exactly the block reward.
\* Too little burns miner funds; too much makes the coinbase invalid and
\* the block unmineable.
PayoutConservation == SumOver(out, Addrs) = total

\* No negative or zero-valued outputs may reach the coinbase builder.
NoNonPositive == \A a \in Addrs : out[a] >= 0

\* Every output must clear the dust threshold, or the coinbase is
\* non-standard and the block will not relay. Conditional on the reward
\* itself clearing the threshold: a reward smaller than the dust limit
\* cannot be paid out standardly at all, and dropping it would burn funds.
NoDustOutputs ==
    total >= Dust => \A a \in Addrs : out[a] = 0 \/ out[a] >= Dust

\* The block finder must receive at least the finder fee it is owed.
\* Only meaningful once the fee itself clears the dust threshold: below that
\* the standardness pass may fold the finder's output into a larger one to
\* keep the coinbase relayable. At any realistic subsidy the fee is orders of
\* magnitude above dust, which PPLNS_MainnetDust.cfg checks.
FinderGetsFee ==
    LET fee == (total * FeeNum) \div FeeDen
    IN (finder # NoFinder /\ fee > 0) => out[finder] >= fee

=============================================================================
