package runtime

import _ "embed"

//go:embed runtime.js
var source string

// Source returns the page runtime injected at document start.
func Source() string { return source }

// Version is the runtime's public version.
const Version = "0.1.0"
