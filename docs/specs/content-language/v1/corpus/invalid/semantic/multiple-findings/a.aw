zone z "Z" {
  room r "R" {
    exit norht -> h
    exit north -> nowhere
  }

  room h "H" {
    exit south -> r
  }
}
