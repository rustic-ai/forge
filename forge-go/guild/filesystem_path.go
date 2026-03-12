package guild

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/rustic-ai/forge/forge-go/protocol"
)

const forgeFilesystemGlobalRootEnv = "FORGE_FILESYSTEM_GLOBAL_ROOT"

// ApplyFilesystemGlobalRoot rewrites the guild filesystem dependency path_base
// so the stored spec carries the Forge-owned root explicitly.
func ApplyFilesystemGlobalRoot(spec *protocol.GuildSpec, globalRoot string) error {
	if spec == nil {
		return nil
	}

	root, err := parseFilesystemGlobalRoot(globalRoot)
	if err != nil {
		return err
	}
	if root == nil {
		return nil
	}

	if err := applyFilesystemGlobalRootToDependencyMap(spec.DependencyMap, root); err != nil {
		return err
	}

	for i := range spec.Agents {
		if err := applyFilesystemGlobalRootToDependencyMap(spec.Agents[i].DependencyMap, root); err != nil {
			agentLabel := spec.Agents[i].ID
			if agentLabel == "" {
				agentLabel = spec.Agents[i].Name
			}
			if agentLabel == "" {
				agentLabel = fmt.Sprintf("index %d", i)
			}
			return fmt.Errorf("agent %s: %w", agentLabel, err)
		}
	}
	return nil
}

func applyFilesystemGlobalRootToDependencyMap(depMap map[string]protocol.DependencySpec, root *filesystemGlobalRoot) error {
	if depMap == nil {
		return nil
	}
	dep, ok := depMap["filesystem"]
	if !ok {
		return nil
	}
	if dep.Properties == nil {
		dep.Properties = map[string]interface{}{}
	}
	resolvedPath, protocolName, err := resolveFilesystemPathBase(root, stringProperty(dep.Properties["path_base"]))
	if err != nil {
		return err
	}
	dep.Properties["path_base"] = resolvedPath
	dep.Properties["protocol"] = protocolName
	depMap["filesystem"] = dep
	return nil
}

type filesystemGlobalRoot struct {
	protocol string
	base     string
	bucket   string
	prefix   string
}

func parseFilesystemGlobalRoot(globalRoot string) (*filesystemGlobalRoot, error) {
	globalRoot = strings.TrimSpace(globalRoot)
	if globalRoot == "" {
		return nil, nil
	}

	if !strings.Contains(globalRoot, "://") {
		root := filepath.Clean(globalRoot)
		if root == "" {
			return nil, fmt.Errorf("filesystem global root is required")
		}
		return &filesystemGlobalRoot{protocol: "file", base: root}, nil
	}

	u, err := url.Parse(globalRoot)
	if err != nil {
		return nil, fmt.Errorf("invalid filesystem global root %q: %w", globalRoot, err)
	}

	switch scheme := strings.ToLower(strings.TrimSpace(u.Scheme)); scheme {
	case "file":
		root := filepath.Clean(strings.TrimSpace(u.Path))
		if root == "" {
			return nil, fmt.Errorf("filesystem global root is required")
		}
		return &filesystemGlobalRoot{protocol: "file", base: root}, nil
	case "s3":
		return parseObjectStoreRoot("s3", u)
	case "gs", "gcs":
		return parseObjectStoreRoot(scheme, u)
	default:
		return nil, fmt.Errorf("unsupported filesystem global root scheme %q", u.Scheme)
	}
}

func parseObjectStoreRoot(protocol string, u *url.URL) (*filesystemGlobalRoot, error) {
	bucket := strings.TrimSpace(u.Host)
	if bucket == "" {
		return nil, fmt.Errorf("filesystem global root must include bucket name")
	}
	prefix, err := relativeObjectPath(u.Path)
	if err != nil {
		return nil, err
	}
	base := protocol + "://" + bucket
	if prefix != "" {
		base += "/" + prefix
	}
	return &filesystemGlobalRoot{
		protocol: protocol,
		base:     base,
		bucket:   bucket,
		prefix:   prefix,
	}, nil
}

func resolveFilesystemPathBase(root *filesystemGlobalRoot, rawBase string) (string, string, error) {
	if root == nil {
		return "", "", fmt.Errorf("filesystem global root is required")
	}

	if root.protocol == "file" {
		resolved, err := resolveLocalFilesystemPathBase(root.base, rawBase)
		return resolved, "file", err
	}
	resolved, protocolName, err := resolveObjectFilesystemPathBase(root, rawBase)
	return resolved, protocolName, err
}

