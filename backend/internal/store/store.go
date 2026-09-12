// Package store 对局战绩的 SQLite 持久化（GORM + glebarez/sqlite 纯 Go 驱动）。
// 所有调用方为 hub（单互斥锁下串行），包内不再加锁。
// 登录仅以用户名为唯一身份（不落库），此处只保存战绩。
package store

import (
	"errors"
	"fmt"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// GamePlayer 一局中单个玩家的摘要
type GamePlayer struct {
	PosID     int    `json:"posId"`
	UserName  string `json:"userName"`
	SumFeng   int    `json:"sumFeng"`
	Remaining int    `json:"remaining"` // 终局剩余手牌数
	Win       bool   `json:"win"`
}

// GameRecord 一局的对局记录；EndReason: normal（正常打完）/ escape（有人逃跑终止）
type GameRecord struct {
	GameID     int64
	DeskID     int
	StartedAt  time.Time
	EndedAt    time.Time
	EndReason  string
	Winner     []int // 仅 GameDetail 输出填充（落库由 Players[].Win 承载，SaveGame 忽略此字段）；escape 局为 nil
	Team0Score int   // 偶数队最终得分（正常局胜队含带走分）
	Team1Score int   // 奇数队最终得分
	Players    []GamePlayer
}

// HistoryItem 个人战绩列表条目
type HistoryItem struct {
	GameID    int64  `json:"gameId"`
	DeskID    int    `json:"deskId"`
	EndedAt   string `json:"endedAt"`
	EndReason string `json:"endReason"`
	Win       bool   `json:"win"`
	TeamScore int    `json:"teamScore"` // 本队最终得分
	OppScore  int    `json:"oppScore"`  // 对手队最终得分
	SumFeng   int    `json:"sumFeng"`   // 本人收分
	Remaining int    `json:"remaining"`
	PlayerNum int    `json:"playerNum"` // 本局记录的玩家人数
}

// gameRow games 表；时间以 RFC3339 文本落库
type gameRow struct {
	ID         int64       `gorm:"primaryKey;autoIncrement"`
	DeskID     int         `gorm:"not null"`
	StartedAt  string      `gorm:"not null"`
	EndedAt    string      `gorm:"not null"`
	EndReason  string      `gorm:"not null"`
	Team0Score int         `gorm:"not null"`
	Team1Score int         `gorm:"not null"`
	Players    []playerRow `gorm:"foreignKey:GameID"` // 仅读路径（Preload）使用；写入走显式事务
}

func (gameRow) TableName() string { return "games" }

// playerRow game_players 表；username+game_id 复合索引服务战绩列表查询
type playerRow struct {
	GameID    int64  `gorm:"not null;index:idx_game_players_user,priority:2,sort:desc"`
	PosID     int    `gorm:"not null"`
	UserName  string `gorm:"column:username;not null;index:idx_game_players_user,priority:1"`
	SumFeng   int    `gorm:"not null"`
	Remaining int    `gorm:"not null"`
	Win       bool   `gorm:"not null"`
}

func (playerRow) TableName() string { return "game_players" }

type Store struct {
	db *gorm.DB
}

// Open 打开（或创建）数据库并 AutoMigrate 建表
func Open(path string) (*Store, error) {
	// PRAGMA 经 DSN 下发：busy_timeout 兜底偶发锁竞争，WAL 提升读写并发
	db, err := gorm.Open(sqlite.Open(path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&gameRow{}, &playerRow{}); err != nil {
		if sqlDB, derr := db.DB(); derr == nil {
			sqlDB.Close()
		}
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// SaveGame 落库一局结果，返回自增对局 ID。
// 关联保存（Create 带子切片）会先插子行且带 upsert 语义，这里改为显式事务两步插入。
func (s *Store) SaveGame(rec GameRecord) (int64, error) {
	g := gameRow{
		DeskID:     rec.DeskID,
		StartedAt:  rec.StartedAt.Format(time.RFC3339),
		EndedAt:    rec.EndedAt.Format(time.RFC3339),
		EndReason:  rec.EndReason,
		Team0Score: rec.Team0Score,
		Team1Score: rec.Team1Score,
	}
	players := make([]playerRow, 0, len(rec.Players))
	for _, p := range rec.Players {
		players = append(players, playerRow{
			PosID:     p.PosID,
			UserName:  p.UserName,
			SumFeng:   p.SumFeng,
			Remaining: p.Remaining,
			Win:       p.Win,
		})
	}

	var id int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&g).Error; err != nil {
			return err
		}
		id = g.ID
		if len(players) == 0 {
			return nil
		}
		for i := range players {
			players[i].GameID = g.ID
		}
		return tx.Create(&players).Error
	})
	return id, err
}

// fmtTime RFC3339 原文转展示格式；解析失败（历史脏数据）原样返回
func fmtTime(raw string) string {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Format("2006-01-02 15:04:05")
	}
	return raw
}

