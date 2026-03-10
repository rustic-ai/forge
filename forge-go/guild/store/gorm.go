package store

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	_ "modernc.org/sqlite"
)

const (
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"
)

type gormStore struct {
	db *gorm.DB
}

// NewGormStore initializes a new GORM-backed Store instance.
// Supported drivers are "sqlite" and "postgres".
// DSN is the data source name (e.g., file path for sqlite, connection string for postgres).
func NewGormStore(driverName, dsn string) (Store, error) {
	var dialector gorm.Dialector

	switch strings.ToLower(driverName) {
	case DriverSQLite:
		cleanDSN := normalizeSQLiteDSN(dsn)
		if err := ensureSQLiteDir(cleanDSN); err != nil {
			return nil, fmt.Errorf("failed to prepare sqlite directory: %w", err)
		}
		// Use the pure-Go modernc driver so SQLite works when binaries are built with CGO disabled.
		dialector = gormsqlite.New(gormsqlite.Config{
			DriverName: "sqlite",
			DSN:        cleanDSN,
		})
	case DriverPostgres:
		dialector = postgres.Open(normalizePostgresDSN(dsn))
	default:
		return nil, fmt.Errorf("unsupported database driver: %s", driverName)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: newGormLogger(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	if strings.ToLower(driverName) == DriverSQLite {
		if err := configureSQLite(db, normalizeSQLiteDSN(dsn)); err != nil {
			return nil, fmt.Errorf("failed to configure sqlite connection: %w", err)
		}
	}

	// AutoMigrate the schema
	if err := db.AutoMigrate(
		&GuildModel{}, &GuildRelaunchModel{}, &AgentModel{}, &GuildRoutes{},
		&Blueprint{}, &BlueprintSharedWithOrganization{}, &BlueprintCommand{},
		&BlueprintStarterPrompt{}, &Tag{}, &BlueprintTag{}, &BlueprintCategory{},
		&BlueprintReview{}, &CatalogAgentEntry{}, &BlueprintAgentLink{},
		&BlueprintGuild{}, &UserGuild{}, &AgentIcon{}, &BlueprintAgentIcon{},
		&Board{}, &BoardMessage{},
	); err != nil {
		return nil, fmt.Errorf("failed to auto-migrate database schema: %w", err)
	}
	if err := runSchemaParityMigrations(db); err != nil {
		return nil, fmt.Errorf("failed to run schema parity migrations: %w", err)
	}

	return &gormStore{db: db}, nil
}

func newGormLogger() gormlogger.Interface {
	return gormlogger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags),
		gormlogger.Config{
			SlowThreshold:             time.Second,
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)
}

func configureSQLite(db *gorm.DB, dsn string) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}

	// SQLite is used as an embedded local metastore in the desktop flow. Keeping
	// a single shared connection avoids parallel writers fighting each other.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)

	if err := applySQLitePragmas(sqlDB, dsn); err != nil {
		return err
	}

	return nil
}

