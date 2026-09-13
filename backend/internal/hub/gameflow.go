// gameflow.go 对局事件处理与生命周期：叫分/出牌/开局/终止/落库/断线保留。
// 调用方（事件 handler）均已持有 h.mu。
package hub

import (
	"math/rand"
	"time"

	"talkcards/backend/internal/bot"
	"talkcards/backend/internal/card"
	"talkcards/backend/internal/game"
	"talkcards/backend/internal/store"
	"talkcards/backend/internal/table"
	"talkcards/backend/internal/wssrv"
)

func (h *Hub) onPlayCard(s wssrv.Conn, data []card.Card) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c, d := h.clientInRoom(s)
	if c == nil || d == nil || d.Game == nil {
		return
	}
	g := d.Game

	// 托管中由系统代打，手动出牌拒绝（避免与代打竞态越序毒化对局）
	if seat := d.Seat(c.posID); seat != nil && seat.Trustee {
		h.emit(c, EvMessage, msgPayload{Msg: "托管中，请先取消托管再手动操作"})
		return
	}

	// 校验+应用一步完成；越序的合法牌会毒化对局（bug-for-bug：帧照播、双事件齐发）
	res := g.Play(c.posID, data)
	h.applyPlayResult(d, c.posID, c, data, res)
}

// applyPlayResult 出牌应用与广播（真人与机器人共用）。
// posID 为出牌者座位；origin 为动作发起者会话（机器人传 nil：不回
// PLAY_CARD_SUCCESS/ERROR），不再从 origin 反推座位——机器人无会话，
// 广播帧的 ctxData.posId 必须是真实出牌者，否则前端会把机器人出的牌
// 全部记到 0 号座位头上（人机模式房主恰为 0 号时显示/提示全部错乱）。
func (h *Hub) applyPlayResult(d *table.Desk, posID int, origin *Session, cards []card.Card, res game.PlayResult) {
	g := d.Game

	if res.Accepted {
		frame, snap := playFrame(g, posID, cards, res)
		// 一圈收分/出完接风后桌面清空（TrickTop 无牌可压）：带上清桌标记，
		// 客户端据此清掉上一圈残留的出牌区与"要压的牌型"
		if g.Phase() == game.PhasePlaying {
			if _, ok := g.TrickTop(); !ok {
				frame.Clear = true
			}
		}
		h.broadCastRoom(EvCtxPlayChange, d.DeskID, frame, nil)
		d.LastPlay = snap // 断线重连时重放
		if !snap.IsPass {
			d.LastValidPlay = snap // 桌面上的有效牌，过牌不覆盖
		}
		if origin != nil {
			h.emit(origin, EvPlayCardOk, playCardSuccess{Data: nonNil(cards), TmpFeng: frame.TmpFeng, SumFeng: frame.SumFeng})
		}

		if g.Phase() == game.PhaseOver {
			h.recordGame(d, "normal")
			h.broadCastRoom(EvGameOver, d.DeskID, newGameOver(g.Result()), nil)
			for pos := 0; pos < table.SeatCount; pos++ {
				d.UpdatePos(pos, 1, nil)
			}
			h.clearTrustees(d)
			h.clearBotSeats(d)
			d.ResetGame()
			h.clearDeskPending(d)
			return
		}
		if g.Phase() == game.PhaseError {
			h.logger.Error("对局进入错误状态（越序出牌毒化）", "desk", d.DeskID, "actor", posID)
			if origin != nil {
				h.emit(origin, EvPlayCardErr, "游戏出错")
			}
		}
	} else if origin != nil {
		h.emit(origin, EvPlayCardErr, cards)
	} else {
		h.logger.Warn("机器人出牌被拒", "desk", d.DeskID, "pos", posID, "cards", len(cards),
			"phase", g.Phase(), "turn", g.Turn())
	}
	h.scheduleBotIfTurn(d)
}

// clearTrustees 终局/终止时清除托管标记并广播（托管者仍在座，仅恢复手动语义；
// 对局已结束，前端按 GAME_OVER/FORCE_EXIT 复位界面）
func (h *Hub) clearTrustees(d *table.Desk) {
	for pos := 0; pos < table.SeatCount; pos++ {
		if seat := d.Seat(pos); seat != nil && seat.Trustee {
			seat.Trustee = false
			h.broadcastTrustee(d, pos)
		}
	}
}

