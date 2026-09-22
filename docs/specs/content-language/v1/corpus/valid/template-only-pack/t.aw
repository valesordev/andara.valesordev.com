// A pack with no zone declaration compiles to no Zones, which is legal:
// andara.core is exactly this shape. An empty World is a server configuration
// error (no_zones_found), not a compile error (semantics.md section 2).
template Only kind item {}
