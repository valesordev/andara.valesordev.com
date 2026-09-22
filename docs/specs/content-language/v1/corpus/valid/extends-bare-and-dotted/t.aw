// A bare reference resolves within the declaring pack.
template Base kind entity {}

template Local extends Base {}

// A dotted reference reaches andara.core, which in v1 is the only other pack a
// Template may extend (semantics.md section 4).
template Remote extends andara.core.Npc {}
