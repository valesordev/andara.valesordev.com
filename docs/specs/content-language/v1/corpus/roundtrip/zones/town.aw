zone town "Town" {
  fallback hall

  room hall "Town Hall" {
    desc "Stone walls and faded banners."
    exit south -> plaza
  }

  room plaza "Market Plaza" {
    desc "A dusty square of packed earth."
    exit north -> hall
    exit south -> docks.pier
  }
}
