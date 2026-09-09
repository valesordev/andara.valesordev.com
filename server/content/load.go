package content

import "github.com/valesordev/andara/server/sim"

// Source names where ZoneDefinitions come from.
const (
	SourceKafka = "kafka"
	SourceDir   = "dir"
)

// Load returns ZoneDefinition inputs from the configured source.
// Kafka is not implemented until AW-SRV-012; it fails with a finding that
// names the source rather than pretending to be an empty World.
func Load(source, path string) ([]sim.Input, []sim.ValidationError) {
	switch source {
	case SourceDir:
		return LoadDir(path)
	case SourceKafka:
		return nil, []sim.ValidationError{{
			Code: sim.ErrEmptyContent,
			Detail: "no Zones were found in kafka: content.source=kafka is not implemented " +
				"(AW-SRV-012); set content.source=dir",
		}}
	default:
		return nil, []sim.ValidationError{{
			Code:   sim.ErrMalformed,
			Detail: `content.source "` + source + `" is not kafka or dir`,
		}}
	}
}
