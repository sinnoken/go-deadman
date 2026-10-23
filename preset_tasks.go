//go:build tasks

package main

import _ "embed"

//go:embed presets/tasks.yaml
var embeddedYaml []byte

const presetName = "tasks"
