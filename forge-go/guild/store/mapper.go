package store

import (
	"encoding/json"

	"github.com/rustic-ai/forge/forge-go/protocol"
)

func protocolRawToStoreRaw(raw protocol.RawJSON) RawJSON {
	if len(raw) == 0 {
		return nil
	}
	cp := make([]byte, len(raw))
	copy(cp, raw)
	return RawJSON(cp)
}

func storeRawToProtocolRaw(raw RawJSON) protocol.RawJSON {
	if len(raw) == 0 {
		return nil
	}
	cp := make([]byte, len(raw))
	copy(cp, raw)
	return protocol.RawJSON(cp)
}

// structToJSONB converts a struct to JSONB via JSON marshaling.
func structToJSONB(v interface{}) JSONB {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m JSONB
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

// jsonbToStruct converts a JSONB map to a struct via JSON marshaling.
func jsonbToStruct[T any](j JSONB) *T {
	if len(j) == 0 {
		return nil
	}
	b, err := json.Marshal(j)
	if err != nil {
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return nil
	}
	return &v
}

func FromGuildSpec(spec *protocol.GuildSpec, organizationID string) *GuildModel {
	spec.Normalize()

	execEngine := "rustic_ai.core.guild.execution.sync.sync_exec_engine.SyncExecutionEngine"
	if custom, ok := spec.Properties["execution_engine"].(string); ok {
		execEngine = custom
	}

	backendModule := "rustic_ai.core.messaging.backend"
	backendClass := "InMemoryMessagingBackend"
	backendConfig := JSONB{}

	if msgConfigMap, ok := spec.Properties["messaging"].(map[string]interface{}); ok {
		if m, ok := msgConfigMap["backend_module"].(string); ok {
			backendModule = m
		}
		if c, ok := msgConfigMap["backend_class"].(string); ok {
			backendClass = c
		}
		if bc, ok := msgConfigMap["backend_config"].(map[string]interface{}); ok {
			backendConfig = JSONB(bc)
		}
	}

	model := &GuildModel{
		ID:              spec.ID,
		Name:            spec.Name,
		Description:     spec.Description,
		OrganizationID:  organizationID,
		ExecutionEngine: execEngine,
		BackendModule:   backendModule,
		BackendClass:    backendClass,
		BackendConfig:   backendConfig,
		Status:          GuildStatusPendingLaunch,
	}

	deps := make(JSONB)
	for k, v := range spec.DependencyMap {
		deps[k] = map[string]interface{}{
			"class_name": v.ClassName,
			"properties": v.Properties,
		}
	}
	model.DependencyMap = deps

	for _, aSpec := range spec.Agents {
		aModel := FromAgentSpec(&aSpec, spec.ID)
		model.Agents = append(model.Agents, *aModel)
	}

	if spec.Routes != nil {
		for _, rSpec := range spec.Routes.Steps {
			rModel := FromRoutingRule(spec.ID, &rSpec)
			model.Routes = append(model.Routes, *rModel)
		}
	}

	return model
}

func ToGuildSpec(model *GuildModel) *protocol.GuildSpec {
	spec := &protocol.GuildSpec{
		ID:          model.ID,
		Name:        model.Name,
		Description: model.Description,
		Properties: map[string]interface{}{
			"execution_engine": model.ExecutionEngine,
			"messaging": map[string]interface{}{
				"backend_module": model.BackendModule,
				"backend_class":  model.BackendClass,
				"backend_config": map[string]interface{}(model.BackendConfig),
			},
		},
	}

	if model.DependencyMap != nil {
		spec.DependencyMap = make(map[string]protocol.DependencySpec)
		for k, v := range model.DependencyMap {
			if vm, ok := v.(map[string]interface{}); ok {
				ds := protocol.DependencySpec{}
				if cn, _ := vm["class_name"].(string); cn != "" {
					ds.ClassName = cn
				}
				if props, _ := vm["properties"].(map[string]interface{}); props != nil {
					ds.Properties = props
				}
				spec.DependencyMap[k] = ds
			}
		}
	}

	for _, aModel := range model.Agents {
		if aModel.Status != AgentStatusDeleted {
			spec.Agents = append(spec.Agents, *ToAgentSpec(&aModel))
		}
	}

	if len(model.Routes) > 0 {
		spec.Routes = &protocol.RoutingSlip{Steps: make([]protocol.RoutingRule, 0)}
		for _, rModel := range model.Routes {
			if rModel.Status != RouteStatusDeleted {
				spec.Routes.Steps = append(spec.Routes.Steps, *ToRoutingRule(&rModel))
			}
		}
	}

	spec.Normalize()
	return spec
}

func FromAgentSpec(spec *protocol.AgentSpec, guildID string) *AgentModel {
	spec.Normalize()

	m := &AgentModel{
		ID:                     spec.ID,
		GuildID:                &guildID,
		Name:                   spec.Name,
		Description:            spec.Description,
		ClassName:              spec.ClassName,
		ListenToDefaultTopic:   true,
		ActOnlyWhenTagged:      false,
		AdditionalTopics:       JSONBStringList(spec.AdditionalTopics),
		AdditionalDependencies: JSONBStringList(spec.AdditionalDependencies),
		ForgeExtraDeps:         JSONBStringList(spec.ForgeExtraDeps),
		Status:                 AgentStatusPendingLaunch,
	}
	if spec.Properties != nil {
		m.Properties = JSONB(spec.Properties)
	}

	agentDeps := make(JSONB)
	for k, v := range spec.DependencyMap {
		agentDeps[k] = map[string]interface{}{
			"class_name": v.ClassName,
			"properties": v.Properties,
		}
	}
	m.DependencyMap = agentDeps

	if spec.ListenToDefaultTopic != nil {
		m.ListenToDefaultTopic = *spec.ListenToDefaultTopic
	}
	if spec.ActOnlyWhenTagged != nil {
		m.ActOnlyWhenTagged = *spec.ActOnlyWhenTagged
	}

	preds := make(JSONB)
	for k, v := range spec.Predicates {
		preds[k] = structToJSONB(&v)
	}
	m.Predicates = preds
	return m
}

func ToAgentSpec(model *AgentModel) *protocol.AgentSpec {
	listenToDefaultTopic := model.ListenToDefaultTopic
	actOnlyWhenTagged := model.ActOnlyWhenTagged
	spec := &protocol.AgentSpec{
		ID:                     model.ID,
		Name:                   model.Name,
		Description:            model.Description,
		ClassName:              model.ClassName,
		ListenToDefaultTopic:   &listenToDefaultTopic,
		ActOnlyWhenTagged:      &actOnlyWhenTagged,
		AdditionalTopics:       []string(model.AdditionalTopics),
		AdditionalDependencies: []string(model.AdditionalDependencies),
		ForgeExtraDeps:         []string(model.ForgeExtraDeps),
	}
	if model.Properties != nil {
		spec.Properties = map[string]interface{}(model.Properties)
	}

	if model.DependencyMap != nil {
		spec.DependencyMap = make(map[string]protocol.DependencySpec)
		for k, v := range model.DependencyMap {
			if vm, ok := v.(map[string]interface{}); ok {
				ds := protocol.DependencySpec{}
				if cn, _ := vm["class_name"].(string); cn != "" {
					ds.ClassName = cn
				}
				if props, ok := vm["properties"].(map[string]interface{}); ok {
					ds.Properties = props
				}
				spec.DependencyMap[k] = ds
			}
		}
	}

	if model.Predicates != nil {
		spec.Predicates = make(map[string]protocol.RuntimePredicate)
		for k, v := range model.Predicates {
			if vm, ok := v.(map[string]interface{}); ok {
				if pred := jsonbToStruct[protocol.RuntimePredicate](JSONB(vm)); pred != nil {
					spec.Predicates[k] = *pred
				}
			}
		}
	}
	spec.Normalize()
	return spec
}

func FromRoutingRule(guildID string, rule *protocol.RoutingRule) *GuildRoutes {
	rule.Normalize()

	r := &GuildRoutes{
		GuildID:              &guildID,
		AgentType:            rule.AgentType,
		MethodName:           rule.MethodName,
		RouteTimes:           1,
		Transformer:          protocolRawToStoreRaw(rule.Transformer),
		AgentStateUpdate:     protocolRawToStoreRaw(rule.AgentStateUpdate),
		GuildStateUpdate:     protocolRawToStoreRaw(rule.GuildStateUpdate),
		ProcessStatus:        (*string)(rule.ProcessStatus),
		Reason:               rule.Reason,
		Status:               RouteStatusActive,
		CurrentMessageFormat: rule.MessageFormat,
	}
	if rule.RouteTimes != nil {
		r.RouteTimes = *rule.RouteTimes
	}

	if rule.Agent != nil {
		r.AgentName = rule.Agent.Name
		r.AgentID = rule.Agent.ID
	}

	if rule.OriginFilter != nil {
		r.OriginTopic = rule.OriginFilter.OriginTopic
		r.OriginMessageFormat = rule.OriginFilter.OriginMessageFormat
		if rule.OriginFilter.OriginSender != nil {
			r.OriginSenderID = rule.OriginFilter.OriginSender.ID
			r.OriginSenderName = rule.OriginFilter.OriginSender.Name
		}
	}

	if rule.Destination != nil {
		r.DestinationPriority = rule.Destination.Priority

		if !rule.Destination.Topics.IsZero() {
			r.DestinationTopics = JSONBStringList(rule.Destination.Topics.ToSlice())
		}

		if len(rule.Destination.RecipientList) > 0 {
			var rList JSONBList
			for _, rec := range rule.Destination.RecipientList {
				m := make(map[string]interface{})
				if rec.ID != nil {
					m["id"] = *rec.ID
				}
				if rec.Name != nil {
					m["name"] = *rec.Name
				}
				rList = append(rList, m)
			}
			r.DestinationRecipientList = rList
		}
	}

	return r
}

func ToRoutingRule(model *GuildRoutes) *protocol.RoutingRule {
	rule := &protocol.RoutingRule{
		AgentType:        model.AgentType,
		MethodName:       model.MethodName,
		RouteTimes:       &model.RouteTimes,
		Transformer:      storeRawToProtocolRaw(model.Transformer),
		AgentStateUpdate: storeRawToProtocolRaw(model.AgentStateUpdate),
		GuildStateUpdate: storeRawToProtocolRaw(model.GuildStateUpdate),
		ProcessStatus:    (*protocol.ProcessStatus)(model.ProcessStatus),
		Reason:           model.Reason,
		MessageFormat:    model.CurrentMessageFormat,
	}

	if model.AgentName != nil || model.AgentID != nil {
		rule.Agent = &protocol.AgentTag{
			Name: model.AgentName,
			ID:   model.AgentID,
		}
	}

	if model.OriginSenderID != nil || model.OriginSenderName != nil || model.OriginTopic != nil || model.OriginMessageFormat != nil {
		rule.OriginFilter = &protocol.RoutingOrigin{
			OriginTopic:         model.OriginTopic,
			OriginMessageFormat: model.OriginMessageFormat,
		}
		if model.OriginSenderID != nil || model.OriginSenderName != nil {
			rule.OriginFilter.OriginSender = &protocol.AgentTag{
				Name: model.OriginSenderName,
				ID:   model.OriginSenderID,
			}
		}
	}

	if len(model.DestinationTopics) > 0 || len(model.DestinationRecipientList) > 0 || model.DestinationPriority != nil {
		rule.Destination = &protocol.RoutingDestination{
			Priority: model.DestinationPriority,
		}
		if len(model.DestinationTopics) > 0 {
			rule.Destination.Topics = protocol.TopicsFromSlice([]string(model.DestinationTopics))
		}
		if len(model.DestinationRecipientList) > 0 {
			var destList []protocol.AgentTag
			for _, m := range model.DestinationRecipientList {
				tag := protocol.AgentTag{}
				if id, ok := m["id"].(string); ok {
					tag.ID = &id
				}
				if name, ok := m["name"].(string); ok {
					tag.Name = &name
				}
				destList = append(destList, tag)
			}
			rule.Destination.RecipientList = destList
		}
	}

	rule.Normalize()
	return rule
}
