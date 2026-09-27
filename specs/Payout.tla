------------------------------- MODULE Payout ------------------------------
(***************************************************************************)
(* A TLA+ model of what the sharechain enforces about a share's coinbase.  *)
(*                                                                         *)
(* Mirrors:                                                                *)
(*   internal/sharechain/validation.go  step 9, the only coinbase output    *)
(*                                      check: ValidateMinerInOutputs       *)
(*   internal/types/coinbase.go         ValidateMinerInOutputs, which       *)
(*                                      returns nil as soon as ANY output   *)
(*                                      pays the share's own miner          *)
(*   internal/pplns/calculator.go       CalculatePayouts, the split the     *)
(*                                      pool is supposed to pay             *)
(*                                                                         *)
(* The project's premise is that payouts need no trusted operator because   *)
(* consensus enforces them:                                                 *)
(*                                                                         *)
(*   "Trustless -- Payouts are enforced by sharechain consensus."           *)
(*                                                                         *)
(* This spec asks whether validation actually enforces that. The model is a *)
(* pure decision procedure, so there are no transitions: Init ranges over   *)
(* every window and every coinbase split in a bounded domain and TLC checks *)
(* the invariants against each one.                                        *)
(***************************************************************************)
EXTENDS Integers, FiniteSets

CONSTANTS
    Miners,        \* miner identities
    Weights,       \* per-miner weight in the PPLNS window
    Total,         \* coinbase value to divide
    Splits,        \* per-miner amounts a crafted coinbase may carry
    EnforcePayouts \* TRUE models validation also checking the distribution

SumOver(f, S) == LET g[T \in SUBSET S] ==
                     IF T = {} THEN 0
                     ELSE LET x == CHOOSE y \in T : TRUE
                          IN f[x] + g[T \ {x}]
                 IN g[S]

VARIABLES window, payout, submitter
vars == <<window, payout, submitter>>

TotalWeight == SumOver(window, Miners)

(***************************************************************************)
(* calculator.go CalculatePayouts, reduced to the proportional split. The   *)
(* finder fee and dust handling are deliberately left out: they perturb the *)
(* split by a few percent, and the question here is whether the split is    *)
(* enforced at all.                                                        *)
(***************************************************************************)
Deserved(m) == (Total * window[m]) \div TotalWeight

(***************************************************************************)
(* validation.go step 9, in full. ParseCoinbaseOutputs then                 *)
(* ValidateMinerInOutputs -- which scans for an output paying the share's   *)
(* own miner and returns nil on the first match. No amount is read, no      *)
(* other miner is looked for, and CalculatePayouts is never called on this  *)
(* path: its only callers are the node's own coinbase builder and the       *)
(* dashboard.                                                              *)
(***************************************************************************)
MinerIsPaid == payout[submitter] > 0

Accepted ==
    IF EnforcePayouts
      THEN MinerIsPaid /\ \A m \in Miners : payout[m] = Deserved(m)
      ELSE MinerIsPaid

-----------------------------------------------------------------------------
Init ==
    /\ window \in [Miners -> Weights]
    /\ payout \in [Miners -> Splits]
    /\ submitter \in Miners
    /\ TotalWeight > 0
    \* A coinbase always pays out exactly the block reward; Bitcoin consensus
    \* enforces that independently of anything here.
    /\ SumOver(payout, Miners) = Total

Next == UNCHANGED vars
Spec == Init /\ [][Next]_vars

-----------------------------------------------------------------------------
(***************************************************************************)
(*                              INVARIANTS                                 *)
(***************************************************************************)

\* THE PROMISE: a share the sharechain accepts pays every miner what the
\* PPLNS window says they earned.
PayoutsMatchWindow ==
    Accepted => \A m \in Miners : payout[m] = Deserved(m)

\* Weaker: an accepted share at least cannot pay a contributing miner less
\* than half of what they earned.
NoMajorityTheft ==
    Accepted => \A m \in Miners : window[m] > 0 => payout[m] * 2 >= Deserved(m)

\* What validation does guarantee: the submitter is paid something.
SubmitterIsPaid == Accepted => payout[submitter] > 0

=============================================================================
