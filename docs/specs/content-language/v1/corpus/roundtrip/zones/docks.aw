zone docks "Docks" {
  fallback pier
  component andara.core.Indoors {}

  room pier "The Pier" {
    desc "Salt air and creaking boards."
    exit north -> town.plaza
    exit south -> warehouse
  }

  room warehouse "Warehouse" {
    desc "Barrels and rope."
    exit north -> pier
    component andara.core.Dark {}
    component andara.core.NoMagic {}
  }
}
