package protocol

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rustic-ai/forge/forge-go/helper/idgen"
	"gopkg.in/yaml.v3"
)

// AgentTag represents a tag that can be assigned to an agent.
type AgentTag struct {
	ID   *string `json:"id,omitempty"`
	Name *string `json:"name,omitempty"`
}

func NewAgentTag() AgentTag {
	return AgentTag{}
}

func (a *AgentTag) Normalize() {}

func (a *AgentTag) UnmarshalJSON(data []byte) error {
	type alias AgentTag
	raw := alias(NewAgentTag())
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*a = AgentTag(raw)
	a.Normalize()
	return nil
}

// DependencySpec maps a dependency to a resolver class.
type DependencySpec struct {
	ClassName    string                 `json:"class_name" yaml:"class_name"`
	ProvidedType string                 `json:"provided_type,omitempty" yaml:"provided_type,omitempty"`
	Properties   map[string]interface{} `json:"properties,omitempty" yaml:"properties,omitempty"`
}

func NewDependencySpec(className string) DependencySpec {
	d := DependencySpec{
		ClassName:  className,
		Properties: map[string]interface{}{},
	}
	d.Normalize()
	return d
}

func (d *DependencySpec) Normalize() {
	d.ClassName = strings.TrimSpace(d.ClassName)
	d.ProvidedType = strings.TrimSpace(d.ProvidedType)
	if d.Properties == nil {
		d.Properties = map[string]interface{}{}
	}
}

func (d *DependencySpec) UnmarshalJSON(data []byte) error {
	type alias DependencySpec
	raw := alias(NewDependencySpec(""))
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*d = DependencySpec(raw)
	d.Normalize()
	return nil
}

type SecretNeed struct {
	Key      string `json:"key" yaml:"key"`
	Label    string `json:"label,omitempty" yaml:"label,omitempty"`
	Optional *bool  `json:"optional,omitempty" yaml:"optional,omitempty"`
}

func NewSecretNeed(key string) SecretNeed {
	s := SecretNeed{Key: key, Label: key}
	s.Normalize()
	return s
}

func (s *SecretNeed) Normalize() {
	s.Key = strings.TrimSpace(s.Key)
	s.Label = strings.TrimSpace(s.Label)
	if s.Label == "" {
		s.Label = s.Key
	}
}

func (s *SecretNeed) UnmarshalJSON(data []byte) error {
	type alias SecretNeed
	raw := alias(NewSecretNeed(""))
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*s = SecretNeed(raw)
	s.Normalize()
	return nil
}

// UnmarshalYAML accepts both plain string ("MY_KEY") and struct ({key: MY_KEY}) forms.
func (s *SecretNeed) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		*s = NewSecretNeed(value.Value)
		return nil
	}
	type alias SecretNeed
	raw := alias(NewSecretNeed(""))
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*s = SecretNeed(raw)
	s.Normalize()
	return nil
}

