package journal

// This file adds the NDJSON export half of internal/journal (TASK-012): a
// streaming writer that emits the journal as newline-delimited JSON in global
// Seq order without ever materializing the whole journal in memory (MOCK-602.2).
// It is the intended drainer for the block overflow policy (ADR-005 / TASK-011),
// so it can optionally acknowledge consumption through [Ring.Drain].

import (
	"context"
	"fmt"
	"io"

	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// ExportOptions configures an NDJSON export.
type ExportOptions struct {
	// Selectors filters the exported records; a record is emitted only if it
	// matches every selector (AND-combined, MOCK-602.4). Empty means export
	// everything.
	Selectors []journalapi.Selector
	// Drain, when true, advances the ring's drained cursor to the point this
	// export consumed after a successful stream, releasing backpressure for a
	// writer blocked under the block overflow policy (ADR-005). It is the
	// explicit "I have taken these" acknowledgement a draining consumer issues.
	// Left false, the export is a non-consuming read and the ring is unchanged.
	Drain bool
	// FlushEvery, when > 0, flushes the underlying writer after this many
	// records so a slow or following consumer sees output promptly rather than
	// only at the end. Zero flushes once at the end.
	FlushEvery int
}

// ExportNDJSON streams the journal to w as NDJSON in global [journalapi.Record.Seq]
// order, one record per line, applying opts.Selectors. It returns the number of
// records written.
//
// # Streaming, not materializing (MOCK-602.2)
//
// The export merges the shards by Seq with a bounded k-way merge: it holds at
// most one record per shard in memory at a time (S ≤ 64), never a slice of the
// whole journal. Each record is encoded and written the moment the merge selects
// it, so a 100 000-record journal streams in constant memory beyond the shard
// heads and the writer's buffer. This is what makes NDJSON the safe drain path
// for the block overflow policy, whose journal may be very large (ADR-005).
//
// Within a shard, cursor order is ascending Seq order — a later write to a shard
// always draws a larger global sequence — so each shard yields its records
// pre-sorted and the merge only compares shard heads. Records read under the
// seqlock are whole; a slot the reader cannot read consistently is skipped and
// the merge advances, exactly as the storage engine's snapshot does.
//
// # Draining (ADR-005)
//
// When opts.Drain is set the ring's drained cursor is advanced after a
// successful stream (see [ExportOptions.Drain]); on an error the ring is left
// untouched so a failed export never silently consumes records. ctx is honored
// between records so a large export is cancellable.
func (r *Ring) ExportNDJSON(ctx context.Context, w io.Writer, opts ExportOptions) (int, error) {
	jw := journalapi.NewWriter(w)
	heads := r.newShardCursors()
	count := 0

	for {
		if err := ctx.Err(); err != nil {
			return count, fmt.Errorf("journal: export canceled: %w", err)
		}
		rec, ok := heads.next()
		if !ok {
			break
		}
		if !matchesAll(rec, opts.Selectors) {
			continue
		}
		if err := jw.Write(rec); err != nil {
			return count, err
		}
		count++
		if opts.FlushEvery > 0 && count%opts.FlushEvery == 0 {
			if err := jw.Flush(); err != nil {
				return count, err
			}
		}
	}

	if err := jw.Close(); err != nil {
		return count, err
	}
	if opts.Drain {
		r.Drain()
	}
	return count, nil
}

// matchesAll reports whether rec satisfies every selector. No selectors means an
// unconstrained export, so it returns true.
func matchesAll(rec journalapi.Record, selectors []journalapi.Selector) bool {
	for _, s := range selectors {
		if !s.Matches(rec) {
			return false
		}
	}
	return true
}

// shardCursors is a bounded k-way merge over the shards, yielding records in
// ascending global Seq order while holding at most one record per shard. It is
// the streaming core of [Ring.ExportNDJSON]: it never builds a slice of the
// whole journal.
type shardCursors struct {
	shards []*shard
	// pos is each shard's next slot index to read, in cursor order.
	pos []uint64
	// end is each shard's exclusive upper cursor bound, snapshotted once so a
	// concurrent writer growing the cursor does not extend an in-progress
	// export unboundedly.
	end []uint64
	// head holds each shard's current front record and whether it is loaded.
	head    []journalapi.Record
	loaded  []bool
	present []bool
}

// newShardCursors snapshots each shard's live cursor window and returns a merge
// positioned at the oldest record of each shard. The window is the last
// heldCount records of the shard (indices cursor-heldCount .. cursor-1), which
// are exactly the live, undropped records in ascending Seq order.
func (r *Ring) newShardCursors() *shardCursors {
	n := len(r.shards)
	c := &shardCursors{
		shards:  r.shards,
		pos:     make([]uint64, n),
		end:     make([]uint64, n),
		head:    make([]journalapi.Record, n),
		loaded:  make([]bool, n),
		present: make([]bool, n),
	}
	for i, sh := range r.shards {
		cur := sh.cursor.Load()
		// heldCount is non-negative and bounded by the ring length (an int), so
		// the widening to uint64 cannot overflow or change sign.
		held := uint64(sh.heldCount()) //nolint:gosec // heldCount ≥ 0, bounded by ring length
		start := uint64(0)
		if cur > held {
			start = cur - held
		}
		c.pos[i] = start
		c.end[i] = cur
	}
	return c
}

// next returns the next record across all shards in ascending Seq order, or
// ok=false when every shard is exhausted. It loads each shard's head lazily and
// advances only the shard whose head it emitted, so at most one record per shard
// is held at any moment.
func (c *shardCursors) next() (journalapi.Record, bool) {
	c.refill()
	best := -1
	for i := range c.shards {
		if !c.present[i] {
			continue
		}
		if best < 0 || c.head[i].Seq < c.head[best].Seq {
			best = i
		}
	}
	if best < 0 {
		return journalapi.Record{}, false
	}
	rec := c.head[best]
	c.loaded[best] = false // consume this shard's head; refill on next call
	c.present[best] = false
	return rec, true
}

// refill loads a head for every shard that does not currently have one, reading
// forward past any slot that is empty or that could not be read consistently
// under the seqlock (skip-and-continue, matching the storage engine's snapshot).
func (c *shardCursors) refill() {
	for i, sh := range c.shards {
		if c.loaded[i] {
			continue
		}
		c.advanceShard(i, sh)
	}
}

// advanceShard reads forward in shard i until it loads a whole record as the
// shard's head or exhausts the shard's cursor window.
func (c *shardCursors) advanceShard(i int, sh *shard) {
	for c.pos[i] < c.end[i] {
		idx := c.pos[i]
		c.pos[i]++
		if rec, ok := sh.slotAt(idx).read(); ok {
			c.head[i] = *rec
			c.loaded[i] = true
			c.present[i] = true
			return
		}
	}
	c.loaded[i] = true // window exhausted; do not re-scan it
	c.present[i] = false
}
