package queue

import (
	"bytes"
	"os"
	"strconv"
)

// detectUsableMemory tries cgroup limits first, then falls back to /proc/meminfo.
func detectUsableMemory() uint64 {
	// 1. Try cgroup v2
	if limit := readCgroupMemoryLimit("/sys/fs/cgroup/memory.max"); limit > 0 {
		return limit
	}
	// 2. Try cgroup v1
	if limit := readCgroupMemoryLimit("/sys/fs/cgroup/memory/memory.limit_in_bytes"); limit > 0 {
		return limit
	}
	// 3. Read total system memory from /proc/meminfo (Linux)
	if total := readProcMemTotal(); total > 0 {
		return total
	}
	return 0
}

// readCgroupMemoryLimit reads a cgroup memory limit file. Returns 0 on failure or "max".
func readCgroupMemoryLimit(path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	s := string(bytes.TrimSpace(data))
	if s == "" || s == "max" {
		return 0
	}
	val, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return val
}

// readProcMemTotal reads MemTotal from /proc/meminfo (Linux only).
// Returns total physical memory in bytes, or 0 on failure.
func readProcMemTotal() uint64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("MemTotal:")) {
			fields := bytes.Fields(line)
			if len(fields) >= 2 {
				kb, err := strconv.ParseUint(string(fields[1]), 10, 64)
				if err == nil {
					return kb * 1024 // Convert kB to bytes
				}
			}
			break
		}
	}
	return 0
}
