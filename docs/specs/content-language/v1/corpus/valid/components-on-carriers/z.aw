zone cellars "The Cellars" {
  component andara.core.Indoors {}
  component andara.core.NoRecall {}

  // The Zone carries Indoors; the Room does not inherit it. A Zone carrying
  // Dark does not make its Rooms dark (AW-SRV-021 AC-4).
  room vault "The Vault" {
    desc "No lamp has been lit here in years."
    component andara.core.Dark {}
    component andara.core.NoMagic {}
  }
}
