// wire_test.go: golden-frame tests; expected frames compared as structs.
package hub

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"talkcards/backend/internal/bot"
	"talkcards/backend/internal/card"
	"talkcards/backend/internal/game"
	"talkcards/backend/internal/store"
	"talkcards/backend/internal/table"
	"talkcards/backend/internal/wssrv"
)

// fakeConn: records Emit frames ("EVENT|json"); pump delivery is async.
type fakeConn struct {
	mu     sync.Mutex
	id     string
	frames []string // "EVENT|json"
}

func (f *fakeConn) ID() string { return f.id }

func (f *fakeConn) Emit(event string, v any) {
	data := []byte(nil)
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		data = b
	}
	f.mu.Lock()
	f.frames = append(f.frames, event+"|"+string(data))
	f.mu.Unlock()
}

func (f *fakeConn) Close() {}

func (f *fakeConn) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.frames...)
}

// waitFrame: poll for the n-th occurrence of an event; frames arrive async.
func waitFrame(t *testing.T, f *fakeConn, event string, n int) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		seen := 0
		for _, fr := range f.snapshot() {
			if ev, data, ok := cutFrame(fr); ok && ev == event {
				seen++
				if seen == n {
					return data
				}
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待 %s#%d 超时，已有帧:\n%s", event, n, formatFrames(f.snapshot()))
	return ""
}

func findEvent(f *fakeConn, event string) (string, bool) {
	for _, fr := range f.snapshot() {
		if ev, data, ok := cutFrame(fr); ok && ev == event {
			return data, true
		}
	}
	return "", false
}

func cutFrame(fr string) (string, string, bool) {
	for i := 0; i < len(fr); i++ {
		if fr[i] == '|' {
			return fr[:i], fr[i+1:], true
		}
	}
	return "", "", false
}

func formatFrames(frames []string) string {
	out := ""
	for _, fr := range frames {
		out += fr + "\n"
	}
	return out
}

func decodeFrame[T any](t *testing.T, data string, what string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(data), &v); err != nil {
		t.Fatalf("%s 解码失败: %v（%s）", what, err, data)
	}
	return v
}

func expectFrame[T any](t *testing.T, f *fakeConn, event string, n int, want T) {
	t.Helper()
	got := decodeFrame[T](t, waitFrame(t, f, event, n), event)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s#%d = %+v, 期望 %+v", event, n, got, want)
	}
}

type ctxPlayFrame = ctxPlayChange

