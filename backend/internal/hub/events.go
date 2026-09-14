// events.go: event contract — names and payload fields must match static/index.html verbatim.
package hub

import (
	"talkcards/backend/internal/card"
	"talkcards/backend/internal/store"
	"talkcards/backend/internal/table"
)

// Inbound events.
const (
	EvLogin         = "LOGIN"
	EvQuickJoin     = "QUICK_JOIN"
	EvSitdown       = "SITDOWN"
	EvUnsitdown     = "UNSITDOWN"
	EvPrepare       = "PREPARE"
	EvCancelPrepare = "CANCEL_PREPARE"
	EvHostStartGame = "HOST_START_GAME" // host starts once everyone is ready
	EvPlayCard      = "PLAY_CARD"
	EvUserMessage   = "USER_MESSAGE"
	EvHistoryList   = "HISTORY_LIST"
	EvHistoryDetail = "HISTORY_DETAIL"
	EvToggleTrustee = "TOGGLE_TRUSTEE" // in-game only
)

// Outbound events.
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
	EvShowTopCard          = "SHOW_TOP_CARD"
	EvCtxPlayChange        = "CTX_PLAY_CHANGE"
	EvPlayCardOk           = "PLAY_CARD_SUCCESS"
	EvPlayCardErr          = "PLAY_CARD_ERROR"
	EvGameOver             = "GAME_OVER"
	EvMessage              = "MESSAGE"
	EvUserMessageOut       = "USER_MESSAGE"
	EvHostChange           = "HOST_CHANGE"    // first sit / host leaves / desk emptied
	EvTrusteeChange        = "TRUSTEE_CHANGE" // manual toggle / timeout auto / reconnect cancel
)

type sitdownReq struct {
	DeskID int `json:"deskId"`
	PosID  int `json:"posId"`
}

type historyListReq struct { // page starts at 1; 0 treated as 1
	Page int `json:"page"`
}

type historyDetailReq struct {
	GameID int64 `json:"gameId"`
}

type reconnectPayload struct { // fields compatible with SITDOWN_SUCCESS
	DeskID    int          `json:"deskId"`
	PosID     int          `json:"posId"`
	PosInfo   []table.Seat `json:"posInfos"`
	HostPosID int          `json:"hostPosId"` // -1 = no host
}

type historyListOut struct {
	List  []store.HistoryItem `json:"list"`
	Total int                 `json:"total"`
	Page  int                 `json:"page"`
}

type historyDetailOut struct {
	GameID     int64              `json:"gameId"`
	DeskID     int                `json:"deskId"`
	StartedAt  string             `json:"startedAt"`
	EndedAt    string             `json:"endedAt"`
	EndReason  string             `json:"endReason"`
	Winner     []int              `json:"winner"`
	Team0Score int                `json:"team0Score"` // even-team final score
	Team1Score int                `json:"team1Score"` // odd-team final score
	Players    []store.GamePlayer `json:"players"`
}

type hostStartGameReq struct { // payload may be omitted entirely
	FillBots bool `json:"fillBots"`
}

type msgPayload struct {
	Msg string `json:"msg"`
}

type quickJoinResult struct { // success: all fields; failure: only success (js literals)
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
	DeskID    int          `json:"deskId"`
	PosID     int          `json:"posId"`
	PosInfo   []table.Seat `json:"posInfos"`
	HostPosID int          `json:"hostPosId"` // may be oneself
}

type trusteeChange struct { // trustee=true: server plays the seat with the bot strategy
	PosID    int    `json:"posId"`
	Trustee  bool   `json:"trustee"`
	UserName string `json:"userName,omitempty"` // attached on timeout auto-trustee
}

type hostChange struct { // posId=-1: desk has no host
	PosID    int    `json:"posId"`
	UserName string `json:"userName"`
}

type houseStatusChange struct { // userName always present ("" on leave)
	DeskID   int    `json:"deskId"`
	PosID    int    `json:"posId"`
	State    int    `json:"state"`
	UserName string `json:"userName"`
}

type posStatusChange struct { // isBot marks bot seats (frontend bot badge)
	PosID    int    `json:"posId"`
	State    int    `json:"state"`
	UserName string `json:"userName,omitempty"`
	IsBot    bool   `json:"isBot,omitempty"`
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

type userMessage struct { // js id was a guid() dropped by stringify; here an int (v-for key only)
	Type  string `json:"type"`
	PosID int    `json:"posId"`
	Msg   string `json:"msg"`
	ID    int64  `json:"id"`
	Time  string `json:"time"`
}

type handGroup struct { // HT: hearts tally incl. jokers (type=0), frontend display need
	ID    int         `json:"id"`
	Cards []card.Card `json:"cards"`
	HT    [15]int     `json:"ht"`
}

type gameStartPayload struct {
	Cards []handGroup `json:"cards"` // bug-for-bug: all 8 hands broadcast (frontend relies on it)
}

type gameOverPayload struct {
	Winner []int `json:"winner"`
	Loser  []int `json:"loser"`
	Score  int   `json:"score"`
	Ratio  int   `json:"ratio"`
}

type showTopCard struct {
	TopCards   []card.Card `json:"topCards"`
	DizhuPosID int         `json:"dizhuPosId"`
	Timeout    int         `json:"timeout"`
}

type ctxPlayCtx struct { // Key/Type: any to match js — "" on lead, value on play, omitted on pass
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
	// Replay: reconnect replay frame — GAME_START already has the latest
	// hand; deducting cards here desyncs (6 decks have duplicate cards).
	Replay bool `json:"replay,omitempty"`
	// Clear: table empty after this frame (trick collected / fresh lead) —
	// clients must clear play areas and the "shape to beat".
	Clear bool `json:"clear,omitempty"`
}

type playCardSuccess struct { // legacy js shape {data,tmpFeng,sumFeng}
	Data    []card.Card    `json:"data"`
	TmpFeng int            `json:"tmpFeng"`
	SumFeng map[string]int `json:"sumFeng"`
}
