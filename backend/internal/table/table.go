// Package table is the lobby/desk/seat aggregate: the lifecycle of 20 desks ×
// 8 seats (sit/prepare/start snapshot), game mounting and disconnect holds.
// It does not depend on the transport layer (wssrv); reconnect countdown
// timers stay in hub (callbacks must hold the hub lock) — this package only
// stores state and deadlines.
package table

import (
	"sort"
	"strconv"
	"time"

	"talkcards/backend/internal/card"
	"talkcards/backend/internal/game"
)

const SeatCount = 8

const DeskCount = 20

// Seat is serialized directly as the wire payload of LOGIN_SUCCESS /
// REFRESH_LIST / POS_STATUS_RESET etc.; field names must match the frontend
// exactly.
type Seat struct {
	PosID    int    `json:"posId"`
	State    int    `json:"state"` // 0 empty, 1 unready, 2 ready
	UserName string `json:"userName"`
	IsBot    bool   `json:"isBot"` // bot-filled seat (filled when host starts, cleared at game over)
	// Trustee marks a human seat being auto-played by the server with the bot
	// strategy: toggled manually during a game or auto-enabled on disconnect
	// timeout; cleared on reconnect/game over.
	Trustee bool `json:"trustee,omitempty"`
}

// Hold is a disconnected player's reserved seat record during a game. The
// authoritative countdown state lives in hub's timer table; table only stores
// the seat ownership, not timers.
type Hold struct {
	UserName string
	PosID    int
}

// PlaySnapshot is the domain snapshot of the last play broadcast; hub rebuilds
// the wire frame from it for reconnect replay.
type PlaySnapshot struct {
	IsPass  bool
	Key     int    // Rank of the played shape (meaningless when IsPass)
	Type    string // shape type name (empty when IsPass)
	Cards   []card.Card
	PosID   int // player who acted
	SumFeng [SeatCount]int
	TmpFeng int
	Next    int // next player when replayed
}

// Desk holds the seat lifecycle, mounted game and disconnect holds. Exported
// internal-state fields (Game/StartedAt/Players/LastPlay/Holds) are all
// json:"-"; the lobby-list wire payload only exposes DeskID/State/Positions.
type Desk struct {
	DeskID    int    `json:"deskId"`
	State     int    `json:"state"` // room state (wire-only; frontend doesn't consume it)
	Positions []Seat `json:"positions"`

	Game      *game.Game        `json:"-"` // this desk's game; nil = not started
	StartedAt time.Time         `json:"-"` // game start time (for persistence)
	Players   [SeatCount]string `json:"-"` // player snapshot at game start (for persistence; unaffected by mid-game seat changes)
	LastPlay  *PlaySnapshot     `json:"-"` // last action snapshot (play or pass; reconnect replay)
	// LastValidPlay is the last non-pass play snapshot; passes don't overwrite
	// it. If the previous action was a pass, LastPlay alone doesn't show what
	// is on the table to beat, so reconnect replays this frame before LastPlay.
	LastValidPlay *PlaySnapshot `json:"-"`
	Holds         []Hold        `json:"-"` // disconnect hold records (by user name)

	HostPosID int `json:"-"` // host seat (first to sit); auto-transferred to the next occupied seat when the host leaves
}

type Lobby struct {
	Desks []*Desk
}

// New creates the lobby (DeskCount desks × SeatCount seats, desk IDs from 1).
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

func (l *Lobby) Desk(deskID int) *Desk {
	if deskID < 1 || deskID > len(l.Desks) {
		return nil
	}
	return l.Desks[deskID-1]
}

// DeskHolding finds the desk holding a user's disconnect reservation; nil if none.
func (l *Lobby) DeskHolding(userName string) *Desk {
	for _, d := range l.Desks {
		if _, ok := d.Hold(userName); ok {
			return d
		}
	}
	return nil
}

// QuickJoin picks the desk with the fewest free seats (most people) and
// returns its first free seat. Stable sort matches the JS version.
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
			continue // full desks are not joinable (equivalent to occupied <= 7)
		}
		ret = append(ret, c)
	}
	sort.SliceStable(ret, func(a, b int) bool { return len(ret[a].free) < len(ret[b].free) })
	if len(ret) == 0 {
		return 0, 0, false
	}
	return ret[0].deskID, ret[0].free[0], true
}

func (d *Desk) Seat(posID int) *Seat {
	if posID < 0 || posID >= len(d.Positions) {
		return nil
	}
	return &d.Positions[posID]
}

func (d *Desk) IsEmpty(posID int) bool {
	s := d.Seat(posID)
	return s != nil && s.State == 0
}

// UpdatePos updates a seat's state; userName nil means "leave unchanged"
// (mirrors JS passing undefined).
func (d *Desk) UpdatePos(posID, state int, userName *string) {
	if s := d.Seat(posID); s != nil {
		s.State = state
		if userName != nil {
			s.UserName = *userName
		}
	}
}

// UpdateOtherPos updates every seat except posID; empty seats also get set to
// state 1 (copied verbatim from JS).
func (d *Desk) UpdateOtherPos(posID, state int) {
	for i := range d.Positions {
		if d.Positions[i].PosID != posID {
			d.Positions[i].State = state
		}
	}
}

