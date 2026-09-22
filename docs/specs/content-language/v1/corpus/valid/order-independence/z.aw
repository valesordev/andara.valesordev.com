// Rooms are in walk order, not alphabetical order, and stay that way: fmt does
// not reorder (formatting.md section 1). The compiled output is sorted anyway.
zone z "Zone" {
  room west_gate "West Gate" {
    exit east -> middle
  }

  room middle "The Middle" {
    exit west -> west_gate
    exit east -> east_gate
  }

  room east_gate "East Gate" {
    exit west -> middle
  }
}

template Early kind entity {}
