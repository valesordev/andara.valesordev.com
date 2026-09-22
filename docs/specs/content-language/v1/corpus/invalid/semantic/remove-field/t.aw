template Base kind entity {
  component andara.core.Behavior { name: "base.idle" }
}

template Silent extends Base {
  remove field name from andara.core.Behavior
}
