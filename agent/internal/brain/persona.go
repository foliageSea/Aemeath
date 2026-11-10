package brain

import _ "embed"

// DefaultPersona is shared by direct brain callers and voice configuration.
// Sources and adaptation notes are documented in docs/aemeath-persona.md.
//
//go:embed persona.md
var DefaultPersona string
