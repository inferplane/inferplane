package audit

import (
	"crypto/sha256"
	"encoding/hex"
)

const maxWriteBatch = 64

// BatchSink accepts one ordered group. Success means every record was accepted.
// Partial delivery must return an error; the writer retains the entire group.
type BatchSink interface {
	Sink
	WriteBatch([][]byte) error
}

func (w *Writer) loop() {
	defer close(w.done)
	for first := range w.queue {
		items := []queuedRecord{first}
		if first.ack == nil {
		drain:
			for len(items) < maxWriteBatch {
				select {
				case item, ok := <-w.queue:
					if !ok {
						break drain
					}
					items = append(items, item)
					if item.ack != nil {
						break drain
					}
				default:
					break drain
				}
			}
		}
		last := len(items)
		if items[last-1].ack != nil {
			last--
		}
		if last > 0 {
			w.writeBatch(items[:last])
		}
		if last < len(items) {
			w.writeOne(items[last])
		}
	}
}

func (w *Writer) writeBatch(items []queuedRecord) {
	records := make([]Record, 0, len(items))
	lines := make([][]byte, 0, len(items))
	for _, item := range items {
		rec := item.record
		rec.Instance, rec.PrevHash = w.instance, w.prevHash
		canon, err := rec.Canonical()
		if err != nil {
			writeFailuresTotal.Add(1)
			w.blocked.CompareAndSwap(0, blockedRecord)
			continue
		}
		sum := sha256.Sum256(canon)
		w.prevHash = "sha256:" + hex.EncodeToString(sum[:])
		records, lines = append(records, rec), append(lines, canon)
	}
	if len(lines) == 0 {
		return
	}
	walErr := w.wal.AppendBatch(lines)
	if walErr != nil {
		w.walFailure()
	} else {
		w.pending += len(lines)
		w.count += int64(len(lines))
		w.head.Store(&chainHead{w.prevHash, w.count})
	}
	flushedAll := true
	for _, sink := range w.sinks {
		var failed bool
		if batch, ok := sink.(BatchSink); ok {
			failed = batch.WriteBatch(lines) != nil
		} else {
			for i, line := range lines {
				var err error
				if rs, ok := sink.(RecordSink); ok {
					err = rs.WriteRecord(&records[i], line)
				} else {
					err = sink.Write(line)
				}
				failed = failed || err != nil
			}
		}
		if failed && sink.Required() {
			flushedAll = false
			writeFailuresTotal.Add(1)
			bufferedRecords.Add(int64(len(lines)))
			w.metrics.IncAuditFailure(sink.Name())
		}
	}
	// Only this complete group may be reclaimed. Older ambiguous fan-out
	// remains operator-managed evidence, including across a restart.
	if walErr == nil && flushedAll && w.pending == len(lines) {
		if err := w.wal.Truncate(); err != nil {
			w.walFailure()
		} else {
			w.pending = 0
		}
	}
	if w.pending >= cap(w.queue) {
		w.blocked.CompareAndSwap(0, blockedBuffer)
	}
	w.metrics.SetAuditBufferUtilization(float64(w.pending) / float64(cap(w.queue)))
}
