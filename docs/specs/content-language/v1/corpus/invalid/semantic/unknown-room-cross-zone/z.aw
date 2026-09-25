zone z "Z" {
  fallback r

  room r "R" {
    exit north -> other.nowhere
  }
}

zone other "Other" {
  fallback q

  room q "Q" {}
}