func TestWireGoldenFrames(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "wire.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer st.Close()
	h := New(st, 0, nil)

	// 8 players: login, sit, prepare.
	conns := make([]*fakeConn, table.SeatCount)
	for i := 0; i < table.SeatCount; i++ {
		conns[i] = &fakeConn{id: fmt.Sprintf("c%d", i)}
		h.onLogin(conns[i], fmt.Sprintf("u%d", i))
		h.onSitdown(conns[i], sitdownReq{DeskID: 1, PosID: i})
	}
	for i := 0; i < table.SeatCount; i++ {
		h.onPrepare(conns[i])
	}

	// Host mode: no auto-start; host (first sitter u0) starts.
	h.onHostStartGame(conns[0], hostStartGameReq{})

	// Strict GAME_START decode: unknown fields fail.
	gs := waitFrame(t, conns[0], EvGameStart, 1)
	var strictStart struct {
		Cards []struct {
			ID    int `json:"id"`
			Cards []struct {
				Value int `json:"value"`
				Type  int `json:"type"`
			} `json:"cards"`
			HT [15]int `json:"ht"`
		} `json:"cards"`
	}
	dec := json.NewDecoder(strings.NewReader(gs))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&strictStart); err != nil {
		t.Fatalf("GAME_START 严格解码失败（存在未知字段）: %v", err)
	}
	if len(strictStart.Cards) != 8 {
		t.Fatalf("GAME_START 应广播 8 家手牌，实际 %d", len(strictStart.Cards))
	}
	hands := make([][]card.Card, table.SeatCount)
	for i, grp := range strictStart.Cards {
		if len(grp.Cards) != 40 && len(grp.Cards) != 41 {
			t.Fatalf("pos %d 手牌 %d 张异常", i, len(grp.Cards))
		}
		// HT tally must match the actual heart count.
		htSum, hearts := 0, 0
		for _, v := range grp.HT {
			htSum += v
		}
		for _, c := range grp.Cards {
			if card.Suit(c.Type) == card.Heart {
				hearts++
			}
			hands[i] = append(hands[i], card.Card{Face: card.Face(c.Value), Suit: card.Suit(c.Type)})
		}
		if htSum != hearts {
			t.Fatalf("pos %d ht 统计 %d != 实际红桃 %d", i, htSum, hearts)
		}
	}

	// SHOW_TOP_CARD golden; lead frame golden (longer first-lead countdown).
	stc := decodeFrame[showTopCard](t, waitFrame(t, conns[0], EvShowTopCard, 1), EvShowTopCard)
	P := stc.DizhuPosID
	if want := (showTopCard{TopCards: []card.Card{}, DizhuPosID: P, Timeout: playTiming}); !reflect.DeepEqual(stc, want) {
		t.Fatalf("SHOW_TOP_CARD = %+v, 期望 %+v", stc, want)
	}
	// Lead frame golden (longer first-lead countdown).
	expectFrame(t, conns[0], EvCtxPlayChange, 1, ctxPlayChange{
		CtxData: ctxPlayCtx{Len: 0, Key: "", Type: "", Cards: []card.Card{}, PosID: P},
		SumFeng: zeroPosMap(), PosID: P, Timeout: firstPlayTiming,
		Clear: true,
	})
	// Leader plays smallest single → play + PLAY_CARD_SUCCESS goldens.
	first := hands[P][0]
	h.onPlayCard(conns[P], []card.Card{first})
	Q := (P + 1) % table.SeatCount
	R := (Q + 1) % table.SeatCount
	tmp := first.Score()
	expectFrame(t, conns[R], EvCtxPlayChange, 2, ctxPlayChange{
		CtxData: ctxPlayCtx{ // JSON numbers decode into any as float64
			Len: 1, Key: float64(first.Face), Type: "A", Cards: []card.Card{first}, PosID: P,
		},
		SumFeng: zeroPosMap(), TmpFeng: tmp, PosID: Q, Timeout: playTiming,
	})
	expectFrame(t, conns[P], EvPlayCardOk, 1, playCardSuccess{Data: []card.Card{first}, TmpFeng: tmp, SumFeng: zeroPosMap()})
	hands[P] = hands[P][1:]

	// Next player passes → pass frame golden (key/type omitted).
	h.onPlayCard(conns[Q], nil)
	expectFrame(t, conns[R], EvCtxPlayChange, 3, ctxPlayChange{
		CtxData: ctxPlayCtx{Len: 0, Cards: []card.Card{}, PosID: Q},
		SumFeng: zeroPosMap(), TmpFeng: tmp, PosID: R, Timeout: playTiming, IsPass: true,
	})

	// Drive to the finish following server rotation: leader plays the
	// smallest single, others beat or pass.
	owner, topRank, turn := P, int(first.Face), R
	seenCtx := 3
	goData := ""
	for step := 0; goData == "" && step < 20000; step++ {
		p := turn
		if p < 0 {
			t.Fatalf("轮转异常 posId=%d", p)
		}
		var play []card.Card
		if p == owner {
			play = []card.Card{hands[p][0]}
		} else {
			for _, c := range hands[p] { // smallest beating single, else pass
				if int(c.Face) > topRank {
					play = []card.Card{c}
					break
				}
			}
		}
		h.onPlayCard(conns[p], play)
		if play != nil {
			for i, c := range hands[p] {
				if c == play[0] {
					hands[p] = append(hands[p][:i], hands[p][i+1:]...)
					break
				}
			}
		}

		fr := decodeFrame[ctxPlayFrame](t, waitFrame(t, conns[R], EvCtxPlayChange, seenCtx+1), EvCtxPlayChange)
		seenCtx++
		turn = fr.PosID
		if !fr.IsPass && len(fr.CtxData.Cards) > 0 {
			lastPos := fr.CtxData.PosID
			if len(hands[lastPos]) == 0 { // finished: new leader takes ownership
				owner = turn
			} else {
				owner = lastPos
			}
			if len(fr.CtxData.Cards) == 1 {
				topRank = int(fr.CtxData.Cards[0].Face)
			}
		}

		// End condition mirrors the server: team all out or out-players'
		// points ≥ 300; GAME_OVER follows in the same queue.
		teamAllOut := [2]bool{true, true}
		outSum := [2]int{}
		for i := 0; i < table.SeatCount; i++ {
			if len(hands[i]) == 0 {
				outSum[i%2] += fr.SumFeng[strconv.Itoa(i)]
			} else {
				teamAllOut[i%2] = false
			}
		}
		if teamAllOut[0] || teamAllOut[1] || outSum[0] >= 300 || outSum[1] >= 300 {
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if s, ok := findEvent(conns[R], EvGameOver); ok {
					goData = s
					break
				}
				time.Sleep(2 * time.Millisecond)
			}
			if goData == "" {
				t.Fatalf("状态已达终局但未等到 GAME_OVER")
			}
		}
	}
	if goData == "" {
		t.Fatalf("20000 步内未终局")
	}
	res := decodeFrame[gameOverPayload](t, goData, EvGameOver)
	if len(res.Winner) != 4 || len(res.Loser) != 4 || res.Ratio != 1 || res.Score <= 0 {
		t.Fatalf("GAME_OVER 异常: %+v", res)
	}

	// Async save queue: drain before querying.
	h.Flush()

	list, total, err := st.HistoryList("u0", 1, 0)
	if err != nil || total != 1 || len(list) != 1 {
		t.Fatalf("战绩应恰好 1 条: total=%d err=%v", total, err)
	}
	if list[0].EndReason != "normal" {
		t.Fatalf("EndReason 应为 normal: %+v", list[0])
	}
}

