package api

import (
	"fmt"
	"strings"

	"github.com/rustic-ai/forge/forge-go/filesystem"
	"github.com/rustic-ai/forge/forge-go/guild/store"
)

func (s *Server) resolveFilesystemDependency(guildID string) (string, filesystem.DependencyConfig, int, error) {
	guildModel, err := s.store.GetGuild(guildID)
	if err != nil {
		return "", filesystem.DependencyConfig{}, 404, fmt.Errorf("guild not found")
	}

	spec := store.ToGuildSpec(guildModel)

	depSpec, ok := spec.DependencyMap["filesystem"]
	if !ok {
		return "", filesystem.DependencyConfig{}, 404, fmt.Errorf("dependency for filesystem not configured for guild %s", guildID)
	}

	cfg := filesystem.DependencyConfig{
		ClassName:      depSpec.ClassName,
		Protocol:       "file",
		StorageOptions: map[string]any{},
	}
	if depSpec.Properties != nil {
		if base, ok := depSpec.Properties["path_base"].(string); ok {
			cfg.PathBase = strings.TrimSpace(base)
		}
		if protocolName, ok := depSpec.Properties["protocol"].(string); ok && strings.TrimSpace(protocolName) != "" {
			cfg.Protocol = strings.ToLower(strings.TrimSpace(protocolName))
		}
		if options, ok := depSpec.Properties["storage_options"].(map[string]any); ok && options != nil {
			cfg.StorageOptions = options
		}
	}
	if cfg.StorageOptions == nil {
		cfg.StorageOptions = map[string]any{}
	}

	orgID := strings.TrimSpace(guildModel.OrganizationID)
	if orgID == "" {
		orgID = guildID
	}

	return orgID, cfg, 200, nil
}
