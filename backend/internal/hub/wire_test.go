// wire_test.go 黄金帧测试：以假连接直驱 hub 各 handler，用结构体编解码锁定
// SHOW_TOP_CARD / CTX_PLAY_CHANGE（首出、出牌、过牌）、
// PLAY_CARD_SUCCESS、GAME_OVER 的 wire 形态，并驱动整局至终局验证落库。
// 期望值一律构造同款结构体后 DeepEqual 对比，不做字符串手拼。
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

// fakeConn 记录 Emit 帧的假连接（hubClient.pump 异步消费后在此同步落账）
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

// waitFrame 等待第 n 次出现的指定事件（n 从 1 起），返回其 json 部分
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

// findEvent 返回首个匹配事件（未出现时 ok=false）
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

// decodeFrame 把帧 json 解码进结构体
func decodeFrame[T any](t *testing.T, data string, what string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(data), &v); err != nil {
		t.Fatalf("%s 解码失败: %v（%s）", what, err, data)
	}
	return v
}

// expectFrame 解码第 n 帧指定事件并与期望结构体 DeepEqual 对比
func expectFrame[T any](t *testing.T, f *fakeConn, event string, n int, want T) {
	t.Helper()
	got := decodeFrame[T](t, waitFrame(t, f, event, n), event)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s#%d = %+v, 期望 %+v", event, n, got, want)
	}
}

// ctxPlayFrame CTX_PLAY_CHANGE 驱动用的解析形态（Key/Type 为 any：出牌为值，过牌缺省 nil）
type ctxPlayFrame = ctxPlayChange

