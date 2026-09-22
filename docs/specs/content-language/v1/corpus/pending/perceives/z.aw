zone z "Zone" {
  room hall "The Hall" {
    exit north -> yard perceives [sound, sight]
  }

  room yard "The Yard" {
    exit south -> hall
  }
}
