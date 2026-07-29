package main

import (
	"os"
	"strconv"
	"strings"
)

const managedParentPIDEnv = "PROJX_MANAGED_PARENT_PID"

// managedChildEnv makes the direct ProjX child responsible for terminating its
// own managed process group if its supervisor disappears unexpectedly.
func managedChildEnv(env []string, parentPID int) []string {
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, managedParentPIDEnv+"=") {
			out = append(out, entry)
		}
	}
	return append(out, managedParentPIDEnv+"="+strconv.Itoa(parentPID))
}

func managedParentPID() int {
	pid, err := strconv.Atoi(strings.TrimSpace(os.Getenv(managedParentPIDEnv)))
	if err != nil || pid < 1 {
		return 0
	}
	return pid
}
