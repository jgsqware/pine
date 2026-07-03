package scanner

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/jgsqware/pine/internal/model"
)

// sig is a cheap change-detection signature for a path: the newest
// modification time (unix nanos) and a size in bytes. Two stats with the same
// sig are treated as identical content, so a previously parsed result may be
// reused. This is the (mtime, size) key model — it does not read file contents.
type sig struct {
	mtime int64
	size  int64
}

// cacheEntry pairs a parse result with the signature it was produced from.
type cacheEntry struct {
	sig sig
	val any
}

// ScanCache memoizes parse results across successive scans, keyed by path.
//
//   - hit  (same path, same mtime+size): the stored parse result is reused,
//     no re-parse happens.
//   - miss (new path or changed mtime/size): the caller parses and stores the
//     fresh result, and the parse counter is incremented.
//   - purge: any cached path that was NOT looked up during the just-finished
//     scan is dropped, so a deleted file never serves a stale result.
//
// It is safe for concurrent use from the parse fan-out (hits are read
// concurrently, misses written concurrently) via sync.Map. Whole scans sharing
// one cache are serialized by scanMu so seen-tracking and purge stay coherent.
// The zero value is unusable — construct with NewScanCache.
//
// Known limitation: the signature covers the worklist path itself (a playbook
// file, a role directory tree). A playbook that statically imports an external
// task file is keyed only on its own mtime/size, so an edit to a separately
// imported file is not detected until that playbook is itself touched. Whole-
// tree invalidation (git sync resets mtimes) makes this a corner case in
// practice; transitive dependency tracking is intentionally out of scope.
type ScanCache struct {
	scanMu  sync.Mutex // serializes whole scans sharing this cache
	entries sync.Map   // path(string) -> cacheEntry
	seen    sync.Map   // path(string) -> struct{}: looked up this scan
	parses  int64      // atomic: cumulative cache misses (real parses)
}

// NewScanCache returns an empty, ready-to-use cache.
func NewScanCache() *ScanCache { return &ScanCache{} }

// Parses reports the cumulative number of cache misses (actual parses) since
// the cache was created. Tests assert that a re-scan with no file changes adds
// zero to this counter. Nil-safe (returns 0).
func (c *ScanCache) Parses() int64 {
	if c == nil {
		return 0
	}
	return atomic.LoadInt64(&c.parses)
}

// beginScan takes the scan lock and clears the per-scan seen set.
func (c *ScanCache) beginScan() {
	c.scanMu.Lock()
	c.seen.Range(func(k, _ any) bool { c.seen.Delete(k); return true })
}

// endScan purges cache entries whose path was not looked up during the scan
// that just finished (the file vanished), then releases the scan lock.
func (c *ScanCache) endScan() {
	c.entries.Range(func(k, _ any) bool {
		if _, ok := c.seen.Load(k); !ok {
			c.entries.Delete(k)
		}
		return true
	})
	c.scanMu.Unlock()
}

// lookup marks path as seen and returns the cached value when a live entry
// matches signature s. ok reports a hit.
func (c *ScanCache) lookup(path string, s sig) (any, bool) {
	c.seen.Store(path, struct{}{})
	if v, ok := c.entries.Load(path); ok {
		if e := v.(cacheEntry); e.sig == s {
			return e.val, true
		}
	}
	return nil, false
}

// store records val for path under signature s and counts one parse (a miss).
func (c *ScanCache) store(path string, s sig, val any) {
	atomic.AddInt64(&c.parses, 1)
	c.entries.Store(path, cacheEntry{sig: s, val: val})
}

// fileSig stats a single file into a signature. ok is false when the path is
// missing or is a directory (callers then parse without caching).
func fileSig(path string) (sig, bool) {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return sig{}, false
	}
	return sig{mtime: fi.ModTime().UnixNano(), size: fi.Size()}, true
}

// roleSig aggregates the change signature of every regular file under a role
// directory into one sig: size is the byte total, mtime the newest
// modification across the tree. Any content edit, file addition or removal
// shifts at least one of the two, so a stale role parse is never served.
func roleSig(dir string) sig {
	var s sig
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		fi, e := d.Info()
		if e != nil {
			return nil
		}
		s.size += fi.Size()
		if m := fi.ModTime().UnixNano(); m > s.mtime {
			s.mtime = m
		}
		return nil
	})
	return s
}

// pbResult is the cached output of parsing a playbook candidate: the parsed
// playbook plus whether the file actually is a playbook (non-playbooks are
// dropped after the fan-out, but the negative result is worth caching too).
type pbResult struct {
	pb model.Playbook
	ok bool
}
