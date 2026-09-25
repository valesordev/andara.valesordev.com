zone z "Zone" {
  fallback shore

  room shore "The Shore" {
    exit north -> dunes
  }

  room dunes "The Dunes" {
    exit south -> shore
  }

  // No Exit in this Zone reaches the island. Legal — a Builder mid-work — and
  // never silent (AW-SRV-001 AC-6).
  room island "The Island" {}
}
