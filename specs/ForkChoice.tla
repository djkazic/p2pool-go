---------------------------- MODULE ForkChoice ----------------------------
(***************************************************************************)
(* A TLA+ model of p2pool-go's sharechain fork choice.                     *)
(*                                                                         *)
(* Mirrors, statement for statement:                                       *)
(*   internal/sharechain/fork.go   AssignWork, ChainWork, SelectTip        *)
(*   internal/sharechain/chain.go  AddShare (store, assign work, then      *)
(*                                 fork choice), PruneOldShares            *)
(*   internal/sharechain/validation.go  the "parent must exist" rule       *)
(*                                                                         *)
(* Share hashes are modelled as the integers 1..MaxShares.  The Go code    *)
(* breaks work ties with "numerically lower hash wins", so the model       *)
(* breaks ties with "numerically lower id wins" -- a deterministic total   *)
(* order, which is all the algorithm relies on.                            *)
(***************************************************************************)
EXTENDS Integers, Sequences, FiniteSets

CONSTANTS
    Nodes,        \* set of node identities
    MaxShares,    \* bound on shares created (model-checking bound)
    PruneKeep,    \* chain.go PruneOldShares(maxKeep)
    Diffs,        \* possible values of shareDifficulty(share)
    AllowPrune    \* TRUE to enable the PruneOldShares action

ShareIds == 1..MaxShares
Nil      == 0          \* the zero hash
NoWork   == -1         \* share.cumulativeWork == nil

VARIABLES
    nextId,   \* next share id to hand out
    parent,   \* [ShareIds -> Nil \cup ShareIds]   share.PrevShareHash
    sdiff,    \* [ShareIds -> Diffs]               shareDifficulty(share)
    knows,    \* [Nodes -> SUBSET ShareIds]        store contents
    tip,      \* [Nodes -> Nil \cup ShareIds]      store.Tip()
    cache     \* [Nodes -> [ShareIds -> Int]]      share.cumulativeWork

vars    == <<nextId, parent, sdiff, knows, tip, cache>>
Created == 1..(nextId - 1)

(***************************************************************************)
(* The work a chain really has: every share back to the zero hash.  This   *)
(* is the yardstick the implementation is measured against, not something  *)
(* the implementation computes -- after pruning it cannot, because the     *)
(* shares below the horizon are gone.                                      *)
(***************************************************************************)
RECURSIVE TrueWork(_,_,_)
TrueWork(par, dif, s) ==
    IF s = Nil THEN 0 ELSE dif[s] + TrueWork(par, dif, par[s])

(***************************************************************************)
(* fork.go ChainWork.                                                      *)
(*                                                                         *)
(* Walk mirrors the backward loop.  It stops on the zero hash, on a share  *)
(* missing from the store (the prune horizon), or on an ancestor that      *)
(* already has a value.  There is no depth limit: the earlier windowSize   *)
(* cap made a long chain report only the work of its most recent           *)
(* windowSize shares.                                                      *)
(***************************************************************************)
RECURSIVE Walk(_,_,_,_,_)
Walk(par, kn, cch, cur, acc) ==
    IF cur = Nil        THEN [pend |-> acc, base |-> 0]
    ELSE IF cur \notin kn THEN [pend |-> acc, base |-> 0]
    ELSE IF cch[cur] # NoWork
                        THEN [pend |-> acc, base |-> cch[cur]]
    ELSE Walk(par, kn, cch, par[cur], Append(acc, cur))

(***************************************************************************)
(* The forward pass.  pend is newest-first, so walking idx down to 1       *)
(* visits oldest-first.  Every walk now ends at a genuine boundary, so     *)
(* every total is final and is written back unconditionally.               *)
(***************************************************************************)
RECURSIVE Accum(_,_,_,_,_)
Accum(dif, pend, idx, w, cch) ==
    IF idx = 0 THEN [work |-> w, cache |-> cch]
    ELSE LET s  == pend[idx]
             nw == w + dif[s]
         IN Accum(dif, pend, idx - 1, nw, [cch EXCEPT ![s] = nw])

ChainWork(par, dif, kn, cch, h) ==
    IF h = Nil \/ h \notin kn   THEN [work |-> 0,      cache |-> cch]
    ELSE IF cch[h] # NoWork     THEN [work |-> cch[h], cache |-> cch]
    ELSE LET r == Walk(par, kn, cch, par[h], <<h>>)
         IN Accum(dif, r.pend, Len(r.pend), r.base, cch)

(***************************************************************************)
(* fork.go AssignWork: a share's work is its parent's work plus its own    *)
(* difficulty, recorded as the share is added.  Validation guarantees the  *)
(* parent is present, so this is O(1) in the steady state.                 *)
(***************************************************************************)
AssignWork(par, dif, kn, cch, s) ==
    LET r == IF par[s] = Nil
             THEN [work |-> 0, cache |-> cch]
             ELSE ChainWork(par, dif, kn, cch, par[s])
        w == r.work + dif[s]
    IN [work |-> w, cache |-> [r.cache EXCEPT ![s] = w]]

(***************************************************************************)
(* fork.go SelectTip: the heavier chain wins, ties go to the lower hash.   *)
(* The "candidate directly extends the current tip" shortcut is gone --    *)
(* with whole-chain work a child always outweighs its parent, so the       *)
(* shortcut is redundant, and without it tip selection is a plain maximum  *)
(* over known shares rather than a function of arrival order.              *)
(***************************************************************************)
SelectTip(par, dif, kn, cch, cur, cand) ==
    IF cur = Nil  THEN [tip |-> cand, cache |-> cch]
    ELSE IF cur = cand THEN [tip |-> cur, cache |-> cch]
    ELSE LET r1 == ChainWork(par, dif, kn, cch,      cur)
             r2 == ChainWork(par, dif, kn, r1.cache, cand)
         IN IF   r2.work > r1.work THEN [tip |-> cand, cache |-> r2.cache]
            ELSE IF r2.work < r1.work THEN [tip |-> cur,  cache |-> r2.cache]
            ELSE IF cur \in kn /\ cand \in kn /\ cand < cur
                 THEN [tip |-> cand, cache |-> r2.cache]
                 ELSE [tip |-> cur,  cache |-> r2.cache]

(***************************************************************************)
(* store.GetAncestors(h, k)                                                *)
(***************************************************************************)
RECURSIVE AncestorsOf(_,_,_,_)
AncestorsOf(par, kn, h, k) ==
    IF k = 0 \/ h = Nil \/ h \notin kn
    THEN {}
    ELSE {h} \cup AncestorsOf(par, kn, par[h], k - 1)

-----------------------------------------------------------------------------
Init ==
    /\ nextId = 1
    /\ parent = [s \in ShareIds |-> Nil]
    /\ sdiff  = [s \in ShareIds |-> 1]
    /\ knows  = [n \in Nodes |-> {}]
    /\ tip    = [n \in Nodes |-> Nil]
    /\ cache  = [n \in Nodes |-> [s \in ShareIds |-> NoWork]]

(***************************************************************************)
(* chain.go AddShare: store the share, assign its cumulative work, then    *)
(* run fork choice.  Validation requires the parent to be present, so a    *)
(* node only ever mines on or learns a share whose parent it already has.  *)
(***************************************************************************)
Mine(n, p, d) ==
    /\ nextId <= MaxShares
    /\ \/ p = Nil
       \/ p \in knows[n]
    /\ LET s   == nextId
           par == [parent EXCEPT ![s] = p]
           dif == [sdiff  EXCEPT ![s] = d]
           kn  == knows[n] \cup {s}
           aw  == AssignWork(par, dif, kn, cache[n], s)
           r   == SelectTip(par, dif, kn, aw.cache, tip[n], s)
       IN /\ nextId' = nextId + 1
          /\ parent' = par
          /\ sdiff'  = dif
          /\ knows'  = [knows EXCEPT ![n] = kn]
          /\ tip'    = [tip   EXCEPT ![n] = r.tip]
          /\ cache'  = [cache EXCEPT ![n] = r.cache]

Learn(n, s) ==
    /\ s \in Created
    /\ s \notin knows[n]
    /\ \/ parent[s] = Nil
       \/ parent[s] \in knows[n]          \* validation.go: parent must exist
    /\ LET kn == knows[n] \cup {s}
           aw == AssignWork(parent, sdiff, kn, cache[n], s)
           r  == SelectTip(parent, sdiff, kn, aw.cache, tip[n], s)
       IN /\ knows' = [knows EXCEPT ![n] = kn]
          /\ tip'   = [tip   EXCEPT ![n] = r.tip]
          /\ cache' = [cache EXCEPT ![n] = r.cache]
    /\ UNCHANGED <<nextId, parent, sdiff>>

(***************************************************************************)
(* chain.go PruneOldShares(maxKeep): keep only the most recent maxKeep     *)
(* shares on the main chain, drop everything else.  Work already recorded  *)
(* on the survivors is left untouched, which is what keeps every value a   *)
(* node holds anchored at the same point.                                  *)
(***************************************************************************)
Prune(n) ==
    /\ AllowPrune
    /\ tip[n] # Nil
    /\ Cardinality(knows[n]) > PruneKeep
    /\ knows' = [knows EXCEPT ![n] = AncestorsOf(parent, knows[n], tip[n], PruneKeep)]
    /\ UNCHANGED <<nextId, parent, sdiff, tip, cache>>

(***************************************************************************)
(* chain.go PruneOrphans: delete every share that is not an ancestor of the *)
(* tip. Runs on the same ticker as PruneOldShares, just before it.          *)
(***************************************************************************)
PruneOrphans(n) ==
    /\ AllowPrune
    /\ tip[n] # Nil
    /\ LET main == AncestorsOf(parent, knows[n], tip[n], MaxShares)
       IN /\ knows[n] # main
          /\ knows' = [knows EXCEPT ![n] = main]
    /\ UNCHANGED <<nextId, parent, sdiff, tip, cache>>

Next ==
    \/ \E n \in Nodes : PruneOrphans(n)
    \/ \E n \in Nodes, p \in {Nil} \cup ShareIds, d \in Diffs : Mine(n, p, d)
    \/ \E n \in Nodes, s \in ShareIds : Learn(n, s)
    \/ \E n \in Nodes : Prune(n)
    \/ (nextId > MaxShares /\ UNCHANGED vars)   \* stutter at the bound

Spec == Init /\ [][Next]_vars

-----------------------------------------------------------------------------
(***************************************************************************)
(*                              INVARIANTS                                 *)
(***************************************************************************)

TypeOK ==
    /\ nextId \in 1..(MaxShares + 1)
    /\ parent \in [ShareIds -> {Nil} \cup ShareIds]
    /\ sdiff  \in [ShareIds -> Diffs]
    /\ knows  \in [Nodes -> SUBSET ShareIds]
    /\ tip    \in [Nodes -> {Nil} \cup ShareIds]

\* The tip must be a share we actually hold (MemoryStore.SetTip enforces this).
TipKnown == \A n \in Nodes : tip[n] = Nil \/ tip[n] \in knows[n]

\* The share a correct heaviest-chain rule would pick out of what n holds:
\* maximal true work, ties broken by lower hash.
Heaviest(n) ==
    LET S    == knows[n]
        best == {x \in S : \A y \in S : TrueWork(parent, sdiff, y)
                                     <= TrueWork(parent, sdiff, x)}
    IN CHOOSE x \in best : \A y \in best : x <= y

\* WORK-CORRECT FORK CHOICE: the selected tip is the heaviest chain known.
TipIsHeaviest ==
    \A n \in Nodes : knows[n] # {} => tip[n] = Heaviest(n)

\* Every recorded value is the share's true work back to the zero hash.
\* Holds only while nothing has been pruned; once a node drops its oldest
\* shares it can no longer see that far back, and ConsistentAnchor below is
\* the property that still matters.
CacheSound ==
    \A n \in Nodes : \A s \in Created :
        cache[n][s] # NoWork => cache[n][s] = TrueWork(parent, sdiff, s)

\* COMPARABILITY: within one node, every recorded value sits the same
\* distance from true work -- they share a single anchor. This is what makes
\* comparing two of them meaningful, and it survives pruning.
ConsistentAnchor ==
    \A n \in Nodes :
        \A s, t \in {x \in Created : cache[n][x] # NoWork} :
            cache[n][s] - TrueWork(parent, sdiff, s)
          = cache[n][t] - TrueWork(parent, sdiff, t)

\* CONSENSUS: two nodes holding exactly the same shares agree on the tip.
AgreeOnEqualKnowledge ==
    \A n, m \in Nodes : knows[n] = knows[m] => tip[n] = tip[m]

=============================================================================
