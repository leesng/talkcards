package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSaveAndHistory(t *testing.T) {
	s := openTest(t)
	// schema must include the username column and the composite index
	if !s.db.Migrator().HasColumn(&playerRow{}, "UserName") || !s.db.Migrator().HasIndex(&playerRow{}, "idx_game_players_user") {
		t.Fatal("建表缺列或缺索引")
	}
	now := time.Now()
	rec := GameRecord{
		DeskID: 3, StartedAt: now.Add(-10 * time.Minute), EndedAt: now, EndReason: "normal",
		Winner: []int{0, 2, 4, 6}, Team0Score: 400, Team1Score: 130,
		Players: []GamePlayer{
			{PosID: 0, UserName: "a", SumFeng: 100, Remaining: 0, Win: true},
			{PosID: 1, UserName: "b", SumFeng: 30, Remaining: 5},
			{PosID: 2, UserName: "c", SumFeng: 200, Remaining: 0, Win: true},
		},
	}
	id, err := s.SaveGame(rec)
	if err != nil || id <= 0 {
		t.Fatalf("SaveGame id=%d err=%v", id, err)
	}

	list, total, err := s.HistoryList("a", 10, 0)
	if err != nil {
		t.Fatalf("HistoryList: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("期望 1 条，得到 total=%d len=%d", total, len(list))
	}
	it := list[0]
	if it.GameID != id || !it.Win || it.SumFeng != 100 || it.Remaining != 0 || it.EndReason != "normal" || it.PlayerNum != 3 {
		t.Fatalf("列表条目不符: %+v", it)
	}
	if it.TeamScore != 400 || it.OppScore != 130 {
		t.Fatalf("偶数队视角得分不符: %+v", it)
	}
	listB, _, _ := s.HistoryList("b", 10, 0)
	if listB[0].TeamScore != 130 || listB[0].OppScore != 400 {
		t.Fatalf("奇数队视角应互换: %+v", listB[0])
	}

	detail, ok, err := s.GameDetail(id)
	if err != nil || !ok {
		t.Fatalf("GameDetail ok=%v err=%v", ok, err)
	}
	if detail.Team0Score != 400 || detail.Team1Score != 130 {
		t.Fatalf("详情队伍得分不符: %+v", detail)
	}
	if len(detail.Players) != 3 || detail.Winner[0] != 0 || detail.Players[1].UserName != "b" || detail.Players[1].Remaining != 5 {
		t.Fatalf("详情不符: %+v", detail)
	}
	if _, ok, _ := s.GameDetail(99999); ok {
		t.Fatal("不存在的对局应返回 ok=false")
	}

	// save an escape game too, verifying newest-first order and escape fields
	esc := rec
	esc.EndReason = "escape"
	esc.Winner = nil
	for i := range esc.Players {
		esc.Players[i].Win = false
	}
	if _, err := s.SaveGame(esc); err != nil {
		t.Fatal(err)
	}
	list, total, _ = s.HistoryList("a", 10, 0)
	if total != 2 || len(list) != 2 || list[0].EndReason != "escape" || list[0].Win {
		t.Fatalf("倒序/escape 不符: %+v", list)
	}
	// pagination
	list, total, _ = s.HistoryList("a", 1, 1)
	if total != 2 || len(list) != 1 || list[0].EndReason != "normal" {
		t.Fatalf("分页不符: total=%d %+v", total, list)
	}
}
