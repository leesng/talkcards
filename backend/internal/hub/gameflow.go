// gameflow.go: game lifecycle; all callers hold h.mu.
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

	// Reject manual plays while trusted out: racing the bot out-of-turn poisons the game.
	if seat := d.Seat(c.posID); seat != nil && seat.Trustee {
		h.emit(c, EvMessage, msgPayload{Msg: "托管中，请先取消托管再手动操作"})
		return
	}

	// Out-of-turn valid plays poison the game (bug-for-bug: frame still broadcast, both events fire).
	res := g.Play(c.posID, data)
	h.applyPlayResult(d, c.posID, c, data, res)
}

// applyPlayResult: shared by humans and bots; origin nil (bots) means no
// SUCCESS/ERROR reply. ctxData.posId must be the real player — defaulting
// to 0 when origin is nil credited all bot plays to seat 0 in the frontend.
func (h *Hub) applyPlayResult(d *table.Desk, posID int, origin *Session, cards []card.Card, res game.PlayResult) {
	g := d.Game

	if res.Accepted {
		frame, snap := playFrame(g, posID, cards, res)
		// Table empty after a collected trick / fresh lead: flag clients to
		// clear the play area and "shape to beat".
		if g.Phase() == game.PhasePlaying {
			if _, ok := g.TrickTop(); !ok {
				frame.Clear = true
			}
		}
		h.broadCastRoom(EvCtxPlayChange, d.DeskID, frame, nil)
		d.LastPlay = snap
		if !snap.IsPass {
			d.LastValidPlay = snap // standing valid play; passes don't overwrite
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

// clearTrustees: clear trustee flags at game end and broadcast.
func (h *Hub) clearTrustees(d *table.Desk) {
	for pos := 0; pos < table.SeatCount; pos++ {
		if seat := d.Seat(pos); seat != nil && seat.Trustee {
			seat.Trustee = false
			h.broadcastTrustee(d, pos)
		}
	}
}

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

// scheduleBotIfTurn: schedule the bot's action on a bot/trusted seat's turn;
// one in-flight timer per desk, random delay.
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

// botAct: runs from time.AfterFunc — takes the lock itself and re-validates
// desk/phase/turn before playing.
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
		play = bot.FollowSmart(bot.FollowCtx{
			Hand:          hand,
			Top:           top,
			TopIsTeammate: g.TrickPos()%2 == pos%2,
			Pot:           g.TmpFeng(),
		}) // nil = cannot/won't follow → pass
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

// resumeClient: reconnect back into the held seat and replay snapshots.
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
	// May have been auto-trusted during the timeout: reconnect restores
	// manual (an in-flight bot timer gives up at its botTurn re-check).
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
			// After a pass, the standing valid play is still on the table —
			// send it first so the reconnecter knows what to beat, but only
			// while the table still has a beatable play.
			_, hasTop := g.TrickTop()
			if d.LastPlay.IsPass && d.LastValidPlay != nil && hasTop {
				h.emit(c, EvCtxPlayChange, replayFrame(d.LastValidPlay))
			}
			last := replayFrame(d.LastPlay)
			if !hasTop {
				last.Clear = true // leading state: clear stale previous-trick display
			}
			h.emit(c, EvCtxPlayChange, last)
		} else {
			// Disconnected before the first lead: no cached frame — send a
			// turn frame or the game deadlocks waiting on the reconnecter.
			h.emit(c, EvCtxPlayChange, leadFrame(g.Turn()))
		}
	}
}

func (h *Hub) holdSeatForReconnect(d *table.Desk, userName string, posID int) {
	d.AddHold(table.Hold{UserName: userName, PosID: posID})
	h.broadCastRoom(EvUserMessageOut, d.DeskID, userMessage{Type: "SYS", PosID: posID, Msg: "玩家[" + userName + "]掉线，等待重连……", ID: h.nextID(), Time: now()}, nil)
	if h.reconnectWait <= 0 {
		return
	}
	h.timers[userName] = time.AfterFunc(h.reconnectWait, func() { h.onReconnectTimeout(userName) })
}

// onReconnectTimeout: mid-game the seat goes auto-trustee and the game
// continues; if no real player is left online it's terminated as an escape;
// outside a game the seat is just released.
func (h *Hub) onReconnectTimeout(userName string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	d := h.lobby.DeskHolding(userName)
	if d == nil { // already reconnected or cleaned up
		return
	}
	hold, _ := d.Hold(userName)
	delete(h.timers, userName) // hold stays for reconnect or end-of-game cleanup

	if !d.GameInProgress() {
		d.RemoveHold(userName)
		h.releaseSeat(d, hold.PosID, nil)
		return
	}

	deskID, posID := d.DeskID, hold.PosID
	if h.anyRealPlayerOnline(d) {
		if seat := d.Seat(posID); seat != nil && !seat.IsBot && !seat.Trustee {
			seat.Trustee = true
			h.broadcastTrustee(d, posID)
		}
		h.broadCastRoom(EvUserMessageOut, deskID, userMessage{Type: "SYS", PosID: posID, Msg: "玩家[" + userName + "]断线超时，系统自动托管", ID: h.nextID(), Time: now()}, nil)
		h.logger.Warn("玩家重连超时，转为自动托管", "user", userName, "desk", deskID, "pos", posID)
		h.scheduleBotIfTurn(d)
		return
	}

	// No real players left: abort as an escape.
	h.logger.Warn("全部真实玩家断线超时，对局直接中止", "desk", deskID, "timeoutUser", userName)
	d.RemoveHold(userName)
	h.releaseSeat(d, posID, nil)
	h.terminateGame(d, posID, userName)
}

// terminateGame: end the game on an escape; broadcast, reset, persist.
func (h *Hub) terminateGame(d *table.Desk, escapePos int, escapee string) {
	h.recordGame(d, "escape")

	if d.Game != nil {
		d.UpdateOtherPos(escapePos, 1) // js behavior kept: empty seats set to 1 too
		h.broadCastRoom(EvPosStatusReset, d.DeskID, posStatusReset{Pos: d.Positions, State: 1}, nil)
		h.broadCastRoom(EvRoomStatusChg, d.DeskID, roomStatusChange{State: 0}, nil)
		h.broadCastRoom(EvForceExit, d.DeskID, forceExitPayload{Msg: "有玩家逃跑，游戏结束", PosID: escapePos}, nil)
	}
	d.ResetGame()
	h.clearDeskPending(d)
	h.clearBotSeats(d)
	h.clearTrustees(d)
	h.logger.Warn("对局因玩家逃跑终止", "desk", d.DeskID, "user", escapee)
}

func (h *Hub) clearDeskPending(d *table.Desk) {
	for _, hold := range d.ClearHolds() {
		if t := h.timers[hold.UserName]; t != nil {
			t.Stop()
			delete(h.timers, hold.UserName)
		}
		h.releaseSeat(d, hold.PosID, nil)
	}
}

// recordGame: must run before ResetGame; players come from the start-of-game
// seat snapshot, unaffected by later cleanup.
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
	t0, t1 := g.TeamScores() // winner's final score uses Result.Score on a normal finish (includes carried-off points)
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
	// No bidding phase: straight to the first leader + turn frame.
	dizhu := g.DizhuPosID()
	h.broadCastRoom(EvShowTopCard, deskID, showTopCard{TopCards: g.TopCards(), DizhuPosID: dizhu, Timeout: playTiming}, nil)
	h.broadCastRoom(EvCtxPlayChange, deskID, leadFrame(dizhu), nil)
	h.scheduleBotIfTurn(d)
}
