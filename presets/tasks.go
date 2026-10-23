//go:build tasks

package presets

import _ "embed"

//go:embed tasks.yaml
var Data []byte

const Name = "tasks"