var _ wssrv.Conn = (*fakeConn)(nil)

// TestCancelPrepare: cancel works pre-game, rejected mid-game.
func TestCancelPrepare(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer st.Close()
	h := New(st, 0, nil)

	c0 := &fakeConn{id: "c0"}
	c1 := &fakeConn{id: "c1"}
	h.onLogin(c0, "a")
	h.onLogin(c1, "b")
	h.onSitdown(c0, sitdownReq{DeskID: 1, PosID: 0})
	h.onSitdown(c1, sitdownReq{DeskID: 1, PosID: 1})

	// Cancel while unprepared: invalid but must not crash.
	h.onCancelPrepare(c0)

	h.onPrepare(c0)
	h.onCancelPrepare(c0)
	waitFrame(t, c0, EvCancelPrepareSuccess, 1)
	// #1 is the prepare broadcast (state=2), #2 the cancel (state=1).
	expectFrame(t, c1, EvPosStatusChange, 2, posStatusChange{PosID: 0, State: 1})
	if s := h.lobby.Desk(1).Seat(0); s.State != 1 {
		t.Fatalf("取消后座位应回到未准备(1)，实际 %d", s.State)
	}

	// Everyone ready → host starts; cancel mid-game must be rejected.
	for i := 0; i < table.SeatCount; i++ {
		if i > 1 {
			name := fmt.Sprintf("u%d", i)
			c := &fakeConn{id: fmt.Sprintf("c%d", i)}
			h.onLogin(c, name)
			h.onSitdown(c, sitdownReq{DeskID: 1, PosID: i})
			h.onPrepare(c)
		}
	}
	h.onPrepare(c0)
	h.onPrepare(c1)
	h.onHostStartGame(c0, hostStartGameReq{}) // c0 = first sitter = host
	waitFrame(t, c0, EvGameStart, 1)
	before := len(c0.snapshot())
	h.onCancelPrepare(c0)
	for _, fr := range c0.snapshot()[before:] {
		if ev, _, ok := cutFrame(fr); ok && ev == EvCancelPrepareSuccess {
			t.Fatalf("对局中不应允许取消准备")
		}
	}
	if s := h.lobby.Desk(1).Seat(0); s.State != 2 {
		t.Fatalf("对局中座位应保持已准备(2)，实际 %d", s.State)
	}
}

