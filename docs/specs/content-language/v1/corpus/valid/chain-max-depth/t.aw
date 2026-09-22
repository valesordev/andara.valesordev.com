// AC-6's boundary from below: a chain of exactly sim.MaxChainDepth = 16,
// self included, compiles. Seventeen is chain_too_deep.
template D01 kind entity {}

template D02 extends D01 {}

template D03 extends D02 {}

template D04 extends D03 {}

template D05 extends D04 {}

template D06 extends D05 {}

template D07 extends D06 {}

template D08 extends D07 {}

template D09 extends D08 {}

template D10 extends D09 {}

template D11 extends D10 {}

template D12 extends D11 {}

template D13 extends D12 {}

template D14 extends D13 {}

template D15 extends D14 {}

template D16 extends D15 {}
