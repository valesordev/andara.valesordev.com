template Base kind entity {
  component andara.core.Behavior { name: "base.idle" }
}

// An empty body merges nothing. The ancestor's fields survive and provenance
// still names the ancestor: this is a no-op, not a reset (semantics.md
// section 5). fmt leaves it alone, because it is how a Builder marks a
// Component as theirs to fill in later.
template Sub extends Base {
  component andara.core.Behavior {}
}
