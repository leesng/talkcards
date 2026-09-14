// persist.go: async save queue; one worker writes SQLite off the global lock.
package hub

import "talkcards/backend/internal/store"

const saveQueue = 128

// saveLoop: single serial worker (store has no internal locking).
func (h *Hub) saveLoop() {
	for rec := range h.saves {
		if _, err := h.st.SaveGame(rec); err != nil {
			h.logger.Error("对局落库失败", "desk", rec.DeskID, "err", err)
		}
		h.saveWG.Done()
	}
}

// enqueueSave: blocks when full rather than drop a record.
func (h *Hub) enqueueSave(rec store.GameRecord) {
	h.saveWG.Add(1)
	h.saves <- rec
}

// Flush: wait for queued saves; call only when no more events will arrive.
func (h *Hub) Flush() { h.saveWG.Wait() }
