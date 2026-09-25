zone z "Z" {
  fallback r

  room r "R" {
    exit north -> a
    exit north -> b
  }

  room a "A" {
    exit south -> r
  }

  room b "B" {
    exit south -> r
  }
}
