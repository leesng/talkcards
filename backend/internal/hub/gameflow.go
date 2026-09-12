// gameflow.go 对局事件处理与生命周期：叫分/出牌/开局/终止/落库/断线保留。
// 调用方（事件 handler）均已持有 h.mu。
package hub

import (
	"time"

	"talkcards/backend/internal/card"
	"talkcards/backend/internal/game"
	"talkcards/backend/internal/store"
	"talkcards/backend/internal/table"
	"talkcards/backend/internal/wssrv"
)

func (h *Hub) onCallScore(s wssrv.Conn, data callScoreReq) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c, d := h.clientInRoom(s)
	if c == nil || d == nil || d.Game == nil {
		return
	}
	g := d.Game
	g.CallScore(c.posID) // 载荷 score 与 js 一致不被采用
	switch g.Phase() {
	case game.PhaseCall:
		h.broadCastRoom(EvCtxUserChange, c.deskID, ctxUserChange{
			CtxPos:       g.Turn(),
			CtxScore:     g.CtxScore(),
			CalledScores: calledScoresMap(g.CalledScores()),
			Timeout:      playTiming,
		}, nil)
	case game.PhasePlaying:
		dizhu := g.DizhuPosID()
		h.broadCastRoom(EvShowTopCard, c.deskID, showTopCard{TopCards: g.TopCards(), DizhuPosID: dizhu, Timeout: playTiming}, nil)
		h.broadCastRoom(EvCtxPlayChange, c.deskID, leadFrame(dizhu), nil)
	case game.PhaseRedeal:
		h.broadCastRoom(EvMessage, c.deskID, msgPayload{Msg: "没有玩家叫分，重新发牌"}, nil)
		h.startGame(c.deskID)
		h.broadCastRoom(EvUserMessageOut, c.deskID, userMessage{Type: "SYS", PosID: c.posID, Msg: "本局游戏无人叫分，重新发牌", ID: h.nextID(), Time: now()}, nil)
	}
}

func (h *Hub) onPlayCard(s wssrv.Conn, data []card.Card) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c, d := h.clientInRoom(s)
	if c == nil || d == nil || d.Game == nil {
		return
	}
	g := d.Game

	// 校验+应用一步完成；越序的合法牌会毒化对局（bug-for-bug：帧照播、双事件齐发）
	res := g.Play(c.posID, data)

	if res.Accepted {
		frame, snap := playFrame(g, c.posID, data, res)
		h.broadCastRoom(EvCtxPlayChange, d.DeskID, frame, nil)
		d.LastPlay = snap // 断线重连时重放
		h.emit(c, EvPlayCardOk, playCardSuccess{Data: nonNil(data), TmpFeng: frame.TmpFeng, SumFeng: frame.SumFeng})

		if g.Phase() == game.PhaseOver {
			h.recordGame(d, "normal")
			h.broadCastRoom(EvGameOver, d.DeskID, newGameOver(g.Result()), nil)
			for pos := 0; pos < table.SeatCount; pos++ {
				d.UpdatePos(pos, 1, nil)
			}
			d.ResetGame()
			h.clearDeskPending(d)
		}
		if g.Phase() == game.PhaseError {
			h.emit(c, EvPlayCardErr, "游戏出错")
		}
	} else {
		h.emit(c, EvPlayCardErr, data)
	}
}

// resumeClient 重连坐回：恢复 session 归属并按阶段重放快照（游戏进行中才重放游戏帧）
func (h *Hub) resumeClient(c *Session, d *table.Desk) {
	name := c.UserName()
	hold, ok := d.Hold(name)
	if !ok {
		return
	}
	d.RemoveHold(name)
	if t := h.timers[name]; t != nil {
		t.Stop()
		delete(h.timers, name)
	}

	c.deskID, c.posID = d.DeskID, hold.PosID
	h.emit(c, EvReconnect, reconnectPayload{DeskID: d.DeskID, PosID: hold.PosID, PosInfo: d.Positions})
	h.broadCastRoom(EvUserMessageOut, d.DeskID, userMessage{Type: "SYS", PosID: hold.PosID, Msg: "玩家[" + name + "]重新连接", ID: h.nextID(), Time: now()}, c.conn)
	h.logger.Info("玩家重连回到座位", "user", name, "desk", d.DeskID, "pos", hold.PosID)

	g := d.Game
	if g == nil {
		return
	}
	switch g.Phase() {
	case game.PhaseCall:
		h.emit(c, EvGameStart, newGameStart(g))
		h.emit(c, EvCtxUserChange, ctxUserChange{
			CtxPos:       g.Turn(),
			CtxScore:     g.CtxScore(),
			CalledScores: calledScoresMap(g.CalledScores()),
			Timeout:      playTiming,
		})
	case game.PhasePlaying:
		h.emit(c, EvGameStart, newGameStart(g))
		h.emit(c, EvShowTopCard, showTopCard{TopCards: g.TopCards(), DizhuPosID: g.DizhuPosID(), Timeout: playTiming})
		if d.LastPlay != nil {
			h.emit(c, EvCtxPlayChange, replayFrame(d.LastPlay)) // 重放时刷新倒计时
		} else {
			// 首出前掉线：无缓存帧，补发轮转帧（同叫分完成引导帧形态），
			// 否则重连者不知道轮到自己，对局死等
			h.emit(c, EvCtxPlayChange, leadFrame(g.Turn()))
		}
	}
}