func TestWireGoldenFrames(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "wire.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer st.Close()
	h := New(st, 0, nil)

	// 8 人登录入座并准备 → 开局
	conns := make([]*fakeConn, table.SeatCount)
	for i := 0; i < table.SeatCount; i++ {
		conns[i] = &fakeConn{id: fmt.Sprintf("c%d", i)}
		h.onLogin(conns[i], fmt.Sprintf("u%d", i))
		h.onSitdown(conns[i], sitdownReq{DeskID: 1, PosID: i})
	}
	for i := 0; i < table.SeatCount; i++ {
		h.onPrepare(conns[i])
	}

	// 主持模式：全员准备好不再自动开局，须由主持人（首坐者 u0）开牌
	h.onHostStartGame(conns[0], hostStartGameReq{})

	// GAME_START 严格解码（未知字段即失败）：8 家手牌、牌对象仅 value/type、ht 15 元
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
		// HT 红桃统计（大小王 type=0 也计入）应与实际红桃牌数一致
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

	// 开局直接进入出牌阶段：SHOW_TOP_CARD 黄金（含首出者）
	stc := decodeFrame[showTopCard](t, waitFrame(t, conns[0], EvShowTopCard, 1), EvShowTopCard)
	P := stc.DizhuPosID
	if want := (showTopCard{TopCards: []card.Card{}, DizhuPosID: P, Timeout: playTiming}); !reflect.DeepEqual(stc, want) {
		t.Fatalf("SHOW_TOP_CARD = %+v, 期望 %+v", stc, want)
	}
	// 首出引导帧黄金
	expectFrame(t, conns[0], EvCtxPlayChange, 1, ctxPlayChange{
		CtxData: ctxPlayCtx{Len: 0, Key: "", Type: "", Cards: []card.Card{}, PosID: P},
		SumFeng: zeroPosMap(), PosID: P, Timeout: playTiming,
		Clear: true, // 引导帧=领出状态：桌面无牌可压，客户端应清桌
	})

	// 先手出最小单张 → 出牌帧 / PLAY_CARD_SUCCESS 黄金（旁观者与出牌者各验一次）
	first := hands[P][0]
	h.onPlayCard(conns[P], []card.Card{first})
	Q := (P + 1) % table.SeatCount
	R := (Q + 1) % table.SeatCount
	tmp := first.Score()
	expectFrame(t, conns[R], EvCtxPlayChange, 2, ctxPlayChange{
		CtxData: ctxPlayCtx{ // JSON 数字解码进 any 为 float64
			Len: 1, Key: float64(first.Face), Type: "A", Cards: []card.Card{first}, PosID: P,
		},
		SumFeng: zeroPosMap(), TmpFeng: tmp, PosID: Q, Timeout: playTiming,
	})
	expectFrame(t, conns[P], EvPlayCardOk, 1, playCardSuccess{Data: []card.Card{first}, TmpFeng: tmp, SumFeng: zeroPosMap()})
	hands[P] = hands[P][1:]

	// 下家过牌 → 过牌帧黄金（key/type 缺省即 nil）
	h.onPlayCard(conns[Q], nil)
	expectFrame(t, conns[R], EvCtxPlayChange, 3, ctxPlayChange{
		CtxData: ctxPlayCtx{Len: 0, Cards: []card.Card{}, PosID: Q},
		SumFeng: zeroPosMap(), TmpFeng: tmp, PosID: R, Timeout: playTiming, IsPass: true,
	})

	// 自由驱动至终局：按服务器轮转出牌（首出者出最小单，其余能压则压、压不住过）
	owner, topRank, turn := P, int(first.Face), R
	seenCtx := 3 // conns[R] 已消费的 CTX_PLAY_CHANGE 帧数
	goData := ""
	for step := 0; goData == "" && step < 20000; step++ {
		p := turn
		if p < 0 {
			t.Fatalf("轮转异常 posId=%d", p)
		}
		var play []card.Card
		if p == owner {
			play = []card.Card{hands[p][0]} // 首出：最小单张
		} else {
			for _, c := range hands[p] { // 跟牌：找最小可压单张，找不到则过牌
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
			if len(hands[lastPos]) == 0 { // 出完触发接风：归属转为新轮转者
				owner = turn
			} else {
				owner = lastPos
			}
			if len(fr.CtxData.Cards) == 1 {
				topRank = int(fr.CtxData.Cards[0].Face)
			}
		}

		// 终局判定与服务器同构：全队出完，或出完者累计抓分 ≥300。
		// 条件达成才等 GAME_OVER（与 ctx 帧同队列，必然紧随其后）
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

	// 落库为异步写队列，终局后需先排空再查库
	h.Flush()

	// 终局后应已落库（本局用户战绩可查）
	list, total, err := st.HistoryList("u0", 1, 0)
	if err != nil || total != 1 || len(list) != 1 {
		t.Fatalf("战绩应恰好 1 条: total=%d err=%v", total, err)
	}
	if list[0].EndReason != "normal" {
		t.Fatalf("EndReason 应为 normal: %+v", list[0])
	}
}

// 编译期保证 fakeConn 满足 wssrv.Conn
var _ wssrv.Conn = (*fakeConn)(nil)

// TestCancelPrepare 取消准备：未开局时可取消（座位回到未准备、房间广播），
// 对局进行中取消应被拒绝
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

	// 未准备时取消：无效但不应崩溃
	h.onCancelPrepare(c0)

	// 准备后取消：发起者收 CANCEL_PREPARE_SUCCESS，同房间他人收 POS_STATUS_CHANGE(1)
	h.onPrepare(c0)
	h.onCancelPrepare(c0)
	waitFrame(t, c0, EvCancelPrepareSuccess, 1)
	// 第 1 帧是准备的广播(state=2)，第 2 帧才是取消(state=1)
	expectFrame(t, c1, EvPosStatusChange, 2, posStatusChange{PosID: 0, State: 1})
	if s := h.lobby.Desk(1).Seat(0); s.State != 1 {
		t.Fatalf("取消后座位应回到未准备(1)，实际 %d", s.State)
	}

	// 全员准备 → 主持人开牌开局；对局中取消应被拒绝（座位保持已准备）
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
	h.onHostStartGame(c0, hostStartGameReq{}) // c0 是首坐者即主持人
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

// TestHostMode 主持模式：首坐者获得主持权（HOST_CHANGE 广播）；全员准备好不自动开局，
// 非主持人开牌被拒，主持人开牌生效；断线（座位保留）主持权不变，
// 主持人真正离桌则自动顺延给下一个有人的座位，全桌清空复位为 -1。
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

	// 首坐者获得主持权并广播 HOST_CHANGE
	h.onSitdown(c0, sitdownReq{DeskID: 1, PosID: 3})
	expectFrame(t, c0, EvHostChange, 1, hostChange{PosID: 3, UserName: "host"})
	// 后坐者不触发新的 HOST_CHANGE，但 SITDOWN_SUCCESS 里带当前主持人座位号
	h.onSitdown(c1, sitdownReq{DeskID: 1, PosID: 5})
	sd := decodeFrame[sitdownSuccess](t, waitFrame(t, c1, EvSitdownSuccess, 1), "SITDOWN_SUCCESS")
	if sd.HostPosID != 3 {
		t.Fatalf("SITDOWN_SUCCESS 应带主持位 3，实际 %d", sd.HostPosID)
	}
	if d := h.lobby.Desk(1); d.HostPosID != 3 {
		t.Fatalf("主持权应归首坐者(3)，实际 %d", d.HostPosID)
	}

	// 其余 6 人入座准备，最后两人也准备 → 全员已准备
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

	// 非主持人开牌：被拒，收 MESSAGE 且无 GAME_START
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

	// 主持人开牌：生效
	h.onHostStartGame(c0, hostStartGameReq{})
	waitFrame(t, c0, EvGameStart, 1)

	// 主持人断线（对局进行中，座位保留等待重连）：主持权保留不转移
	h.onDisconnect(c0)
	if d := h.lobby.Desk(1); d.HostPosID != 3 {
		t.Fatalf("主持人断线后主持权应保留(3)，实际 %d", d.HostPosID)
	}
	if _, ok := h.lobby.Desk(1).Hold("host"); !ok {
		t.Fatalf("主持人断线座位应保留等待重连")
	}

	// 前主持人重连坐回：仍是主持人
	c0b := &fakeConn{id: "c0b"}
	h.onLogin(c0b, "host")
	waitFrame(t, c0b, EvReconnect, 1)
	if d := h.lobby.Desk(1); d.HostPosID != 3 {
		t.Fatalf("前主持人重连后应仍是主持人(3)，实际 %d", d.HostPosID)
	}

	// 主持人真正离桌（对局因逃跑终止、座位释放）：主持权自动顺延给下一个有人的座位（4）
	h.onUnsitdown(c0b)
	expectFrame(t, c1, EvHostChange, 1, hostChange{PosID: 4, UserName: "u4"})
	if d := h.lobby.Desk(1); d.HostPosID != 4 {
		t.Fatalf("主持人离桌后主持权应顺延给 4，实际 %d", d.HostPosID)
	}

	// 非主持人离场不影响主持权；主持人（现为 4）在其他人在座时离桌会继续顺延（→5），
	// 最后一人（同时也是主持人）离桌 → 桌空复位并广播 HOST_CHANGE(-1)
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

// TestHostFillBots 人机模式：单人入座准备，开牌时确认空位填充机器人；
// 不确认则被拒；填充后机器人自动叫分/出牌至终局，终局机器人座位清理，战绩落库。
func TestHostFillBots(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "fillbots.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer st.Close()
	h := New(st, 0, nil)
	h.botDelayMin, h.botDelayMax = time.Millisecond, time.Millisecond // 测试加速机器人行动

	c0 := &fakeConn{id: "c0"}
	h.onLogin(c0, "host")
	h.onSitdown(c0, sitdownReq{DeskID: 1, PosID: 3})
	h.onPrepare(c0)

	// 有空位但不确认填充：被拒，提示确认，无 GAME_START
	h.onHostStartGame(c0, hostStartGameReq{})
	if msg := waitFrame(t, c0, EvMessage, 1); !strings.Contains(msg, "空位") {
		t.Fatalf("不确认填充应收到空位提示，实际 %q", msg)
	}
	time.Sleep(20 * time.Millisecond) // 等发送队列排空，确认无 GAME_START 混入
	if _, ok := findEvent(c0, EvGameStart); ok {
		t.Fatalf("不确认填充不应开局")
	}

	// 确认填充：7 个空位变机器人（POS_STATUS_CHANGE 带 isBot），随后开局。
	// 帧经各会话 pump 异步送达，轮询等待 7 帧到齐。
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

	// 驱动至终局：机器人自动行动，轮到真人（pos 3）时代其按同策略叫分/出牌
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
			mine  bool // 是否轮到真人（pos 3）
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
					act.play = bot.Follow(hand, top) // 跟不住返回 nil → 过牌
				} else {
					act.play = bot.Lead(hand)
				}
			}
		}
		h.mu.Unlock()

		// 只在轮到真人时代打（否则会越序毒化对局）；过牌也要发送 nil
		if act.mine && act.phase == game.PhasePlaying {
			h.onPlayCard(c0, act.play)
		} else {
			time.Sleep(2 * time.Millisecond)
		}
	}

	// 终局：机器人座位清空，真人座位归位未准备
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

	// 战绩落库：含真人 + 7 机器人共 8 名玩家
	h.Flush()
	list, total, err := st.HistoryList("host", 20, 0)
	if err != nil || total != 1 {
		t.Fatalf("应落库 1 条战绩，实际 total=%d err=%v", total, err)
	}
	if len(list) != 1 || list[0].PlayerNum != 8 {
		t.Fatalf("战绩应含 8 名玩家，实际 %+v", list[0])
	}
}

// countEvent 统计指定事件出现次数（诊断用）
func countEvent(f *fakeConn, event string) int {
	n := 0
	for _, fr := range f.snapshot() {
		if ev, _, ok := cutFrame(fr); ok && ev == event {
			n++
		}
	}
	return n
}
