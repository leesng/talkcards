// Package hub: Go port of server.js GameServer. Handlers run serialized
// under one mutex; sends are async via per-session pumps; saves are async
// via the queue in persist.go.
package hub

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"talkcards/backend/internal/account"
	"talkcards/backend/internal/store"
	"talkcards/backend/internal/table"
	"talkcards/backend/internal/wssrv"
)

const (
	playTiming      = 60 // per-turn countdown (seconds)
	firstPlayTiming = 99 // first lead only: extra thinking time
	outQueue        = 512
)

// Hub: handlers run serialized under mu.
type Hub struct {
	mu       sync.Mutex
	sessions *sessionRegistry
	lobby    *table.Lobby
	msgSeq   int64
	uidSeq   int64

	st            *store.Store
	reconnectWait time.Duration          // reconnect grace period; <=0 = wait forever
	timers        map[string]*time.Timer // disconnect-hold countdowns; callbacks must hold mu
	botTimers     map[int]*time.Timer    // bot action timers; callbacks must hold mu
	botDelayMin   time.Duration
	botDelayMax   time.Duration
	saves         chan store.GameRecord
	saveWG        sync.WaitGroup // in-flight saves, drained by Flush
	logger        *slog.Logger
}

const (
	botDelayMinDefault = 1 * time.Second
	botDelayMaxDefault = 3 * time.Second
)

// SetBotDelay sets the bot action delay range (CLI injection; defaults if <=0, swaps if min>max).
func (h *Hub) SetBotDelay(minv, maxv time.Duration) {
	if minv <= 0 || maxv <= 0 {
		minv, maxv = botDelayMinDefault, botDelayMaxDefault
	}
	if minv > maxv {
		minv, maxv = maxv, minv
	}
	h.botDelayMin, h.botDelayMax = minv, maxv
}

// New creates the lobby; reconnectWait <= 0 means wait forever.
func New(st *store.Store, reconnectWait time.Duration, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Hub{
		sessions:      newSessionRegistry(),
		lobby:         table.New(),
		st:            st,
		reconnectWait: reconnectWait,
		timers:        make(map[string]*time.Timer),
		botTimers:     make(map[int]*time.Timer),
		botDelayMin:   botDelayMinDefault,
		botDelayMax:   botDelayMaxDefault,
		saves:         make(chan store.GameRecord, saveQueue),
		logger:        logger,
	}
	go h.saveLoop()
	return h
}

// Register mounts all event routes.
func (h *Hub) Register(srv *wssrv.Server) {
	on(srv, EvLogin, h.logger, h.onLogin)
	on(srv, EvSitdown, h.logger, h.onSitdown)
	on(srv, EvPlayCard, h.logger, h.onPlayCard)
	on(srv, EvUserMessage, h.logger, h.onUserMessage)
	on(srv, EvHistoryList, h.logger, h.onHistoryList)
	on(srv, EvHistoryDetail, h.logger, h.onHistoryDetail)
	noData(srv, EvQuickJoin, h.onQuickJoin)
	noData(srv, EvUnsitdown, h.onUnsitdown)
	noData(srv, EvPrepare, h.onPrepare)
	noData(srv, EvCancelPrepare, h.onCancelPrepare)
	on(srv, EvHostStartGame, h.logger, h.onHostStartGame) // payload may be omitted (fillBots)
	noData(srv, EvToggleTrustee, h.onToggleTrustee)
	on(srv, EvSpectate, h.logger, h.onSpectate) // watch an in-progress game
	srv.OnDisconnect(h.onDisconnect)
}

func on[T any](srv *wssrv.Server, ev string, logger *slog.Logger, fn func(wssrv.Conn, T)) {
	srv.OnEvent(ev, func(c wssrv.Conn, raw json.RawMessage) {
		var v T
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &v); err != nil {
				logger.Warn("事件载荷解析失败", "event", ev, "err", err)
				return
			}
		}
		fn(c, v)
	})
}

func noData(srv *wssrv.Server, ev string, fn func(wssrv.Conn)) {
	srv.OnEvent(ev, func(c wssrv.Conn, _ json.RawMessage) { fn(c) })
}

// clientInRoom: the logged-in seated session and its desk. Caller must hold the lock.
func (h *Hub) clientInRoom(conn wssrv.Conn) (*Session, *table.Desk) {
	c := h.sessions.find(conn)
	if c == nil || c.deskID == -1 {
		return nil, nil
	}
	return c, h.lobby.Desk(c.deskID)
}