// holdSeatForReconnect 保留掉线者座位并（若配置了时限）启动重连倒计时
func (h *Hub) holdSeatForReconnect(d *table.Desk, userName string, posID int) {
	d.AddHold(table.Hold{UserName: userName, PosID: posID})
	h.broadCastRoom(EvUserMessageOut, d.DeskID, userMessage{Type: "SYS", PosID: posID, Msg: "玩家[" + userName + "]掉线，等待重连……", ID: h.nextID(), Time: now()}, nil)
	if h.reconnectWait <= 0 {
		return
	}
	h.timers[userName] = time.AfterFunc(h.reconnectWait, func() { h.onReconnectTimeout(userName) })
}

// onReconnectTimeout 重连时限已过：该玩家仍未回来，判逃跑终止（若对局已结束则仅清座位）
func (h *Hub) onReconnectTimeout(userName string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	d := h.lobby.DeskHolding(userName)
	if d == nil { // 已重连或对局已结束清理
		return
	}
	hold, _ := d.Hold(userName)
	d.RemoveHold(userName)
	delete(h.timers, userName)

	deskID, posID := d.DeskID, hold.PosID
	h.logger.Warn("玩家重连超时，按逃跑处理", "user", userName, "desk", deskID, "pos", posID)

	// 座位释放（等价 exitRoom 的座位/广播部分；desk.state 怪癖已修正为 0）
	h.releaseSeat(d, posID, nil)

	if d.GameInProgress() {
		h.terminateGame(d, posID, userName)
	}
}

// terminateGame 有人逃跑时终止对局：广播、重置、落库（endReason=escape）
func (h *Hub) terminateGame(d *table.Desk, escapePos int, escapee string) {
	h.recordGame(d, "escape")

	if d.Game != nil {
		d.UpdateOtherPos(escapePos, 1) // js 行为：空座位也会被置为 1，照抄
		h.broadCastRoom(EvPosStatusReset, d.DeskID, posStatusReset{Pos: d.Positions, State: 1}, nil)
		h.broadCastRoom(EvRoomStatusChg, d.DeskID, roomStatusChange{State: 0}, nil)
		h.broadCastRoom(EvForceExit, d.DeskID, forceExitPayload{Msg: "有玩家逃跑，游戏结束", PosID: escapePos}, nil)
	}
	d.ResetGame()
	h.clearDeskPending(d) // 对局已终止，清掉本桌所有掉线保留记录
	h.logger.Warn("对局因玩家逃跑终止", "desk", d.DeskID, "user", escapee)
}

// clearDeskPending 清理某桌全部掉线保留记录并释放其座位（对局结束/终止时调用）
func (h *Hub) clearDeskPending(d *table.Desk) {
	for _, hold := range d.ClearHolds() {
		if t := h.timers[hold.UserName]; t != nil {
			t.Stop()
			delete(h.timers, hold.UserName)
		}
		h.releaseSeat(d, hold.PosID, nil)
	}
}

// recordGame 把该桌当前（即将结束的）对局落库。须在对局复位（ResetGame）之前调用。
// 玩家列表取自开局时的座位快照，不受逃跑清理/断线释放影响。
func (h *Hub) recordGame(d *table.Desk, endReason string) {
	g := d.Game
	if g == nil || d.StartedAt.IsZero() {
		return
	}
	sumFeng := g.SumFeng()
	res := g.Result()
	winnerSet := map[int]bool{}
	for _, w := range res.Winner {
		winnerSet[w] = true
	}
	t0, t1 := g.TeamScores() // 全队已收分合计；正常终局胜队改用 Result.Score（含带走分）
	rec := store.GameRecord{
		DeskID:     d.DeskID,
		StartedAt:  d.StartedAt,
		EndedAt:    time.Now(),
		EndReason:  endReason,
		Team0Score: t0,
		Team1Score: t1,
		Players:    make([]store.GamePlayer, 0, table.SeatCount),
	}
	for i := 0; i < table.SeatCount; i++ {
		name := d.Players[i]
		if name == "" {
			continue
		}
		rec.Players = append(rec.Players, store.GamePlayer{
			PosID:     i,
			UserName:  name,
			SumFeng:   sumFeng[i],
			Remaining: g.HandLen(i),
			Win:       winnerSet[i],
		})
	}
	h.enqueueSave(rec)
}

// startGame 全员准备完毕开局（或无人叫分后重发）
func (h *Hub) startGame(deskID int) {
	d := h.lobby.Desk(deskID)
	if d == nil {
		return
	}
	if d.Game == nil {
		d.Game = game.New()
	}
	g := d.Game
	g.Init()
	g.Start()
	d.StartedAt = time.Now()
	d.LastPlay = nil
	d.Players = d.PlayerSnapshot()
	h.broadCastRoom(EvGameStart, deskID, newGameStart(g), nil)
	h.broadCastRoom(EvCtxUserChange, deskID, ctxUserChange{
		CtxPos:   g.Turn(),
		CtxScore: g.CtxScore(),
		Timeout:  playTiming,
	}, nil)
}
