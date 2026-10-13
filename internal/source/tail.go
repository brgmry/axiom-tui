package source

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// counters are the ingest statistics shared by the tailer and its handler.
type counters struct {
	bytes           atomic.Int64
	untimed         atomic.Int64
	skippedLines    atomic.Int64
	parseErrors     atomic.Int64
	unreadableFiles atomic.Int64
}

// lineHandler turns one complete line into a store event plus the
// source's own entry id ("" when it has none). reset is called when a
// file's generation changes (truncation or rotation) so per-file state can
// be dropped.
type lineHandler interface {
	handle(file int32, gen uint32, offset int64, line []byte, ingest time.Time) (ev event, id string, ok bool)
	reset(file int32)
}

// tailFile is the per-file state of the tailer. gen, gone and done are
// read by other goroutines and are written only under tailer.mu; the rest
// belongs to the tailer goroutine.
type tailFile struct {
	path       string
	idx        int32
	gen        uint32
	dev, ino   uint64
	idOK       bool
	offset     int64 // byte offset just past the last byte consumed
	skipping   bool  // dropping an over-limit line up to its newline
	gone       bool  // path disappeared; events kept, polling stopped
	unreadable bool  // open failed; counted once
	done       bool  // read to EOF at least once
}

const (
	defaultPollEvery = 2 * time.Second
	discoverEvery    = 5 * time.Second
	readBufSize      = 256 * 1024
	batchMax         = 4096
	batchMaxAge      = 50 * time.Millisecond
)

var errStaleRef = errors.New("source line no longer on disk")

// tailer discovers files under the configured paths and follows them by
// polling: no fsnotify, no descriptor per file, one code path everywhere.
type tailer struct {
	paths     []string
	backfill  time.Duration
	maxLine   int
	pollEvery time.Duration
	store     *store
	handler   lineHandler
	counters  *counters

	// newTicker returns a channel ticking every d and its stop func; tests
	// swap it to drive the loop without waiting on the clock.
	newTicker func(d time.Duration) (<-chan time.Time, func())

	mu      sync.Mutex
	files   []*tailFile
	byPath  map[string]*tailFile
	loading bool

	life    sync.Mutex // guards started and stopped
	started bool
	stopped bool
	stop    chan struct{}
	done    chan struct{} // closed when the goroutine has returned
}

func newTailer(sc SourceConfig, st *store, h lineHandler, c *counters, pollEvery time.Duration) *tailer {
	paths := make([]string, 0, len(sc.Paths))
	for _, p := range sc.Paths {
		paths = append(paths, ExpandHome(p))
	}
	if pollEvery <= 0 {
		pollEvery = defaultPollEvery
	}
	return &tailer{
		paths:     paths,
		backfill:  sc.Backfill.D(),
		maxLine:   sc.MaxLineBytes,
		pollEvery: pollEvery,
		store:     st,
		handler:   h,
		counters:  c,
		newTicker: realTicker,
		byPath:    map[string]*tailFile{},
		loading:   true,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// start runs the first full pass then keeps polling until stopped. A
// second call, or a call after close, does nothing.
func (t *tailer) start() {
	t.life.Lock()
	defer t.life.Unlock()
	if t.started || t.stopped {
		return
	}
	t.started = true
	go func() {
		defer close(t.done)
		t.discoverOnce()
		t.pollOnce()
		poll, stopPoll := t.newTicker(t.pollEvery)
		defer stopPoll()
		discover, stopDiscover := t.newTicker(discoverEvery)
		defer stopDiscover()
		for {
			select {
			case <-t.stop:
				return
			case <-discover:
				// Poll straight after so a new file is read without waiting
				// for the next poll tick.
				t.discoverOnce()
				t.pollOnce()
			case <-poll:
				t.pollOnce()
			}
		}
	}()
}

// close stops the goroutine and waits for it to return, so no event is
// appended once close returns. Idempotent; safe on a tailer never started.
// Must not be called from the tailer goroutine itself.
func (t *tailer) close() {
	t.life.Lock()
	if !t.stopped {
		t.stopped = true
		close(t.stop)
	}
	started := t.started
	t.life.Unlock()
	if started {
		<-t.done
	}
}

// stopping reports whether close has been called, so long passes can end
// early instead of holding close up.
func (t *tailer) stopping() bool {
	select {
	case <-t.stop:
		return true
	default:
		return false
	}
}

func realTicker(d time.Duration) (<-chan time.Time, func()) {
	tk := time.NewTicker(d)
	return tk.C, tk.Stop
}

// progress reports the ingest state for the footer and --check.
func (t *tailer) progress() Progress {
	t.mu.Lock()
	p := Progress{Loading: t.loading}
	for _, f := range t.files {
		if f.gone {
			continue
		}
		p.FilesTotal++
		if f.done {
			p.FilesDone++
		}
	}
	t.mu.Unlock()
	p.Bytes = t.counters.bytes.Load()
	p.Events = int64(t.store.len())
	p.Untimed = t.counters.untimed.Load()
	p.SkippedLines = t.counters.skippedLines.Load()
	p.ParseErrors = t.counters.parseErrors.Load()
	p.UnreadableFiles = t.counters.unreadableFiles.Load()
	return p
}

// ─── Discovery ───────────────────────────────────────────────────────────────

// discoverOnce expands every path entry: directories are walked for
// *.jsonl, globs re-evaluated, plain files used as is. Files older than
// backfill are ignored; they are picked up later when their mtime moves.
func (t *tailer) discoverOnce() {
	cutoff := time.Now().Add(-t.backfill)
	for _, p := range t.paths {
		fi, err := os.Stat(p)
		switch {
		case err == nil && fi.IsDir():
			t.walkDir(p, cutoff)
		case err == nil:
			t.consider(p, fi, cutoff)
		default:
			matches, _ := filepath.Glob(p)
			for _, m := range matches {
				mi, err := os.Stat(m)
				if err != nil {
					continue
				}
				if mi.IsDir() {
					t.walkDir(m, cutoff)
				} else {
					t.consider(m, mi, cutoff)
				}
			}
		}
	}
}

func (t *tailer) walkDir(root string, cutoff time.Time) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		t.consider(path, fi, cutoff)
		return nil
	})
}

