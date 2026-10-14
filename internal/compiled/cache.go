package compiled

import (
	"container/list"
	"encoding/json"
	"sync"

	"github.com/lkarlslund/lovkilde/internal/lexdoc"
)

// Cache is a byte-bounded document cache in front of an Artifact.
//
// It holds each document's decompressed JSON rather than the decoded struct, for two reasons that
// are both about being right rather than about being fast. A decoded *lexdoc.Document is mutable:
// consolidation splices text into its spans and renumbers subsections in place, so handing the same
// pointer to two concurrent requests would let one answer edit another one's law. And a payload's
// byte length is exactly what it costs to hold, so "bounded in bytes" is a measured fact here rather
// than an estimate of struct size, which is the form the bound was specified in.
//
// What a cached read therefore skips is the seek, the frame and the decompression; the unmarshal
// stays, and its cost is in the measurement report rather than assumed away.
type Cache struct {
	artifact *Artifact
	budget   int64

	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List // front is the most recently read
	used    int64
	hits    int64
	misses  int64
	drops   int64 // payloads too large to ever fit the budget
}

type cacheEntry struct {
	accession string
	payload   []byte
}

// CacheStats reports what the cache did, because a cache without a hit rate is a guess that looks
// like a tuning knob.
type CacheStats struct {
	Entries   int   `json:"entries"`
	UsedBytes int64 `json:"used_bytes"`
	Budget    int64 `json:"budget_bytes"`
	Hits      int64 `json:"hits"`
	Misses    int64 `json:"misses"`
	Drops     int64 `json:"drops"`
}

// OpenCached opens an artifact and caches up to budgetBytes of decompressed payloads.
func OpenCached(path string, budgetBytes int64) (*Cache, error) {
	a, err := OpenArtifact(path)
	if err != nil {
		return nil, err
	}
	return &Cache{
		artifact: a,
		budget:   budgetBytes,
		entries:  map[string]*list.Element{},
		order:    list.New(),
	}, nil
}

// Artifact gives at the uncached reader, for callers that want the file itself: the artifact's
// provenance line, or a measurement that must not be flattered by a warm cache.
func (c *Cache) Artifact() *Artifact { return c.artifact }

// Document returns one document, reading it from the artifact only if the cache does not hold it.
//
// The document handed back belongs to the caller: it is a fresh value every time, so a caller may
// consolidate it, render it, or throw it away without another request noticing.
func (c *Cache) Document(accession string) (*lexdoc.Document, error) {
	payload, err := c.payload(accession)
	if err != nil {
		return nil, err
	}
	var doc lexdoc.Document
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// payload is the cached part: the document's JSON, from the cache where it is warm.
func (c *Cache) payload(accession string) ([]byte, error) {
	c.mu.Lock()
	if el, ok := c.entries[accession]; ok {
		c.order.MoveToFront(el)
		c.hits++
		payload := el.Value.(*cacheEntry).payload
		c.mu.Unlock()
		return payload, nil
	}
	c.misses++
	c.mu.Unlock()

	// The read is deliberately outside the lock: decompressing a frame is the slow part of a cold
	// lookup, and holding the lock through it would make one cold document stall every warm one.
	raw, err := c.artifact.Payload(accession)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if int64(len(raw)) > c.budget {
		// A document larger than the whole budget cannot be held. Counting it is what makes the
		// budget's shape visible: a corpus with many oversized documents needs a bigger bound, and
		// silence would leave a reader thinking the cache was simply not being used.
		c.drops++
		return raw, nil
	}
	if el, ok := c.entries[accession]; ok {
		// Another request cached it while this one was reading. Keep theirs, evict nothing.
		c.order.MoveToFront(el)
		return el.Value.(*cacheEntry).payload, nil
	}
	for c.used+int64(len(raw)) > c.budget {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.remove(oldest)
	}
	el := c.order.PushFront(&cacheEntry{accession: accession, payload: raw})
	c.entries[accession] = el
	c.used += int64(len(raw))
	return raw, nil
}

// remove drops one entry. The caller holds the lock.
func (c *Cache) remove(el *list.Element) {
	e := el.Value.(*cacheEntry)
	c.order.Remove(el)
	delete(c.entries, e.accession)
	c.used -= int64(len(e.payload))
}

// Stats reports the cache's state and hit counts.
func (c *Cache) Stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CacheStats{
		Entries:   len(c.entries),
		UsedBytes: c.used,
		Budget:    c.budget,
		Hits:      c.hits,
		Misses:    c.misses,
		Drops:     c.drops,
	}
}

func (c *Cache) Close() error { return c.artifact.Close() }
