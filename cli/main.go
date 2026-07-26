// Command wee is the workflow execution engine's CLI: one static binary that
// runs, replays, inspects, validates, exports, and scaffolds workflows. It is a
// pure client of core/ — every command wraps a core package and adds nothing of
// its own to the domain (PRIN-02). See docs/spec/cli.md (REQ-CLI-*).
package main

import "github.com/thettteus/workflow-execution-engine/cli/cmd"

func main() { cmd.Main() }
