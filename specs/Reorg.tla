------------------------------- MODULE Reorg -------------------------------
(***************************************************************************)
(* A TLA+ model of the sharechain event pipeline and the job miners are    *)
(* working on.                                                             *)
(*                                                                         *)
(* Mirrors:                                                                *)
(*   internal/sharechain/chain.go  AddShare's emit order -- EventReorg      *)
(*                                 first, then EventNewTip -- and emit(),   *)
(*                                 whose select has a `default` that DROPS  *)
(*                                 the event when the subscriber channel is *)
(*                                 full                                    *)
(*   internal/node/node.go         handleChainEvent. It no longer suppresses *)
(*                                 the EventNewTip that follows a reorg;     *)
(*                                 DedupeEnabled models the flag it used to  *)
(*                                 do that with                              *)
(*   internal/work/generator.go    GenerateJob, which reads the CURRENT     *)
(*                                 chain tip via prevShareHashFn rather     *)
(*                                 than the tip named in the event, and the *)
(*                                 JobRefreshInterval timer that pushes a   *)
(*                                 fresh job every 30s regardless           *)
(*                                                                         *)
(* The question: can miners be left working on a stale tip, and if so does  *)
(* anything but the refresh timer rescue them?                             *)
(***************************************************************************)
EXTENDS Integers, Sequences, FiniteSets

CONSTANTS
    Tips,           \* candidate tip identities
    QueueCap,       \* buffer of the subscriber channel (16 in chain.go)
    MaxChanges,     \* bound on tip changes (model-checking bound)
    RefreshEnabled, \* TRUE models generator.go's JobRefreshInterval timer
    DedupeEnabled,  \* TRUE models the removed lastReorgTip suppression
    NoTip           \* the zero hash: no tip yet / no job yet

VARIABLES
    tip,           \* store.Tip()
    queue,         \* the subscriber channel
    lastReorgTip,  \* node.go lastReorgTip
    jobTip,        \* the tip the miners' current job commits to
    changes

vars == <<tip, queue, lastReorgTip, jobTip, changes>>

Init ==
    /\ tip = NoTip
    /\ queue = <<>>
    /\ lastReorgTip = NoTip
    /\ jobTip = NoTip
    /\ changes = 0

(***************************************************************************)
(* chain.go emit(): push if there is room, otherwise drop on the floor.    *)
(***************************************************************************)
Push(q, e) == IF Len(q) < QueueCap THEN Append(q, e) ELSE q

\* A share extends the tip: AddShare emits EventNewTip alone.
Extend(h) ==
    /\ changes < MaxChanges
    /\ h # tip
    /\ tip' = h
    /\ queue' = Push(queue, [type |-> "newtip", hash |-> h])
    /\ changes' = changes + 1
    /\ UNCHANGED <<lastReorgTip, jobTip>>

\* A share displaces the tip from another branch: EventReorg then EventNewTip.
ReorgTo(h) ==
    /\ changes < MaxChanges
    /\ h # tip
    /\ tip # NoTip
    /\ tip' = h
    /\ queue' = Push(Push(queue, [type |-> "reorg", hash |-> h]),
                     [type |-> "newtip", hash |-> h])
    /\ changes' = changes + 1
    /\ UNCHANGED <<lastReorgTip, jobTip>>

(***************************************************************************)
(* node.go handleChainEvent. GenerateJob reads the current tip, not the    *)
(* hash carried by the event, so a job always commits to whatever the tip  *)
(* is at the moment it is handled.                                        *)
(***************************************************************************)
Handle ==
    /\ queue # <<>>
    /\ LET e == Head(queue) IN
       /\ queue' = Tail(queue)
       /\ IF e.type = "reorg"
            THEN /\ jobTip' = tip
                 /\ lastReorgTip' = IF DedupeEnabled THEN e.hash ELSE NoTip
            ELSE IF DedupeEnabled /\ lastReorgTip # NoTip /\ e.hash = lastReorgTip
                   \* swallowed: the reorg branch already generated a job
                   THEN /\ lastReorgTip' = NoTip
                        /\ UNCHANGED jobTip
                   ELSE /\ jobTip' = tip
                        /\ UNCHANGED lastReorgTip
    /\ UNCHANGED <<tip, changes>>

\* generator.go: a refresh job every JobRefreshInterval, built from the
\* current tip, independent of the event pipeline.
Refresh ==
    /\ RefreshEnabled
    /\ tip # NoTip
    /\ jobTip # tip
    /\ jobTip' = tip
    /\ UNCHANGED <<tip, queue, lastReorgTip, changes>>

Next ==
    \/ \E h \in Tips : Extend(h)
    \/ \E h \in Tips : ReorgTo(h)
    \/ Handle
    \/ Refresh

Fairness == WF_vars(Handle) /\ WF_vars(Refresh)

Spec == Init /\ [][Next]_vars /\ Fairness

-----------------------------------------------------------------------------
(***************************************************************************)
(*                            PROPERTIES                                   *)
(***************************************************************************)

TypeOK == /\ Len(queue) <= QueueCap
          /\ changes \in 0..MaxChanges

\* Miners are working on the current tip. Anything else is hashrate spent on
\* shares that will land on a stale parent and end up off the main chain.
InSync == jobTip = tip

\* LIVENESS: once the chain stops moving, miners converge on the real tip.
Converges == <>[]InSync

=============================================================================
