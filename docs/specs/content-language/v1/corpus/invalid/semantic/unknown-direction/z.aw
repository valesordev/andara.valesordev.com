zone z "Z" {
  room r "R" {
    exit norht -> h
  }

  room h "H" {
    exit south -> r
  }

  fallback r
}
