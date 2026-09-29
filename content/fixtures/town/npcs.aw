// The people of the town.
//
// Both bind a Behavior by name. That name is the one seam between the Template
// hierarchy here and the Python class hierarchy an Agent runs (ADR-0010), and
// nothing in this file knows anything about the Python.
//
// andara.core.Memory arrives from andara.core.Npc. It appears in the flattened
// output and in no provenance entry, because a marker Component sets no field
// and there is no field for an ancestor to have set.

// Stands behind the largest stall on the square.
template Merchant extends andara.core.Npc {
  component andara.core.Behavior { name: "town.merchant" }
}

// The guard is deliberately the same shape as the merchant. Two Templates that
// differ only in the Behavior they bind is the common case in a MUD — the
// interesting difference is in the Python, which is not content's business — and
// it is worth one corpus case proving that the boring case stays boring.
//
// Neither declares a kind: both inherit ENTITY from andara.core.Npc, which
// inherits it from andara.core.Entity. A chain has one kind (AW-SRV-022), so
// restating it would be template_head rather than emphasis.
//
// What neither of them may do is drop andara.core.Memory. A guard that does not
// remember is Memory with an off switch on it, not a Template with Memory
// removed (ADR-0010 decision 5, errors.md removed_by_subtype) — the day the
// Component grows that switch.

template Guard extends andara.core.Npc {
  component andara.core.Behavior { name: "town.guard" }
}