func applySQLitePragmas(db *sql.DB, dsn string) error {
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000;`); err != nil {
		return err
	}
	if isSQLiteFileDSN(dsn) {
		if _, err := db.Exec(`PRAGMA journal_mode = WAL;`); err != nil {
			return err
		}
		if _, err := db.Exec(`PRAGMA synchronous = NORMAL;`); err != nil {
			return err
		}
	}
	return nil
}

func runSchemaParityMigrations(db *gorm.DB) error {
	if db.Dialector.Name() != DriverPostgres {
		return nil
	}

	// Align historic Forge schemas with Python SQLModel table/column names.
	queries := []string{
		// Python table names
		`DO $$
		BEGIN
			IF to_regclass('public.blueprint_shared_with_organization') IS NOT NULL
			   AND to_regclass('public.blueprintsharedwithorganization') IS NULL THEN
				ALTER TABLE blueprint_shared_with_organization RENAME TO blueprintsharedwithorganization;
			END IF;
		END $$;`,
		`DO $$
		BEGIN
			IF to_regclass('public.blueprint_review') IS NOT NULL
			   AND to_regclass('public.blueprint_reviews') IS NULL THEN
				ALTER TABLE blueprint_review RENAME TO blueprint_reviews;
			END IF;
		END $$;`,

		// Python review columns
		`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'blueprint_reviews' AND column_name = 'author_id'
			) THEN
				ALTER TABLE blueprint_reviews RENAME COLUMN author_id TO user_id;
			END IF;
		END $$;`,
		`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'blueprint_reviews' AND column_name = 'review_text'
			) THEN
				ALTER TABLE blueprint_reviews RENAME COLUMN review_text TO review;
			END IF;
		END $$;`,

		// Agents PK must be composite (id, guild_id), matching SQLModel.
		`ALTER TABLE IF EXISTS agents DROP CONSTRAINT IF EXISTS agents_pkey;`,
		`ALTER TABLE IF EXISTS agents ALTER COLUMN id SET NOT NULL;`,
		`ALTER TABLE IF EXISTS agents ALTER COLUMN guild_id SET NOT NULL;`,
		`ALTER TABLE IF EXISTS agents ADD CONSTRAINT agents_pkey PRIMARY KEY (id, guild_id);`,

		// Drop Go-only legacy columns so schema matches Python SQLModel exactly.
		`ALTER TABLE IF EXISTS blueprint_command DROP COLUMN IF EXISTS created_at;`,
		`ALTER TABLE IF EXISTS blueprint_starter_prompt DROP COLUMN IF EXISTS created_at;`,
		`ALTER TABLE IF EXISTS agent_entry DROP COLUMN IF EXISTS schema;`,
		`ALTER TABLE IF EXISTS agent_entry DROP COLUMN IF EXISTS icon;`,
		`ALTER TABLE IF EXISTS agent_entry DROP COLUMN IF EXISTS is_official;`,
		`ALTER TABLE IF EXISTS agent_entry DROP COLUMN IF EXISTS created_at;`,
		`ALTER TABLE IF EXISTS agent_entry DROP COLUMN IF EXISTS updated_at;`,
		`ALTER TABLE IF EXISTS guild_routes DROP COLUMN IF EXISTS mark_forwarded;`,
		`ALTER TABLE IF EXISTS agents DROP COLUMN IF EXISTS resources;`,
		`ALTER TABLE IF EXISTS agents DROP COLUMN IF EXISTS qos;`,
		`ALTER TABLE IF EXISTS guilds DROP COLUMN IF EXISTS gateway;`,
		`ALTER TABLE IF EXISTS guilds DROP COLUMN IF EXISTS created_at;`,
		`ALTER TABLE IF EXISTS guilds DROP COLUMN IF EXISTS updated_at;`,
		`ALTER TABLE IF EXISTS guilds DROP COLUMN IF EXISTS deleted_at;`,
	}

	for _, q := range queries {
		if err := db.Exec(q).Error; err != nil {
			return err
		}
	}
	return nil
}

func ResolveDriverAndDSN(rawDSN string) (driverName string, dsn string) {
	trimmed := strings.TrimSpace(rawDSN)
	if isPostgresDSN(trimmed) {
		return DriverPostgres, normalizePostgresDSN(trimmed)
	}
	return DriverSQLite, normalizeSQLiteDSN(trimmed)
}

func isPostgresDSN(dsn string) bool {
	lower := strings.ToLower(strings.TrimSpace(dsn))
	return strings.HasPrefix(lower, "postgres://") ||
		strings.HasPrefix(lower, "postgresql://") ||
		strings.HasPrefix(lower, "postgresql+psycopg://") ||
		strings.HasPrefix(lower, "postgresql+psycopg2://") ||
		strings.HasPrefix(lower, "postgres:")
}

func normalizePostgresDSN(dsn string) string {
	out := strings.TrimSpace(dsn)
	replacer := strings.NewReplacer(
		"postgresql+psycopg://", "postgres://",
		"postgresql+psycopg2://", "postgres://",
		"postgresql://", "postgres://",
	)
	return replacer.Replace(out)
}

func normalizeSQLiteDSN(dsn string) string {
	clean := strings.TrimSpace(strings.TrimPrefix(dsn, "sqlite://"))
	return expandLeadingTilde(clean)
}

func expandLeadingTilde(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func ensureSQLiteDir(dsn string) error {
	if dsn == "" || dsn == ":memory:" || strings.HasPrefix(dsn, "file:") {
		return nil
	}
	parent := filepath.Dir(dsn)
	if parent == "." || parent == "/" {
		return nil
	}
	return os.MkdirAll(parent, 0o755)
}

func isSQLiteFileDSN(dsn string) bool {
	switch {
	case dsn == "", dsn == ":memory:", strings.HasPrefix(dsn, "file::memory:"):
		return false
	case strings.HasPrefix(dsn, "file:"):
		return !strings.Contains(dsn, "mode=memory")
	default:
		return true
	}
}

func (s *gormStore) CreateGuild(guild *GuildModel) error {
	return s.db.Create(guild).Error
}

func (s *gormStore) CreateGuildWithAgents(guild *GuildModel, agents []AgentModel) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(guild).Error; err != nil {
			return err
		}
		for i := range agents {
			if err := tx.Create(&agents[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *gormStore) GetGuild(id string) (*GuildModel, error) {
	var guild GuildModel
	err := s.db.Preload("Agents").Preload("Routes").Where("id = ?", id).First(&guild).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &guild, nil
}

func (s *gormStore) GetGuildByName(name string) (*GuildModel, error) {
	var guild GuildModel
	err := s.db.Preload("Agents").Preload("Routes").Where("name = ?", name).First(&guild).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &guild, nil
}

func (s *gormStore) ListGuilds() ([]GuildModel, error) {
	var guilds []GuildModel
	err := s.db.Preload("Agents").Preload("Routes").Find(&guilds).Error
	return guilds, err
}

func (s *gormStore) UpdateGuildStatus(id string, status GuildStatus) error {
	result := s.db.Model(&GuildModel{}).Where("id = ?", id).Update("status", status)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *gormStore) UpdateGuild(guild *GuildModel) error {
	return s.db.Save(guild).Error
}

func (s *gormStore) DeleteGuild(id string) error {
	result := s.db.Where("id = ?", id).Delete(&GuildModel{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *gormStore) PurgeGuild(guild *GuildModel) error {
	s.db.Unscoped().Where("guild_id = ?", guild.ID).Delete(&AgentModel{})
	s.db.Unscoped().Where("guild_id = ?", guild.ID).Delete(&GuildRoutes{})
	return s.db.Unscoped().Delete(guild).Error
}

func (s *gormStore) CreateGuildRelaunch(entry *GuildRelaunchModel) error {
	return s.db.Create(entry).Error
}

func (s *gormStore) CreateAgent(agent *AgentModel) error {
	return s.db.Create(agent).Error
}

func (s *gormStore) GetAgent(guildID, id string) (*AgentModel, error) {
	var agent AgentModel
	err := s.db.Where("guild_id = ? AND id = ?", guildID, id).First(&agent).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &agent, nil
}

func (s *gormStore) ListAgentsByGuild(guildID string) ([]AgentModel, error) {
	var agents []AgentModel
	err := s.db.Where("guild_id = ?", guildID).Find(&agents).Error
	return agents, err
}

func (s *gormStore) UpdateAgentStatus(guildID, id string, status AgentStatus) error {
	result := s.db.Model(&AgentModel{}).Where("guild_id = ? AND id = ?", guildID, id).Update("status", status)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *gormStore) UpdateAgent(agent *AgentModel) error {
	return s.db.Save(agent).Error
}

func (s *gormStore) DeleteAgent(guildID, id string) error {
	result := s.db.Where("guild_id = ? AND id = ?", guildID, id).Delete(&AgentModel{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *gormStore) CreateGuildRoute(route *GuildRoutes) error {
	return s.db.Create(route).Error
}

func (s *gormStore) UpdateGuildRouteStatus(guildID, routeID string, status RouteStatus) error {
	result := s.db.Model(&GuildRoutes{}).
		Where("guild_id = ? AND id = ?", guildID, routeID).
		Update("status", status)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *gormStore) ProcessHeartbeatStatus(
	guildID, agentID string,
	agentStatus AgentStatus,
	guildStatus GuildStatus,
) (effectiveAgentStatus AgentStatus, agentFound bool, err error) {
	effectiveAgentStatus = agentStatus

	err = s.db.Transaction(func(tx *gorm.DB) error {
		var guild GuildModel
		if err := tx.Where("id = ?", guildID).First(&guild).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		var agent AgentModel
		if err := tx.Where("guild_id = ? AND id = ?", guildID, agentID).First(&agent).Error; err == nil {
			agentFound = true
			effectiveAgentStatus = agent.Status
			if agent.Status != AgentStatusDeleted {
				effectiveAgentStatus = agentStatus
				if agent.Status != agentStatus {
					agent.Status = agentStatus
					if err := tx.Save(&agent).Error; err != nil {
						return err
					}
				}
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		if guild.Status != guildStatus {
			guild.Status = guildStatus
			if err := tx.Save(&guild).Error; err != nil {
				return err
			}
		}
		return nil
	})

	return effectiveAgentStatus, agentFound, err
}

func (s *gormStore) Close() error {
	db, err := s.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

func (s *gormStore) CreateBoard(board *Board) error {
	return s.db.Create(board).Error
}

func (s *gormStore) GetBoard(id string) (*Board, error) {
	var board Board
	err := s.db.Where("id = ?", id).First(&board).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &board, nil
}

func (s *gormStore) GetBoardsByGuild(guildID string) ([]Board, error) {
	var boards []Board
	err := s.db.Where("guild_id = ?", guildID).Find(&boards).Error
	return boards, err
}

func (s *gormStore) AddMessageToBoard(boardID, messageID string) error {
	// Check board exists
	var board Board
	if err := s.db.Where("id = ?", boardID).First(&board).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}

	// Check for duplicate
	var existing BoardMessage
	err := s.db.Where("board_id = ? AND message_id = ?", boardID, messageID).First(&existing).Error
	if err == nil {
		return ErrConflict
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	return s.db.Create(&BoardMessage{BoardID: boardID, MessageID: messageID}).Error
}

func (s *gormStore) GetBoardMessageIDs(boardID string) ([]string, error) {
	// Check board exists
	var board Board
	if err := s.db.Where("id = ?", boardID).First(&board).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	var ids []string
	err := s.db.Model(&BoardMessage{}).Where("board_id = ?", boardID).Pluck("message_id", &ids).Error
	return ids, err
}

func (s *gormStore) RemoveMessageFromBoard(boardID, messageID string) error {
	// Check board exists
	var board Board
	if err := s.db.Where("id = ?", boardID).First(&board).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}

	// Check message exists on board
	var msg BoardMessage
	if err := s.db.Where("board_id = ? AND message_id = ?", boardID, messageID).First(&msg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}

	return s.db.Where("board_id = ? AND message_id = ?", boardID, messageID).Delete(&BoardMessage{}).Error
}
