------------------------------- MODULE Sync --------------------------------
(***************************************************************************)
(* A TLA+ model of a fresh node bootstrapping its sharechain from a peer.  *)
(*                                                                         *)
(* Mirrors:                                                                *)
(*   internal/node/node.go        handleInvRequest / handleDataRequest     *)
(*                                (a peer serves its MAIN CHAIN, oldest    *)
(*                                first), syncFromAllPeers (adds shares    *)
(*                                in that order via AddShareQuiet)         *)
(*   internal/sharechain/chain.go PruneOldShares(PPLNSWindowSize)          *)
(*   internal/sharechain/validation.go  the "parent must exist" rule       *)
(*                                                                         *)
(* The chain is linear: share s has parent s-1, and share 1 has the zero   *)
(* hash as its parent.  Forks are irrelevant here -- handleInvRequest only *)
(* ever serves main-chain hashes.                                          *)
(***************************************************************************)
EXTENDS Integers, FiniteSets

CONSTANTS
    ChainLen,   \* how many shares the seed node has mined
    Keep        \* chain.go PruneOldShares(maxKeep) == config.PPLNSWindowSize

Shares == 1..ChainLen
Nil    == 0

\* share.PrevShareHash
Parent(s) == s - 1

VARIABLES
    seedHas,   \* shares the seed node still stores
    served,    \* shares the fresh node has downloaded from the seed
    acquired,  \* shares the fresh node has validated and added
    pruned     \* whether the seed has run PruneOldShares

vars == <<seedHas, served, acquired, pruned>>

Init ==
    /\ seedHas  = Shares
    /\ served   = {}
    /\ acquired = {}
    /\ pruned   = FALSE

(***************************************************************************)
(* chain.go PruneOldShares(maxKeep): keep the most recent maxKeep shares   *)
(* on the main chain, delete everything older.  Runs on a ticker in        *)
(* node.go once the store grows past maxKeep.                              *)
(***************************************************************************)
Prune ==
    /\ ~pruned
    /\ Cardinality(seedHas) > Keep
    /\ seedHas' = {s \in Shares : s > ChainLen - Keep}
    /\ pruned'  = TRUE
    /\ UNCHANGED <<served, acquired>>

(***************************************************************************)
(* node.go handleInvRequest: with no locators (a fresh node has no tip,    *)
(* so buildLocator returns nil) the responder finds no fork point and      *)
(* serves its whole main chain, oldest first.  handleDataRequest then      *)
(* returns the bodies.                                                     *)
(***************************************************************************)
Serve ==
    /\ served # seedHas
    /\ served' = seedHas
    /\ UNCHANGED <<seedHas, acquired, pruned>>

(***************************************************************************)
(* chain.go AddShareQuiet -> validation.go ValidateShare: a share whose    *)
(* PrevShareHash is not in the store is rejected (CategoryIndeterminate).  *)
(* So a share can only be added once its parent has been added.            *)
(***************************************************************************)
Add(s) ==
    /\ s \in served
    /\ s \notin acquired
    /\ \/ Parent(s) = Nil
       \/ Parent(s) \in acquired
    /\ acquired' = acquired \cup {s}
    /\ UNCHANGED <<seedHas, served, pruned>>

(***************************************************************************)
(* chain.go AddShareAsRoot: the sync path accepts a share whose parent it    *)
(* will never see as the root of a chain -- every peer prunes, so the chain  *)
(* they serve has to start somewhere. This covers a node joining for the     *)
(* first time and a node whose own chain has fallen below every peer's       *)
(* horizon after being offline longer than the window.                       *)
(*                                                                          *)
(* syncFromAllPeers merges peer inventories in descending inventory length,  *)
(* which keeps the merged list oldest-first, and calls AddShareAsRoot for    *)
(* the OLDEST share of the batch only. Rooting anywhere else would strand    *)
(* every share below it; leaving a gap later in the batch un-rooted keeps a  *)
(* failed download from quietly spawning a disconnected fragment.            *)
(***************************************************************************)
AddRoot(s) ==
    /\ s \in served
    /\ s \notin acquired
    /\ \A t \in (served \ acquired) : s <= t  \* the oldest share of the batch
    /\ Parent(s) # Nil                        \* a real dangling parent, not genesis
    /\ Parent(s) \notin acquired
    /\ acquired' = acquired \cup {s}
    /\ UNCHANGED <<seedHas, served, pruned>>

Next ==
    \/ Prune
    \/ Serve
    \/ \E s \in Shares : Add(s)
    \/ \E s \in Shares : AddRoot(s)

Fairness == /\ WF_vars(Serve)
            /\ WF_vars(\E s \in Shares : Add(s))
            /\ WF_vars(\E s \in Shares : AddRoot(s))

Spec == Init /\ [][Next]_vars /\ Fairness

-----------------------------------------------------------------------------
(***************************************************************************)
(*                         PROPERTIES                                      *)
(***************************************************************************)

\* The fresh node ends up holding everything the seed is willing to serve.
\* Subset, not equality: the node may still hold older shares it picked up
\* before the seed pruned them away.
Synced == served \subseteq acquired

\* LIVENESS: a node that joins the pool can always catch up to its peers.
Bootstraps == <>Synced

\* SAFETY, the same thing stated as a reachability check: whatever the seed
\* is serving, there is always some share the fresh node can act on next,
\* until it is fully caught up -- either because its parent is already in
\* hand, or because it can anchor a chain that has nothing below it.
ProgressPossible ==
    LET missing == served \ acquired IN
    missing # {} =>
        \E s \in missing : \/ Parent(s) = Nil
                           \/ Parent(s) \in acquired
                           \/ \A t \in missing : s <= t

=============================================================================
