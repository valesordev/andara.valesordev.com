zone aux "Zone" {
  fallback hall

  room hall "The Hall" {
    exit north -> yard
  }

  room yard "The Yard" {
    exit south -> hall
  }
}