// seatedClient: like clientInRoom but nil for spectators (deskID set, posID
// -1) — spectator input must never touch game/seat state.
func (h *Hub) seatedClient(conn wssrv.Conn) (*Session, *table.Desk) {
	c, d := h.clientInRoom(conn)
	if c == nil || c.posID == -1 {
		return nil, nil
	}
	return c, d
}

// onSpectate: watch an in-progress game (entered from the lobby by clicking
// any seat of a playing desk). Spectators receive every room frame except
// hand faces — GAME_START is redacted to hand sizes only.
func (h *Hub) onSpectate(s wssrv.Conn, data sitdownReq) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c := h.sessions.find(s)
	if c == nil {
		return
	}
	if c.deskID != -1 && c.posID != -1 {
		// 入座（含对局中）玩家一律不能观战：对局中座位不可释放转观战
		h.emit(c, EvMessage, msgPayload{Msg: "入座中不能观战，请先退出房间"})
		return
	}
	d := h.lobby.Desk(data.DeskID)
	if d == nil || !d.GameInProgress() {
		h.emit(c, EvSpectateError, loginFailPayload{Msg: "该桌没有进行中的对局"})
		return
	}
	// Switching desks while already spectating: leave the old one quietly.
	if c.deskID != -1 && c.deskID != d.DeskID {
		h.broadcastSpectatorLeave(c.UserName(), c.deskID)
	}
	c.deskID, c.posID = d.DeskID, -1
	h.emit(c, EvSpectateSuccess, spectateSuccess{DeskID: d.DeskID, PosInfo: d.Positions, HostPosID: d.HostPosID})
	h.replayGameFrames(c, d, true)
	h.broadCastRoom(EvUserMessageOut, d.DeskID, userMessage{Type: "SYS", PosID: -1, Msg: "玩家[" + c.UserName() + "]进入观战", ID: h.nextID(), Time: now()}, c.conn)
	h.logger.Info("玩家进入观战", "user", c.UserName(), "desk", d.DeskID)
}

func (h *Hub) broadcastSpectatorLeave(name string, deskID int) {
	h.broadCastRoom(EvUserMessageOut, deskID, userMessage{Type: "SYS", PosID: -1, Msg: "玩家[" + name + "]退出观战", ID: h.nextID(), Time: now()}, nil)
}

func (h *Hub) onLogin(s wssrv.Conn, name string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if err := account.ValidateName(name); err != nil {
		go s.Emit(EvLoginFail, loginFailPayload{Msg: err.Error()})
		return
	}
	if h.sessions.byName(name) != nil {
		go s.Emit(EvLoginFail, loginFailPayload{Msg: "该用户名已存在"})
		return
	}
	c := &Session{conn: s, person: account.New(h.nextUID(), name), deskID: -1, posID: -1, out: make(chan func(), outQueue)}
	go c.pump()
	h.sessions.add(c)
	h.emit(c, EvLoginSuccess, h.lobby.Desks)
	h.logger.Info("有客户端登录", "user", name)

	// Reconnect: auto sit-back if this username has a held seat.
	if d := h.lobby.DeskHolding(name); d != nil {
		h.resumeClient(c, d)
	}
}

func (h *Hub) onHistoryList(s wssrv.Conn, data historyListReq) {
	// Release the lock for the DB query, re-lock to emit.
	h.mu.Lock()
	c := h.sessions.find(s)
	if c == nil {
		h.mu.Unlock()
		return
	}
	userName := c.UserName()
	h.mu.Unlock()

	const pageSize = 20
	if data.Page < 1 {
		data.Page = 1
	}
	list, total, err := h.st.HistoryList(userName, pageSize, (data.Page-1)*pageSize)

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessions.find(s) == nil { // disconnected during the query: drop the response
		return
	}
	h.emit(c, EvHistoryListOut, historyListOut{List: list, Total: total, Page: data.Page})
	if err != nil {
		h.logger.Error("查询战绩失败", "user", userName, "err", err)
		h.emit(c, EvHistoryFail, loginFailPayload{Msg: "查询战绩失败"})
	}
}

