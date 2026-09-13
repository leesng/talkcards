// Package table 大厅桌位聚合：20 桌 × 8 座的生命周期（入座/准备/开局快照）、
// 对局挂载与断线保留。不依赖通信层（wssrv）；重连倒计时器留在 hub
// （回调须持 hub 锁），本包只存状态与截止时间。
package table

import (
	"sort"
	"strconv"
	"time"

	"talkcards/backend/internal/card"
	"talkcards/backend/internal/game"
)

// SeatCount 每桌座位数
const SeatCount = 8

// DeskCount 大厅桌数
const DeskCount = 20

// Seat 座位（LOGIN_SUCCESS / REFRESH_LIST / POS_STATUS_RESET 等 wire 载荷直接
// 序列化本结构，字段名与前端消费逐字一致）
type Seat struct {
	PosID    int    `json:"posId"`
	State    int    `json:"state"` // 0没人 1未准备 2已准备
	UserName string `json:"userName"`
	IsBot    bool   `json:"isBot"` // 人机模式填充的机器人座位（主持人开牌时填充，终局清理）
	// Trustee 托管中（真人座位，托管期间由服务器按机器人策略代打）：
	// 对局中玩家手动开启/关闭，或断线超时自动开启；重连/终局时清除。
	Trustee bool `json:"trustee,omitempty"`
}

// Hold 游戏中断线玩家的保留记录。
// 重连倒计时的权威状态在 hub 的定时器表（table 只存座位归属，不持有计时器）。
type Hold struct {
	UserName string
	PosID    int
}

// PlaySnapshot 最近一次出牌广播的领域快照，hub 据此重建 wire 帧做重连重放
type PlaySnapshot struct {
	IsPass  bool
	Key     int    // 压牌牌型 Rank（IsPass 时无意义）
	Type    string // 牌型名（IsPass 时为空）
	Cards   []card.Card
	PosID   int // 出牌者
	SumFeng [SeatCount]int
	TmpFeng int
	Next    int // 重放时的轮转者
}

// Desk 一桌：座位生命周期 + 对局挂载 + 断线保留。
// 导出的内部状态字段（Game/StartedAt/Players/LastPlay/Holds）均标 json:"-"，
// 大厅列表 wire 载荷只透出 DeskID/State/Positions。
type Desk struct {
	DeskID    int    `json:"deskId"`
	State     int    `json:"state"` // 房间状态（仅 wire 透出；前端未消费）
	Positions []Seat `json:"positions"`

	Game      *game.Game        `json:"-"` // 本桌对局；nil=未开局
	StartedAt time.Time         `json:"-"` // 本局开始时间（落库用）
	Players   [SeatCount]string `json:"-"` // 开局时玩家快照（落库用，不受中途座位变动影响）
	LastPlay  *PlaySnapshot     `json:"-"` // 最近一次动作快照（出牌或过牌，断线重连重放）
	// LastValidPlay 最近一次"有效出牌"（非过牌）快照：过牌不覆盖它。
	// 上一手是过牌时，重连者只靠 LastPlay 看不到桌面上还有哪手牌要压，
	// 故重连时先补发本帧再补发 LastPlay。
	LastValidPlay *PlaySnapshot `json:"-"`
	Holds     []Hold            `json:"-"` // 断线保留记录（按用户名）

	HostPosID int `json:"-"` // 主持人座位号（第一个入座者）；主持人离桌时自动顺延给下一个有人的座位
}

// Lobby 大厅
type Lobby struct {
	Desks []*Desk
}

// New 创建大厅（DeskCount 桌 × SeatCount 座，桌号从 1 起）
func New() *Lobby {
	l := &Lobby{Desks: make([]*Desk, DeskCount)}
	for i := range l.Desks {
		pos := make([]Seat, SeatCount)
		for j := range pos {
			pos[j] = Seat{PosID: j}
		}
		l.Desks[i] = &Desk{DeskID: i + 1, Positions: pos, HostPosID: -1}
	}
	return l
}

// Desk 按桌号查找（DeskID 即 1 起的下标，O(1)）
func (l *Lobby) Desk(deskID int) *Desk {
	if deskID < 1 || deskID > len(l.Desks) {
		return nil
	}
	return l.Desks[deskID-1]
}

// DeskHolding 查某用户断线保留所在的桌；无保留返回 nil
func (l *Lobby) DeskHolding(userName string) *Desk {
	for _, d := range l.Desks {
		if _, ok := d.Hold(userName); ok {
			return d
		}
	}
	return nil
}

// QuickJoin 挑空位最少的桌（即人最多的桌）返回其首个空位；稳定排序对齐 js。
func (l *Lobby) QuickJoin() (deskID, posID int, ok bool) {
	type candidate struct {
		deskID int
		free   []int
	}
	var ret []candidate
	for _, desk := range l.Desks {
		c := candidate{deskID: desk.DeskID}
		for _, pos := range desk.Positions {
			if pos.State == 0 {
				c.free = append(c.free, pos.PosID)
			}
		}
		if len(c.free) == 0 {
			continue // 满员桌不可加入（occupied <= 7 的等价表述）
		}
		ret = append(ret, c)
	}
	sort.SliceStable(ret, func(a, b int) bool { return len(ret[a].free) < len(ret[b].free) })
	if len(ret) == 0 {
		return 0, 0, false
	}
	return ret[0].deskID, ret[0].free[0], true
}

// Seat 按座位号查找（座位号即下标，O(1)）
func (d *Desk) Seat(posID int) *Seat {
	if posID < 0 || posID >= len(d.Positions) {
		return nil
	}
	return &d.Positions[posID]
}

