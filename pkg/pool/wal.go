package pool

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
)

var errWALPoisoned = fmt.Errorf("WAL is poisoned after sync failure, recovery required")

// WAL is an append-only write-ahead log stored as JSONL (one JSON event per line).
type WAL struct {
	mu             sync.Mutex
	path           string
	f              *os.File
	seq            uint64
	poisoned       bool
	CorruptLines   int
	TruncatedBytes int64
}

// OpenWAL opens or creates a WAL file at path. It scans existing entries to
// recover the highest sequence number.
func OpenWAL(path string) (*WAL, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open wal: %w", err)
	}

	// Scan for the highest existing sequence number.
	var maxSeq uint64
	var corruptCount int
	var lastValidEnd int64
	var offset int64
	lineNum := 0
	const maxLineBytes = 1024 * 1024
	reader := bufio.NewReaderSize(f, maxLineBytes)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) == 0 && err == io.EOF {
			break
		}
		if err != nil && err != io.EOF {
			f.Close()
			return nil, fmt.Errorf("scan wal: %w", err)
		}
		if len(line) > maxLineBytes {
			f.Close()
			return nil, fmt.Errorf("scan wal: line %d exceeds %d bytes", lineNum+1, maxLineBytes)
		}
		lineNum++
		offset += int64(len(line))
		trimmed := bytes.TrimRight(line, "\r\n")
		if len(trimmed) == 0 {
			lastValidEnd = offset
			if err == io.EOF {
				break
			}
			continue
		}
		var e Event
		if err := json.Unmarshal(trimmed, &e); err != nil {
			corruptCount++
			log.Printf("WAL open: skipping corrupt line %d in %s: %v", lineNum, path, err)
		} else {
			lastValidEnd = offset
			if e.Sequence > maxSeq {
				maxSeq = e.Sequence
			}
		}
		if err == io.EOF {
			break
		}
	}

	fileInfo, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("stat wal: %w", err)
	}
	truncatedBytes := int64(0)
	if fileInfo.Size() > lastValidEnd && corruptCount > 0 {
		truncatedBytes = fileInfo.Size() - lastValidEnd
		log.Printf("WAL %s: found %d corrupt line(s), truncating %d trailing bytes", path, corruptCount, truncatedBytes)
		if err := f.Truncate(lastValidEnd); err != nil {
			f.Close()
			return nil, fmt.Errorf("truncate wal: %w", err)
		}
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return nil, fmt.Errorf("seek wal end: %w", err)
	}

	return &WAL{
		path:           path,
		f:              f,
		seq:            maxSeq,
		CorruptLines:   corruptCount,
		TruncatedBytes: truncatedBytes,
	}, nil
}

// Append writes an event to the WAL with an assigned sequence number and
// timestamp. It fsyncs after every write. On write or fsync failure the WAL
// is poisoned: the event may already be on disk so the sequence number cannot
// be reused. All subsequent Appends will fail until the session recovers via
// WAL replay.
func (w *WAL) Append(e Event) (Event, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.poisoned {
		return e, errWALPoisoned
	}

	w.seq++
	e.Sequence = w.seq

	data, err := json.Marshal(e)
	if err != nil {
		w.seq-- // safe: nothing written to disk yet
		return e, fmt.Errorf("marshal wal event: %w", err)
	}

	data = append(data, '\n')
	if _, err := w.f.Write(data); err != nil {
		w.poisoned = true
		return e, fmt.Errorf("write wal: %w", err)
	}
	if err := w.f.Sync(); err != nil {
		w.poisoned = true
		return e, fmt.Errorf("fsync wal: %w", err)
	}
	return e, nil
}

// Replay reads the entire WAL and calls fn for each event in order.
// Sequence numbers from the file are preserved (not re-assigned).
func (w *WAL) Replay(fn func(Event) error) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	f, err := os.Open(w.path)
	if err != nil {
		return fmt.Errorf("replay wal: %w", err)
	}
	defer f.Close()

	lineNum := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			log.Printf("WAL replay: skipping corrupt line %d in %s: %v", lineNum, w.path, err)
			continue
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// Close closes the WAL file handle.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f != nil {
		return w.f.Close()
	}
	return nil
}

// Seq returns the current highest sequence number.
func (w *WAL) Seq() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seq
}

// Poisoned returns true if the WAL entered a poisoned state after a write or
// fsync failure. A poisoned WAL refuses all further Appends.
func (w *WAL) Poisoned() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.poisoned
}
