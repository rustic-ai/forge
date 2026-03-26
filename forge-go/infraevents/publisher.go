package infraevents

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/rustic-ai/forge/forge-go/helper/idgen"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

const (
	Topic         = "infra_events_topic"
	Format        = "rustic_ai.forge.runtime.InfraEvent"
	SchemaVersion = 1
	SeverityInfo  = "info"
	SeverityWarn  = "warning"
	SeverityError = "error"
	DefaultSender = "forge_runtime"
)

type Source struct {
	Component  string `json:"component"`
	InstanceID string `json:"instance_id,omitempty"`
}

type Event struct {
	SchemaVersion  int            `json:"schema_version"`
	EventID        string         `json:"event_id"`
	Kind           string         `json:"kind"`
	Severity       string         `json:"severity"`
	Timestamp      string         `json:"timestamp"`
	GuildID        string         `json:"guild_id"`
	AgentID        string         `json:"agent_id,omitempty"`
	OrganizationID string         `json:"organization_id,omitempty"`
	RequestID      string         `json:"request_id,omitempty"`
	NodeID         string         `json:"node_id,omitempty"`
	Source         Source         `json:"source"`
	Attempt        *int           `json:"attempt,omitempty"`
	Message        string         `json:"message"`
	Detail         map[string]any `json:"detail,omitempty"`
}

type EmitParams struct {
	Kind             string
	Severity         string
	GuildID          string
	AgentID          string
	OrganizationID   string
	RequestID        string
	NodeID           string
	SourceComponent  string
	SourceInstanceID string
	Attempt          *int
	Message          string
	Detail           map[string]any
}

type Publisher struct {
	backend messaging.Backend
	mu      sync.Mutex
	gen     *protocol.GemstoneGenerator
}

func NewPublisher(backend messaging.Backend) (*Publisher, error) {
	gen, err := protocol.NewGemstoneGenerator(0)
	if err != nil {
		return nil, err
	}
	return &Publisher{backend: backend, gen: gen}, nil
}

func (p *Publisher) Emit(ctx context.Context, params EmitParams) error {
	if p == nil || p.backend == nil {
		return nil
	}

	p.mu.Lock()
	gid, err := p.gen.Generate(protocol.PriorityNormal)
	p.mu.Unlock()
	if err != nil {
		return err
	}

	event := Event{
		SchemaVersion:  SchemaVersion,
		EventID:        idgen.NewShortUUID(),
		Kind:           params.Kind,
		Severity:       params.Severity,
		Timestamp:      time.Now().UTC().Format(time.RFC3339Nano),
		GuildID:        params.GuildID,
		AgentID:        params.AgentID,
		OrganizationID: params.OrganizationID,
		RequestID:      params.RequestID,
		NodeID:         params.NodeID,
		Source: Source{
			Component:  params.SourceComponent,
			InstanceID: params.SourceInstanceID,
		},
		Attempt: params.Attempt,
		Message: params.Message,
		Detail:  params.Detail,
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}

	msg := protocol.NewMessageFromGemstoneID(gid)
	msg.Topics = protocol.TopicsFromString(Topic)
	senderID := DefaultSender
	if params.SourceComponent != "" {
		senderID = params.SourceComponent
	}
	msg.Sender = protocol.AgentTag{ID: &senderID}
	msg.Format = Format
	msg.Payload = payload

	return p.backend.PublishMessage(ctx, params.GuildID, Topic, &msg)
}