// clearBotSeats 终局清理机器人座位：复位为空座并广播（真人座位不受影响）
func (h *Hub) clearBotSeats(d *table.Desk) {
	for _, pos := range d.ClearBots() {
		h.broadCastRoom(EvPosStatusChange, d.DeskID, posStatusChange{PosID: pos, State: 0}, nil)
		h.broadCastHouse(EvStatusChange, houseStatusChange{DeskID: d.DeskID, PosID: pos, State: 0, UserName: ""})
	}
	if t := h.botTimers[d.DeskID]; t != nil {
		t.Stop()
		delete(h.botTimers, d.DeskID)
	}
}

// scheduleBotIfTurn 轮到机器人时安排其自动行动（开局/每次叫分与出牌后调用）。
// 每桌至多一个在途定时器，换代时停旧表。延迟在 [min,max] 内随机（默认 1-3 秒）。
func (h *Hub) scheduleBotIfTurn(d *table.Desk) {
	if !h.botTurn(d) {
		return
	}
	if t := h.botTimers[d.DeskID]; t != nil {
		t.Stop()
	}
	span := h.botDelayMax - h.botDelayMin
	delay := h.botDelayMin
	if span > 0 {
		delay += time.Duration(rand.Int63n(int64(span) + 1))
	}
	deskID := d.DeskID
	h.botTimers[deskID] = time.AfterFunc(delay, func() { h.botAct(deskID) })
}

// botTurn 当前轮到者是否由系统代打（机器人座位或托管中的真人座位；出牌阶段才有意义）
func (h *Hub) botTurn(d *table.Desk) bool {
	if d.Game == nil {
		return false
	}
	g := d.Game
	if g.Phase() != game.PhasePlaying {
		return false
	}
	seat := d.Seat(g.Turn())
	return seat != nil && (seat.IsBot || seat.Trustee)
}

