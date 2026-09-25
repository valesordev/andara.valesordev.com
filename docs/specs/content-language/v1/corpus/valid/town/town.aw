zone town "Town" {
  fallback plaza

  room plaza "Market Plaza" {
    desc "A dusty square of packed earth."
    exit north -> hall
    exit east -> wilds.trail
    exit south -> docks.pier
  }

  room hall "Town Hall" {
    desc "Stone walls and faded banners."
    exit south -> plaza
  }
}
