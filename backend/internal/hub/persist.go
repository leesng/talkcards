// persist.go 战绩落库的异步写队列：hub 持锁路径只做领域快照并入队，
// 由单一 worker 串行写 SQLite，避免磁盘 I/O 阻塞全局锁。
package hub

import "talkcards/backend/internal/store"

// saveQueue 待落库记录缓冲；worker 持续排空，正常负载下不会填满
const saveQueue = 128

// saveLoop 单 worker 串行落库（store 内部无锁，串行写即安全）
func (h *Hub) saveLoop() {
	for rec := range h.saves {
		if _, err := h.st.SaveGame(rec); err != nil {
			h.logger.Error("对局落库失败", "desk", rec.DeskID, "err", err)
		}
		h.saveWG.Done()
	}
}

// enqueueSave 入队一条落库记录（调用方持锁）。队列满时阻塞至 worker 取走，
// 保证不丢记录；worker 只做纯写库，阻塞窗口远小于原先的同步落库。
func (h *Hub) enqueueSave(rec store.GameRecord) {
	h.saveWG.Add(1)
	h.saves <- rec
}

// Flush 等待已入队的落库全部完成。须在不再有事件处理时调用（关服/测试）。
func (h *Hub) Flush() { h.saveWG.Wait() }