// botAct 机器人行动：持锁重验桌/阶段/轮到者后按策略出牌。
// 回调由 time.AfterFunc 触发，需自行持锁。
func (h *Hub) botAct(deskID int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.botTimers, deskID)

	d := h.lobby.Desk(deskID)
	if d == nil || !h.botTurn(d) {
		return
	}
	g := d.Game
	pos := g.Turn()

	var hand []card.Card
	for _, hs := range g.Hands() {
		if hs.PosID == pos {
			hand = hs.Cards
			break
		}
	}
	var play []card.Card
	lead := false
	if top, ok := g.TrickTop(); ok {
		// 智能跟牌：不吃队友大牌、炸弹按桌面分掷骰（细节见 bot.FollowSmart）
		play = bot.FollowSmart(bot.FollowCtx{
			Hand:          hand,
			Top:           top,
			TopIsTeammate: g.TrickPos()%2 == pos%2,
			Pot:           g.TmpFeng(),
		}) // 跟不住/掷骰未中返回 nil → 过牌
	} else {
		play = bot.Lead(hand)
		lead = true
	}
	if play == nil {
		play = []card.Card{}
	}
	h.logger.Debug("机器人出牌", "desk", d.DeskID, "pos", pos, "cards", len(play), "lead", lead)
	res := g.Play(pos, play)
	h.applyPlayResult(d, pos, nil, play, res)
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
	h.emit(c, EvReconnect, reconnectPayload{DeskID: d.DeskID, PosID: hold.PosID, PosInfo: d.Positions, HostPosID: d.HostPosID})
	// 断线超时期间可能已被自动托管：重连即恢复手动（在途代打定时器会在
	// botAct 持锁重验 botTurn 时发现托管已取消而自行放弃）
	if seat := d.Seat(hold.PosID); seat != nil && seat.Trustee && d.GameInProgress() {
		seat.Trustee = false
		h.broadcastTrustee(d, hold.PosID)
	}
	h.broadCastRoom(EvUserMessageOut, d.DeskID, userMessage{Type: "SYS", PosID: hold.PosID, Msg: "玩家[" + name + "]重新连接", ID: h.nextID(), Time: now()}, c.conn)
	h.logger.Info("玩家重连回到座位", "user", name, "desk", d.DeskID, "pos", hold.PosID)

	g := d.Game
	if g == nil {
		return
	}
	switch g.Phase() {
	case game.PhasePlaying:
		h.emit(c, EvGameStart, newGameStart(g))
		h.emit(c, EvShowTopCard, showTopCard{TopCards: g.TopCards(), DizhuPosID: g.DizhuPosID(), Timeout: playTiming})
		if d.LastPlay != nil {
			// 上一手是过牌：桌面上仍压着本圈的有效牌，先补发它，
			// 重连者才知道"要压什么"（否则只看到"不出"，牌面信息丢失）。
			// 仅在游戏仍存在桌面牌可压时补发：一圈无人压牌后桌面已收分清空，
			// 此时再补发会造成"还有牌要压"的错觉。
			_, hasTop := g.TrickTop()
			if d.LastPlay.IsPass && d.LastValidPlay != nil && hasTop {
				h.emit(c, EvCtxPlayChange, replayFrame(d.LastValidPlay))
			}
			last := replayFrame(d.LastPlay) // 重放时刷新倒计时
			// 当前已是一圈收分/接风后的领出状态：带清桌标记，
			// 让重连者清掉（自动重连未刷新页面时残留的）上一圈显示与牌型
			if !hasTop {
				last.Clear = true
			}
			h.emit(c, EvCtxPlayChange, last)
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

// onReconnectTimeout 重连时限已过：该玩家仍未回来。
// 对局进行中则转为自动托管继续打（出牌规则与机器人一致，保留座位可随时重连接管）；
// 若此时已无任何真实玩家在线（全部断线超时），对局无从继续，直接按逃跑终止并落库。
// 对局不在进行中（间隙掉线等）则仅释放座位。
func (h *Hub) onReconnectTimeout(userName string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	d := h.lobby.DeskHolding(userName)
	if d == nil { // 已重连或对局已结束清理
		return
	}
	hold, _ := d.Hold(userName)
	delete(h.timers, userName) // 倒计时已到，保留记录留待重连接管或终局清理

	if !d.GameInProgress() {
		// 原路径：对局不在进行中，超时仅释放座位（不终止对局）
		d.RemoveHold(userName)
		h.releaseSeat(d, hold.PosID, nil)
		return
	}

	deskID, posID := d.DeskID, hold.PosID
	if h.anyRealPlayerOnline(d) {
		// 还有别的真人在打：断线者转托管，对局继续
		if seat := d.Seat(posID); seat != nil && !seat.IsBot && !seat.Trustee {
			seat.Trustee = true
			h.broadcastTrustee(d, posID)
		}
		h.broadCastRoom(EvUserMessageOut, deskID, userMessage{Type: "SYS", PosID: posID, Msg: "玩家[" + userName + "]断线超时，系统自动托管", ID: h.nextID(), Time: now()}, nil)
		h.logger.Warn("玩家重连超时，转为自动托管", "user", userName, "desk", deskID, "pos", posID)
		h.scheduleBotIfTurn(d) // 恰轮到托管者时立即安排代打
		return
	}

	// 全部真实玩家均已断线超时：无人可继续对局，直接中止（判逃跑）
	h.logger.Warn("全部真实玩家断线超时，对局直接中止", "desk", deskID, "timeoutUser", userName)
	d.RemoveHold(userName)
	h.releaseSeat(d, posID, nil)
	h.terminateGame(d, posID, userName)
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
	h.clearBotSeats(d)    // 人机局终止同样清理机器人座位
	h.clearTrustees(d)    // 托管者座位同样恢复手动语义
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

// startGame 全员准备完毕开局
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
	// 开局即出牌阶段：直接广播首出者与轮转帧（无叫分流程）
	dizhu := g.DizhuPosID()
	h.broadCastRoom(EvShowTopCard, deskID, showTopCard{TopCards: g.TopCards(), DizhuPosID: dizhu, Timeout: playTiming}, nil)
	h.broadCastRoom(EvCtxPlayChange, deskID, leadFrame(dizhu), nil)
	h.scheduleBotIfTurn(d) // 人机局：轮到机器人先出牌
}
