//go:build linux || darwin || windows

package persistence

import (
	"context"
	"math"
	"os"
	"time"
)

// sampleUsage requires g.mu. It measures at most one catalogued generation and
// two fixed files. It never follows unknown names or reads database/provider data.
func (g *collectionGenerations) sampleUsage(ctx context.Context) {
	if g.closed || len(g.catalog.Completed) == 0 || ctx.Err() != nil {
		return
	}
	if g.usageCursor >= len(g.catalog.Completed) {
		g.usageCursor = 0
	}
	name := g.catalog.Completed[g.usageCursor]
	g.usageCursor++
	g.usageAttempted = true
	if g.usage == nil {
		g.usage = make(map[string]collectionUsageSample)
	}
	delete(g.usage, name) // An unsuccessful refresh cannot retain a success.
	if !collectionGCName(name) || g.openRetirement() != nil {
		return
	}
	visible, err := os.Lstat(g.directory)
	anchored, statErr := g.retirement.dir.Stat()
	rootIdentity, nativeErr := collectionGCNative(g.retirement.dir, true)
	if err != nil || statErr != nil || nativeErr != nil || !os.SameFile(visible, anchored) || rootIdentity != g.retirement.identity {
		return
	}
	before, err := g.retirement.root.Lstat(name)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return
	}
	root, err := g.retirement.root.OpenRoot(name)
	if err != nil {
		return
	}
	defer root.Close()
	directory, err := collectionGCOpenDirectory(root)
	if err != nil {
		return
	}
	defer directory.Close()
	after, err := directory.Stat()
	if err != nil || !os.SameFile(before, after) {
		return
	}
	identity, err := collectionGCNative(directory, true)
	if err != nil || !collectionGCSameMount(identity, g.retirement.identity) {
		return
	}
	sample := collectionUsageSample{allocationAvailable: true}
	for _, leaf := range []string{"ledger.db", "generation.json"} {
		if ctx.Err() != nil {
			return
		}
		file, info, _, err := collectionGCOpenVerified(root, leaf, identity, false)
		if err != nil {
			return
		}
		allocated, available := collectionAllocatedBytes(file)
		closeErr := file.Close()
		if closeErr != nil || info.Size() < 0 || info.Size() > math.MaxInt64-sample.apparent || allocated < 0 || allocated > math.MaxInt64-sample.allocated {
			return
		}
		sample.apparent += info.Size()
		sample.allocated += allocated
		sample.allocationAvailable = sample.allocationAvailable && available
	}
	sample.observedAt = time.Now().UTC()
	g.usage[name] = sample
}
