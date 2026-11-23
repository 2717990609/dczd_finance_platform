package main

import (
	"container/list"
	"crypto/sha256"
	"sync"
)

const (
	maxDraftCachedPeriods = 8
	maxDraftCachedRows    = 64 * 1024
)

type draftCalculationCaches struct {
	vip      vipDraftQuoteCache
	practice practiceDraftQuoteCache
}

// A cached period is immutable once published. The cache holds trial results
// only, so eviction or restart can always fall back to calculation.
type draftQuoteCache[T any] struct {
	mu      sync.Mutex
	periods list.List
	rows    int
}

type draftQuotePeriod[T any] struct {
	id     int64
	scope  [sha256.Size]byte
	quotes map[int64]T
	rows   int
}

func (p *draftQuotePeriod[T]) add(id int64, quote T, rows int) {
	p.rows += rows
	if p.rows <= maxDraftCachedRows {
		p.quotes[id] = quote
	}
}

func (c *draftQuoteCache[T]) read(id int64, scope [sha256.Size]byte) map[int64]T {
	c.mu.Lock()
	defer c.mu.Unlock()
	for entry := c.periods.Front(); entry != nil; entry = entry.Next() {
		stored := entry.Value.(draftQuotePeriod[T])
		if stored.id == id && stored.scope == scope {
			c.periods.MoveToFront(entry)
			return stored.quotes
		}
	}
	return nil
}

func (c *draftQuoteCache[T]) remember(period draftQuotePeriod[T]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for entry := c.periods.Front(); entry != nil; entry = entry.Next() {
		if entry.Value.(draftQuotePeriod[T]).id == period.id {
			c.rows -= c.periods.Remove(entry).(draftQuotePeriod[T]).rows
			break
		}
	}
	if period.rows == 0 || period.rows > maxDraftCachedRows {
		return
	}
	for c.periods.Len() >= maxDraftCachedPeriods || c.rows+period.rows > maxDraftCachedRows {
		c.rows -= c.periods.Remove(c.periods.Back()).(draftQuotePeriod[T]).rows
	}
	c.periods.PushFront(period)
	c.rows += period.rows
}
