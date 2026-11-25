// Package protocol embeds the pinned vLLM versions written by scripts/audit.py.
package protocol

import _ "embed"

//go:embed versions.json
var Versions []byte
