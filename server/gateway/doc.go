// Package gateway is the server side of the Protocol: one TLS endpoint serving
// andara.game.v1.Game and andara.admin.v1.Admin over gRPC, gRPC-Web, and
// Connect from the code AW-SRV-020 generates (ADR-0003, AW-SRV-005).
//
// The Gateway knows about connections; the simulation core does not. What
// lives here is transport, Session lifecycle, Protocol version negotiation,
// the interceptor chain, and graceful drain. What deliberately does not live
// here is behavior: Submit hands off to an Ingress (AW-SRV-010), Subscribe to
// an Egress (AW-SRV-011), and token verification to a TokenVerifier
// (AW-SRV-008). Each has a stub so the seam exists today and is not
// retrofitted.
package gateway
