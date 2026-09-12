package table

import (
	"testing"
	"time"

	"talkcards/backend/internal/game"
)

func TestQuickJoinPicksFullestDesk(t *testing.T) {
	l := New()
	// 1 号桌 1 人（pos0），2 号桌 2 人（pos0,pos1）
	l.Desk(1).UpdatePos(0, 1, ptr("a"))
	l.Desk(2).UpdatePos(0, 1, ptr("a"))
	l.Desk(2).UpdatePos(1, 1, ptr("b"))

	deskID, posID, ok := l.QuickJoin()
	if !ok || deskID != 2 || posID != 2 {
		t.Fatalf("应挑 2 号桌 pos2，实际 desk=%d pos=%d ok=%v", deskID, posID, ok)
	}

	// 2 号桌坐满后应回落 1 号桌
	for i := 2; i < SeatCount; i++ {
		l.Desk(2).UpdatePos(i, 1, ptr("x"))
	}
	deskID, _, ok = l.QuickJoin()
	if !ok || deskID != 1 {
		t.Fatalf("满员后应挑 1 号桌，实际 desk=%d ok=%v", deskID, ok)
	}

	// 所有桌全部坐满则失败
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
	for i := 0; i < SeatCount; i++ {
		if d.AllPrepared() {
			t.Fatalf("尚有人未入座即全员准备")
		}
		d.UpdatePos(i, 2, ptr("u"))
	}
	if !d.AllPrepared() {
		t.Fatalf("全部置 2 后应全员准备")
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