func (h *Hub) onHistoryDetail(s wssrv.Conn, data historyDetailReq) {
	// Release the lock for the DB query, re-lock to emit.
	h.mu.Lock()
	c := h.sessions.find(s)
	if c == nil {
		h.mu.Unlock()
		return
	}
	userName := c.UserName()
	h.mu.Unlock()

	rec, ok, err := h.st.GameDetail(data.GameID)
	if err != nil {
		h.logger.Error("查询对局详情失败", "user", userName, "gameID", data.GameID, "err", err)
	}
	// Only own games visible; missing and forbidden share one message.
	joined := false
	for _, p := range rec.Players {
		if p.UserName == userName {
			joined = true
			break
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessions.find(s) == nil {
		return
	}
	if err != nil || !ok || !joined {
		h.emit(c, EvHistoryFail, loginFailPayload{Msg: "对局记录不存在"})
		return
	}
	h.emit(c, EvHistoryDetailOut, historyDetailOut{
		GameID:     rec.GameID,
		DeskID:     rec.DeskID,
		StartedAt:  rec.StartedAt.Format("2006-01-02 15:04:05"),
		EndedAt:    rec.EndedAt.Format("2006-01-02 15:04:05"),
		EndReason:  rec.EndReason,
		Winner:     rec.Winner,
		Team0Score: rec.Team0Score,
		Team1Score: rec.Team1Score,
		Players:    rec.Players,
	})
}

func (h *Hub) onQuickJoin(s wssrv.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c := h.sessions.find(s)
	if c == nil {
		return
	}
	if deskID, posID, ok := h.lobby.QuickJoin(); ok {
		h.emit(c, EvQuickJoinOut, quickJoinResult{DeskID: deskID, PosID: posID, Success: true})
	} else {
		h.emit(c, EvQuickJoinOut, quickJoinFail{})
	}
}

func (h *Hub) onSitdown(s wssrv.Conn, data sitdownReq) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c := h.sessions.find(s)
	if c == nil {
		return
	}
	if c.deskID != -1 && c.posID == -1 {
		h.emit(c, EvMessage, msgPayload{Msg: "观战中，请先退出观战再入座"})
		return
	}
	d := h.lobby.Desk(data.DeskID)
	if d == nil || !d.IsEmpty(data.PosID) {
		h.emit(c, EvSitdownError, loginFailPayload{Msg: "该位置已有人"})
		h.emit(c, EvRefreshList, h.lobby.Desks)
		return
	}

	h.logger.Info("用户进入房间", "user", c.UserName(), "desk", data.DeskID, "pos", data.PosID)
	name := c.UserName()
	d.UpdatePos(data.PosID, 1, &name)
	c.deskID = data.DeskID
	c.posID = data.PosID

	h.emit(c, EvSitdownSuccess, sitdownSuccess{DeskID: data.DeskID, PosID: data.PosID, PosInfo: d.Positions, HostPosID: d.HostPosID})
	if d.AssignHost(data.PosID) {
		h.broadCastRoom(EvHostChange, data.DeskID, hostChange{PosID: data.PosID, UserName: name}, nil)
	}
	h.broadCastHouse(EvStatusChange, houseStatusChange{DeskID: data.DeskID, PosID: data.PosID, State: 1, UserName: c.UserName()})
	h.broadCastRoom(EvPosStatusChange, data.DeskID, posStatusChange{PosID: data.PosID, State: 1, UserName: c.UserName()}, s)

	h.emit(c, EvUserMessageOut, userMessage{Type: "SYS", PosID: data.PosID, Msg: "欢迎您加入本房间，祝您游戏愉快！", ID: h.nextID(), Time: now()})
	h.broadCastRoom(EvUserMessageOut, data.DeskID, userMessage{Type: "SYS", PosID: data.PosID, Msg: "玩家[" + c.UserName() + "]进入房间", ID: h.nextID(), Time: now()}, s)
}

func (h *Hub) onUnsitdown(s wssrv.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c, _ := h.clientInRoom(s)
	if c == nil {
		return
	}
	if c.posID == -1 { // spectator stops watching: no seat to release
		deskID, userName := c.deskID, c.UserName()
		c.deskID, c.posID = -1, -1
		h.emit(c, EvUnsitSuccess, h.lobby.Desks)
		h.broadcastSpectatorLeave(userName, deskID)
		h.logger.Info("玩家退出观战", "user", userName, "desk", deskID)
		return
	}
	deskID, posID, userName := c.deskID, c.posID, c.UserName()
	h.exitRoom(c)
	h.emit(c, EvUnsitSuccess, h.lobby.Desks)
	// Bug-for-bug: js did not exclude the initiator here; keep that.
	h.broadCastRoom(EvUserMessageOut, deskID, userMessage{Type: "SYS", PosID: posID, Msg: "玩家[" + userName + "]退出房间", ID: h.nextID(), Time: now()}, nil)
}

func (h *Hub) onPrepare(s wssrv.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c, d := h.seatedClient(s)
	if c == nil || d == nil {
		return
	}
	d.UpdatePos(c.posID, 2, nil)
	d.SetState(1)
	h.emitNoData(c, EvPrepareSuccess)
	h.broadCastRoom(EvPosStatusChange, c.deskID, posStatusChange{PosID: c.posID, State: 2}, s)

	// Host mode: no auto-start on all-ready; the host decides.
	if d.AllPrepared() && !d.GameInProgress() {
		if host := d.Seat(d.HostPosID); host != nil {
			msg := "已入座玩家全部准备完毕，等待主持人[" + host.UserName + "]开牌"
			if n := len(d.EmptySeats()); n > 0 {
				msg += "（还有 " + itoa(n) + " 个空位，可开牌用机器人补位）"
			}
			h.broadCastRoom(EvUserMessageOut, c.deskID, userMessage{Type: "SYS", PosID: d.HostPosID, Msg: msg, ID: h.nextID(), Time: now()}, nil)
		}
	}
}

// onHostStartGame: host-only start; fillBots=true fills empty seats with bots.
// Host rights persist across a disconnect (held seat); leaving hands them over.
func (h *Hub) onHostStartGame(s wssrv.Conn, data hostStartGameReq) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c, d := h.seatedClient(s)
	if c == nil || d == nil {
		return
	}
	if d.GameInProgress() {
		h.emit(c, EvMessage, msgPayload{Msg: "对局正在进行中"})
		return
	}
	if d.HostPosID != c.posID {
		h.emit(c, EvMessage, msgPayload{Msg: "只有主持人才能开牌"})
		return
	}
	if !d.AllPrepared() {
		h.emit(c, EvMessage, msgPayload{Msg: "还有玩家未准备，不能开牌"})
		return
	}
	if empty := d.EmptySeats(); len(empty) > 0 && !data.FillBots {
		h.emit(c, EvMessage, msgPayload{Msg: "还有 " + itoa(len(empty)) + " 个空位，开牌时请确认是否填充为机器人"})
		return
	}
	if filled := d.FillBots(); len(filled) > 0 {
		h.logger.Info("主持人填充机器人开局", "user", c.UserName(), "desk", c.deskID, "bots", len(filled))
		for _, pos := range filled {
			seat := d.Seat(pos)
			h.broadCastRoom(EvPosStatusChange, c.deskID, posStatusChange{PosID: pos, State: 2, UserName: seat.UserName, IsBot: true}, nil)
		}
		h.broadCastRoom(EvUserMessageOut, c.deskID, userMessage{Type: "SYS", PosID: d.HostPosID, Msg: "主持人将 " + itoa(len(filled)) + " 个空位填充为机器人，游戏开始", ID: h.nextID(), Time: now()}, nil)
	}
	h.logger.Info("主持人开牌", "user", c.UserName(), "desk", c.deskID, "pos", c.posID)
	h.startGame(c.deskID)
}

