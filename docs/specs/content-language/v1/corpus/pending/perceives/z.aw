zone z "Zone" {
  fallback hall

  room hall "The Hall" {
    exit north -> yard perceives [sound, sight]
  }

  room yard "The Yard" {
    exit south -> hall
  }
}