// HistoryList 某玩家参与的战绩（按结束时间倒序），返回条目与总数
func (s *Store) HistoryList(userName string, limit, offset int) ([]HistoryItem, int, error) {
	var total int64
	if err := s.db.Model(&playerRow{}).Where("username = ?", userName).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var games []gameRow
	if err := s.db.
		Joins("JOIN game_players usr ON usr.game_id = games.id AND usr.username = ?", userName).
		Preload("Players", func(tx *gorm.DB) *gorm.DB { return tx.Order("pos_id") }).
		Order("ended_at DESC, id DESC").
		Limit(limit).Offset(offset).
		Find(&games).Error; err != nil {
		return nil, 0, err
	}

	var list []HistoryItem
	for _, g := range games {
		it := HistoryItem{
			GameID:    g.ID,
			DeskID:    g.DeskID,
			EndedAt:   fmtTime(g.EndedAt),
			EndReason: g.EndReason,
			PlayerNum: len(g.Players),
		}
		for _, p := range g.Players {
			if p.UserName != userName {
				continue
			}
			it.Win, it.SumFeng, it.Remaining = p.Win, p.SumFeng, p.Remaining
			it.TeamScore, it.OppScore = g.Team0Score, g.Team1Score
			if p.PosID%2 != 0 { // 奇数队视角互换
				it.TeamScore, it.OppScore = g.Team1Score, g.Team0Score
			}
		}
		list = append(list, it)
	}
	return list, int(total), nil
}

// GameDetail 单局详情；不存在返回 false
func (s *Store) GameDetail(gameID int64) (GameRecord, bool, error) {
	var g gameRow
	err := s.db.Preload("Players", func(tx *gorm.DB) *gorm.DB { return tx.Order("pos_id") }).
		First(&g, gameID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return GameRecord{GameID: gameID}, false, nil
	}
	if err != nil {
		return GameRecord{GameID: gameID}, false, err
	}
	startedAt, err := time.Parse(time.RFC3339, g.StartedAt)
	if err != nil {
		return GameRecord{GameID: gameID}, false, fmt.Errorf("started_at 解析失败: %w", err)
	}
	endedAt, err := time.Parse(time.RFC3339, g.EndedAt)
	if err != nil {
		return GameRecord{GameID: gameID}, false, fmt.Errorf("ended_at 解析失败: %w", err)
	}

	rec := GameRecord{
		GameID:     gameID,
		DeskID:     g.DeskID,
		StartedAt:  startedAt,
		EndedAt:    endedAt,
		EndReason:  g.EndReason,
		Team0Score: g.Team0Score,
		Team1Score: g.Team1Score,
	}
	for _, p := range g.Players {
		if p.Win {
			rec.Winner = append(rec.Winner, p.PosID)
		}
		rec.Players = append(rec.Players, GamePlayer{
			PosID:     p.PosID,
			UserName:  p.UserName,
			SumFeng:   p.SumFeng,
			Remaining: p.Remaining,
			Win:       p.Win,
		})
	}
	return rec, true, nil
}