// onToggleTrustee: in-game only; bot seats excluded. While trusted out, the
// server plays this seat with the bot strategy.
func (h *Hub) onToggleTrustee(s wssrv.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c, d := h.seatedClient(s)
	if c == nil || d == nil {
		return
	}
	if !d.GameInProgress() {
		h.emit(c, EvMessage, msgPayload{Msg: "只有对局进行中才能托管"})
		return
	}
	seat := d.Seat(c.posID)
	if seat == nil || seat.State == 0 || seat.IsBot {
		return
	}
	seat.Trustee = !seat.Trustee
	on := seat.Trustee
	h.logger.Info("玩家切换托管", "user", c.UserName(), "desk", c.deskID, "pos", c.posID, "trustee", on)
	h.broadcastTrustee(d, c.posID)
	msg := "玩家[" + c.UserName() + "]关闭托管，恢复手动操作"
	if on {
		msg = "玩家[" + c.UserName() + "]开启托管，由系统代打"
	}
	h.broadCastRoom(EvUserMessageOut, d.DeskID, userMessage{Type: "SYS", PosID: c.posID, Msg: msg, ID: h.nextID(), Time: now()}, nil)
	if on {
		h.scheduleBotIfTurn(d) // may already be this seat's turn
	}
}

func (h *Hub) broadcastTrustee(d *table.Desk, posID int) {
	seat := d.Seat(posID)
	h.broadCastRoom(EvTrusteeChange, d.DeskID, trusteeChange{PosID: posID, Trustee: seat.Trustee}, nil)
}