func (d *Desk) SetState(state int) { d.State = state }

// AllPrepared reports whether every seated player is ready (empty seats don't
// block; the host may fill them with bots to start short-handed).
func (d *Desk) AllPrepared() bool {
	for i := range d.Positions {
		if d.Positions[i].State != 0 && d.Positions[i].State != 2 {
			return false
		}
	}
	return true
}

// EmptySeats lists free seat IDs in ascending order; the host uses this to
// decide whether to fill bots when starting.
func (d *Desk) EmptySeats() []int {
	var empty []int
	for i := range d.Positions {
		if d.Positions[i].State == 0 {
			empty = append(empty, d.Positions[i].PosID)
		}
	}
	return empty
}

// botName62 62 进制字符表：0-9a-zA-Z
const botName62 = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// botName 生成机器人名："机" + 2 位桌号 62 进制 + 1 位座位号（1-8）。
// 例：3 号桌座位 5 → 机035；12 号桌座位 7 → 机0c7（12 的 62 进制为 c）。
// 桌内按座位唯一、跨桌按桌号位唯一，无需全局计数器，重启/多桌并发均不会重名。
func botName(deskID, posID int) string {
	// 桌号转 62 进制，不足 2 位左侧补零占位
	d := deskID
	var db [8]byte
	i := len(db)
	for {
		i--
		db[i] = botName62[d%62]
		d /= 62
		if d == 0 {
			break
		}
	}
	for len(db)-i < 2 {
		i--
		db[i] = '0'
	}
	// 座位号 1-8（内部 PosID 为 0-7，展示时 +1）
	return "机" + string(db[i:]) + strconv.Itoa(posID+1)
}

// FillBots fills every free seat with a ready bot and returns the filled seat
// IDs (ascending). Bots exist only for the duration of the game and are
// cleaned up by ClearBots.
func (d *Desk) FillBots() []int {
	var filled []int
	for i := range d.Positions {
		if d.Positions[i].State == 0 {
			d.Positions[i].State = 2
			d.Positions[i].UserName = botName(d.DeskID, d.Positions[i].PosID)
			d.Positions[i].IsBot = true
			filled = append(filled, d.Positions[i].PosID)
		}
	}
	return filled
}

// ClearBots resets all bot seats to empty (called at game over) and returns
// the cleared seat IDs.
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

// AssignHost grants posID the host role if the desk has no host yet (first
// sitter); returns whether it was granted. A disconnect (seat held) does not
// affect host rights; only actually leaving (seat released) transfers via
// TransferHostFrom.
func (d *Desk) AssignHost(posID int) bool {
	if d.HostPosID == -1 {
		d.HostPosID = posID
		return true
	}
	return false
}

// TransferHostFrom is called when the host leaves (seat released): if posID is
// the host, the first occupied seat clockwise after them takes over; if the
// desk is empty the host resets to -1. Returns the new host seat (-1 = reset)
// and whether anything changed. A non-host leaving changes nothing.
func (d *Desk) TransferHostFrom(posID int) (int, bool) {
	if d.HostPosID != posID {
		return d.HostPosID, false
	}
	for i := 1; i <= SeatCount; i++ {
		p := (posID + i) % SeatCount
		if d.Positions[p].State != 0 && !d.Positions[p].IsBot { // bots never take over as host
			d.HostPosID = p
			return p, true
		}
	}
	d.HostPosID = -1
	return -1, true
}

// GameInProgress reports whether the desk's game is in the playing phase.
func (d *Desk) GameInProgress() bool {
	return d.Game != nil && d.Game.Phase() == game.PhasePlaying
}

// PlayerSnapshot captures the names of currently seated players (taken at
// game start for persistence; later seat changes don't affect the record).
func (d *Desk) PlayerSnapshot() [SeatCount]string {
	var names [SeatCount]string
	for i := range d.Positions {
		names[i] = d.Positions[i].UserName
	}
	return names
}

// ResetGame cleans up after a game ends or is aborted: reset the game and
// clear the start traces.
func (d *Desk) ResetGame() {
	if d.Game != nil {
		d.Game.Init()
	}
	d.StartedAt = time.Time{}
	d.LastPlay = nil
	d.LastValidPlay = nil
}

// ClearTrustees clears all trustee flags (called at game over/abort). The
// trustee's seat itself stays as-is (the human remains seated; resetting the
// state to unready is the caller's job) — they're just no longer auto-played.
func (d *Desk) ClearTrustees() {
	for i := range d.Positions {
		d.Positions[i].Trustee = false
	}
}

func (d *Desk) Hold(userName string) (Hold, bool) {
	for _, h := range d.Holds {
		if h.UserName == userName {
			return h, true
		}
	}
	return Hold{}, false
}

func (d *Desk) AddHold(h Hold) { d.Holds = append(d.Holds, h) }

func (d *Desk) RemoveHold(userName string) {
	for i := range d.Holds {
		if d.Holds[i].UserName == userName {
			d.Holds = append(d.Holds[:i], d.Holds[i+1:]...)
			return
		}
	}
}

// ClearHolds clears all hold records and returns them (the caller broadcasts
// and stops the timers based on them).
func (d *Desk) ClearHolds() []Hold {
	holds := d.Holds
	d.Holds = nil
	return holds
}
