// AC-4: a three-deep chain whose flattened output carries the merged Component
// set and whose provenance names the ancestor each value came from.
template Creature kind entity {
  component andara.core.Behavior { name: "core.idle" }
}

// Beast adds a Component and overrides nothing. Its Behavior.name is
// "core.idle" and its provenance says p.Creature — a value in a file the author
// of Beast never opened, which is the whole reason provenance exists.
template Beast extends Creature {
  component andara.core.Memory {}
}

// Wolf overrides. Memory survives from Beast; the name is Wolf's own.
template Wolf extends Beast {
  component andara.core.Behavior { name: "wilds.wolf" }
}