// IsEmpty 座位是否空闲
func (d *Desk) IsEmpty(posID int) bool {
	s := d.Seat(posID)
	return s != nil && s.State == 0
}

// UpdatePos 更新座位状态；userName 传 nil 表示不修改（对应 js 传 undefined）
func (d *Desk) UpdatePos(posID, state int, userName *string) {
	if s := d.Seat(posID); s != nil {
		s.State = state
		if userName != nil {
			s.UserName = *userName
		}
	}
}

// UpdateOtherPos 更新除 posID 外全部座位状态；空座位也会被置为 1（js 照抄）
func (d *Desk) UpdateOtherPos(posID, state int) {
	for i := range d.Positions {
		if d.Positions[i].PosID != posID {
			d.Positions[i].State = state
		}
	}
}

// SetState 更新房间状态
func (d *Desk) SetState(state int) { d.State = state }

// AllPrepared 所有已入座玩家均已准备（空座不阻塞；人未齐时可由主持人填充机器人开局）
func (d *Desk) AllPrepared() bool {
	for i := range d.Positions {
		if d.Positions[i].State != 0 && d.Positions[i].State != 2 {
			return false
		}
	}
	return true
}

// EmptySeats 空座位号列表（升序），主持人开牌时据此判断是否需要填充机器人
func (d *Desk) EmptySeats() []int {
	var empty []int
	for i := range d.Positions {
		if d.Positions[i].State == 0 {
			empty = append(empty, d.Positions[i].PosID)
		}
	}
	return empty
}

// FillBots 把全部空位填充为机器人（视为已准备），返回填充的座位号列表（升序）。
// 机器人名字按填充顺序编号，仅对局期间存在，终局由 ClearBots 清理。
func (d *Desk) FillBots() []int {
	var filled []int
	for i := range d.Positions {
		if d.Positions[i].State == 0 {
			d.Positions[i].State = 2
			d.Positions[i].UserName = "机器人#" + strconv.Itoa(d.DeskID) + "-" + strconv.Itoa(d.Positions[i].PosID)
			d.Positions[i].IsBot = true
			filled = append(filled, d.Positions[i].PosID)
		}
	}
	return filled
}

// ClearBots 清理全部机器人座位（终局调用）：复位为空座，返回被清理的座位号列表
func (d *Desk) ClearBots() []int {
	var cleared []int
	for i := range d.Positions {
		if d.Positions[i].IsBot {
			d.Positions[i] = Seat{PosID: d.Positions[i].PosID}
			cleared = append(cleared, d.Positions[i].PosID)
		}
	}
	return cleared
}

// AssignHost 入座时调用：本桌尚无主持人（空桌首坐）则授予 posID 主持权，返回是否授予。
// 断线（座位保留）不影响主持权；仅当主持人真正离桌（座位释放）时经 TransferHostFrom 顺延。
func (d *Desk) AssignHost(posID int) bool {
	if d.HostPosID == -1 {
		d.HostPosID = posID
		return true
	}
	return false
}

// TransferHostFrom 主持人离桌（座位释放）时调用：若 posID 正是主持人，
// 从其下一座起顺时针找第一个有人的座位接任；全桌无人则复位为 -1。
// 返回新主持人座位号（-1=复位）及是否发生变化。非主持人离桌不产生变化。
func (d *Desk) TransferHostFrom(posID int) (int, bool) {
	if d.HostPosID != posID {
		return d.HostPosID, false
	}
	for i := 1; i <= SeatCount; i++ {
		p := (posID + i) % SeatCount
		if d.Positions[p].State != 0 && !d.Positions[p].IsBot { // 机器人不接任主持人
			d.HostPosID = p
			return p, true
		}
	}
	d.HostPosID = -1
	return -1, true
}

// GameInProgress 对局进行中（出牌阶段）
func (d *Desk) GameInProgress() bool {
	return d.Game != nil && d.Game.Phase() == game.PhasePlaying
}

// PlayerSnapshot 当前已入座玩家的名字快照（开局落库用；此后中途座位变动不影响本局记录）
func (d *Desk) PlayerSnapshot() [SeatCount]string {
	var names [SeatCount]string
	for i := range d.Positions {
		names[i] = d.Positions[i].UserName
	}
	return names
}

// ResetGame 对局结束/终止后的清理：对局复位、开局痕迹清空
func (d *Desk) ResetGame() {
	if d.Game != nil {
		d.Game.Init()
	}
	d.StartedAt = time.Time{}
	d.LastPlay = nil
	d.LastValidPlay = nil
}

// ClearTrustees 清除全部托管标记（终局/终止时调用）；托管者座位保持原样
// （真人仍在座、状态复位为未准备由调用方负责），只是不再代打。
func (d *Desk) ClearTrustees() {
	for i := range d.Positions {
		d.Positions[i].Trustee = false
	}
}

// Hold 查某用户的保留记录
func (d *Desk) Hold(userName string) (Hold, bool) {
	for _, h := range d.Holds {
		if h.UserName == userName {
			return h, true
		}
	}
	return Hold{}, false
}

// AddHold 登记断线保留
func (d *Desk) AddHold(h Hold) { d.Holds = append(d.Holds, h) }

// RemoveHold 移除某用户的保留记录
func (d *Desk) RemoveHold(userName string) {
	for i := range d.Holds {
		if d.Holds[i].UserName == userName {
			d.Holds = append(d.Holds[:i], d.Holds[i+1:]...)
			return
		}
	}
}

// ClearHolds 清空全部保留记录并返回之（调用方据此广播与停表）
func (d *Desk) ClearHolds() []Hold {
	holds := d.Holds
	d.Holds = nil
	return holds
}
