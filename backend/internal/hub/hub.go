// Package hub 移植自 server.js 的 GameServer：20 桌 × 8 座的大厅/房间状态、
// 事件路由与广播。所有事件处理在单一互斥锁下串行执行，
// 语义对齐 Node 单线程事件循环。
//
// 分层：table 持桌位/对局聚合，account 持用户身份，game 持牌局状态机，
// store 持战绩落库；hub 只做 Session 管理、事件路由（router）、
// wire 载荷翻译（translator）与广播（broadcast）。table/game 不依赖 wssrv。
//
// 发送模型：每个客户端持有一条串行发送队列（pump goroutine），
// handler 持锁期间只做载荷快照（json.Marshal）与入队，实际发送由 pump
// 异步串行执行；连接层 Emit 本身亦非阻塞（wssrv 内部另有发送缓冲），
// 双层保证持锁路径绝不因写死连接而卡死整个 hub。
//
// 持久化模型：落库经 persist.go 的异步写队列串行执行，避免 SQLite 写事务
// 阻塞全局锁；战绩查询在锁外执行（见 onHistoryList/onHistoryDetail）。
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
	playTiming = 45 // 出牌/叫分倒计时（秒），与 js 一致
	outQueue   = 512
)

// Hub 所有事件处理在 mu 下串行执行
type Hub struct {
	mu       sync.Mutex
	sessions *sessionRegistry
	lobby    *table.Lobby
	msgSeq   int64
	uidSeq   int64

	st            *store.Store           // 战绩历史持久化
	reconnectWait time.Duration          // 游戏中断线后的重连等待时限；<=0 表示无限等待
	timers        map[string]*time.Timer // 断线保留倒计时（按用户名；回调须持 mu）
	botTimers     map[int]*time.Timer    // 机器人行动定时器（按桌；回调须持 mu）
	botDelayMin   time.Duration          // 机器人行动随机延迟下限（默认 1s）
	botDelayMax   time.Duration          // 机器人行动随机延迟上限（默认 3s）；测试可调小加速
	saves         chan store.GameRecord  // 异步落库队列（见 persist.go）
	saveWG        sync.WaitGroup         // 在途落库计数，供 Flush 排空
	logger        *slog.Logger
}

// 机器人默认行动延迟区间：真人视角下机器人"思考" 1-3 秒随机再出牌
const (
	botDelayMinDefault = 1 * time.Second
	botDelayMaxDefault = 3 * time.Second
)

// SetBotDelay 设置机器人行动随机延迟区间（供 CLI 注入；min>max 时交换，<=0 取默认）
func (h *Hub) SetBotDelay(minv, maxv time.Duration) {
	if minv <= 0 || maxv <= 0 {
		minv, maxv = botDelayMinDefault, botDelayMaxDefault
	}
	if minv > maxv {
		minv, maxv = maxv, minv
	}
	h.botDelayMin, h.botDelayMax = minv, maxv
}

// New 创建大厅（20 桌 × 8 座）；reconnectWait <= 0 表示无限等待重连。
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

// Register 在 wssrv 服务上挂载全部事件路由
func (h *Hub) Register(srv *wssrv.Server) {
	on(srv, EvLogin, h.logger, h.onLogin)
	on(srv, EvSitdown, h.logger, h.onSitdown)
	on(srv, EvCallScore, h.logger, h.onCallScore)
	on(srv, EvPlayCard, h.logger, h.onPlayCard)
	on(srv, EvUserMessage, h.logger, h.onUserMessage)
	on(srv, EvHistoryList, h.logger, h.onHistoryList)
	on(srv, EvHistoryDetail, h.logger, h.onHistoryDetail)
	noData(srv, EvQuickJoin, h.onQuickJoin)
	noData(srv, EvUnsitdown, h.onUnsitdown)
	noData(srv, EvPrepare, h.onPrepare)
	noData(srv, EvCancelPrepare, h.onCancelPrepare)
	on(srv, EvHostStartGame, h.logger, h.onHostStartGame) // 载荷可缺省（fillBots）
	srv.OnDisconnect(h.onDisconnect)
}

// on 注册带载荷事件
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

// noData 注册无载荷事件
func noData(srv *wssrv.Server, ev string, fn func(wssrv.Conn)) {
	srv.OnEvent(ev, func(c wssrv.Conn, _ json.RawMessage) { fn(c) })
}

// ---------- 会话查询助手（调用方持锁） ----------

// clientInRoom 取已登录且已入座的会话与所在桌；未登录/在大厅时返回 nil
func (h *Hub) clientInRoom(conn wssrv.Conn) (*Session, *table.Desk) {
	c := h.sessions.find(conn)
	if c == nil || c.deskID == -1 {
		return nil, nil
	}
	return c, h.lobby.Desk(c.deskID)
}

// ---------- 事件处理 ----------

// onLogin 用户名即唯一身份：非空、不超长、不与在线重名即可登录。
// 载荷为纯用户名字符串（json 字符串）。
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

	// 断线重连：该用户名有保留中的座位则自动坐回
	if d := h.lobby.DeskHolding(name); d != nil {
		h.resumeClient(c, d)
	}
}

func (h *Hub) onHistoryList(s wssrv.Conn, data historyListReq) {
	// 取用户名后放锁查库，避免磁盘 I/O 堵塞全局锁
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
	if h.sessions.find(s) == nil { // 查询期间已断开：丢弃响应
		return
	}
	h.emit(c, EvHistoryListOut, historyListOut{List: list, Total: total, Page: data.Page})
	if err != nil {
		h.logger.Error("查询战绩失败", "user", userName, "err", err)
		h.emit(c, EvHistoryFail, loginFailPayload{Msg: "查询战绩失败"})
	}
}