// TestHostMode: first sitter becomes host; non-host start rejected; host
// rights survive disconnect and pass on leaving, resetting to -1 when empty.
func TestHostMode(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer st.Close()
	h := New(st, 0, nil)

	c0 := &fakeConn{id: "c0"}
	c1 := &fakeConn{id: "c1"}
	h.onLogin(c0, "host")
	h.onLogin(c1, "guest")

	// First sitter becomes host; later sitters' SITDOWN_SUCCESS carries the host seat.
	h.onSitdown(c0, sitdownReq{DeskID: 1, PosID: 3})
	expectFrame(t, c0, EvHostChange, 1, hostChange{PosID: 3, UserName: "host"})
	h.onSitdown(c1, sitdownReq{DeskID: 1, PosID: 5})
	sd := decodeFrame[sitdownSuccess](t, waitFrame(t, c1, EvSitdownSuccess, 1), "SITDOWN_SUCCESS")
	if sd.HostPosID != 3 {
		t.Fatalf("SITDOWN_SUCCESS 应带主持位 3，实际 %d", sd.HostPosID)
	}
	if d := h.lobby.Desk(1); d.HostPosID != 3 {
		t.Fatalf("主持权应归首坐者(3)，实际 %d", d.HostPosID)
	}

	// Everyone ready.
	for i := 0; i < table.SeatCount; i++ {
		if i == 3 || i == 5 {
			continue
		}
		c := &fakeConn{id: fmt.Sprintf("c%d", i)}
		h.onLogin(c, fmt.Sprintf("u%d", i))
		h.onSitdown(c, sitdownReq{DeskID: 1, PosID: i})
		h.onPrepare(c)
	}
	h.onPrepare(c0)
	h.onPrepare(c1)

	// Non-host start: rejected, no GAME_START.
	before := len(c1.snapshot())
	h.onHostStartGame(c1, hostStartGameReq{})
	msg := waitFrame(t, c1, EvMessage, 1)
	if !strings.Contains(msg, "主持人") {
		t.Fatalf("非主持人开牌应收到主持人提示，实际 %q", msg)
	}
	for _, fr := range c1.snapshot()[before:] {
		if ev, _, ok := cutFrame(fr); ok && ev == EvGameStart {
			t.Fatalf("非主持人开牌不应触发开局")
		}
	}

	// Host start: works.
	h.onHostStartGame(c0, hostStartGameReq{})
	waitFrame(t, c0, EvGameStart, 1)

	// Host disconnects mid-game: seat held, host rights unchanged.
	h.onDisconnect(c0)
	if d := h.lobby.Desk(1); d.HostPosID != 3 {
		t.Fatalf("主持人断线后主持权应保留(3)，实际 %d", d.HostPosID)
	}
	if _, ok := h.lobby.Desk(1).Hold("host"); !ok {
		t.Fatalf("主持人断线座位应保留等待重连")
	}

	// Host reconnects: still host.
	c0b := &fakeConn{id: "c0b"}
	h.onLogin(c0b, "host")
	waitFrame(t, c0b, EvReconnect, 1)
	if d := h.lobby.Desk(1); d.HostPosID != 3 {
		t.Fatalf("前主持人重连后应仍是主持人(3)，实际 %d", d.HostPosID)
	}

	// Host leaves: rights pass to the next occupied seat (4), then 5, then -1.
	h.onUnsitdown(c0b)
	expectFrame(t, c1, EvHostChange, 1, hostChange{PosID: 4, UserName: "u4"})
	if d := h.lobby.Desk(1); d.HostPosID != 4 {
		t.Fatalf("主持人离桌后主持权应顺延给 4，实际 %d", d.HostPosID)
	}

	for _, i := range []int{0, 1, 2, 6, 7} {
		h.releaseSeat(h.lobby.Desk(1), i, nil)
	}
	h.releaseSeat(h.lobby.Desk(1), 4, nil)
	expectFrame(t, c1, EvHostChange, 2, hostChange{PosID: 5, UserName: "guest"})
	if d := h.lobby.Desk(1); d.HostPosID != 5 {
		t.Fatalf("主持人(4)离桌后主持权应顺延给 5，实际 %d", d.HostPosID)
	}
	h.releaseSeat(h.lobby.Desk(1), 5, nil)
	if d := h.lobby.Desk(1); d.HostPosID != -1 {
		t.Fatalf("全桌清空后主持权应复位为 -1，实际 %d", d.HostPosID)
	}
}

