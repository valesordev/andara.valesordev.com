zone z "Zone" {
  room hall "The Hall" {
    exit north -> yard perceives [smell]
  }

  room yard "The Yard" {
    exit south -> hall
  }
}