// consider registers a candidate file, or re-activates one that went away.
//
// The handler is never called with t.mu held: the handler has a lock of
// its own and calls back into the tailer (filePath) while holding it, so
// holding t.mu across reset would be a lock-order inversion.
func (t *tailer) consider(path string, fi os.FileInfo, cutoff time.Time) {
	if fi.ModTime().Before(cutoff) {
		return
	}
	reset := int32(-1)
	t.mu.Lock()
	if f, ok := t.byPath[path]; ok {
		if f.gone {
			f.gone = false
			dev, ino, idOK := fileID(fi)
			if idOK && f.idOK && (dev != f.dev || ino != f.ino) {
				// A different file at the same path: start over.
				f.gen++
				f.offset = 0
				f.skipping = false
				f.dev, f.ino = dev, ino
				reset = f.idx
			}
		}
	} else {
		f := &tailFile{path: path, idx: int32(len(t.files))}
		f.dev, f.ino, f.idOK = fileID(fi)
		t.files = append(t.files, f)
		t.byPath[path] = f
	}
	t.mu.Unlock()
	if reset >= 0 {
		t.handler.reset(reset)
	}
}

// ─── Polling ─────────────────────────────────────────────────────────────────

// update runs fn under the file-table lock. Every write to a tailFile
// field that progress, hydrate or filePath read (gen, gone, done) goes
// through here; offset is only touched by the tailer goroutine.
func (t *tailer) update(fn func()) {
	t.mu.Lock()
	fn()
	t.mu.Unlock()
}

// pollOnce stats every known file and reads what is new, handling
// truncation and rotation. The first call completes the backfill pass.
func (t *tailer) pollOnce() {
	t.mu.Lock()
	files := append([]*tailFile(nil), t.files...)
	t.mu.Unlock()

	for _, f := range files {
		if t.stopping() {
			return
		}
		if f.gone || f.unreadable {
			continue
		}
		fi, err := os.Stat(f.path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				t.update(func() { f.gone = true })
			}
			continue
		}
		if dev, ino, ok := fileID(fi); ok && f.idOK && (dev != f.dev || ino != f.ino) {
			// Rotation: the path now names a different file. Old events keep
			// their generation; Hydrate fails soft for them.
			t.update(func() {
				f.gen++
				f.offset = 0
				f.skipping = false
				f.dev, f.ino = dev, ino
			})
			t.handler.reset(f.idx)
		}
		if fi.Size() < f.offset {
			// Truncation or in-place rewrite: forget everything from this file.
			t.update(func() {
				f.gen++
				f.offset = 0
				f.skipping = false
			})
			t.store.dropFile(f.idx)
			t.handler.reset(f.idx)
		}
		if fi.Size() > f.offset {
			t.readFrom(f)
		}
		t.update(func() { f.done = true })
	}

	t.mu.Lock()
	t.loading = false
	t.mu.Unlock()
}

