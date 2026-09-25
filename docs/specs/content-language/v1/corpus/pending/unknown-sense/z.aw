zone z "Zone" {
  fallback hall

  room hall "The Hall" {
    exit north -> yard perceives [smell]
  }

  room yard "The Yard" {
    exit south -> hall
  }
}
