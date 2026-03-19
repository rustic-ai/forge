package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/rustic-ai/forge/forge-go/filesystem"
	"github.com/rustic-ai/forge/forge-go/helper/idgen"
)

type MediaLink struct {
	ID           string         `json:"id,omitempty"`
	Name         string         `json:"name"`
	URL          string         `json:"url"`
	Metadata     map[string]any `json:"metadata"`
	MimeType     string         `json:"mimetype"`
	Encoding     *string        `json:"encoding,omitempty"`
	ContentHash  *string        `json:"content_hash,omitempty"`
	SizeInBytes  *int64         `json:"size_in_bytes,omitempty"`
	OnFilesystem bool           `json:"on_filesystem"`
}

func (s *Server) handleFileUploadCore(w http.ResponseWriter, r *http.Request, guildID, agentID string) {
	if s.fileStore == nil {
		ReplyError(w, http.StatusInternalServerError, "filesystem store not configured")
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		ReplyError(w, http.StatusBadRequest, "Invalid input: file and filename are required")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil || header == nil || strings.TrimSpace(header.Filename) == "" {
		ReplyError(w, http.StatusBadRequest, "Invalid input: file and filename are required")
		return
	}
	defer func() { _ = file.Close() }()

	content, err := io.ReadAll(file)
	if err != nil {
		ReplyError(w, http.StatusInternalServerError, err.Error())
		return
	}

	contentType := strings.TrimSpace(header.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	fileMeta := map[string]any{}
	if rawMeta := strings.TrimSpace(r.FormValue("file_meta")); rawMeta != "" {
		if err := json.Unmarshal([]byte(rawMeta), &fileMeta); err != nil {
			ReplyError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	orgID, cfg, statusCode, err := s.resolveFilesystemDependency(guildID)
	if err != nil {
		ReplyError(w, statusCode, err.Error())
		return
	}

	exists, err := s.fileStore.Exists(r.Context(), cfg, orgID, guildID, agentID, header.Filename)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "invalid filename") {
			ReplyError(w, http.StatusBadRequest, "invalid filename")
			return
		}
		ReplyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if exists {
		ReplyError(w, http.StatusConflict, "File already exists")
		return
	}

	if err := s.fileStore.Upload(
		r.Context(),
		cfg,
		orgID,
		guildID,
		agentID,
		header.Filename,
		content,
		contentType,
		fileMeta,
	); err != nil {
		if errors.Is(err, filesystem.ErrFileAlreadyExists) {
			ReplyError(w, http.StatusConflict, "File already exists")
			return
		}
		if strings.Contains(strings.ToLower(err.Error()), "invalid filename") {
			ReplyError(w, http.StatusBadRequest, "invalid filename")
			return
		}
		ReplyError(w, http.StatusInternalServerError, err.Error())
		return
	}

	ReplyJSON(w, http.StatusOK, map[string]any{
		"guild_id":       guildID,
		"filename":       header.Filename,
		"url":            "/rustic/api/guilds/" + guildID + "/files/" + url.PathEscape(header.Filename),
		"content_type":   contentType,
		"content_length": len(content),
	})
}

func (s *Server) handleFileListCore(w http.ResponseWriter, r *http.Request, guildID, agentID string) {
	if s.fileStore == nil {
		ReplyError(w, http.StatusInternalServerError, "filesystem store not configured")
		return
	}

	orgID, cfg, statusCode, err := s.resolveFilesystemDependency(guildID)
	if err != nil {
		ReplyError(w, statusCode, err.Error())
		return
	}

	files, err := s.fileStore.List(r.Context(), cfg, orgID, guildID, agentID)
	if err != nil {
		ReplyJSON(w, http.StatusOK, []any{})
		return
	}

	links := make([]map[string]any, 0, len(files))
	for _, f := range files {
		link := map[string]any{
			"id":            idgen.NewShortUUID(),
			"url":           f.Filename,
			"name":          f.Filename,
			"metadata":      f.Metadata,
			"on_filesystem": true,
			"mimetype":      f.MimeType,
			"encoding":      nil,
			"content_hash":  nil,
			"size_in_bytes": nil,
		}
		links = append(links, link)
	}
	if len(links) == 0 {
		links = []map[string]any{}
	}

	ReplyJSON(w, http.StatusOK, links)
}

func (s *Server) handleFileDownloadCore(w http.ResponseWriter, r *http.Request, guildID, agentID, filename string) {
	if s.fileStore == nil {
		ReplyError(w, http.StatusInternalServerError, "filesystem store not configured")
		return
	}

	orgID, cfg, statusCode, err := s.resolveFilesystemDependency(guildID)
	if err != nil {
		ReplyError(w, statusCode, err.Error())
		return
	}

	content, err := s.fileStore.Read(r.Context(), cfg, orgID, guildID, agentID, filename)
	if err != nil {
		if errors.Is(err, filesystem.ErrFileNotFound) {
			ReplyError(w, http.StatusNotFound, "File not found")
			return
		}
		ReplyError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if parseDownloadFlag(r.URL.Query().Get("download")) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	} else {
		w.Header().Set("Content-Type", guessMediaType(filename))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func (s *Server) handleFileDeleteCore(w http.ResponseWriter, r *http.Request, guildID, agentID, filename string) {
	if s.fileStore == nil {
		ReplyError(w, http.StatusInternalServerError, "filesystem store not configured")
		return
	}

	orgID, cfg, statusCode, err := s.resolveFilesystemDependency(guildID)
	if err != nil {
		ReplyError(w, statusCode, err.Error())
		return
	}

	exists, err := s.fileStore.Exists(r.Context(), cfg, orgID, guildID, agentID, filename)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "invalid filename") {
			ReplyError(w, http.StatusBadRequest, "invalid filename")
			return
		}
		ReplyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !exists {
		ReplyError(w, http.StatusNotFound, "File not found")
		return
	}

	if err := s.fileStore.Delete(r.Context(), cfg, orgID, guildID, agentID, filename); err != nil {
		ReplyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseDownloadFlag(raw string) bool {
	b, err := strconv.ParseBool(strings.TrimSpace(raw))
	return err == nil && b
}

func guessMediaType(filename string) string {
	if ext := path.Ext(filename); ext != "" {
		if t := mime.TypeByExtension(ext); t != "" {
			return t
		}
	}
	return "application/octet-stream"
}

// Guild-scoped file handlers

func (s *Server) HandleFileUpload(w http.ResponseWriter, r *http.Request) {
	guildID := r.PathValue("id")
	if guildID == "" {
		ReplyError(w, http.StatusBadRequest, "guild_id required")
		return
	}
	s.handleFileUploadCore(w, r, guildID, "")
}

func (s *Server) HandleFileList(w http.ResponseWriter, r *http.Request) {
	guildID := r.PathValue("id")
	if guildID == "" {
		ReplyError(w, http.StatusBadRequest, "guild_id required")
		return
	}
	s.handleFileListCore(w, r, guildID, "")
}

func (s *Server) HandleFileDownload(w http.ResponseWriter, r *http.Request) {
	guildID := r.PathValue("id")
	filename := r.PathValue("filename")
	if guildID == "" || filename == "" {
		ReplyError(w, http.StatusBadRequest, "guild_id and filename required")
		return
	}
	s.handleFileDownloadCore(w, r, guildID, "", filename)
}

func (s *Server) HandleFileDelete(w http.ResponseWriter, r *http.Request) {
	guildID := r.PathValue("id")
	filename := r.PathValue("filename")
	if guildID == "" || filename == "" {
		ReplyError(w, http.StatusBadRequest, "guild_id and filename required")
		return
	}
	s.handleFileDeleteCore(w, r, guildID, "", filename)
}

// Agent-scoped file handlers

func (s *Server) HandleAgentFileUpload(w http.ResponseWriter, r *http.Request) {
	guildID := r.PathValue("id")
	agentID := r.PathValue("agent_id")
	if guildID == "" || agentID == "" {
		ReplyError(w, http.StatusBadRequest, "guild_id and agent_id required")
		return
	}
	s.handleFileUploadCore(w, r, guildID, agentID)
}

func (s *Server) HandleAgentFileList(w http.ResponseWriter, r *http.Request) {
	guildID := r.PathValue("id")
	agentID := r.PathValue("agent_id")
	if guildID == "" || agentID == "" {
		ReplyError(w, http.StatusBadRequest, "guild_id and agent_id required")
		return
	}
	s.handleFileListCore(w, r, guildID, agentID)
}

func (s *Server) HandleAgentFileDownload(w http.ResponseWriter, r *http.Request) {
	guildID := r.PathValue("id")
	agentID := r.PathValue("agent_id")
	filename := r.PathValue("filename")
	if guildID == "" || agentID == "" || filename == "" {
		ReplyError(w, http.StatusBadRequest, "guild_id, agent_id, and filename required")
		return
	}
	s.handleFileDownloadCore(w, r, guildID, agentID, filename)
}

func (s *Server) HandleAgentFileDelete(w http.ResponseWriter, r *http.Request) {
	guildID := r.PathValue("id")
	agentID := r.PathValue("agent_id")
	filename := r.PathValue("filename")
	if guildID == "" || agentID == "" || filename == "" {
		ReplyError(w, http.StatusBadRequest, "guild_id, agent_id, and filename required")
		return
	}
	s.handleFileDeleteCore(w, r, guildID, agentID, filename)
}
