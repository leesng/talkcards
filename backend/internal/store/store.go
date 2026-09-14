// Package store persists game records to SQLite (GORM + the pure-Go
// glebarez/sqlite driver). All callers are hub (serialized under a single
// mutex), so no locking here. Login identity is the user name only (not
// persisted); only game records are stored.
package store

import (
	"errors"
	"fmt"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// GamePlayer is one player's summary within a game.
type GamePlayer struct {
	PosID     int    `json:"posId"`
	UserName  string `json:"userName"`
	SumFeng   int    `json:"sumFeng"`
	Remaining int    `json:"remaining"` // cards left at game over
	Win       bool   `json:"win"`
}

// GameRecord is one game's record; EndReason: normal (played out) / escape
// (aborted by a player leaving).
type GameRecord struct {
	GameID     int64
	DeskID     int
	StartedAt  time.Time
	EndedAt    time.Time
	EndReason  string
	Winner     []int // filled only for GameDetail output (persistence uses Players[].Win; SaveGame ignores it); nil for escape games
	Team0Score int   // even team's final score (normal games include the takeover points)
	Team1Score int   // odd team's final score
	Players    []GamePlayer
}

// HistoryItem is one entry of a player's history list.
type HistoryItem struct {
	GameID    int64  `json:"gameId"`
	DeskID    int    `json:"deskId"`
	EndedAt   string `json:"endedAt"`
	EndReason string `json:"endReason"`
	Win       bool   `json:"win"`
	TeamScore int    `json:"teamScore"` // player's own team final score
	OppScore  int    `json:"oppScore"`  // opposing team final score
	SumFeng   int    `json:"sumFeng"`   // player's own captured points
	Remaining int    `json:"remaining"`
	PlayerNum int    `json:"playerNum"` // number of players recorded for the game
}

// gameRow is the games table; times stored as RFC3339 text.
type gameRow struct {
	ID         int64       `gorm:"primaryKey;autoIncrement"`
	DeskID     int         `gorm:"not null"`
	StartedAt  string      `gorm:"not null"`
	EndedAt    string      `gorm:"not null"`
	EndReason  string      `gorm:"not null"`
	Team0Score int         `gorm:"not null"`
	Team1Score int         `gorm:"not null"`
	Players    []playerRow `gorm:"foreignKey:GameID"` // read path only (Preload); writes use an explicit transaction
}

func (gameRow) TableName() string { return "games" }

// playerRow is the game_players table; the username+game_id composite index
// serves the history-list query.
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

// Open opens (or creates) the database and auto-migrates the schema.
func Open(path string) (*Store, error) {
	// PRAGMAs via DSN: busy_timeout absorbs occasional lock contention, WAL improves read/write concurrency.
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

// SaveGame persists one game result and returns its auto-incremented ID.
// GORM's association save (Create with a child slice) inserts children first
// with upsert semantics, so this uses an explicit two-step transaction instead.
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

// fmtTime converts an RFC3339 timestamp to display format; unparseable values
// (dirty historical data) are returned as-is.
func fmtTime(raw string) string {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Format("2006-01-02 15:04:05")
	}
	return raw
}

// HistoryList lists a player's games (newest first), returning the entries
// and the total count.
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
			if p.PosID%2 != 0 { // odd team sees the scores swapped
				it.TeamScore, it.OppScore = g.Team1Score, g.Team0Score
			}
		}
		list = append(list, it)
	}
	return list, int(total), nil
}

// GameDetail fetches one game's detail; false if not found.
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
