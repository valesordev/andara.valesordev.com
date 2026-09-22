zone docks "Docks" {
  room pier "The Pier" {
    desc "Salt air and creaking boards."
    exit north -> town.plaza
    exit south -> warehouse
  }

  room warehouse "Warehouse" {
    desc "Barrels and rope."
    exit north -> pier
  }
}