// readFrom consumes complete lines from f.offset to EOF. Bytes after the
// last newline are left for the next poll, unless they are already over
// maxLine: then the line is counted as skipped and its bytes are consumed
// and dropped up to the next newline, across polls if need be, so an
// oversized line costs neither memory nor re-reads.
func (t *tailer) readFrom(f *tailFile) {
	file, err := os.Open(f.path)
	if err != nil {
		f.unreadable = true
		t.counters.unreadableFiles.Add(1)
		return
	}
	defer file.Close()
	if _, err := file.Seek(f.offset, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReaderSize(file, readBufSize)
	batch := make([]event, 0, 256)
	ids := make([]string, 0, 256)
	batchStart := time.Now()
	flush := func() {
		if len(batch) > 0 {
			t.store.appendBatch(batch, ids)
			batch = make([]event, 0, 256)
			ids = make([]string, 0, 256)
		}
		batchStart = time.Now()
	}

	consume := func(n int) {
		f.offset += int64(n)
		t.counters.bytes.Add(int64(n))
	}

	var long []byte // fragments of a line longer than the buffer, never over maxLine
	for {
		frag, err := r.ReadSlice('\n')
		if f.skipping {
			consume(len(frag))
			if err == bufio.ErrBufferFull {
				continue
			}
			if err != nil {
				break
			}
			f.skipping = false
			continue
		}
		if err != nil {
			if t.overLimit(long, frag) {
				// Too long to ever be ingested: count it now, drop what is
				// here and discard the rest as it arrives.
				t.counters.skippedLines.Add(1)
				consume(len(long) + len(frag))
				long = nil
				f.skipping = true
				if err == bufio.ErrBufferFull {
					continue
				}
				break
			}
			if err == bufio.ErrBufferFull {
				long = append(long, frag...)
				continue
			}
			// EOF (or a read error) with a partial line: leave it for later.
			break
		}
		var line []byte
		if long != nil {
			line = append(long, frag...)
			long = nil
		} else {
			line = frag
		}
		lineStart := f.offset
		consume(len(line))
		line = bytes.TrimRight(line, "\r\n")
		if ev, id, ok := t.processLine(f, lineStart, line); ok {
			batch = append(batch, ev)
			ids = append(ids, id)
		}
		if len(batch) >= batchMax || time.Since(batchStart) > batchMaxAge {
			flush()
			if t.stopping() {
				return
			}
		}
	}
	flush()
}

// overLimit reports whether a partial line, long followed by frag, is
// already longer than maxLine. A trailing carriage return may be the first
// half of CRLF, so it does not count.
func (t *tailer) overLimit(long, frag []byte) bool {
	n := len(long) + len(frag)
	if n <= t.maxLine {
		return false
	}
	if n > t.maxLine+1 {
		return true
	}
	last := long
	if len(frag) > 0 {
		last = frag
	}
	return last[len(last)-1] != '\r'
}

// processLine applies the size and shape rules, then hands the line to the
// handler. The offset still advances past rejected lines.
func (t *tailer) processLine(f *tailFile, offset int64, line []byte) (event, string, bool) {
	if len(line) > t.maxLine {
		t.counters.skippedLines.Add(1)
		return event{}, "", false
	}
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return event{}, "", false
	}
	if trimmed[0] != '{' || !json.Valid(trimmed) {
		t.counters.parseErrors.Add(1)
		return event{}, "", false
	}
	ev, id, ok := t.handler.handle(f.idx, f.gen, offset, line, time.Now())
	if !ok {
		return event{}, "", false
	}
	ev.ref = eventRef{file: f.idx, generation: f.gen, offset: offset, length: int32(len(line))}
	return ev, id, true
}

// hydrate re-reads the line behind ref and decodes it. A generation
// mismatch or a missing file yields errStaleRef; a done ctx yields its
// error.
func (t *tailer) hydrate(ctx context.Context, ref eventRef) (map[string]any, error) {
	t.mu.Lock()
	if int(ref.file) >= len(t.files) {
		t.mu.Unlock()
		return nil, errStaleRef
	}
	f := t.files[ref.file]
	path, gen := f.path, f.gen
	t.mu.Unlock()
	if gen != ref.generation {
		return nil, errStaleRef
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("hydrate: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errStaleRef
	}
	defer file.Close()
	buf := make([]byte, ref.length)
	if _, err := file.ReadAt(buf, ref.offset); err != nil && !errors.Is(err, io.EOF) {
		return nil, errStaleRef
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("hydrate: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(buf, &out); err != nil {
		return nil, errStaleRef
	}
	return out, nil
}

// filePath returns the path of a file index, for the pi handler.
func (t *tailer) filePath(idx int32) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if int(idx) >= len(t.files) {
		return ""
	}
	return t.files[idx].path
}