// anyRealPlayerOnline: any online human (not held, not bot). If none, a timed-out game is terminated.
func (h *Hub) anyRealPlayerOnline(d *table.Desk) bool {
	for i := range d.Positions {
		s := &d.Positions[i]
		if s.State == 0 || s.IsBot {
			continue
		}
		if _, held := d.Hold(s.UserName); held {
			continue // on disconnect hold (incl. timed-out auto-trustee)
		}
		if h.sessions.byName(s.UserName) != nil {
			return true
		}
	}
	return false
}

func (h *Hub) onCancelPrepare(s wssrv.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c, d := h.seatedClient(s)
	if c == nil || d == nil {
		return
	}
	if seat := d.Seat(c.posID); seat == nil || seat.State != 2 {
		return
	}
	if d.GameInProgress() {
		return
	}
	d.UpdatePos(c.posID, 1, nil)
	h.emitNoData(c, EvCancelPrepareSuccess)
	h.broadCastRoom(EvPosStatusChange, c.deskID, posStatusChange{PosID: c.posID, State: 1}, s)
}

func (h *Hub) onUserMessage(s wssrv.Conn, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c, _ := h.seatedClient(s)
	if c == nil {
		return
	}
	h.broadCastRoom(EvUserMessageOut, c.deskID, userMessage{Type: "USER", PosID: c.posID, Msg: msg, ID: h.nextID(), Time: now()}, nil)
}

func (h *Hub) onDisconnect(s wssrv.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c := h.sessions.find(s)
	if c == nil {
		return
	}
	userName := c.UserName()
	deskID, posID := c.deskID, c.posID
	d := h.lobby.Desk(deskID)
	if deskID != -1 && posID == -1 {
		// Spectator disconnect: no seat, no hold, never terminates the game.
		h.sessions.remove(s)
		h.broadcastSpectatorLeave(userName, deskID)
		h.logger.Info("观战者断开连接", "user", userName, "desk", deskID)
		return
	}
	if deskID != -1 && d != nil && d.GameInProgress() {
		// Mid-game disconnect: hold seat+game for reconnect; host rights unchanged.
		h.sessions.remove(s)
		h.holdSeatForReconnect(d, userName, posID)
		h.logger.Warn("玩家游戏中掉线，保留座位等待重连", "user", userName, "desk", deskID, "pos", posID)
		return
	}
	if deskID != -1 && d != nil {
		h.exitRoom(c)
	}
	h.sessions.remove(s)
	if deskID != -1 {
		h.broadCastRoom(EvUserMessageOut, deskID, userMessage{Type: "SYS", PosID: posID, Msg: "玩家[" + userName + "]退出房间", ID: h.nextID(), Time: now()}, nil)
	}
	h.logger.Info("客户端断开连接", "user", userName)
}

// releaseSeat frees a seat and broadcasts (leave / timeout / game-end shared).
func (h *Hub) releaseSeat(d *table.Desk, posID int, exclude wssrv.Conn) {
	if d == nil {
		return
	}
	d.UpdatePos(posID, 0, ptrStr(""))
	// js bug kept in check: it passed posId as the desk state; frontend never
	// reads desk.state, so we write the correct idle 0.
	d.SetState(0)
	if newHost, changed := d.TransferHostFrom(posID); changed {
		name := ""
		if newHost != -1 {
			name = d.Seat(newHost).UserName
		}
		h.broadCastRoom(EvHostChange, d.DeskID, hostChange{PosID: newHost, UserName: name}, nil)
	}
	h.broadCastRoom(EvPosStatusChange, d.DeskID, posStatusChange{PosID: posID, State: 0}, exclude)
	h.broadCastHouse(EvStatusChange, houseStatusChange{DeskID: d.DeskID, PosID: posID, State: 0, UserName: ""})
}

func (h *Hub) exitRoom(c *Session) {
	deskID, posID := c.deskID, c.posID
	d := h.lobby.Desk(deskID)
	h.logger.Info("用户退出房间", "user", c.UserName(), "desk", deskID, "pos", posID)

	h.releaseSeat(d, posID, c.conn)
	c.deskID, c.posID = -1, -1

	if d != nil && d.GameInProgress() {
		h.terminateGame(d, posID, c.UserName())
	}
}
