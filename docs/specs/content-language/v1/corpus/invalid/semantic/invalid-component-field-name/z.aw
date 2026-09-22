// The story's own contract sketch wrote this line. andara.core.Dark is a
// marker: it declares no fields, so there is no `enabled` to set, and the
// off switch ADR-0010 decision 5 assumes does not exist yet.
zone z "Z" {
  room r "R" {
    component andara.core.Dark { enabled: false }
  }
}