func (h *Hub) onHistoryDetail(s wssrv.Conn, data historyDetailReq) {
	// 取用户名后放锁查库，避免磁盘 I/O 堵塞全局锁
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
	// 只允许查看自己参与过的对局；不存在与无权限对前端统一为同一文案
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
	d := h.lobby.Desk(data.DeskID)
	if d == nil || !d.IsEmpty(data.PosID) {
		h.emit(c, EvSitdownError, loginFailPayload{Msg: "该位置已有人"})
		// 客户端数据可能不同步，推送一次全量桌数据
		h.emit(c, EvRefreshList, h.lobby.Desks)
		return
	}

	h.logger.Info("用户进入房间", "user", c.UserName(), "desk", data.DeskID, "pos", data.PosID)
	name := c.UserName()
	d.UpdatePos(data.PosID, 1, &name)
	c.deskID = data.DeskID
	c.posID = data.PosID

	h.emit(c, EvSitdownSuccess, sitdownSuccess{DeskID: data.DeskID, PosID: data.PosID, PosInfo: d.Positions, HostPosID: d.HostPosID})
	// 空桌首坐获得主持权
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
	deskID, posID, userName := c.deskID, c.posID, c.UserName()
	h.exitRoom(c)
	h.emit(c, EvUnsitSuccess, h.lobby.Desks)
	// js 中该广播不排除发起者（broadCastRoom 未传 socket），保持一致
	h.broadCastRoom(EvUserMessageOut, deskID, userMessage{Type: "SYS", PosID: posID, Msg: "玩家[" + userName + "]退出房间", ID: h.nextID(), Time: now()}, nil)
}

func (h *Hub) onPrepare(s wssrv.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c, d := h.clientInRoom(s)
	if c == nil || d == nil {
		return
	}
	d.UpdatePos(c.posID, 2, nil)
	d.SetState(1)
	h.emitNoData(c, EvPrepareSuccess)
	h.broadCastRoom(EvPosStatusChange, c.deskID, posStatusChange{PosID: c.posID, State: 2}, s)

	// 主持模式：已入座玩家全部准备好也不自动开牌，由主持人决定何时开牌（HOST_START_GAME）
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

// onHostStartGame 主持人开牌：仅主持人、已入座玩家全准备且对局未进行时生效。
// 载荷 fillBots=true 时把空位填充为机器人开局（人机模式/单人练习）；
// 有空位但未确认填充则拒绝开牌。
// 主持人断线（座位保留）期间主持权不变；真正离桌则由 releaseSeat 顺延给下一人。
func (h *Hub) onHostStartGame(s wssrv.Conn, data hostStartGameReq) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c, d := h.clientInRoom(s)
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

// onCancelPrepare 取消准备：仅已准备且对局未开始（叫分/出牌中不可取消）时生效
func (h *Hub) onCancelPrepare(s wssrv.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c, d := h.clientInRoom(s)
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

	c, _ := h.clientInRoom(s)
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
	if deskID != -1 && d != nil && d.GameInProgress() {
		// 游戏进行中断线：保留座位与对局，限时等待重连
		// （座位保留期间不释放，主持权不变；重连后主持人标识依旧）
		h.sessions.remove(s)
		h.holdSeatForReconnect(d, userName, posID)
		h.logger.Warn("玩家游戏中掉线，保留座位等待重连", "user", userName, "desk", deskID, "pos", posID)
		return
	}
	if deskID != -1 && d != nil {
		h.exitRoom(c) // js: 仅入座客户端走退出流程
	}
	h.sessions.remove(s)
	if deskID != -1 {
		h.broadCastRoom(EvUserMessageOut, deskID, userMessage{Type: "SYS", PosID: posID, Msg: "玩家[" + userName + "]退出房间", ID: h.nextID(), Time: now()}, nil)
	}
	h.logger.Info("客户端断开连接", "user", userName)
}

// releaseSeat 释放座位并广播状态（退房/掉线超时/对局终止清理共用）。
// exclude 传发起者连接可让其不收到 POS_STATUS_CHANGE（js 退房路径语义）。
func (h *Hub) releaseSeat(d *table.Desk, posID int, exclude wssrv.Conn) {
	if d == nil {
		return
	}
	d.UpdatePos(posID, 0, ptrStr(""))
	// 旧 js 误把 posId 当房间状态写入 updateRoomStatus(deskId, posId, 0)；
	// 已查证前端不消费 desk.state，此处修正为语义正确的等待态 0
	d.SetState(0)
	// 主持人离桌：主持权自动顺延到下一个有人的座位；全桌无人则复位为 -1
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

// exitRoom 退出/掉线超时的公共处理：释放座位、广播、逃跑终止。
// 不修改 session 记录本身（js 中 updateClientState 由各事件自行处理）。
func (h *Hub) exitRoom(c *Session) {
	deskID, posID := c.deskID, c.posID
	d := h.lobby.Desk(deskID)
	h.logger.Info("用户退出房间", "user", c.UserName(), "desk", deskID, "pos", posID)

	h.releaseSeat(d, posID, c.conn)
	c.deskID, c.posID = -1, -1

	// 游戏进行中有人逃跑：终止本局
	if d != nil && d.GameInProgress() {
		h.terminateGame(d, posID, c.UserName())
	}
}