type OAuthNeed struct {
	Provider string   `json:"provider" yaml:"provider"`
	Label    string   `json:"label,omitempty" yaml:"label,omitempty"`
	Scopes   []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`
	Optional *bool    `json:"optional,omitempty" yaml:"optional,omitempty"`
}

func NewOAuthNeed(provider string) OAuthNeed {
	o := OAuthNeed{
		Provider: provider,
		Label:    strings.ToUpper(strings.TrimSpace(provider)) + "_TOKEN",
		Scopes:   []string{},
	}
	o.Normalize()
	return o
}

func (o *OAuthNeed) Normalize() {
	o.Provider = strings.TrimSpace(o.Provider)
	o.Label = strings.TrimSpace(o.Label)
	if o.Label == "" {
		o.Label = strings.ToUpper(o.Provider) + "_TOKEN"
	}
	if o.Scopes == nil {
		o.Scopes = []string{}
	}
	for i := range o.Scopes {
		o.Scopes[i] = strings.TrimSpace(o.Scopes[i])
	}
}

func (o *OAuthNeed) UnmarshalJSON(data []byte) error {
	type alias OAuthNeed
	raw := alias{Scopes: []string{}}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*o = OAuthNeed(raw)
	o.Normalize()
	return nil
}

func (o *OAuthNeed) UnmarshalYAML(value *yaml.Node) error {
	type alias OAuthNeed
	raw := alias{Scopes: []string{}}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*o = OAuthNeed(raw)
	o.Normalize()
	return nil
}

type CapabilityNeed struct {
	Type  string `json:"type" yaml:"type"`
	Label string `json:"label,omitempty" yaml:"label,omitempty"`
}

func NewCapabilityNeed(capabilityType string) CapabilityNeed {
	c := CapabilityNeed{Type: capabilityType}
	c.Normalize()
	return c
}

func (c *CapabilityNeed) Normalize() {
	c.Type = strings.TrimSpace(c.Type)
	c.Label = strings.TrimSpace(c.Label)
}

func (c *CapabilityNeed) UnmarshalJSON(data []byte) error {
	type alias CapabilityNeed
	raw := alias(NewCapabilityNeed(""))
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*c = CapabilityNeed(raw)
	c.Normalize()
	return nil
}

type NetworkNeeds struct {
	Allow []string `json:"allow,omitempty" yaml:"allow,omitempty"`
}

func NewNetworkNeeds() NetworkNeeds {
	n := NetworkNeeds{Allow: []string{}}
	n.Normalize()
	return n
}

func (n *NetworkNeeds) Normalize() {
	if n.Allow == nil {
		n.Allow = []string{}
	}
	for i := range n.Allow {
		n.Allow[i] = strings.TrimSpace(n.Allow[i])
	}
}

func (n *NetworkNeeds) UnmarshalJSON(data []byte) error {
	type alias NetworkNeeds
	raw := alias(NewNetworkNeeds())
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*n = NetworkNeeds(raw)
	n.Normalize()
	return nil
}

type FilesystemAccessNeed struct {
	Path string `json:"path" yaml:"path"`
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"`
}

func NewFilesystemAccessNeed(path string) FilesystemAccessNeed {
	f := FilesystemAccessNeed{Path: path}
	f.Normalize()
	return f
}

func (f *FilesystemAccessNeed) Normalize() {
	f.Path = strings.TrimSpace(f.Path)
	f.Mode = strings.TrimSpace(f.Mode)
}

func (f *FilesystemAccessNeed) UnmarshalJSON(data []byte) error {
	type alias FilesystemAccessNeed
	raw := alias(NewFilesystemAccessNeed(""))
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*f = FilesystemAccessNeed(raw)
	f.Normalize()
	return nil
}

type FilesystemNeeds struct {
	Allow []FilesystemAccessNeed `json:"allow,omitempty" yaml:"allow,omitempty"`
}

func NewFilesystemNeeds() FilesystemNeeds {
	f := FilesystemNeeds{Allow: []FilesystemAccessNeed{}}
	f.Normalize()
	return f
}

func (f *FilesystemNeeds) Normalize() {
	if f.Allow == nil {
		f.Allow = []FilesystemAccessNeed{}
	}
	for i := range f.Allow {
		f.Allow[i].Normalize()
	}
}

func (f *FilesystemNeeds) UnmarshalJSON(data []byte) error {
	type alias FilesystemNeeds
	raw := alias(NewFilesystemNeeds())
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*f = FilesystemNeeds(raw)
	f.Normalize()
	return nil
}

