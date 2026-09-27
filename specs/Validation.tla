---------------------------- MODULE Validation -----------------------------
(***************************************************************************)
(* A TLA+ model of how p2pool-go decides a share's consensus target, and    *)
(* what happens when two nodes disagree.                                   *)
(*                                                                         *)
(* Mirrors:                                                                *)
(*   internal/sharechain/validation.go  historyIsComplete and the target    *)
(*                                      checks it gates                     *)
(*   internal/sharechain/difficulty.go  NextTarget, which returns           *)
(*                                      MaxShareTarget outright for a       *)
(*                                      window shorter than two shares      *)
(*   internal/node/node.go              handleP2PShare, which charges a     *)
(*                                      CategoryProvable rejection to the   *)
(*                                      peer that relayed the share         *)
(*                                                                         *)
(* The chain is linear: share s has parent s-1, and share 1 starts the      *)
(* chain. Node n holds shares horizon[n]..ChainLen -- everything below its  *)
(* horizon has been pruned. Nodes prune at different points, so they hold   *)
(* different amounts of history behind the very same parent.               *)
(*                                                                         *)
(* This is a pure decision procedure, so the model has no transitions:      *)
(* Init ranges over every combination of horizons and parents, and TLC      *)
(* checks the invariants against each one.                                  *)
(***************************************************************************)
EXTENDS Integers

CONSTANTS
    Nodes,
    ChainLen,      \* shares 1..ChainLen
    DiffWindow,    \* DifficultyAdjustmentWindow
    GateOnHistory  \* TRUE models the historyIsComplete gate

MaxT      == 0   \* stands for MaxShareTarget
Consensus == 1   \* stands for the retargeted value a full window produces

\* validation.go historyIsComplete: walking back from parent p, do we reach
\* the start of the chain or a full window without hitting a share we no
\* longer hold? A node whose horizon is 1 holds everything.
Complete(h, p) == h = 1 \/ (p - h + 1) >= DiffWindow

\* difficulty.go NextTarget over the ancestors this node can actually see.
\* Fewer than two and it returns MaxShareTarget outright.
Expected(h, p) == IF (p - h + 1) < 2 THEN MaxT ELSE Consensus

\* The share under test is a correct one: it declares what a node holding the
\* whole chain computes, which is what the rest of the network accepts.
Declared(p) == Expected(1, p)

(***************************************************************************)
(* validation.go, steps 5-7. With the gate, a node that cannot see the      *)
(* history the target is derived from does not claim to know the consensus  *)
(* target; it checks the share against its own declared target instead,     *)
(* which a correct share always satisfies. Without the gate it compares     *)
(* against whatever its truncated window produced.                          *)
(***************************************************************************)
Verdict(h, p) ==
    IF GateOnHistory /\ ~Complete(h, p)
      THEN "accept"
      ELSE IF Expected(h, p) = Declared(p) THEN "accept" ELSE "rejectProvable"

VARIABLES horizon, parent, verdict
vars == <<horizon, parent, verdict>>

Init ==
    /\ horizon \in [Nodes -> 1..ChainLen]
    /\ parent \in 1..ChainLen
    \* Only nodes that actually hold the parent get to validate the share at
    \* all; without it they return CategoryIndeterminate, which is not charged.
    /\ \A n \in Nodes : parent >= horizon[n]
    /\ verdict = [n \in Nodes |-> Verdict(horizon[n], parent)]

Next == UNCHANGED vars
Spec == Init /\ [][Next]_vars

-----------------------------------------------------------------------------
(***************************************************************************)
(*                              INVARIANTS                                 *)
(***************************************************************************)

\* No node charges a peer with PROVABLE misbehavior for relaying a share the
\* rest of the network accepts. A provable rejection feeds recordPeerOutcome
\* and, repeated, disconnects an honest peer.
NoProvableRejection ==
    \A n \in Nodes : verdict[n] # "rejectProvable"

\* Every node that holds the parent reaches the same verdict on the share.
\* Strictly stronger than the above, and NOT expected to hold: a node at its
\* prune horizon accepts on the relaxed path while a node with the full
\* history accepts on the strict one -- same verdict here, but the relaxed
\* path would also accept a share the strict path rejects. The relaxation is
\* confined to shares within one difficulty window of a node's horizon.
SameVerdict ==
    \A n, m \in Nodes : verdict[n] = verdict[m]

=============================================================================
