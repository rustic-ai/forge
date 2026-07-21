package store

import (
	"time"

	"github.com/rustic-ai/forge/forge-go/helper/idgen"
	"gorm.io/gorm"
)

type JSONB map[string]interface{}
type JSONBList []map[string]interface{}
type JSONBStringList []string

type GuildStatus string

const (
	GuildStatusRequested     GuildStatus = "requested"
	GuildStatusStarting      GuildStatus = "starting"
	GuildStatusRunning       GuildStatus = "running"
	GuildStatusStopped       GuildStatus = "stopped"
	GuildStatusStopping      GuildStatus = "stopping"
	GuildStatusUnknown       GuildStatus = "unknown"
	GuildStatusWarning       GuildStatus = "warning"
	GuildStatusBacklogged    GuildStatus = "backlogged"
	GuildStatusError         GuildStatus = "error"
	GuildStatusPendingLaunch GuildStatus = "not_launched"
)

type AgentStatus string

const (
	AgentStatusPendingLaunch AgentStatus = "not_launched"
	AgentStatusStarting      AgentStatus = "starting"
	AgentStatusRunning       AgentStatus = "running"
	AgentStatusStopped       AgentStatus = "stopped"
	AgentStatusError         AgentStatus = "error"
	AgentStatusDeleted       AgentStatus = "deleted"
)

type RouteStatus string

const (
	RouteStatusActive  RouteStatus = "active"
	RouteStatusDeleted RouteStatus = "deleted"
)

type GuildRoutes struct {
	ID                       string  `gorm:"primaryKey;index"`
	GuildID                  *string `gorm:"primaryKey;index"`
	AgentName                *string
	AgentID                  *string
	AgentType                *string
	OriginSenderID           *string
	OriginSenderName         *string
	OriginTopic              *string
	OriginMessageFormat      *string
	CurrentMessageFormat     *string
	Transformer              RawJSON         `gorm:"type:jsonb"`
	DestinationTopics        JSONBStringList `gorm:"type:jsonb"`
	DestinationRecipientList JSONBList       `gorm:"type:jsonb"`
	DestinationPriority      *int
	MethodName               *string
	RouteTimes               int     `gorm:"default:1"`
	AgentStateUpdate         RawJSON `gorm:"type:jsonb"`
	GuildStateUpdate         RawJSON `gorm:"type:jsonb"`
	ProcessStatus            *string
	Reason                   *string
	Status                   RouteStatus `gorm:"default:active"`

	Guild *GuildModel `gorm:"foreignKey:GuildID;references:ID"` // Back reference
}

func (GuildRoutes) TableName() string {
	return "guild_routes"
}

func (r *GuildRoutes) normalizeDefaults() {
	ensureJSONBStringList(&r.DestinationTopics)
	ensureJSONBList(&r.DestinationRecipientList)
}

type AgentModel struct {
	ID                     string  `gorm:"primaryKey;index"`
	GuildID                *string `gorm:"primaryKey;index"`
	Name                   string  `gorm:"index"`
	Description            string
	ClassName              string
	Properties             JSONB           `gorm:"type:jsonb"`
	AdditionalTopics       JSONBStringList `gorm:"type:jsonb"`
	ListenToDefaultTopic   bool
	ActOnlyWhenTagged      bool
	DependencyMap          JSONB           `gorm:"type:jsonb"`
	AdditionalDependencies JSONBStringList `gorm:"type:jsonb"`
	ForgeExtraDeps         JSONBStringList `gorm:"type:jsonb"`
	Predicates             JSONB           `gorm:"type:jsonb"`
	Status                 AgentStatus     `gorm:"default:not_launched"`

	Guild *GuildModel `gorm:"foreignKey:GuildID;references:ID"` // Back reference
}

func (AgentModel) TableName() string {
	return "agents"
}

func (a *AgentModel) normalizeDefaults() {
	ensureJSONB(&a.Properties)
	ensureJSONBStringList(&a.AdditionalTopics)
	ensureJSONB(&a.DependencyMap)
	ensureJSONBStringList(&a.AdditionalDependencies)
	ensureJSONBStringList(&a.ForgeExtraDeps)
	ensureJSONB(&a.Predicates)
}

type GuildModel struct {
	ID              string      `gorm:"primaryKey;index" json:"id"`
	Name            string      `gorm:"index" json:"name"`
	Description     string      `json:"description"`
	ExecutionEngine string      `gorm:"default:rustic_ai.core.guild.execution.sync.sync_exec_engine.SyncExecutionEngine" json:"execution_engine"`
	BackendModule   string      `gorm:"default:rustic_ai.core.messaging.backend" json:"backend_module"`
	BackendClass    string      `gorm:"default:InMemoryMessagingBackend" json:"backend_class"`
	BackendConfig   JSONB       `gorm:"type:jsonb" json:"backend_config"`
	OrganizationID  string      `gorm:"index;not null" json:"organization_id"`
	DependencyMap   JSONB       `gorm:"type:jsonb" json:"dependency_map"`
	Status          GuildStatus `gorm:"default:unknown" json:"status"`

	Routes []GuildRoutes `gorm:"foreignKey:GuildID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	Agents []AgentModel  `gorm:"foreignKey:GuildID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
}

func (GuildModel) TableName() string {
	return "guilds"
}

func (g *GuildModel) normalizeDefaults() {
	ensureJSONB(&g.BackendConfig)
	ensureJSONB(&g.DependencyMap)
	if g.Routes == nil {
		g.Routes = []GuildRoutes{}
	}
	if g.Agents == nil {
		g.Agents = []AgentModel{}
	}
}

type GuildRelaunchModel struct {
	ID        string    `gorm:"primaryKey;index"`
	GuildID   string    `gorm:"index"`
	Timestamp time.Time `gorm:"autoCreateTime"`
}

func (GuildRelaunchModel) TableName() string {
	return "guilds_relaunch"
}

func (g *GuildRelaunchModel) BeforeCreate(tx *gorm.DB) (err error) {
	if g.ID == "" {
		g.ID = idgen.NewShortUUID()
	}
	return nil
}

func (r *GuildRoutes) BeforeCreate(tx *gorm.DB) (err error) {
	if r.ID == "" {
		r.ID = idgen.NewShortUUID()
	}
	r.normalizeDefaults()
	return nil
}

func (r *GuildRoutes) AfterFind(tx *gorm.DB) (err error) {
	r.normalizeDefaults()
	return nil
}

func (a *AgentModel) BeforeCreate(tx *gorm.DB) (err error) {
	a.normalizeDefaults()
	return nil
}

func (a *AgentModel) AfterFind(tx *gorm.DB) (err error) {
	a.normalizeDefaults()
	return nil
}

func (g *GuildModel) BeforeCreate(tx *gorm.DB) (err error) {
	g.normalizeDefaults()
	return nil
}

func (g *GuildModel) AfterFind(tx *gorm.DB) (err error) {
	g.normalizeDefaults()
	return nil
}