type NeedsSpec struct {
	Secrets      []SecretNeed     `json:"secrets,omitempty" yaml:"secrets,omitempty"`
	OAuth        []OAuthNeed      `json:"oauth,omitempty" yaml:"oauth,omitempty"`
	Capabilities []CapabilityNeed `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	Network      NetworkNeeds     `json:"network,omitempty" yaml:"network,omitempty"`
	Filesystem   FilesystemNeeds  `json:"filesystem,omitempty" yaml:"filesystem,omitempty"`
}

func NewNeedsSpec() NeedsSpec {
	n := NeedsSpec{
		Secrets:      []SecretNeed{},
		OAuth:        []OAuthNeed{},
		Capabilities: []CapabilityNeed{},
		Network:      NewNetworkNeeds(),
		Filesystem:   NewFilesystemNeeds(),
	}
	n.Normalize()
	return n
}

func (n *NeedsSpec) Normalize() {
	if n.Secrets == nil {
		n.Secrets = []SecretNeed{}
	}
	for i := range n.Secrets {
		n.Secrets[i].Normalize()
	}
	if n.OAuth == nil {
		n.OAuth = []OAuthNeed{}
	}
	for i := range n.OAuth {
		n.OAuth[i].Normalize()
	}
	if n.Capabilities == nil {
		n.Capabilities = []CapabilityNeed{}
	}
	for i := range n.Capabilities {
		n.Capabilities[i].Normalize()
	}
	n.Network.Normalize()
	n.Filesystem.Normalize()
}

func (n *NeedsSpec) UnmarshalJSON(data []byte) error {
	type alias NeedsSpec
	raw := alias(NewNeedsSpec())
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*n = NeedsSpec(raw)
	n.Normalize()
	return nil
}

type AgentNeeds struct {
	ClassName string    `json:"class_name" yaml:"class_name"`
	Needs     NeedsSpec `json:"needs" yaml:"needs"`
}

func NewAgentNeeds(className string) AgentNeeds {
	a := AgentNeeds{
		ClassName: className,
		Needs:     NewNeedsSpec(),
	}
	a.Normalize()
	return a
}

func (a *AgentNeeds) Normalize() {
	a.ClassName = strings.TrimSpace(a.ClassName)
	a.Needs.Normalize()
}

func (a *AgentNeeds) UnmarshalJSON(data []byte) error {
	type alias AgentNeeds
	raw := alias(NewAgentNeeds(""))
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*a = AgentNeeds(raw)
	a.Normalize()
	return nil
}

type DependencyNeeds struct {
	ClassName string    `json:"class_name" yaml:"class_name"`
	Needs     NeedsSpec `json:"needs" yaml:"needs"`
}

func NewDependencyNeeds(className string) DependencyNeeds {
	d := DependencyNeeds{
		ClassName: className,
		Needs:     NewNeedsSpec(),
	}
	d.Normalize()
	return d
}

func (d *DependencyNeeds) Normalize() {
	d.ClassName = strings.TrimSpace(d.ClassName)
	d.Needs.Normalize()
}

func (d *DependencyNeeds) UnmarshalJSON(data []byte) error {
	type alias DependencyNeeds
	raw := alias(NewDependencyNeeds(""))
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*d = DependencyNeeds(raw)
	d.Normalize()
	return nil
}

// ResourceSpec specifies the resources required by an agent.
type ResourceSpec struct {
	NumCPUs         *float64               `json:"num_cpus,omitempty"`
	NumGPUs         *float64               `json:"num_gpus,omitempty"`
	Secrets         []string               `json:"secrets,omitempty"`
	CustomResources map[string]interface{} `json:"custom_resources,omitempty"`
}

func NewResourceSpec() ResourceSpec {
	r := ResourceSpec{
		Secrets:         []string{},
		CustomResources: map[string]interface{}{},
	}
	r.Normalize()
	return r
}

func (r *ResourceSpec) Normalize() {
	if r.Secrets == nil {
		r.Secrets = []string{}
	}
	if r.CustomResources == nil {
		r.CustomResources = map[string]interface{}{}
	}
}

func (r *ResourceSpec) UnmarshalJSON(data []byte) error {
	type alias ResourceSpec
	raw := alias(NewResourceSpec())
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = ResourceSpec(raw)
	r.Normalize()
	return nil
}

// ValidateCustomResources checks that all custom resource values are numeric.
func (r *ResourceSpec) ValidateCustomResources() error {
	for k, v := range r.CustomResources {
		switch v.(type) {
		case float64, float32, int, int64, int32, int16, int8,
			uint, uint64, uint32, uint16, uint8, json.Number:
			// valid numeric type
		default:
			return fmt.Errorf("custom_resources[%q]: value must be numeric, got %T", k, v)
		}
	}
	return nil
}

// Validate checks the ResourceSpec for correctness.
func (r *ResourceSpec) Validate() error {
	return r.ValidateCustomResources()
}

// QOSSpec specifies Quality of Service settings for an agent.
type QOSSpec struct {
	Timeout    *int `json:"timeout,omitempty"`
	RetryCount *int `json:"retry_count,omitempty"`
	Latency    *int `json:"latency,omitempty"`
}

func NewQOSSpec() QOSSpec {
	q := QOSSpec{}
	q.Normalize()
	return q
}

func (q *QOSSpec) Normalize() {}

func (q *QOSSpec) UnmarshalJSON(data []byte) error {
	type alias QOSSpec
	raw := alias(NewQOSSpec())
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*q = QOSSpec(raw)
	q.Normalize()
	return nil
}

type ProcessStatus string

const (
	ProcessStatusRunning   ProcessStatus = "running"
	ProcessStatusError     ProcessStatus = "error"
	ProcessStatusCompleted ProcessStatus = "completed"
)

type RoutingOrigin struct {
	OriginSender        *AgentTag `json:"origin_sender,omitempty"`
	OriginTopic         *string   `json:"origin_topic,omitempty"`
	OriginMessageFormat *string   `json:"origin_message_format,omitempty"`
}

func NewRoutingOrigin() RoutingOrigin {
	r := RoutingOrigin{}
	r.Normalize()
	return r
}

func (r *RoutingOrigin) Normalize() {
	if r.OriginSender != nil {
		r.OriginSender.Normalize()
	}
}

func (r *RoutingOrigin) UnmarshalJSON(data []byte) error {
	type alias RoutingOrigin
	raw := alias(NewRoutingOrigin())
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = RoutingOrigin(raw)
	r.Normalize()
	return nil
}

type RoutingDestination struct {
	Topics        Topics     `json:"topics,omitempty"`
	RecipientList []AgentTag `json:"recipient_list,omitempty"`
	Priority      *int       `json:"priority,omitempty"`
}

func NewRoutingDestination() RoutingDestination {
	d := RoutingDestination{
		RecipientList: []AgentTag{},
	}
	d.Normalize()
	return d
}

func (d *RoutingDestination) Normalize() {
	if d.RecipientList == nil {
		d.RecipientList = []AgentTag{}
	}
	for i := range d.RecipientList {
		d.RecipientList[i].Normalize()
	}
}

func (d *RoutingDestination) UnmarshalJSON(data []byte) error {
	type alias RoutingDestination
	raw := alias(NewRoutingDestination())
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*d = RoutingDestination(raw)
	d.Normalize()
	return nil
}

// PredicateType defines the kind of runtime predicate.
type PredicateType string

const (
	PredicateJSONata    PredicateType = "jsonata_fn"
	PredicateCel        PredicateType = "cel_fn"
	PredicateTypeEquals PredicateType = "type_equals"
)

// RuntimePredicate describes a runtime condition for routing or agent dispatch.
type RuntimePredicate struct {
	PredicateType PredicateType `json:"predicate_type"`
	Expression    *string       `json:"expression,omitempty"`
	ExpectedType  *string       `json:"expected_type,omitempty"`
}

func NewRuntimePredicate() RuntimePredicate {
	r := RuntimePredicate{}
	r.Normalize()
	return r
}

func (r *RuntimePredicate) Normalize() {}

func (r *RuntimePredicate) UnmarshalJSON(data []byte) error {
	type alias RuntimePredicate
	var raw alias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = RuntimePredicate(raw)
	r.Normalize()
	return nil
}

type RoutingRule struct {
	Agent            *AgentTag           `json:"agent,omitempty"`
	AgentType        *string             `json:"agent_type,omitempty"`
	MethodName       *string             `json:"method_name,omitempty"`
	OriginFilter     *RoutingOrigin      `json:"origin_filter,omitempty"`
	MessageFormat    *string             `json:"message_format,omitempty"`
	Destination      *RoutingDestination `json:"destination,omitempty"`
	MarkForwarded    bool                `json:"mark_forwarded"`
	RouteTimes       *int                `json:"route_times,omitempty"`
	Transformer      RawJSON             `json:"transformer,omitempty"`
	AgentStateUpdate RawJSON             `json:"agent_state_update,omitempty"`
	GuildStateUpdate RawJSON             `json:"guild_state_update,omitempty"`
	ProcessStatus    *ProcessStatus      `json:"process_status,omitempty"`
	Reason           *string             `json:"reason,omitempty"`
}

func NewRoutingRule() RoutingRule {
	r := RoutingRule{
		MarkForwarded: false,
		RouteTimes:    intPtr(1),
	}
	r.Normalize()
	return r
}

func (r *RoutingRule) Normalize() {
	if r.Agent != nil {
		r.Agent.Normalize()
	}
	if r.OriginFilter != nil {
		r.OriginFilter.Normalize()
	}
	if r.Destination != nil {
		r.Destination.Normalize()
	}
	if r.RouteTimes == nil {
		r.RouteTimes = intPtr(1)
	}
}

func (r *RoutingRule) UnmarshalJSON(data []byte) error {
	type alias RoutingRule
	raw := alias(NewRoutingRule())
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = RoutingRule(raw)
	r.Normalize()
	return nil
}

type RoutingSlip struct {
	Steps []RoutingRule `json:"steps"`
}

func NewRoutingSlip() RoutingSlip {
	s := RoutingSlip{
		Steps: []RoutingRule{},
	}
	s.Normalize()
	return s
}

func (s *RoutingSlip) Normalize() {
	if s.Steps == nil {
		s.Steps = []RoutingRule{}
	}
	for i := range s.Steps {
		s.Steps[i].Normalize()
	}
}

func (s *RoutingSlip) UnmarshalJSON(data []byte) error {
	type alias RoutingSlip
	raw := alias(NewRoutingSlip())
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*s = RoutingSlip(raw)
	s.Normalize()
	return nil
}

// AgentSpec defines an agent's configuration.
type AgentSpec struct {
	ID                     string                      `json:"id"`
	Name                   string                      `json:"name"`
	Description            string                      `json:"description"`
	ClassName              string                      `json:"class_name"`
	AdditionalTopics       []string                    `json:"additional_topics"`
	Properties             map[string]interface{}      `json:"properties"`
	ListenToDefaultTopic   *bool                       `json:"listen_to_default_topic,omitempty"`
	ActOnlyWhenTagged      *bool                       `json:"act_only_when_tagged,omitempty"`
	Predicates             map[string]RuntimePredicate `json:"predicates,omitempty"`
	DependencyMap          map[string]DependencySpec   `json:"dependency_map,omitempty"`
	AdditionalDependencies []string                    `json:"additional_dependencies,omitempty"`
	// ForgeExtraDeps lists extra Python packages to install into this agent's uvx
	// environment (passed as `uvx --with <dep>`). It is the per-agent counterpart of the
	// guild-wide FORGE_EXTRA_DEPS environment variable and uses the same value format.
	// This is a Forge extension: rustic-ai core's AgentSpec ignores unknown keys, so the
	// field is dropped when a spec round-trips through the Python guild manager. Forge
	// re-attaches it from the guild store when handling a spawn request.
	ForgeExtraDeps []string     `json:"forge_extra_deps,omitempty"`
	Resources      ResourceSpec `json:"resources,omitempty"`
	QOS            QOSSpec      `json:"qos,omitempty"`
}

func NewAgentSpec() AgentSpec {
	a := AgentSpec{
		ID:                     idgen.NewShortUUID(),
		AdditionalTopics:       []string{},
		Properties:             map[string]interface{}{},
		ListenToDefaultTopic:   boolPtr(true),
		ActOnlyWhenTagged:      boolPtr(false),
		Predicates:             map[string]RuntimePredicate{},
		DependencyMap:          map[string]DependencySpec{},
		AdditionalDependencies: []string{},
		ForgeExtraDeps:         []string{},
		Resources:              NewResourceSpec(),
		QOS:                    NewQOSSpec(),
	}
	a.Normalize()
	return a
}

func (a *AgentSpec) Normalize() {
	if a.ID == "" {
		a.ID = idgen.NewShortUUID()
	}
	// Whitespace stripping (matches Python's str_strip_whitespace)
	a.Name = strings.TrimSpace(a.Name)
	a.Description = strings.TrimSpace(a.Description)
	a.ClassName = strings.TrimSpace(a.ClassName)
	if a.AdditionalTopics == nil {
		a.AdditionalTopics = []string{}
	}
	if a.Properties == nil {
		a.Properties = map[string]interface{}{}
	}
	if a.ListenToDefaultTopic == nil {
		a.ListenToDefaultTopic = boolPtr(true)
	}
	if a.ActOnlyWhenTagged == nil {
		a.ActOnlyWhenTagged = boolPtr(false)
	}
	if a.Predicates == nil {
		a.Predicates = map[string]RuntimePredicate{}
	}
	if a.DependencyMap == nil {
		a.DependencyMap = map[string]DependencySpec{}
	}
	for key, dep := range a.DependencyMap {
		dep.Normalize()
		a.DependencyMap[key] = dep
	}
	if a.AdditionalDependencies == nil {
		a.AdditionalDependencies = []string{}
	}
	if a.ForgeExtraDeps == nil {
		a.ForgeExtraDeps = []string{}
	}
	a.Resources.Normalize()
	a.QOS.Normalize()
}

// Validate checks the AgentSpec for correctness.
func (a *AgentSpec) Validate() error {
	if len(a.Name) < 1 || len(a.Name) > 64 {
		return fmt.Errorf("agent name must be 1-64 characters, got %d", len(a.Name))
	}
	if len(a.Description) < 1 {
		return fmt.Errorf("agent description must not be empty")
	}
	return nil
}

func (a *AgentSpec) UnmarshalJSON(data []byte) error {
	type alias AgentSpec
	raw := alias(NewAgentSpec())
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*a = AgentSpec(raw)
	a.Normalize()
	return nil
}

// GatewayConfig specifies the automatic GatewayAgent configuration.
type GatewayConfig struct {
	Enabled         bool     `json:"enabled"`
	InputFormats    []string `json:"input_formats"`
	OutputFormats   []string `json:"output_formats"`
	ReturnedFormats []string `json:"returned_formats,omitempty"`
}

func NewGatewayConfig() GatewayConfig {
	g := GatewayConfig{
		Enabled:         true,
		InputFormats:    []string{},
		OutputFormats:   []string{},
		ReturnedFormats: []string{},
	}
	g.Normalize()
	return g
}

func (g *GatewayConfig) Normalize() {
	if g.InputFormats == nil {
		g.InputFormats = []string{}
	}
	if g.OutputFormats == nil {
		g.OutputFormats = []string{}
	}
	if g.ReturnedFormats == nil {
		g.ReturnedFormats = []string{}
	}
}

func (g *GatewayConfig) UnmarshalJSON(data []byte) error {
	var raw struct {
		Enabled         *bool    `json:"enabled"`
		InputFormats    []string `json:"input_formats"`
		OutputFormats   []string `json:"output_formats"`
		ReturnedFormats []string `json:"returned_formats"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	cfg := NewGatewayConfig()
	if raw.Enabled != nil {
		cfg.Enabled = *raw.Enabled
	}
	cfg.InputFormats = raw.InputFormats
	cfg.OutputFormats = raw.OutputFormats
	cfg.ReturnedFormats = raw.ReturnedFormats
	cfg.Normalize()
	*g = cfg
	return nil
}