func resolveLocalFilesystemPathBase(globalRoot, rawBase string) (string, error) {
	root := filepath.Clean(strings.TrimSpace(globalRoot))
	rawBase = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rawBase), "file://"))
	if rawBase == "" {
		return root, nil
	}

	cleanedBase := filepath.Clean(rawBase)
	if isWithinRoot(cleanedBase, root) {
		return cleanedBase, nil
	}

	relativeBase, err := relativeFilesystemPath(cleanedBase)
	if err != nil {
		return "", err
	}
	if relativeBase == "" {
		return root, nil
	}
	return filepath.Join(root, relativeBase), nil
}

func resolveObjectFilesystemPathBase(root *filesystemGlobalRoot, rawBase string) (string, string, error) {
	rawBase = strings.TrimSpace(rawBase)
	if rawBase == "" {
		return root.base, root.protocol, nil
	}

	if strings.Contains(rawBase, "://") {
		u, err := url.Parse(rawBase)
		if err != nil {
			return "", "", fmt.Errorf("invalid filesystem path_base %q: %w", rawBase, err)
		}
		protocolName := normalizeObjectProtocol(u.Scheme)
		if protocolName == "" {
			return "", "", fmt.Errorf("unsupported filesystem path_base scheme %q", u.Scheme)
		}
		if protocolName != normalizeObjectProtocol(root.protocol) {
			return "", "", fmt.Errorf("filesystem path_base scheme %q does not match Forge global root scheme %q", u.Scheme, root.protocol)
		}
		if strings.TrimSpace(u.Host) != root.bucket {
			return "", "", fmt.Errorf("filesystem path_base bucket %q does not match Forge global root bucket %q", u.Host, root.bucket)
		}

		prefix, err := relativeObjectPath(u.Path)
		if err != nil {
			return "", "", err
		}
		if isWithinObjectRoot(prefix, root.prefix) {
			return buildObjectURL(root.protocol, root.bucket, prefix), root.protocol, nil
		}
		if prefix == "" {
			return root.base, root.protocol, nil
		}
		return buildObjectURL(root.protocol, root.bucket, joinObjectPath(root.prefix, prefix)), root.protocol, nil
	}

	relativeBase, err := relativeObjectPath(rawBase)
	if err != nil {
		return "", "", err
	}
	if relativeBase == "" {
		return root.base, root.protocol, nil
	}
	return buildObjectURL(root.protocol, root.bucket, joinObjectPath(root.prefix, relativeBase)), root.protocol, nil
}

func relativeFilesystemPath(path string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" || cleaned == "." {
		return "", nil
	}
	if hasParentTraversal(cleaned) {
		return "", fmt.Errorf("invalid filesystem path_base %q: path traversal is not allowed", path)
	}
	if filepath.IsAbs(cleaned) {
		if vol := filepath.VolumeName(cleaned); vol != "" {
			cleaned = strings.TrimPrefix(cleaned, vol)
		}
		cleaned = strings.TrimLeft(cleaned, `/\`)
	}
	if cleaned == "" || cleaned == "." {
		return "", nil
	}
	return cleaned, nil
}

func relativeObjectPath(raw string) (string, error) {
	cleaned := path.Clean(strings.TrimSpace(raw))
	if cleaned == "" || cleaned == "." || cleaned == "/" {
		return "", nil
	}
	if hasObjectParentTraversal(cleaned) {
		return "", fmt.Errorf("invalid filesystem path_base %q: path traversal is not allowed", raw)
	}
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" || cleaned == "." {
		return "", nil
	}
	return cleaned, nil
}

func hasParentTraversal(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func hasObjectParentTraversal(path string) bool {
	for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func isWithinRoot(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func stringProperty(v interface{}) string {
	s, _ := v.(string)
	return s
}

func normalizeObjectProtocol(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "gs", "gcs":
		return "gcs"
	case "s3":
		return "s3"
	default:
		return ""
	}
}

func isWithinObjectRoot(candidatePrefix, rootPrefix string) bool {
	candidatePrefix = strings.Trim(candidatePrefix, "/")
	rootPrefix = strings.Trim(rootPrefix, "/")
	if rootPrefix == "" {
		return true
	}
	return candidatePrefix == rootPrefix || strings.HasPrefix(candidatePrefix, rootPrefix+"/")
}

func joinObjectPath(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(part, "/")
		if part != "" {
			out = append(out, part)
		}
	}
	return path.Join(out...)
}

func buildObjectURL(protocol, bucket, prefix string) string {
	base := protocol + "://" + bucket
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return base
	}
	return base + "/" + prefix
}
