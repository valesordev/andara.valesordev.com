zone z "Zone" {
  room loft "The Loft" {
    // A chute is a real thing, so a one-way Exit is legal. Forgetting the way
    // back is far more common, so it is never silent (AW-SRV-021 AC-7).
    exit down -> cellar
  }

  room cellar "The Cellar" {}
}