// GuildSpec defines the overall guild configuration.
type GuildSpec struct {
	ID            string                    `json:"id"`
	Name          string                    `json:"name"`
	Description   string                    `json:"description"`
	Properties    map[string]interface{}    `json:"properties"`
	Configuration map[string]interface{}    `json:"configuration,omitempty"`
	Agents        []AgentSpec               `json:"agents"`
	DependencyMap map[string]DependencySpec `json:"dependency_map,omitempty"`
	Routes        *RoutingSlip              `json:"routes,omitempty"`
	Gateway       *GatewayConfig            `json:"gateway,omitempty"`
}

func NewGuildSpec() GuildSpec {
	g := GuildSpec{
		ID:            idgen.NewShortUUID(),
		Properties:    map[string]interface{}{},
		Agents:        []AgentSpec{},
		DependencyMap: map[string]DependencySpec{},
		Routes:        routingSlipPtr(NewRoutingSlip()),
	}
	g.Normalize()
	return g
}

func (g *GuildSpec) Normalize() {
	if g.ID == "" {
		g.ID = idgen.NewShortUUID()
	}
	// Whitespace stripping (matches Python's str_strip_whitespace)
	g.Name = strings.TrimSpace(g.Name)
	g.Description = strings.TrimSpace(g.Description)
	if g.Properties == nil {
		g.Properties = map[string]interface{}{}
	}
	if g.Agents == nil {
		g.Agents = []AgentSpec{}
	}
	for i := range g.Agents {
		g.Agents[i].Normalize()
	}
	if g.DependencyMap == nil {
		g.DependencyMap = map[string]DependencySpec{}
	}
	for key, dep := range g.DependencyMap {
		dep.Normalize()
		g.DependencyMap[key] = dep
	}
	if g.Routes == nil {
		g.Routes = routingSlipPtr(NewRoutingSlip())
	}
	g.Routes.Normalize()
	if g.Gateway != nil {
		g.Gateway.Normalize()
	}
}

