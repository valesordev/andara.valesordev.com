zone wilds "Wilds" {
  fallback trail

  room trail "Forest Trail" {
    desc "A narrow path under pines."
    exit west -> town.plaza
    exit north -> copse
  }

  room copse "Copse" {
    desc "A ring of younger trees."
    exit south -> trail
  }
}
