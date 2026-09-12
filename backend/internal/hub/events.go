// events.go Socket.IO 事件契约：事件名与 payload 字段名必须与 static/index.html 中
// 前端消费的字段逐字一致，改动前先对照前端代码。
package hub

import (
	"talkcards/backend/internal/card"
	"talkcards/backend/internal/store"
	"talkcards/backend/internal/table"
)

// 入站事件
const (
	EvLogin         = "LOGIN"
	EvQuickJoin     = "QUICK_JOIN"
	EvSitdown       = "SITDOWN"
	EvUnsitdown     = "UNSITDOWN"
	EvPrepare       = "PREPARE"
	EvCancelPrepare = "CANCEL_PREPARE"
	EvCallScore     = "CALL_SCORE"
	EvPlayCard      = "PLAY_CARD"
	EvUserMessage   = "USER_MESSAGE"
	EvHistoryList   = "HISTORY_LIST"
	EvHistoryDetail = "HISTORY_DETAIL"
)

// 出站事件
const (
	EvLoginSuccess         = "LOGIN_SUCCESS"
	EvLoginFail            = "LOGIN_FAIL"
	EvReconnect            = "RECONNECT"
	EvHistoryListOut       = "HISTORY_LIST"
	EvHistoryDetailOut     = "HISTORY_DETAIL"
	EvHistoryFail          = "HISTORY_FAIL"
	EvQuickJoinOut         = "QUICK_JOIN"
	EvSitdownSuccess       = "SITDOWN_SUCCESS"
	EvSitdownError         = "SITDOWN_ERROR"
	EvUnsitSuccess         = "UNSITDOWN_SUCCESS"
	EvRefreshList          = "REFRESH_LIST"
	EvStatusChange         = "STATUS_CHANGE"
	EvPosStatusChange      = "POS_STATUS_CHANGE"
	EvPosStatusReset       = "POS_STATUS_RESET"
	EvRoomStatusChg        = "ROOM_STATUS_CHANGE"
	EvForceExit            = "FORCE_EXIT_EV"
	EvPrepareSuccess       = "PREPARE_SUCCESS"
	EvCancelPrepareSuccess = "CANCEL_PREPARE_SUCCESS"
	EvGameStart            = "GAME_START"
	EvCtxUserChange        = "CTX_USER_CHANGE"
	EvShowTopCard          = "SHOW_TOP_CARD"
	EvCtxPlayChange        = "CTX_PLAY_CHANGE"
	EvPlayCardOk           = "PLAY_CARD_SUCCESS"
	EvPlayCardErr          = "PLAY_CARD_ERROR"
	EvGameOver             = "GAME_OVER"
	EvMessage              = "MESSAGE"
	EvUserMessageOut       = "USER_MESSAGE"
)

// 座位/牌桌 wire 结构见 internal/table（Seat/Desk 直接序列化，字段与前端逐字一致）

type sitdownReq struct {
	DeskID int `json:"deskId"`
	PosID  int `json:"posId"`
}

// historyListReq HISTORY_LIST 载荷（page 从 1 起，0 视为 1）
type historyListReq struct {
	Page int `json:"page"`
}

type historyDetailReq struct {
	GameID int64 `json:"gameId"`
}

// reconnectPayload RECONNECT：客户端据此回到房间界面（字段兼容 SITDOWN_SUCCESS）
type reconnectPayload struct {
	DeskID  int          `json:"deskId"`
	PosID   int          `json:"posId"`
	PosInfo []table.Seat `json:"posInfos"`
}

// historyListOut HISTORY_LIST 出站
type historyListOut struct {
	List  []store.HistoryItem `json:"list"`
	Total int                 `json:"total"`
	Page  int                 `json:"page"`
}

// historyDetailOut HISTORY_DETAIL 出站
type historyDetailOut struct {
	GameID     int64              `json:"gameId"`
	DeskID     int                `json:"deskId"`
	StartedAt  string             `json:"startedAt"`
	EndedAt    string             `json:"endedAt"`
	EndReason  string             `json:"endReason"`
	Winner     []int              `json:"winner"`
	Team0Score int                `json:"team0Score"` // 偶数队最终得分
	Team1Score int                `json:"team1Score"` // 奇数队最终得分
	Players    []store.GamePlayer `json:"players"`
}

