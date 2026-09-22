template Creature kind entity {
  component andara.core.Behavior { name: "core.idle" }
}

template Relic kind item {}

template Wolf extends Creature {
  component andara.core.Behavior { name: "wilds.wolf" }
  component andara.core.Memory {}
}
