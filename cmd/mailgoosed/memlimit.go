package main

import (
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// The container's memory, as the kernel limits it. Go doesn't read it by
// itself, and without a soft limit its heap can grow to twice what's live
// before it collects, past the container's memory (MG-72: reading a
// Mailchimp export holds a few hundred MB for a moment).
var cgroupMemoryFiles = []string{
	"/sys/fs/cgroup/memory.max",                   // cgroup v2
	"/sys/fs/cgroup/memory/memory.limit_in_bytes", // cgroup v1
}

// memoryLimitShare is the part of the container's memory the Go heap is
// held to; the rest is for the runtime, stacks and the C side.
const memoryLimitShare = 0.8

// setMemoryLimit sets the Go runtime's soft memory limit from the
// container's, unless GOMEMLIMIT already sets one, and logs what it did.
func setMemoryLimit(log *slog.Logger) {
	if os.Getenv("GOMEMLIMIT") != "" {
		log.Info("memory limit from GOMEMLIMIT", "bytes", debug.SetMemoryLimit(-1))
		return
	}
	limit := containerMemory(cgroupMemoryFiles)
	if limit <= 0 {
		log.Info("no container memory limit found; the Go heap has no soft limit")
		return
	}
	soft := int64(float64(limit) * memoryLimitShare)
	debug.SetMemoryLimit(soft)
	log.Info("memory limit set from the container's", "container_bytes", limit, "bytes", soft)
}

// containerMemory is the first limit the files give, or 0 for none.
func containerMemory(files []string) int64 {
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // fixed kernel paths
		if err != nil {
			continue
		}
		v := strings.TrimSpace(string(b))
		n, err := strconv.ParseInt(v, 10, 64)
		// "max", or v1's "no limit" of close to 2^63.
		if err != nil || n <= 0 || n >= 1<<60 {
			return 0
		}
		return n
	}
	return 0
}
