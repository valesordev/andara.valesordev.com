// A pack is the unit of publication; an Exit may not leave it. `docks` is a
// Zone in another pack, so it is simply not a Zone this compile can see.
zone z "Z" {
  fallback r

  room r "R" {
    exit east -> docks.pier
  }
}
