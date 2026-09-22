template Beast kind entity {
  component andara.core.Aggro {
    threshold: 3
    enabled: true
  }
}

// One field stated, the other inherited: this is ADR-0010 decision 4, and it
// is the case the corpus cannot reach today.
template Wolf extends Beast {
  component andara.core.Aggro { threshold: 1 }
}