// Validate checks the GuildSpec for correctness.
func (g *GuildSpec) Validate() error {
	if len(g.Name) < 1 || len(g.Name) > 64 {
		return fmt.Errorf("guild name must be 1-64 characters, got %d", len(g.Name))
	}
	if len(g.Description) < 1 {
		return fmt.Errorf("guild description must not be empty")
	}
	return nil
}

func (g *GuildSpec) UnmarshalJSON(data []byte) error {
	type alias GuildSpec
	raw := alias(NewGuildSpec())
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*g = GuildSpec(raw)
	g.Normalize()
	return nil
}

// MessagingConfig represents the configuration of a message bus.
type MessagingConfig struct {
	BackendModule string                 `json:"backend_module"`
	BackendClass  string                 `json:"backend_class"`
	BackendConfig map[string]interface{} `json:"backend_config"`
}

func NewMessagingConfig() MessagingConfig {
	m := MessagingConfig{
		BackendConfig: map[string]interface{}{},
	}
	m.Normalize()
	return m
}

func (m *MessagingConfig) Normalize() {
	if m.BackendConfig == nil {
		m.BackendConfig = map[string]interface{}{}
	}
}

func (m *MessagingConfig) UnmarshalJSON(data []byte) error {
	type alias MessagingConfig
	raw := alias(NewMessagingConfig())
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = MessagingConfig(raw)
	m.Normalize()
	return nil
}

func boolPtr(v bool) *bool { return &v }
func intPtr(v int) *int    { return &v }

func routingSlipPtr(s RoutingSlip) *RoutingSlip { return &s }
