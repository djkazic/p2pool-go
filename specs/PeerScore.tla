----------------------------- MODULE PeerScore -----------------------------
(***************************************************************************)
(* A TLA+ model of p2pool-go's misbehavior scoring under clock skew.       *)
(*                                                                         *)
(* Mirrors:                                                                *)
(*   internal/sharechain/validation.go  the MaxTimeFuture check, which     *)
(*                                      returns a ValidationError with no  *)
(*                                      Category set -- and CategoryProvable*)
(*                                      is the zero value, so a timestamp  *)
(*                                      rejection counts as provable       *)
(*                                      misbehavior by the sender          *)
(*   internal/node/node.go              recordPeerOutcome, peerScoreMinBad,*)
(*                                      peerScoreBadRatio, handleP2PShare  *)
(*                                                                         *)
(* Two HONEST nodes.  B's clock is the origin; A's clock reads Skew units  *)
(* ahead.  Share timestamps come from bitcoind's curtime, which validation.go*)
(* notes can already run ~90 minutes ahead of real time before ASIC ntime  *)
(* rolling is added -- so an honest miner on A emits timestamps anywhere in *)
(* [0, A's clock + MaxFuture], every one of which A itself would accept.   *)
(*                                                                         *)
(* The question: can relaying those shares get A disconnected by B?        *)
(***************************************************************************)
EXTENDS Integers

CONSTANTS
    MaxFuture,   \* validation.go MaxTimeFuture
    SkewSet,     \* candidate clock offsets between the two honest nodes
    MinBad,      \* node.go peerScoreMinBad
    RatioNum,    \* node.go peerScoreBadRatio, as RatioNum/RatioDen
    RatioDen,
    MaxEvents,        \* bound on relayed shares (model-checking bound)
    TimestampProvable \* TRUE models the old behaviour, where a timestamp
                      \* rejection carried the zero-value CategoryProvable

VARIABLES
    skew,      \* how far A's clock is ahead of B's
    good,      \* peerScore.good that B holds for A
    bad,       \* peerScore.bad   that B holds for A
    dropped,   \* peerScore.disconnected
    events

vars == <<skew, good, bad, dropped, events>>

Init ==
    /\ skew \in SkewSet
    /\ good = 0
    /\ bad = 0
    /\ dropped = FALSE
    /\ events = 0

(***************************************************************************)
(* A relays an honestly produced share carrying timestamp ts.  A would     *)
(* accept it itself, so ts <= skew + MaxFuture.                            *)
(*                                                                         *)
(* B applies the same rule against its own clock, which reads 0:           *)
(*   if shareTime.After(now.Add(MaxTimeFuture)) -> reject, Category unset  *)
(* An unset Category is CategoryProvable, so handleP2PShare charges the    *)
(* rejection to A via recordPeerOutcome(from, true).                       *)
(***************************************************************************)
Relay(ts) ==
    /\ events < MaxEvents
    /\ ts \in 0..(skew + MaxFuture)
    /\ events' = events + 1
    /\ IF ts > MaxFuture
         THEN IF TimestampProvable
                THEN /\ bad'  = bad + 1
                     /\ good' = good
                \* validation.go now returns CategoryIndeterminate here, and
                \* handleP2PShare returns early without touching the counters.
                ELSE /\ bad'  = bad
                     /\ good' = good
         ELSE /\ good' = good + 1
              /\ bad'  = bad
    \* recordPeerOutcome: trip when bad >= MinBad AND bad/(good+bad) > ratio
    /\ dropped' = \/ dropped
                  \/ /\ bad' >= MinBad
                     /\ bad' * RatioDen > RatioNum * (good' + bad')
    /\ UNCHANGED skew

Next == \E ts \in 0..(MaxFuture * 2) : Relay(ts)

Spec == Init /\ [][Next]_vars

-----------------------------------------------------------------------------
(***************************************************************************)
(*                              INVARIANTS                                 *)
(***************************************************************************)

TypeOK ==
    /\ good \in 0..MaxEvents
    /\ bad \in 0..MaxEvents
    /\ dropped \in BOOLEAN

\* An honest node must never be disconnected for misbehavior by another
\* honest node. Every share here was honestly produced and would pass
\* validation on the node that relayed it.
NoHonestDisconnect == ~dropped

\* Weaker: an honest node should never even be charged with PROVABLE
\* misbehavior, whatever the threshold policy is.
NoProvableChargeAgainstHonestPeer == bad = 0

=============================================================================