// TestHostFillBots: lone player + bot fill; without confirmation rejected;
// bots play to the finish; seats cleaned; record persisted.
func TestHostFillBots(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "fillbots.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer st.Close()
	h := New(st, 0, nil)
	h.botDelayMin, h.botDelayMax = time.Millisecond, time.Millisecond

	c0 := &fakeConn{id: "c0"}
	h.onLogin(c0, "host")
	h.onSitdown(c0, sitdownReq{DeskID: 1, PosID: 3})
	h.onPrepare(c0)

	// No fill confirmation: rejected, no GAME_START.
	h.onHostStartGame(c0, hostStartGameReq{})
	if msg := waitFrame(t, c0, EvMessage, 1); !strings.Contains(msg, "空位") {
		t.Fatalf("不确认填充应收到空位提示，实际 %q", msg)
	}
	time.Sleep(20 * time.Millisecond) // drain queue before asserting
	if _, ok := findEvent(c0, EvGameStart); ok {
		t.Fatalf("不确认填充不应开局")
	}

	// Confirm fill: 7 bots (async pump delivery — poll until all 7 in).
	h.onHostStartGame(c0, hostStartGameReq{FillBots: true})
	botSeats := 0
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		botSeats = 0
		for _, fr := range c0.snapshot() {
			if ev, data, ok := cutFrame(fr); ok && ev == EvPosStatusChange {
				pc := decodeFrame[posStatusChange](t, data, EvPosStatusChange)
				if pc.IsBot {
					if pc.State != 2 || pc.UserName == "" || pc.PosID == 3 {
						t.Fatalf("机器人座位异常: %+v", pc)
					}
					botSeats++
				}
			}
		}
		if botSeats == 7 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if botSeats != 7 {
		t.Fatalf("应填充 7 个机器人座位，实际 %d", botSeats)
	}
	waitFrame(t, c0, EvGameStart, 1)
	if s := h.lobby.Desk(1).Seat(3); s.IsBot {
		t.Fatalf("真人座位不应被标记为机器人")
	}

	// Bots act on their own; play for the human (pos 3) on their turn.
	gameDeadline := time.Now().Add(30 * time.Second)
	for {
		if _, ok := findEvent(c0, EvGameOver); ok {
			break
		}
		if time.Now().After(gameDeadline) {
			t.Fatalf("人机局 30s 内未终局，已有帧:\n%s", formatFrames(c0.snapshot()))
		}

		h.mu.Lock()
		d := h.lobby.Desk(1)
		var act struct {
			phase game.Phase
			mine  bool
			play  []card.Card
		}
		if g := d.Game; g != nil {
			act.phase = g.Phase()
			act.mine = act.phase == game.PhasePlaying && g.Turn() == 3
			if act.mine && act.phase == game.PhasePlaying {
				var hand []card.Card
				for _, hs := range g.Hands() {
					if hs.PosID == 3 {
						hand = hs.Cards
						break
					}
				}
				if top, ok := g.TrickTop(); ok {
					act.play = bot.Follow(hand, top) // nil → pass
				} else {
					act.play = bot.Lead(hand)
				}
			}
		}
		h.mu.Unlock()

		// Play only on the human's turn (out-of-turn plays poison the game).
		if act.mine && act.phase == game.PhasePlaying {
			h.onPlayCard(c0, act.play)
		} else {
			time.Sleep(2 * time.Millisecond)
		}
	}

	// Bot seats emptied, human seat back to unprepared.
	for pos := 0; pos < table.SeatCount; pos++ {
		s := h.lobby.Desk(1).Seat(pos)
		if pos == 3 {
			if s.IsBot || s.State != 1 {
				t.Fatalf("真人座位应归位未准备，实际 %+v", s)
			}
			continue
		}
		if s.State != 0 || s.IsBot || s.UserName != "" {
			t.Fatalf("机器人座位 %d 终局应清空，实际 %+v", pos, s)
		}
	}

	// Persisted: the human + 7 bots = 8 players.
	h.Flush()
	list, total, err := st.HistoryList("host", 20, 0)
	if err != nil || total != 1 {
		t.Fatalf("应落库 1 条战绩，实际 total=%d err=%v", total, err)
	}
	if len(list) != 1 || list[0].PlayerNum != 8 {
		t.Fatalf("战绩应含 8 名玩家，实际 %+v", list[0])
	}
}

func countEvent(f *fakeConn, event string) int {
	n := 0
	for _, fr := range f.snapshot() {
		if ev, _, ok := cutFrame(fr); ok && ev == event {
			n++
		}
	}
	return n
}