type callScoreReq struct {
	Score int `json:"score"`
}

type msgPayload struct {
	Msg string `json:"msg"`
}

// quickJoinResult 成功时三字段齐全；失败时仅 success（对应 js 两种不同字面量）
type quickJoinResult struct {
	DeskID  int  `json:"deskId"`
	PosID   int  `json:"posId"`
	Success bool `json:"success"`
}

type quickJoinFail struct {
	Success bool `json:"success"`
}

type loginFailPayload struct {
	Msg string `json:"msg"`
}

type sitdownSuccess struct {
	DeskID  int          `json:"deskId"`
	PosID   int          `json:"posId"`
	PosInfo []table.Seat `json:"posInfos"`
}

// houseStatusChange STATUS_CHANGE：userName 恒存在（离开时为 ""，前端用于清名）
type houseStatusChange struct {
	DeskID   int    `json:"deskId"`
	PosID    int    `json:"posId"`
	State    int    `json:"state"`
	UserName string `json:"userName"`
}

// posStatusChange POS_STATUS_CHANGE：userName 可缺省（准备/离开时不带）
type posStatusChange struct {
	PosID    int    `json:"posId"`
	State    int    `json:"state"`
	UserName string `json:"userName,omitempty"`
}

type posStatusReset struct {
	Pos   []table.Seat `json:"pos"`
	State int          `json:"state"`
}

type roomStatusChange struct {
	State int `json:"state"`
}

type forceExitPayload struct {
	Msg   string `json:"msg"`
	PosID int    `json:"posId"`
}

// userMessage USER_MESSAGE 广播。
// js 中 id 为 guid() 返回的函数、被 JSON.stringify 丢弃；这里改为自增整数（前端仅作 v-for key）
type userMessage struct {
	Type  string `json:"type"`
	PosID int    `json:"posId"`
	Msg   string `json:"msg"`
	ID    int64  `json:"id"`
	Time  string `json:"time"`
}

// handGroup 一家手牌的 wire 形态（GAME_START）：id/cards/ht 与前端逐字一致；
// HT 为红桃统计（大小王 type=0 也计入），是前端展示需求，故留在 wire 层
type handGroup struct {
	ID    int         `json:"id"`
	Cards []card.Card `json:"cards"`
	HT    [15]int     `json:"ht"`
}

type gameStartPayload struct {
	Cards []handGroup `json:"cards"` // 注意：js 将 8 家手牌全部广播（前端亮牌/剩余张数依赖此行为）
}

// gameOverPayload GAME_OVER 出站
type gameOverPayload struct {
	Winner []int `json:"winner"`
	Loser  []int `json:"loser"`
	Score  int   `json:"score"`
	Ratio  int   `json:"ratio"`
}

// ctxUserChange CTX_USER_CHANGE；开局首次广播无 calledScores
type ctxUserChange struct {
	CtxPos       int            `json:"ctxPos"`
	CtxScore     [3]int         `json:"ctxScore"`
	CalledScores map[string]int `json:"calledScores,omitempty"`
	Timeout      int            `json:"timeout"`
}

type showTopCard struct {
	TopCards   []card.Card `json:"topCards"`
	DizhuPosID int         `json:"dizhuPosId"`
	Timeout    int         `json:"timeout"`
}

// ctxPlayCtx Key/Type 用 any 对齐 js：初牌为 ""，正常出牌为数值/字符串，过牌时省略
type ctxPlayCtx struct {
	Len   int         `json:"len"`
	Key   interface{} `json:"key,omitempty"`
	Type  interface{} `json:"type,omitempty"`
	Cards []card.Card `json:"cards"`
	PosID int         `json:"posId"`
}

type ctxPlayChange struct {
	CtxData ctxPlayCtx     `json:"ctxData"`
	SumFeng map[string]int `json:"sumFeng"`
	TmpFeng int            `json:"tmpFeng"`
	PosID   int            `json:"posId"`
	Timeout int            `json:"timeout"`
	IsPass  bool           `json:"isPass"`
}

// playCardSuccess js 原样发送 {data,tmpFeng,sumFeng}（前端按旧用法消费）
type playCardSuccess struct {
	Data    []card.Card    `json:"data"`
	TmpFeng int            `json:"tmpFeng"`
	SumFeng map[string]int `json:"sumFeng"`
}
