//go:build linux

package limits

import (
	"os"
	"strconv"
	"strings"
)

func detect() Limits {
	cores := detectCPUCgroup()
	mem := detectMemCgroup()

	return Limits{
		Cores:       cores,
		MemoryBytes: mem,
		Source:      "cgroup",
	}
}

func detectCPUCgroup() *int {
	// cgroup v2: cpu.max
	if data, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 2 {
			if fields[0] != "max" {
				if quota, err1 := strconv.ParseFloat(fields[0], 64); err1 == nil {
					if period, err2 := strconv.ParseFloat(fields[1], 64); err2 == nil && period > 0 {
						v := int((quota / period) + 0.9999) // ceil
						return &v
					}
				}
			}
		}
	}

	// cgroup v1: cpu.cfs_quota_us / cpu.cfs_period_us
	quota, err1 := readInt("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	period, err2 := readInt("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if err1 == nil && err2 == nil && quota > 0 && period > 0 {
		v := int((float64(quota)/float64(period)) + 0.9999)
		return &v
	}
	return nil
}

func detectMemCgroup() *int64 {
	// cgroup v2: memory.max
	if data, err := os.ReadFile("/sys/fs/cgroup/memory.max"); err == nil {
		s := strings.TrimSpace(string(data))
		if s != "max" {
			if v, err := strconv.ParseInt(s, 10, 64); err == nil && v > 0 {
				return &v
			}
		}
	}

	// cgroup v1: memory.limit_in_bytes
	if v, err := readInt("/sys/fs/cgroup/memory/memory.limit_in_bytes"); err == nil && v > 0 && v < (1<<60) {
		v64 := int64(v)
		return &v64
	}
	return nil
}

func readInt(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(data))
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	return v, nil
}
