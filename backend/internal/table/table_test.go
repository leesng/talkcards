package table

import (
	"strconv"
	"testing"
	"time"

	"talkcards/backend/internal/game"
)

func TestQuickJoinPicksFullestDesk(t *testing.T) {
	l := New()
	// desk 1 has one player (pos0), desk 2 has two (pos0,pos1)
	l.Desk(1).UpdatePos(0, 1, ptr("a"))
	l.Desk(2).UpdatePos(0, 1, ptr("a"))
	l.Desk(2).UpdatePos(1, 1, ptr("b"))

	deskID, posID, ok := l.QuickJoin()
	if !ok || deskID != 2 || posID != 2 {
		t.Fatalf("应挑 2 号桌 pos2，实际 desk=%d pos=%d ok=%v", deskID, posID, ok)
	}

	// once desk 2 is full, fall back to desk 1
	for i := 2; i < SeatCount; i++ {
		l.Desk(2).UpdatePos(i, 1, ptr("x"))
	}
	deskID, _, ok = l.QuickJoin()
	if !ok || deskID != 1 {
		t.Fatalf("满员后应挑 1 号桌，实际 desk=%d ok=%v", deskID, ok)
	}

	// all desks full → fail
	for _, dk := range l.Desks {
		for i := 0; i < SeatCount; i++ {
			if dk.IsEmpty(i) {
				dk.UpdatePos(i, 1, ptr("x"))
			}
		}
	}
	if _, _, ok := l.QuickJoin(); ok {
		t.Fatalf("全满应返回失败")
	}
}

func TestPrepareLifecycle(t *testing.T) {
	d := New().Desk(1)
	// empty desk counts as "all seated players prepared" (host decides whether
	// to fill bots and start short-handed)
	if !d.AllPrepared() {
		t.Fatalf("空桌应视为已入座者全准备")
	}
	// someone seated but not ready → not prepared
	d.UpdatePos(0, 1, ptr("u0"))
	if d.AllPrepared() {
		t.Fatalf("有人未准备不应通过")
	}
	// every seated player ready is enough (empty seats don't block)
	d.UpdatePos(0, 2, nil)
	if !d.AllPrepared() {
		t.Fatalf("已入座者全准备应通过（空座不阻塞）")
	}
	for i := 1; i < SeatCount; i++ {
		d.UpdatePos(i, 1, ptr("u"))
		if d.AllPrepared() {
			t.Fatalf("第 %d 座已入座未准备，不应通过", i)
		}
		d.UpdatePos(i, 2, nil)
	}
	if !d.AllPrepared() {
		t.Fatalf("全部置 2 后应全员准备")
	}
}

func TestFillBotsAndClearBots(t *testing.T) {
	d := New().Desk(1)
	d.UpdatePos(3, 2, ptr("host")) // a ready human
	if got := len(d.EmptySeats()); got != 7 {
		t.Fatalf("应剩 7 个空位，实际 %d", got)
	}
	filled := d.FillBots()
	if len(filled) != 7 {
		t.Fatalf("应填充 7 个机器人，实际 %d", len(filled))
	}
	for _, p := range filled {
		s := d.Seat(p)
		if !s.IsBot || s.State != 2 || s.UserName == "" {
			t.Fatalf("座位 %d 应为已准备机器人", p)
		}
		// 名字格式："机" + 2 位桌号 62 进制 + 1 位座位号（1-8）
		// 1 号桌座位 p → "机0" + "1"(桌号 1 的 62 进制) + itoa(p+1)
		want := "机01" + strconv.Itoa(p+1)
		if s.UserName != want {
			t.Fatalf("座位 %d 机器人名应为 %q，实际 %q", p, want, s.UserName)
		}
	}
	// names are unique within one fill
	seen := map[string]bool{}
	for _, p := range filled {
		n := d.Seat(p).UserName
		if seen[n] {
			t.Fatalf("机器人名重复 %q", n)
		}
		seen[n] = true
	}
	if s := d.Seat(3); s.IsBot {
		t.Fatalf("真人座位不应被标记为机器人")
	}
	if len(d.EmptySeats()) != 0 || !d.AllPrepared() {
		t.Fatalf("填充后应满员且全准备")
	}
	// bots never take over as host
	d.HostPosID = 3
	if p, changed := d.TransferHostFrom(3); !changed || p == filled[0] && d.Seat(p).IsBot {
		// TransferHostFrom skips bot seats; here we only verify it didn't land on a bot
		for i := 0; i < SeatCount; i++ {
			if s := d.Seat(i); s.IsBot && i == d.HostPosID {
				t.Fatalf("主持人不应顺延给机器人，实际给了座位 %d", i)
			}
		}
	}
	cleared := d.ClearBots()
	if len(cleared) != 7 {
		t.Fatalf("应清理 7 个机器人座位，实际 %d", len(cleared))
	}
	if s := d.Seat(cleared[0]); s.State != 0 || s.UserName != "" || s.IsBot {
		t.Fatalf("清理后座位应复位为空座")
	}
	if s := d.Seat(3); s.State != 2 || s.IsBot {
		t.Fatalf("真人座位不应受清理影响")
	}
}

func TestGameInProgressAndReset(t *testing.T) {
	d := New().Desk(3)
	if d.GameInProgress() {
		t.Fatalf("无对局不应进行中")
	}
	d.Game = game.New()
	d.Game.Start()
	if !d.GameInProgress() {
		t.Fatalf("叫分阶段应算进行中")
	}
	d.StartedAt = time.Now()
	d.Players[0] = "u0"
	d.LastPlay = &PlaySnapshot{Next: 1}
	d.ResetGame()
	if d.GameInProgress() || !d.StartedAt.IsZero() || d.LastPlay != nil || d.Players[0] != "u0" {
		t.Fatalf("ResetGame 后开局痕迹应清空（玩家快照保留供落库前调用方使用）")
	}
}

func TestHolds(t *testing.T) {
	d := New().Desk(1)
	d.AddHold(Hold{UserName: "u3", PosID: 3})
	if h, ok := d.Hold("u3"); !ok || h.PosID != 3 {
		t.Fatalf("保留记录应可查")
	}
	d.RemoveHold("u3")
	if _, ok := d.Hold("u3"); ok {
		t.Fatalf("移除后不应可查")
	}
	d.AddHold(Hold{UserName: "a", PosID: 1})
	d.AddHold(Hold{UserName: "b", PosID: 2})
	holds := d.ClearHolds()
	if len(holds) != 2 || len(d.Holds) != 0 {
		t.Fatalf("ClearHolds 应返回并清空全部记录")
	}
}

func ptr(s string) *string { return &s }

func TestDeskLookupBoundsAndSnapshot(t *testing.T) {
	l := New()
	if l.Desk(1) == nil || l.Desk(DeskCount) == nil {
		t.Fatalf("合法桌号应可命中")
	}
	if l.Desk(0) != nil || l.Desk(DeskCount+1) != nil {
		t.Fatalf("越界桌号应返回 nil")
	}

	d := l.Desk(1)
	d.UpdatePos(3, 1, ptr("u3"))
	snap := d.PlayerSnapshot()
	if snap[3] != "u3" || snap[0] != "" {
		t.Fatalf("玩家快照异常: %v", snap)
	}
	if d.Seat(-1) != nil || d.Seat(SeatCount) != nil {
		t.Fatalf("越界座位号应返回 nil")
	}
}
