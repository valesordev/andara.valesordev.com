// A guard that does not remember is Memory with an off switch, not a
// Template with Memory deleted (ADR-0010 decision 5).
template Forgetful extends andara.core.Npc {
  remove component andara.core.Memory
}
