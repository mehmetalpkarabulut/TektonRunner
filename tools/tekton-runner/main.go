package main

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"
)

type Input struct {
	Namespace      string      `json:"namespace"`
	Task           string      `json:"task"`
	AppName        string      `json:"app_name"`
	Apps           []AppSpec   `json:"apps"`
	RuntimeProfile string      `json:"runtime_profile"`
	Workspace      string      `json:"workspace"`
	Deploy         Deploy      `json:"deploy"`
	Source         Source      `json:"source"`
	Image          Image       `json:"image"`
	FileStorage    FileStorage `json:"file_storage"`
	Dependency     Dependency  `json:"dependency"`
	Migration      Migration   `json:"migration"`
	ExtraEnv       []EnvVar    `json:"extra_env"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type AppSpec struct {
	AppName        string `json:"app_name"`
	Project        string `json:"project"`
	Tag            string `json:"tag"`
	ContainerPort  int    `json:"container_port"`
	ContextSubDir  string `json:"context_sub_path"`
	RuntimeProfile string `json:"runtime_profile"`
}

type Source struct {
	Type        string     `json:"type"`
	RepoURL     string     `json:"repo_url"`
	Revision    string     `json:"revision"`
	GitUsername string     `json:"git_username"`
	GitToken    string     `json:"git_token"`
	GitSecret   string     `json:"git_secret"`
	PVCName     string     `json:"pvc_name"`
	ZipURL      string     `json:"zip_url"`
	ZipUsername string     `json:"zip_username"`
	ZipPassword string     `json:"zip_password"`
	ContextSub  string     `json:"context_sub_path"`
	NFS         *NFSConfig `json:"nfs"`
	SMB         *SMBConfig `json:"smb"`
}

type NFSConfig struct {
	Server string `json:"server"`
	Path   string `json:"path"`
	Size   string `json:"size"`
}

type SMBConfig struct {
	Server       string `json:"server"`
	Share        string `json:"share"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	Size         string `json:"size"`
	VolumeHandle string `json:"volume_handle"`
	SecretName   string `json:"secret_name"`
}

type Image struct {
	Project  string `json:"project"`
	Tag      string `json:"tag"`
	Registry string `json:"registry"`
}

type FileStorage struct {
	Enabled   bool       `json:"enabled"`
	PVCName   string     `json:"pvc_name"`
	MountPath string     `json:"mount_path"`
	SubPath   string     `json:"sub_path"`
	NFS       *NFSConfig `json:"nfs"`
	SMB       *SMBConfig `json:"smb"`
}

type TaskRunStatus struct {
	Name           string `json:"name"`
	Namespace      string `json:"namespace"`
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
	Message        string `json:"message,omitempty"`
	PodName        string `json:"pod_name,omitempty"`
	StartTime      string `json:"start_time,omitempty"`
	CompletionTime string `json:"completion_time,omitempty"`
}

type workspaceMetrics struct {
	Workspace string            `json:"workspace"`
	Node      map[string]any    `json:"node"`
	Usage     map[string]any    `json:"usage"`
	Requests  map[string]any    `json:"requests"`
	Limits    map[string]any    `json:"limits"`
	Disk      map[string]any    `json:"disk"`
	Pods      []map[string]any  `json:"pods"`
	Errors    map[string]string `json:"errors,omitempty"`
	Raw       map[string]string `json:"raw,omitempty"`
}

type clusterMetrics struct {
	Clusters []map[string]any  `json:"clusters"`
	Totals   map[string]any    `json:"totals"`
	Errors   map[string]string `json:"errors,omitempty"`
}

type Deploy struct {
	ContainerPort int `json:"container_port"`
}

type Dependency struct {
	Type  string          `json:"type"`
	Redis RedisDependency `json:"redis"`
	SQL   SQLDependency   `json:"sql"`
}

type RedisDependency struct {
	Image         string `json:"image"`
	ServiceName   string `json:"service_name"`
	Port          int    `json:"port"`
	ConnectionEnv string `json:"connection_env"`
}

type SQLDependency struct {
	Image         string `json:"image"`
	ServiceName   string `json:"service_name"`
	Port          int    `json:"port"`
	Database      string `json:"database"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	ConnectionEnv string `json:"connection_env"`
}

type AppEnvSecretRef struct {
	EnvName    string
	SecretName string
	SecretKey  string
	Value      string
}

func appendEnvRef(refs []AppEnvSecretRef, envName, secretName, secretKey string) []AppEnvSecretRef {
	envName = strings.TrimSpace(envName)
	secretName = strings.TrimSpace(secretName)
	secretKey = strings.TrimSpace(secretKey)
	if envName == "" || secretName == "" || secretKey == "" {
		return refs
	}
	for _, ref := range refs {
		if ref.EnvName == envName {
			return refs
		}
	}
	return append(refs, AppEnvSecretRef{
		EnvName:    envName,
		SecretName: secretName,
		SecretKey:  secretKey,
	})
}

func appendEnvValue(refs []AppEnvSecretRef, envName, value string) []AppEnvSecretRef {
	envName = strings.TrimSpace(envName)
	value = strings.TrimSpace(value)
	if envName == "" || value == "" {
		return refs
	}
	for _, ref := range refs {
		if ref.EnvName == envName {
			return refs
		}
	}
	return append(refs, AppEnvSecretRef{
		EnvName: envName,
		Value:   value,
	})
}

func upsertEnvValue(refs []AppEnvSecretRef, envName, value string) []AppEnvSecretRef {
	envName = strings.TrimSpace(envName)
	value = strings.TrimSpace(value)
	if envName == "" || value == "" {
		return refs
	}
	for i := range refs {
		if refs[i].EnvName == envName {
			refs[i].SecretName = ""
			refs[i].SecretKey = ""
			refs[i].Value = value
			return refs
		}
	}
	return append(refs, AppEnvSecretRef{
		EnvName: envName,
		Value:   value,
	})
}

func isConnectionEnvName(envName string) bool {
	name := strings.TrimSpace(envName)
	if strings.HasPrefix(name, "ConnectionStrings__") {
		return true
	}
	switch strings.ToUpper(name) {
	case "DATABASE_URL", "SQL_CONNECTION_STRING", "DEFAULT_CONNECTION", "REDIS_URL", "REDIS_CONNECTION", "REDIS_CONNECTION_STRING":
		return true
	default:
		return false
	}
}

func normalizeRuntimeProfile(raw string) string {
	p := strings.ToLower(strings.TrimSpace(raw))
	switch p {
	case "", "default":
		return "auto"
	case "auto", "dotnet", "node", "python", "go", "java", "custom":
		return p
	default:
		return p
	}
}

func validateRuntimeProfile(raw, field string) error {
	p := normalizeRuntimeProfile(raw)
	switch p {
	case "auto", "dotnet", "node", "python", "go", "java", "custom":
		return nil
	default:
		return fmt.Errorf("%s must be one of: auto, dotnet, node, python, go, java, custom", field)
	}
}

func runtimeProfileForApp(in Input, appName string) string {
	profile := in.RuntimeProfile
	if len(in.Apps) > 0 {
		for _, a := range in.Apps {
			if sanitizeName(a.AppName) != appName {
				continue
			}
			if strings.TrimSpace(a.RuntimeProfile) != "" {
				profile = a.RuntimeProfile
			}
			break
		}
	}
	return normalizeRuntimeProfile(profile)
}

func profileConnectionEnvRefs(profile, depType, secretName string) []AppEnvSecretRef {
	profile = normalizeRuntimeProfile(profile)
	depType = strings.ToLower(strings.TrimSpace(depType))
	if profile == "auto" || profile == "custom" {
		return nil
	}
	refs := make([]AppEnvSecretRef, 0, 4)
	switch profile {
	case "dotnet":
		if depType == "sql" || depType == "both" {
			refs = appendEnvRef(refs, "ConnectionStrings__DefaultConnection", secretName, "sql-conn")
		}
		if depType == "redis" || depType == "both" {
			refs = appendEnvRef(refs, "ConnectionStrings__Redis", secretName, "redis-conn")
		}
	case "node", "python", "go", "java":
		if depType == "sql" || depType == "both" {
			refs = appendEnvRef(refs, "DATABASE_URL", secretName, "database-url")
		}
		if depType == "redis" || depType == "both" {
			refs = appendEnvRef(refs, "REDIS_URL", secretName, "redis-url")
		}
	}
	return refs
}

func redisSecretData(host string, port int) map[string]string {
	conn := fmt.Sprintf("%s:%d", host, port)
	return map[string]string{
		"redis-conn": conn,
		"redis-url":  fmt.Sprintf("redis://%s/0", conn),
		"redis-host": host,
		"redis-port": strconv.Itoa(port),
	}
}

func sqlSecretData(host string, cfg SQLDependency) map[string]string {
	return map[string]string{
		"sql-conn":     fmt.Sprintf("Host=%s;Port=%d;Database=%s;Username=%s;Password=%s;SSL Mode=Disable;", host, cfg.Port, cfg.Database, cfg.Username, cfg.Password),
		"database-url": fmt.Sprintf("postgresql://%s:%s@%s:%d/%s?sslmode=disable", neturl.QueryEscape(cfg.Username), neturl.QueryEscape(cfg.Password), host, cfg.Port, neturl.PathEscape(cfg.Database)),
		"db-host":      host,
		"db-port":      strconv.Itoa(cfg.Port),
		"db-name":      cfg.Database,
		"db-user":      cfg.Username,
		"db-password":  cfg.Password,
		"db-sslmode":   "disable",
	}
}

func defaultRedisEnvRefs(secretName string, cfg RedisDependency) []AppEnvSecretRef {
	refs := make([]AppEnvSecretRef, 0, 6)
	refs = appendEnvRef(refs, "REDIS_URL", secretName, "redis-url")
	refs = appendEnvRef(refs, "REDIS_HOST", secretName, "redis-host")
	refs = appendEnvRef(refs, "REDIS_PORT", secretName, "redis-port")
	refs = appendEnvRef(refs, "REDIS_CONNECTION", secretName, "redis-conn")
	refs = appendEnvRef(refs, "REDIS_CONNECTION_STRING", secretName, "redis-conn")
	refs = appendEnvRef(refs, cfg.ConnectionEnv, secretName, "redis-conn")
	return refs
}

func defaultSQLEnvRefs(secretName string, cfg SQLDependency) []AppEnvSecretRef {
	refs := make([]AppEnvSecretRef, 0, 10)
	isDotNetProfile := strings.HasPrefix(strings.TrimSpace(cfg.ConnectionEnv), "ConnectionStrings__") ||
		strings.EqualFold(strings.TrimSpace(cfg.ConnectionEnv), "SQL_CONNECTION_STRING") ||
		strings.EqualFold(strings.TrimSpace(cfg.ConnectionEnv), "DEFAULT_CONNECTION")
	if !isDotNetProfile {
		refs = appendEnvRef(refs, "DATABASE_URL", secretName, "database-url")
	}
	refs = appendEnvRef(refs, "DB_HOST", secretName, "db-host")
	refs = appendEnvRef(refs, "DB_PORT", secretName, "db-port")
	refs = appendEnvRef(refs, "DB_NAME", secretName, "db-name")
	refs = appendEnvRef(refs, "DB_USER", secretName, "db-user")
	refs = appendEnvRef(refs, "DB_PASSWORD", secretName, "db-password")
	refs = appendEnvRef(refs, "DB_SSLMODE", secretName, "db-sslmode")
	refs = appendEnvRef(refs, "SQL_CONNECTION_STRING", secretName, "sql-conn")
	refs = appendEnvRef(refs, "DEFAULT_CONNECTION", secretName, "sql-conn")
	refs = appendEnvRef(refs, cfg.ConnectionEnv, secretName, "sql-conn")
	return refs
}

type ResolvedFileStorage struct {
	PVCName   string
	MountPath string
	SubPath   string
}

type Migration struct {
	Enabled bool     `json:"enabled"`
	Image   string   `json:"image"`
	Command []string `json:"command"`
	Args    []string `json:"args"`
	EnvName string   `json:"env_name"`
}

type AppTarget struct {
	AppName       string
	Project       string
	Tag           string
	ContainerPort int
	ContextSubDir string
	BaseImageMap  string
}

type RenderContext struct {
	Namespace    string
	Task         string
	SourceType   string
	RepoURL      string
	Revision     string
	Project      string
	Repo         string
	Tag          string
	Registry     string
	ZipURL       string
	ZipUsername  string
	ZipPassword  string
	GitSecret    string
	HasGit       bool
	HasZip       bool
	AppName      string
	ContextSub   string
	BaseImageMap string
}

func harborProjectName(workspace string) string {
	name := sanitizeName(strings.TrimSpace(workspace))
	name = strings.TrimPrefix(name, "ws-")
	name = strings.Trim(name, "-")
	if name == "" {
		return "default"
	}
	return name
}

type ServerState struct {
	mu        sync.Mutex
	endpoints map[string]string
}

type ExternalPortEntry struct {
	Workspace    string `json:"workspace"`
	App          string `json:"app"`
	ExternalPort int    `json:"external_port"`
}

type ExternalPortStore struct {
	mu      sync.Mutex
	path    string
	entries []ExternalPortEntry
}

type RunEvent struct {
	Timestamp string            `json:"timestamp"`
	RunID     string            `json:"run_id"`
	Workspace string            `json:"workspace"`
	App       string            `json:"app"`
	Stage     string            `json:"stage"`
	Status    string            `json:"status"`
	Message   string            `json:"message"`
	Meta      map[string]string `json:"meta,omitempty"`
}

type TaskRunLogBlock struct {
	TaskRun   string `json:"taskrun"`
	Namespace string `json:"namespace"`
	Logs      string `json:"logs,omitempty"`
	Error     string `json:"error,omitempty"`
	Source    string `json:"source,omitempty"`
	Collected string `json:"collected_at,omitempty"`
}

type ContainerLogBlock struct {
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Logs      string `json:"logs,omitempty"`
	Error     string `json:"error,omitempty"`
	Source    string `json:"source,omitempty"`
	Collected string `json:"collected_at,omitempty"`
}

type EventStore struct {
	mu      sync.Mutex
	path    string
	events  []RunEvent
	maxKeep int
}

var serverHostIP string
var serverKubeconfig string
var serverState = &ServerState{endpoints: map[string]string{}}
var portStore = &ExternalPortStore{path: "/home/beko/port-map.json"}
var eventStore = &EventStore{path: "/home/beko/run-events.jsonl", maxKeep: 5000}
var logArchiveRoot = "/home/beko/run-log-archive"
var pathTokenRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
var zipServerNamespace = "tekton-pipelines"
var zipServerServiceName = "zip-server"

type Forward struct {
	Port int
	Cmd  *exec.Cmd
}

var forwardMu sync.Mutex
var forwards = map[string]*Forward{}

func deriveWorkspace(in Input) string {
	ws := strings.TrimSpace(in.Workspace)
	if ws != "" {
		return ws
	}
	app := sanitizeName(in.AppName)
	if app == "" && len(in.Apps) > 0 {
		app = sanitizeName(in.Apps[0].AppName)
	}
	if app == "" {
		return ""
	}
	return "ws-" + app
}

func normalizeContextSubDir(raw string) (string, error) {
	p := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if p == "" {
		return "", nil
	}
	p = path.Clean(p)
	p = strings.TrimSpace(p)
	if p == "." {
		return ".", nil
	}
	if p == "" {
		return "", nil
	}
	if strings.HasPrefix(p, "/") || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") {
		return "", fmt.Errorf("invalid context_sub_path: %q", raw)
	}
	return strings.TrimPrefix(p, "./"), nil
}

func normalizeStorageSubPath(raw string) (string, error) {
	s := strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	s = strings.TrimPrefix(s, "./")
	s = strings.Trim(s, "/")
	if s == "" {
		return "", nil
	}
	parts := strings.Split(s, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "." || p == ".." {
			return "", fmt.Errorf("invalid file_storage.sub_path: %q", raw)
		}
		out = append(out, p)
	}
	return strings.Join(out, "/"), nil
}

func resolveAppTargets(in Input) ([]AppTarget, error) {
	targets := make([]AppTarget, 0, 4)
	basePort := in.Deploy.ContainerPort
	if basePort == 0 {
		basePort = 8080
	}
	baseTag := in.Image.Tag
	if strings.TrimSpace(baseTag) == "" {
		baseTag = "latest"
	}
	if len(in.Apps) == 0 {
		p := strings.TrimSpace(in.Image.Project)
		if p == "" {
			p = sanitizeName(in.AppName)
		}
		if p == "" {
			return nil, fmt.Errorf("app_name is required")
		}
		ctx, err := normalizeContextSubDir(in.Source.ContextSub)
		if err != nil {
			return nil, err
		}
		targets = append(targets, AppTarget{
			AppName:       sanitizeName(in.AppName),
			Project:       sanitizeName(p),
			Tag:           strings.TrimSpace(baseTag),
			ContainerPort: basePort,
			ContextSubDir: ctx,
		})
		return targets, nil
	}
	seen := map[string]bool{}
	for i, app := range in.Apps {
		name := sanitizeName(app.AppName)
		if name == "" {
			return nil, fmt.Errorf("apps[%d].app_name is required", i)
		}
		if seen[name] {
			return nil, fmt.Errorf("apps has duplicate app_name: %s", name)
		}
		seen[name] = true
		project := sanitizeName(app.Project)
		if project == "" {
			project = name
		}
		tag := strings.TrimSpace(app.Tag)
		if tag == "" {
			tag = strings.TrimSpace(baseTag)
		}
		port := app.ContainerPort
		ctx, err := normalizeContextSubDir(app.ContextSubDir)
		if err != nil {
			return nil, fmt.Errorf("apps[%d]: %w", i, err)
		}
		if port == 0 {
			port = basePort
		}
		targets = append(targets, AppTarget{
			AppName:       name,
			Project:       project,
			Tag:           tag,
			ContainerPort: port,
			ContextSubDir: ctx,
		})
	}
	return targets, nil
}

func deriveApp(in Input) string {
	targets, err := resolveAppTargets(in)
	if err != nil || len(targets) == 0 {
		return sanitizeName(in.AppName)
	}
	return targets[0].AppName
}

func nextRunID() string {
	return time.Now().UTC().Format("20060102T150405Z") + "-" + randSuffix()
}

type extractedBuildError struct {
	TaskRun string
	Line    string
}

func extractBuildErrors(taskrunLogs []TaskRunLogBlock, limit int) []extractedBuildError {
	if limit <= 0 {
		limit = 20
	}
	out := make([]extractedBuildError, 0, limit)
	seen := map[string]bool{}
	for _, tr := range taskrunLogs {
		lines := strings.Split(tr.Logs, "\n")
		for _, raw := range lines {
			line := strings.TrimSpace(stripANSICodes(raw))
			if line == "" {
				continue
			}
			lower := strings.ToLower(line)
			if !strings.Contains(lower, ": error ") &&
				!strings.Contains(lower, ": warning ") &&
				!strings.Contains(lower, "error building image:") &&
				!strings.Contains(lower, "exception") &&
				!strings.Contains(lower, "panic:") &&
				!strings.Contains(lower, "traceback") &&
				!strings.Contains(lower, "validationerror") &&
				!strings.Contains(lower, "module not found") {
				continue
			}
			key := tr.TaskRun + "\n" + line
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, extractedBuildError{
				TaskRun: tr.TaskRun,
				Line:    line,
			})
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

func stripANSICodes(s string) string {
	ansiRe := regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	return ansiRe.ReplaceAllString(s, "")
}

func emitRunEvent(runID, workspace, app, stage, status, msg string, meta map[string]string) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(workspace) == "" || strings.TrimSpace(app) == "" {
		return
	}
	_ = eventStore.append(RunEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		RunID:     runID,
		Workspace: workspace,
		App:       app,
		Stage:     stage,
		Status:    status,
		Message:   msg,
		Meta:      meta,
	})
}

func main() {
	inPath := flag.String("in", "", "input JSON file (default: stdin)")
	outDir := flag.String("out-dir", "", "output directory (default: stdout only)")
	apply := flag.Bool("apply", false, "kubectl apply generated manifests")
	server := flag.Bool("server", false, "run HTTP server")
	addr := flag.String("addr", ":8088", "server listen address")
	apiKey := flag.String("api-key", "", "optional API key for server auth (Bearer)")
	hostIP := flag.String("host-ip", "", "host IP for endpoint generation (optional)")
	kubeconfig := flag.String("kubeconfig", "", "kubeconfig for kubectl (optional)")
	flag.Parse()

	if *server {
		serverHostIP = *hostIP
		serverKubeconfig = *kubeconfig
		runServer(*addr, *apiKey)
		return
	}

	var data []byte
	var err error
	if *inPath == "" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(*inPath)
	}
	if err != nil {
		fatal("read input", err)
	}

	var in Input
	if err := json.Unmarshal(data, &in); err != nil {
		fatal("parse JSON", err)
	}

	manifests, targets, err := buildManifests(&in)
	if err != nil {
		fatal("validate input", err)
	}

	if *outDir != "" {
		if err := os.MkdirAll(*outDir, 0o755); err != nil {
			fatal("mkdir out-dir", err)
		}
		for i, m := range manifests {
			path := filepath.Join(*outDir, fmt.Sprintf("manifest-%02d.yaml", i+1))
			if err := os.WriteFile(path, []byte(m), 0o644); err != nil {
				fatal("write manifest", err)
			}
		}
	}

	if !*apply {
		for i, m := range manifests {
			if i > 0 {
				fmt.Println("---")
			}
			fmt.Print(m)
		}
		return
	}

	taskRunByApp := map[string]string{}
	taskRunOrder := make([]string, 0, len(targets))
	runID := nextRunID()
	ws := deriveWorkspace(in)
	app := deriveApp(in)
	emitRunEvent(runID, ws, app, "request", "accepted", "CLI apply started", map[string]string{
		"source_type": in.Source.Type,
	})
	for _, m := range manifests {
		if isTaskRun(m) {
			name, err := kubectlCreateName(m, in.Namespace)
			if err != nil {
				emitRunEvent(runID, ws, app, "taskrun", "failed", "TaskRun create failed", nil)
				fatal("kubectl create", err)
			}
			taskRunOrder = append(taskRunOrder, name)
			emitRunEvent(runID, ws, app, "taskrun", "submitted", "TaskRun created", map[string]string{
				"taskrun":   name,
				"namespace": in.Namespace,
			})
		} else {
			if err := kubectlApply(m); err != nil {
				emitRunEvent(runID, ws, app, "manifests", "failed", "Prerequisite manifest apply failed", nil)
				fatal("kubectl apply", err)
			}
		}
	}
	for i, t := range targets {
		if i < len(taskRunOrder) {
			taskRunByApp[t.AppName] = taskRunOrder[i]
		}
	}

	if len(taskRunByApp) > 0 && (in.Source.Type == "zip" || in.Source.Type == "git") {
		if err := handleZipDeploy(in, targets, taskRunByApp, runID); err != nil {
			emitRunEvent(runID, ws, app, "deploy", "failed", err.Error(), nil)
			fatal("zip deploy", err)
		}
		emitRunEvent(runID, ws, app, "deploy", "succeeded", "End-to-end flow completed", nil)
	}
}

func runServer(addr, apiKey string) {
	if err := portStore.load(); err != nil {
		log.Printf("port map load error: %v", err)
	}
	if err := eventStore.load(); err != nil {
		log.Printf("event log load error: %v", err)
	}
	// Start port forwards for existing mappings
	for _, e := range portStore.list() {
		if err := ensureForward(e.Workspace, e.App, e.ExternalPort); err != nil {
			log.Printf("forward start failed for %s/%s: %v", e.Workspace, e.App, err)
		}
	}

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	http.HandleFunc("/zip/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if apiKey != "" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+apiKey {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		const maxUploadSize = 512 << 20 // 512MiB
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "invalid multipart form", http.StatusBadRequest)
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "file is required (multipart field: file)", http.StatusBadRequest)
			return
		}
		defer file.Close()

		filenameInput := strings.TrimSpace(r.FormValue("filename"))
		if filenameInput == "" && header != nil {
			filenameInput = header.Filename
		}
		filename, err := sanitizeZipFilename(filenameInput)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		tmp, err := os.CreateTemp("", "zip-upload-*.zip")
		if err != nil {
			http.Error(w, "temp file create failed", http.StatusInternalServerError)
			return
		}
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)
		if _, err := io.Copy(tmp, file); err != nil {
			tmp.Close()
			http.Error(w, "failed to store upload", http.StatusInternalServerError)
			return
		}
		if err := tmp.Close(); err != nil {
			http.Error(w, "failed to finalize upload", http.StatusInternalServerError)
			return
		}

		pod, err := getRunningZipServerPod()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := kubectlExecInPod(zipServerNamespace, pod, "mkdir", "-p", "/srv"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := kubectlCopyToPod(zipServerNamespace, tmpPath, pod, "/srv/"+filename); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		host := resolveExternalHost(r)
		externalPort := zipServerExternalPort()
		internalURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:8080/%s", zipServerServiceName, zipServerNamespace, filename)
		externalURL := fmt.Sprintf("http://%s:%s/%s", host, externalPort, filename)
		resp := map[string]any{
			"status":       "uploaded",
			"filename":     filename,
			"pod":          pod,
			"internal_url": internalURL,
			"external_url": externalURL,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	})

	http.HandleFunc("/zip/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if apiKey != "" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+apiKey {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		filenameInput := strings.TrimSpace(r.URL.Query().Get("filename"))
		if filenameInput == "" {
			filenameInput = strings.TrimSpace(r.FormValue("filename"))
		}
		filename, err := sanitizeZipFilename(filenameInput)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		pod, err := getRunningZipServerPod()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := kubectlExecInPod(zipServerNamespace, pod, "rm", "-f", "/srv/"+filename); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		resp := map[string]any{
			"status":   "deleted",
			"filename": filename,
			"pod":      pod,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	})

	http.HandleFunc("/run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if apiKey != "" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+apiKey {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}

		var in Input
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		workspace := deriveWorkspace(in)
		app := deriveApp(in)
		runID := nextRunID()
		emitRunEvent(runID, workspace, app, "request", "accepted", "Run request accepted", map[string]string{
			"source_type": in.Source.Type,
		})

		manifests, targets, err := buildManifests(&in)
		if err != nil {
			emitRunEvent(runID, workspace, app, "validate", "failed", err.Error(), nil)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		emitRunEvent(runID, workspace, app, "validate", "succeeded", "Input validated and manifests generated", nil)

		dryRun := r.URL.Query().Get("dry_run") == "true"
		if dryRun {
			w.Header().Set("Content-Type", "application/yaml")
			for i, m := range manifests {
				if i > 0 {
					w.Write([]byte("\n---\n"))
				}
				w.Write([]byte(m))
			}
			return
		}

		taskRunByApp := map[string]string{}
		taskRunOrder := make([]string, 0, len(targets))
		for _, m := range manifests {
			if isTaskRun(m) {
				name, err := kubectlCreateName(m, in.Namespace)
				if err != nil {
					emitRunEvent(runID, workspace, app, "taskrun", "failed", "TaskRun create failed", map[string]string{
						"namespace": in.Namespace,
					})
					http.Error(w, "kubectl create failed", http.StatusInternalServerError)
					return
				}
				taskRunOrder = append(taskRunOrder, name)
				emitRunEvent(runID, workspace, app, "taskrun", "submitted", "TaskRun created", map[string]string{
					"taskrun":   name,
					"namespace": in.Namespace,
				})
			} else {
				if err := kubectlApply(m); err != nil {
					emitRunEvent(runID, workspace, app, "manifests", "failed", "Prerequisite manifest apply failed", nil)
					http.Error(w, "kubectl apply failed", http.StatusInternalServerError)
					return
				}
			}
		}
		for i, t := range targets {
			if i < len(taskRunOrder) {
				taskRunByApp[t.AppName] = taskRunOrder[i]
			}
		}
		emitRunEvent(runID, workspace, app, "manifests", "succeeded", "Prerequisite manifests applied", nil)

		if len(taskRunByApp) > 0 && (in.Source.Type == "zip" || in.Source.Type == "git") {
			go func(req Input, tgts []AppTarget, taskRuns map[string]string, rid string) {
				if err := handleZipDeploy(req, tgts, taskRuns, rid); err != nil {
					emitRunEvent(rid, deriveWorkspace(req), deriveApp(req), "deploy", "failed", err.Error(), nil)
					log.Printf("deploy error: %v", err)
					return
				}
				emitRunEvent(rid, deriveWorkspace(req), deriveApp(req), "deploy", "succeeded", "End-to-end flow completed", nil)
			}(in, targets, taskRunByApp, runID)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		appNames := make([]string, 0, len(targets))
		for _, t := range targets {
			appNames = append(appNames, t.AppName)
		}
		taskRunPrimary := ""
		if len(targets) > 0 {
			taskRunPrimary = taskRunByApp[targets[0].AppName]
		}
		resp := map[string]any{
			"status":    "submitted",
			"taskrun":   taskRunPrimary,
			"taskruns":  taskRunByApp,
			"apps":      appNames,
			"namespace": in.Namespace,
			"workspace": workspace,
			"app":       deriveApp(in),
			"run_id":    runID,
			"multi_app": len(targets) > 1,
			"app_count": len(targets),
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	http.HandleFunc("/endpoint", func(w http.ResponseWriter, r *http.Request) {
		workspace := r.URL.Query().Get("workspace")
		app := r.URL.Query().Get("app")
		if workspace == "" || app == "" {
			http.Error(w, "workspace and app are required", http.StatusBadRequest)
			return
		}
		key := workspace + "/" + app
		kcfgPath := filepath.Join("/home/beko/kubeconfigs", workspace+".yaml")
		reqHost := resolveExternalHost(r)
		serverState.mu.Lock()
		if url, ok := serverState.endpoints[key]; ok {
			serverState.mu.Unlock()
			url = withResolvedHost(url, reqHost)
			if extPort, ok := portStore.get(workspace, app); ok {
				url = withResolvedPort(url, extPort)
			}
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{"endpoint": url}
			if info, err := getDependencyAccessInfo(kcfgPath, workspace, app); err == nil && len(info) > 0 {
				addConnectionURL(info, url)
				resp["access"] = info
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		serverState.mu.Unlock()

		port, err := getServiceNodePort(kcfgPath, workspace, app)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		url := fmt.Sprintf("http://%s:%d", reqHost, port)
		if extPort, ok := portStore.get(workspace, app); ok {
			url = withResolvedPort(url, extPort)
		}
		serverState.mu.Lock()
		serverState.endpoints[key] = url
		serverState.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"endpoint": url}
		if info, err := getDependencyAccessInfo(kcfgPath, workspace, app); err == nil && len(info) > 0 {
			addConnectionURL(info, url)
			resp["access"] = info
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	http.HandleFunc("/workspaces", func(w http.ResponseWriter, r *http.Request) {
		list, err := listWorkspaces()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(list)
	})

	http.HandleFunc("/workspace/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		workspace := r.URL.Query().Get("workspace")
		if workspace == "" {
			http.Error(w, "workspace is required", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(workspace, "ws-") {
			http.Error(w, "workspace must start with ws-", http.StatusBadRequest)
			return
		}
		if err := deleteWorkspace(workspace); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"deleted"}`))
	})

	http.HandleFunc("/workspace/status", func(w http.ResponseWriter, r *http.Request) {
		workspace := r.URL.Query().Get("workspace")
		if workspace == "" {
			http.Error(w, "workspace is required", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(workspace, "ws-") {
			http.Error(w, "workspace must start with ws-", http.StatusBadRequest)
			return
		}
		info, err := getWorkspaceStatus(workspace)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(info)
	})

	http.HandleFunc("/workspace/metrics", func(w http.ResponseWriter, r *http.Request) {
		workspace := r.URL.Query().Get("workspace")
		if workspace == "" {
			http.Error(w, "workspace is required", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(workspace, "ws-") {
			http.Error(w, "workspace must start with ws-", http.StatusBadRequest)
			return
		}
		info, err := getWorkspaceMetrics(workspace)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(info)
	})

	http.HandleFunc("/cluster/metrics", func(w http.ResponseWriter, r *http.Request) {
		info, err := getClusterMetrics()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(info)
	})

	http.HandleFunc("/taskrun/status", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		ns := r.URL.Query().Get("namespace")
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if ns == "" {
			ns = "tekton-pipelines"
		}
		info, err := getTaskRunStatus(ns, name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(info)
	})

	http.HandleFunc("/taskrun/all", func(w http.ResponseWriter, r *http.Request) {
		ns := r.URL.Query().Get("namespace")
		if ns == "" {
			ns = "tekton-pipelines"
		}
		info, err := getAllTaskRuns(ns)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(info)
	})

	http.HandleFunc("/taskrun/logs", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		ns := r.URL.Query().Get("namespace")
		tail := r.URL.Query().Get("tail")
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if ns == "" {
			ns = "tekton-pipelines"
		}
		out, err := getTaskRunLogs(ns, name, tail)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(out)
	})

	http.HandleFunc("/taskrun/logs/stream", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		ns := r.URL.Query().Get("namespace")
		tail := r.URL.Query().Get("tail")
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if ns == "" {
			ns = "tekton-pipelines"
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "stream not supported", http.StatusInternalServerError)
			return
		}

		pod := ""
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			statusBytes, err := getTaskRunStatus(ns, name)
			if err == nil {
				var st TaskRunStatus
				if json.Unmarshal(statusBytes, &st) == nil && st.PodName != "" {
					pod = st.PodName
					break
				}
			}
			io.WriteString(w, "Waiting for TaskRun pod...\n")
			flusher.Flush()
			time.Sleep(2 * time.Second)
		}
		if pod == "" {
			http.Error(w, "taskrun pod not created yet", http.StatusGatewayTimeout)
			return
		}

		tailArg := "2000"
		if strings.TrimSpace(tail) != "" {
			tailArg = strings.TrimSpace(tail)
		}
		cmd := kubectlCmd("-n", ns, "logs", "-f", pod, "--all-containers", "--tail", tailArg, "--max-log-requests=10")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			http.Error(w, "logs stream failed", http.StatusInternalServerError)
			return
		}
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			http.Error(w, "logs stream start failed", http.StatusInternalServerError)
			return
		}

		go func() {
			<-r.Context().Done()
			_ = cmd.Process.Kill()
		}()

		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				w.Write(buf[:n])
				flusher.Flush()
			}
			if err != nil {
				break
			}
		}
		_ = cmd.Wait()
	})

	http.HandleFunc("/run/logs", func(w http.ResponseWriter, r *http.Request) {
		workspace := strings.TrimSpace(r.URL.Query().Get("workspace"))
		app := strings.TrimSpace(r.URL.Query().Get("app"))
		if app != "" {
			app = sanitizeName(app)
		}
		runID := strings.TrimSpace(r.URL.Query().Get("run_id"))
		limit := 300
		if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 5000 {
				limit = n
			}
		}
		if workspace == "" {
			http.Error(w, "workspace is required", http.StatusBadRequest)
			return
		}
		events := eventStore.query(workspace, app, runID, limit)
		includeTaskRun := parseBoolQuery(r.URL.Query().Get("include_taskrun"), true)
		includeContainers := parseBoolQuery(r.URL.Query().Get("include_containers"), true)
		tailRaw := 400
		if v := strings.TrimSpace(r.URL.Query().Get("tail_raw")); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 5000 {
				tailRaw = n
			}
		}
		taskrunLogs := make([]TaskRunLogBlock, 0, 4)
		containerLogs := make([]ContainerLogBlock, 0, 8)
		errors := map[string]string{}

		if includeTaskRun {
			refs := extractTaskRuns(events)
			for _, ref := range refs {
				b, err := getTaskRunLogs(ref.Namespace, ref.TaskRun, strconv.Itoa(tailRaw))
				if err != nil {
					ref.Error = err.Error()
					archived, aerr := loadArchivedTaskRunLog(workspace, app, ref.Namespace, ref.TaskRun)
					if aerr == nil && strings.TrimSpace(archived.Logs) != "" {
						ref.Logs = archived.Logs
						ref.Source = "archive"
						ref.Collected = archived.Collected
						ref.Error = ""
						errors["taskrun_live_"+ref.TaskRun] = err.Error()
					}
				} else {
					ref.Logs = string(b)
					ref.Source = "live"
					ref.Collected = time.Now().UTC().Format(time.RFC3339)
					if aerr := archiveTaskRunLog(workspace, app, ref); aerr != nil {
						errors["taskrun_archive_"+ref.TaskRun] = aerr.Error()
					}
				}
				taskrunLogs = append(taskrunLogs, ref)
			}
		}
		if includeContainers {
			blocks, err := getWorkspaceContainerLogs(workspace, app, tailRaw)
			if err != nil {
				errors["container_logs_live"] = err.Error()
				archived, aerr := loadArchivedContainerLogs(workspace, app)
				if aerr != nil {
					errors["container_logs_archive"] = aerr.Error()
				} else {
					containerLogs = archived
				}
			} else {
				for i := range blocks {
					if strings.TrimSpace(blocks[i].Logs) != "" {
						blocks[i].Source = "live"
						blocks[i].Collected = time.Now().UTC().Format(time.RFC3339)
						if aerr := archiveContainerLog(workspace, app, blocks[i]); aerr != nil {
							errors["container_archive_"+blocks[i].Pod+"_"+blocks[i].Container] = aerr.Error()
						}
						continue
					}
					if strings.TrimSpace(blocks[i].Error) != "" {
						archived, aerr := loadArchivedContainerLog(workspace, app, blocks[i].Pod, blocks[i].Container)
						if aerr == nil && strings.TrimSpace(archived.Logs) != "" {
							blocks[i].Logs = archived.Logs
							blocks[i].Source = "archive"
							blocks[i].Collected = archived.Collected
							blocks[i].Error = ""
							errors["container_live_"+blocks[i].Pod+"_"+blocks[i].Container] = "served from archive"
						}
					}
				}
				containerLogs = blocks
			}
		}
		format := strings.TrimSpace(r.URL.Query().Get("format"))
		if format == "text" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			var b strings.Builder
			writeTextLogSection(&b, "Run Summary")
			fmt.Fprintf(&b, "Workspace : %s\n", workspace)
			if app != "" {
				fmt.Fprintf(&b, "App       : %s\n", app)
			}
			if runID != "" {
				fmt.Fprintf(&b, "Run ID    : %s\n", runID)
			}
			fmt.Fprintf(&b, "Events    : %d\n", len(events))
			if len(events) > 0 {
				fmt.Fprintf(&b, "Started   : %s\n", events[0].Timestamp)
				fmt.Fprintf(&b, "Updated   : %s\n", events[len(events)-1].Timestamp)
			}

			summary := summarizeRunEvents(events)
			if len(summary) > 0 {
				b.WriteString("\nLatest stage status:\n")
				for _, e := range summary {
					fmt.Fprintf(&b, "  %s %-12s %s\n", formatEventBadge(e.Status), e.Stage, e.Message)
				}
			}

			writeTextLogSection(&b, "Timeline")
			for _, e := range events {
				fmt.Fprintf(&b, "%s %s %-12s %s\n",
					e.Timestamp, formatEventBadge(e.Status), e.Stage, e.Message)
				if len(e.Meta) > 0 {
					metaKeys := make([]string, 0, len(e.Meta))
					for k := range e.Meta {
						metaKeys = append(metaKeys, k)
					}
					sort.Strings(metaKeys)
					for _, k := range metaKeys {
						fmt.Fprintf(&b, "  - %s: %s\n", k, e.Meta[k])
					}
				}
			}
			buildErrors := extractBuildErrors(taskrunLogs, 24)
			if len(buildErrors) > 0 {
				writeTextLogSection(&b, "Build Errors")
				for _, item := range buildErrors {
					fmt.Fprintf(&b, "- [%s] %s\n", item.TaskRun, item.Line)
				}
			}
			if includeTaskRun {
				writeTextLogSection(&b, "TaskRun Logs")
				for _, tr := range taskrunLogs {
					fmt.Fprintf(&b, "--- TaskRun %s (ns=%s", tr.TaskRun, tr.Namespace)
					if tr.Source != "" {
						fmt.Fprintf(&b, ", source=%s", tr.Source)
					}
					if tr.Collected != "" {
						fmt.Fprintf(&b, ", collected=%s", tr.Collected)
					}
					b.WriteString(") ---\n")
					if tr.Error != "" {
						fmt.Fprintf(&b, "ERROR: %s\n", tr.Error)
						continue
					}
					b.WriteString(trimLogBlock(tr.Logs))
					if strings.TrimSpace(tr.Logs) != "" {
						b.WriteString("\n")
					}
					b.WriteString("\n")
				}
			}
			if includeContainers {
				writeTextLogSection(&b, "Workspace Container Logs")
				for _, c := range containerLogs {
					fmt.Fprintf(&b, "--- Pod %s / Container %s", c.Pod, c.Container)
					if c.Source != "" {
						fmt.Fprintf(&b, " (source=%s", c.Source)
						if c.Collected != "" {
							fmt.Fprintf(&b, ", collected=%s", c.Collected)
						}
						b.WriteString(")")
					}
					b.WriteString(" ---\n")
					if c.Error != "" {
						fmt.Fprintf(&b, "ERROR: %s\n", c.Error)
						continue
					}
					b.WriteString(trimLogBlock(c.Logs))
					if strings.TrimSpace(c.Logs) != "" {
						b.WriteString("\n")
					}
					b.WriteString("\n")
				}
			}
			if len(errors) > 0 {
				writeTextLogSection(&b, "Diagnostics")
				keys := make([]string, 0, len(errors))
				for k := range errors {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Fprintf(&b, "- %s: %s\n", k, errors[k])
				}
			}
			w.Write([]byte(b.String()))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"workspace":          workspace,
			"app":                app,
			"run_id":             runID,
			"count":              len(events),
			"events":             events,
			"taskrun_logs":       taskrunLogs,
			"container_logs":     containerLogs,
			"include_taskrun":    includeTaskRun,
			"include_containers": includeContainers,
			"errors":             errors,
		})
	})

	http.HandleFunc("/pod/logs/stream", func(w http.ResponseWriter, r *http.Request) {
		workspace := r.URL.Query().Get("workspace")
		pod := r.URL.Query().Get("pod")
		container := r.URL.Query().Get("container")
		tail := r.URL.Query().Get("tail")
		if workspace == "" || pod == "" {
			http.Error(w, "workspace and pod are required", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(workspace, "ws-") {
			http.Error(w, "workspace must start with ws-", http.StatusBadRequest)
			return
		}
		kcfg := filepath.Join("/home/beko/kubeconfigs", workspace+".yaml")
		if _, err := os.Stat(kcfg); err != nil {
			http.Error(w, "kubeconfig not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "stream not supported", http.StatusInternalServerError)
			return
		}

		tailArg := "500"
		if strings.TrimSpace(tail) != "" {
			tailArg = strings.TrimSpace(tail)
		}
		args := []string{"--kubeconfig", kcfg, "-n", workspace, "logs", "-f", pod, "--tail", tailArg, "--max-log-requests=10"}
		if strings.TrimSpace(container) != "" {
			args = append(args, "-c", container)
		} else {
			args = append(args, "--all-containers")
		}
		cmd := exec.Command("kubectl", args...)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			http.Error(w, "logs stream failed", http.StatusInternalServerError)
			return
		}
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			http.Error(w, "logs stream start failed", http.StatusInternalServerError)
			return
		}

		go func() {
			<-r.Context().Done()
			_ = cmd.Process.Kill()
		}()

		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				w.Write(buf[:n])
				flusher.Flush()
			}
			if err != nil {
				break
			}
		}
		_ = cmd.Wait()
	})

	http.HandleFunc("/app/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		workspace := r.URL.Query().Get("workspace")
		app := r.URL.Query().Get("app")
		if workspace == "" || app == "" {
			http.Error(w, "workspace and app are required", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(workspace, "ws-") {
			http.Error(w, "workspace must start with ws-", http.StatusBadRequest)
			return
		}
		if err := deleteApp(workspace, app); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"deleted"}`))
	})

	http.HandleFunc("/workspace/scale", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		workspace := r.URL.Query().Get("workspace")
		app := r.URL.Query().Get("app")
		replicas := r.URL.Query().Get("replicas")
		if workspace == "" || app == "" || replicas == "" {
			http.Error(w, "workspace, app, replicas are required", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(workspace, "ws-") {
			http.Error(w, "workspace must start with ws-", http.StatusBadRequest)
			return
		}
		if err := scaleApp(workspace, app, replicas); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"scaled"}`))
	})

	http.HandleFunc("/app/restart", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		workspace := r.URL.Query().Get("workspace")
		app := r.URL.Query().Get("app")
		if workspace == "" || app == "" {
			http.Error(w, "workspace and app are required", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(workspace, "ws-") {
			http.Error(w, "workspace must start with ws-", http.StatusBadRequest)
			return
		}
		if err := rolloutRestart(workspace, app); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"restarted"}`))
	})

	http.HandleFunc("/workspace/restart", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		workspace := r.URL.Query().Get("workspace")
		if workspace == "" {
			http.Error(w, "workspace is required", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(workspace, "ws-") {
			http.Error(w, "workspace must start with ws-", http.StatusBadRequest)
			return
		}
		if err := rolloutRestartAll(workspace); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"restarted"}`))
	})

	http.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(openAPISpec()))
	})

	http.HandleFunc("/docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(swaggerHTML()))
	})

	http.HandleFunc("/hostinfo", func(w http.ResponseWriter, r *http.Request) {
		host := serverHostIP
		if host == "" {
			host = strings.Split(r.Host, ":")[0]
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(`{"host_ip":"%s"}`, host)))
	})

	http.HandleFunc("/external-map", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			entries := portStore.list()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			b, _ := json.Marshal(entries)
			w.Write(b)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req ExternalPortEntry
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if req.Workspace == "" || req.App == "" || req.ExternalPort <= 0 {
			http.Error(w, "workspace, app and external_port are required", http.StatusBadRequest)
			return
		}
		if err := portStore.upsert(req); err != nil {
			if err == errPortConflict {
				http.Error(w, "port already in use", http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := ensureForward(req.Workspace, req.App, req.ExternalPort); err != nil {
			_ = portStore.remove(req.Workspace, req.App)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Optional UI at /ui/ (served from ./ui next to the binary)
	if uiDir := findUIDir(); uiDir != "" {
		fs := http.FileServer(http.Dir(uiDir))
		http.Handle("/ui/", http.StripPrefix("/ui/", fs))
		http.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/ui/", http.StatusFound)
		})
	}

	// Backward compatibility for older UI builds that call /api/* endpoints.
	registerAPIAlias := func(path string) {
		http.HandleFunc("/api"+path, func(w http.ResponseWriter, r *http.Request) {
			clone := r.Clone(r.Context())
			clone.URL.Path = path
			http.DefaultServeMux.ServeHTTP(w, clone)
		})
	}
	for _, p := range []string{
		"/healthz",
		"/zip/upload",
		"/zip/delete",
		"/run",
		"/endpoint",
		"/workspaces",
		"/workspace/delete",
		"/workspace/status",
		"/workspace/metrics",
		"/workspace/scale",
		"/workspace/restart",
		"/cluster/metrics",
		"/taskrun/all",
		"/taskrun/status",
		"/taskrun/logs",
		"/taskrun/logs/stream",
		"/run/logs",
		"/pod/logs/stream",
		"/app/delete",
		"/app/restart",
		"/openapi.json",
		"/docs",
		"/hostinfo",
		"/external-map",
	} {
		registerAPIAlias(p)
	}

	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func buildManifests(in *Input) ([]string, []AppTarget, error) {
	setDefaults(in)
	if err := autoDiscoverZipApps(in); err != nil {
		return nil, nil, err
	}
	if err := autoPopulateMissingPorts(in); err != nil {
		return nil, nil, err
	}
	if err := validate(in); err != nil {
		return nil, nil, err
	}
	targets, err := resolveAppTargets(*in)
	if err != nil {
		return nil, nil, err
	}
	if err := attachBaseImageMirrors(*in, targets); err != nil {
		log.Printf("base image mirror skipped: %v", err)
	}

	manifests := make([]string, 0, 4)
	if in.Source.Type == "git" && in.Source.GitUsername != "" && in.Source.GitToken != "" {
		if in.Source.GitSecret == "" {
			in.Source.GitSecret = "git-cred-" + randSuffix()
		}
		manifests = append(manifests, renderGitSecret(*in))
	}

	for _, t := range targets {
		manifests = append(manifests, renderTaskRun(*in, t))
	}
	return manifests, targets, nil
}

func isTaskRun(m string) bool {
	return strings.Contains(m, "\nkind: TaskRun\n") || strings.HasPrefix(m, "kind: TaskRun\n")
}

func kubectlApply(m string) error {
	cmd := kubectlCmd("apply", "-f", "-")
	cmd.Stdin = strings.NewReader(m)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func kubectlCreateName(m, ns string) (string, error) {
	cmd := kubectlCmd("-n", ns, "create", "-f", "-", "-o", "name")
	cmd.Stdin = strings.NewReader(m)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	parts := strings.Split(strings.TrimSpace(out.String()), "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("unexpected create output: %s", out.String())
	}
	return parts[1], nil
}

func kubectlCmd(args ...string) *exec.Cmd {
	cmd := exec.Command("kubectl", args...)
	if serverKubeconfig != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+serverKubeconfig)
	}
	return cmd
}

func autoDiscoverZipApps(in *Input) error {
	if in.Source.Type != "zip" {
		return nil
	}
	if len(in.Apps) > 0 {
		return nil
	}
	if strings.TrimSpace(in.Source.ContextSub) != "" {
		return nil
	}
	if strings.TrimSpace(in.Source.ZipURL) == "" {
		return nil
	}

	contexts, err := discoverZipDockerfileContexts(in.Source.ZipURL, in.Source.ZipUsername, in.Source.ZipPassword)
	if err != nil {
		return nil
	}
	if len(contexts) <= 1 {
		return nil
	}

	seen := map[string]int{}
	apps := make([]AppSpec, 0, len(contexts))
	for _, ctx := range contexts {
		base := autoAppBaseName(ctx, in.AppName, in.Image.Project)
		base = sanitizeName(base)
		if base == "" {
			base = "app"
		}
		name := base
		if seen[base] > 0 {
			name = fmt.Sprintf("%s-%d", base, seen[base]+1)
		}
		seen[base]++
		apps = append(apps, AppSpec{
			AppName:       name,
			Project:       name,
			Tag:           strings.TrimSpace(in.Image.Tag),
			ContainerPort: 0,
			ContextSubDir: ctx,
		})
	}
	in.Apps = apps
	if strings.TrimSpace(in.AppName) == "" {
		in.AppName = apps[0].AppName
	}
	if strings.TrimSpace(in.Image.Project) == "" {
		in.Image.Project = apps[0].Project
	}
	return nil
}

func autoAppBaseName(contextSub, appName, imageProject string) string {
	if contextSub == "" || contextSub == "." {
		if strings.TrimSpace(appName) != "" {
			return appName
		}
		if strings.TrimSpace(imageProject) != "" {
			return imageProject
		}
		return "app"
	}
	p := strings.Trim(contextSub, "/")
	if p == "" {
		return "app"
	}
	parts := strings.Split(p, "/")
	return parts[len(parts)-1]
}

func discoverZipDockerfileContexts(zipURL, zipUser, zipPass string) ([]string, error) {
	ctxPorts, err := discoverZipDockerfilePorts(zipURL, zipUser, zipPass)
	if err != nil {
		return nil, err
	}
	contexts := make([]string, 0, len(ctxPorts))
	for ctx := range ctxPorts {
		contexts = append(contexts, ctx)
	}
	sort.Strings(contexts)
	return contexts, nil
}

func discoverZipDockerfilePorts(zipURL, zipUser, zipPass string) (map[string]int, error) {
	tmp, err := os.CreateTemp("", "runner-zip-*.zip")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	req, err := http.NewRequest(http.MethodGet, zipURL, nil)
	if err != nil {
		return nil, err
	}
	if zipUser != "" || zipPass != "" {
		req.SetBasicAuth(zipUser, zipPass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("zip fetch failed: %s", resp.Status)
	}

	out, err := os.Create(tmpPath)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		return nil, err
	}
	if err := out.Close(); err != nil {
		return nil, err
	}

	zr, err := zip.OpenReader(tmpPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	contexts := map[string]int{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := strings.ReplaceAll(strings.TrimSpace(f.Name), "\\", "/")
		if name == "" {
			continue
		}
		if !strings.EqualFold(path.Base(name), "Dockerfile") {
			continue
		}
		dir := path.Dir(name)
		ctx := "."
		if dir != "." {
			ctx = strings.TrimPrefix(path.Clean(dir), "./")
		}
		if _, ok := contexts[ctx]; !ok {
			contexts[ctx] = 0
		}
		if contexts[ctx] == 0 {
			rc, err := f.Open()
			if err == nil {
				data, _ := io.ReadAll(rc)
				_ = rc.Close()
				if p := inferExposedPortFromDockerfile(string(data)); p > 0 {
					contexts[ctx] = p
				}
			}
		}
	}
	return contexts, nil
}

func inferExposedPortFromDockerfile(content string) int {
	lines := strings.Split(content, "\n")
	re := regexp.MustCompile(`(?i)^\s*EXPOSE\s+(.+)$`)
	for _, line := range lines {
		m := re.FindStringSubmatch(line)
		if len(m) < 2 {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(m[1]))
		for _, field := range fields {
			token := strings.TrimSpace(strings.SplitN(field, "/", 2)[0])
			if token == "" {
				continue
			}
			if p, err := strconv.Atoi(token); err == nil && p > 0 {
				return p
			}
		}
	}
	return 0
}

func autoPopulateMissingPorts(in *Input) error {
	if len(in.Apps) == 0 || in.Source.Type != "zip" || strings.TrimSpace(in.Source.ZipURL) == "" {
		return nil
	}
	ctxPorts, err := discoverZipDockerfilePorts(in.Source.ZipURL, in.Source.ZipUsername, in.Source.ZipPassword)
	if err != nil {
		return nil
	}
	basePort := in.Deploy.ContainerPort
	if basePort == 0 {
		basePort = 8080
	}
	for i := range in.Apps {
		if in.Apps[i].ContainerPort > 0 {
			continue
		}
		ctx, err := normalizeContextSubDir(in.Apps[i].ContextSubDir)
		if err != nil {
			return fmt.Errorf("apps[%d]: %w", i, err)
		}
		if ctx == "" {
			ctx = "."
		}
		if p := ctxPorts[ctx]; p > 0 {
			in.Apps[i].ContainerPort = p
		} else {
			in.Apps[i].ContainerPort = basePort
		}
	}
	return nil
}

func sanitizeZipFilename(name string) (string, error) {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." {
		return "", fmt.Errorf("filename is required")
	}
	safe := pathTokenRe.ReplaceAllString(base, "-")
	safe = strings.Trim(safe, "-.")
	if safe == "" {
		return "", fmt.Errorf("invalid filename")
	}
	if !strings.HasSuffix(strings.ToLower(safe), ".zip") {
		return "", fmt.Errorf("filename must end with .zip")
	}
	return safe, nil
}

func getRunningZipServerPod() (string, error) {
	cmd := kubectlCmd("-n", zipServerNamespace, "get", "pods", "-l", "app="+zipServerServiceName, "--field-selector=status.phase=Running", "-o", "jsonpath={.items[0].metadata.name}")
	out, err := cmd.CombinedOutput()
	pod := strings.TrimSpace(string(out))
	if err != nil {
		return "", fmt.Errorf("zip-server pod query failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if pod == "" {
		return "", fmt.Errorf("no running zip-server pod found")
	}
	return pod, nil
}

func kubectlExecInPod(namespace, pod string, args ...string) error {
	base := []string{"-n", namespace, "exec", pod, "--"}
	base = append(base, args...)
	cmd := kubectlCmd(base...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl exec failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func kubectlCopyToPod(namespace, src, pod, dst string) error {
	spec := namespace + "/" + pod + ":" + dst
	cmd := kubectlCmd("-n", namespace, "cp", src, spec)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl cp failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func zipServerExternalPort() string {
	p := strings.TrimSpace(os.Getenv("ZIP_SERVER_EXTERNAL_PORT"))
	if p == "" {
		return "18080"
	}
	return p
}

func resolveExternalHost(r *http.Request) string {
	if strings.TrimSpace(serverHostIP) != "" {
		return serverHostIP
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return "127.0.0.1"
	}
	if strings.Contains(host, ":") {
		parts := strings.Split(host, ":")
		host = parts[0]
	}
	if host == "" || host == "0.0.0.0" || host == "127.0.0.1" || strings.EqualFold(host, "localhost") {
		if ip := detectPrimaryIPv4(); ip != "" {
			return ip
		}
		return "127.0.0.1"
	}
	return host
}

func detectPrimaryIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if (iface.Flags&net.FlagUp) == 0 || (iface.Flags&net.FlagLoopback) != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP == nil {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if ip.IsPrivate() {
				return ip.String()
			}
		}
	}
	return ""
}

func attachBaseImageMirrors(in Input, targets []AppTarget) error {
	if len(targets) == 0 {
		return nil
	}
	srcDir, cleanup, err := prepareSourceForMirror(in)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	cache := map[string]string{}
	for i := range targets {
		df := filepath.Join(srcDir, filepath.FromSlash(strings.TrimPrefix(targets[i].ContextSubDir, "./")), "Dockerfile")
		if strings.TrimSpace(targets[i].ContextSubDir) == "" || targets[i].ContextSubDir == "." {
			df = filepath.Join(srcDir, "Dockerfile")
		}
		mapping, err := buildDockerfileMirrorMap(df, in.Image.Registry, cache)
		if err != nil {
			log.Printf("base image mirror skipped for %s: %v", targets[i].AppName, err)
			continue
		}
		if len(mapping) == 0 {
			continue
		}
		b, err := json.Marshal(mapping)
		if err != nil {
			return fmt.Errorf("marshal base image map for %s: %v", targets[i].AppName, err)
		}
		targets[i].BaseImageMap = string(b)
	}
	return nil
}

func prepareSourceForMirror(in Input) (string, func(), error) {
	switch in.Source.Type {
	case "git":
		dir, err := os.MkdirTemp("", "tekton-runner-git-*")
		if err != nil {
			return "", nil, err
		}
		cleanup := func() { _ = os.RemoveAll(dir) }
		url := strings.TrimSpace(in.Source.RepoURL)
		if url == "" {
			cleanup()
			return "", nil, fmt.Errorf("git repo_url is empty")
		}
		if in.Source.GitUsername != "" && in.Source.GitToken != "" && strings.HasPrefix(url, "https://") {
			url = strings.Replace(url, "https://", "https://"+neturl.QueryEscape(in.Source.GitUsername)+":"+neturl.QueryEscape(in.Source.GitToken)+"@", 1)
		}
		ref := strings.TrimSpace(in.Source.Revision)
		if ref == "" {
			ref = "main"
		}
		ref = strings.TrimPrefix(ref, "refs/heads/")
		cmd := exec.Command("git", "clone", "--depth", "1", "--branch", ref, url, dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			cleanup()
			return "", nil, fmt.Errorf("git clone failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return dir, cleanup, nil
	case "zip":
		dir, err := os.MkdirTemp("", "tekton-runner-zip-src-*")
		if err != nil {
			return "", nil, err
		}
		cleanup := func() { _ = os.RemoveAll(dir) }
		req, err := http.NewRequest(http.MethodGet, in.Source.ZipURL, nil)
		if err != nil {
			cleanup()
			return "", nil, err
		}
		if in.Source.ZipUsername != "" || in.Source.ZipPassword != "" {
			req.SetBasicAuth(in.Source.ZipUsername, in.Source.ZipPassword)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cleanup()
			return "", nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			cleanup()
			return "", nil, fmt.Errorf("zip fetch failed: %s", resp.Status)
		}
		tmpZip, err := os.CreateTemp("", "tekton-runner-src-*.zip")
		if err != nil {
			cleanup()
			return "", nil, err
		}
		tmpZipPath := tmpZip.Name()
		if _, err := io.Copy(tmpZip, resp.Body); err != nil {
			tmpZip.Close()
			_ = os.Remove(tmpZipPath)
			cleanup()
			return "", nil, err
		}
		if err := tmpZip.Close(); err != nil {
			_ = os.Remove(tmpZipPath)
			cleanup()
			return "", nil, err
		}
		defer os.Remove(tmpZipPath)
		zr, err := zip.OpenReader(tmpZipPath)
		if err != nil {
			cleanup()
			return "", nil, err
		}
		defer zr.Close()
		for _, f := range zr.File {
			name := filepath.Clean(strings.ReplaceAll(f.Name, "\\", "/"))
			if name == "." || strings.HasPrefix(name, "..") {
				continue
			}
			dst := filepath.Join(dir, name)
			if f.FileInfo().IsDir() {
				if err := os.MkdirAll(dst, 0o755); err != nil {
					cleanup()
					return "", nil, err
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				cleanup()
				return "", nil, err
			}
			rc, err := f.Open()
			if err != nil {
				cleanup()
				return "", nil, err
			}
			out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode())
			if err != nil {
				rc.Close()
				cleanup()
				return "", nil, err
			}
			if _, err := io.Copy(out, rc); err != nil {
				out.Close()
				rc.Close()
				cleanup()
				return "", nil, err
			}
			out.Close()
			rc.Close()
		}
		return dir, cleanup, nil
	default:
		return "", nil, fmt.Errorf("unsupported source type for mirror: %s", in.Source.Type)
	}
}

func buildDockerfileMirrorMap(dockerfilePath, harborRegistry string, cache map[string]string) (map[string]string, error) {
	b, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(b), "\n")
	args := map[string]string{}
	out := map[string]string{}
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		upper := strings.ToUpper(trimmed)
		if strings.HasPrefix(upper, "ARG ") {
			body := strings.TrimSpace(trimmed[4:])
			parts := strings.SplitN(body, "=", 2)
			if len(parts) == 2 {
				args[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			}
			continue
		}
		if !strings.HasPrefix(upper, "FROM ") {
			continue
		}
		src := resolveDockerfileFromImage(trimmed, args)
		if src == "" || src == "scratch" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(src), strings.ToLower(harborRegistry)+"/") {
			continue
		}
		if mirrored, ok := cache[src]; ok {
			out[src] = mirrored
			continue
		}
		mirrored, err := mirrorImageToHarbor(src, harborRegistry)
		if err != nil {
			return nil, err
		}
		cache[src] = mirrored
		out[src] = mirrored
	}
	return out, nil
}

func resolveDockerfileFromImage(line string, args map[string]string) string {
	fields := strings.Fields(line)
	if len(fields) < 2 || !strings.EqualFold(fields[0], "FROM") {
		return ""
	}
	idx := 1
	for idx < len(fields) && strings.HasPrefix(fields[idx], "--") {
		idx++
	}
	if idx >= len(fields) {
		return ""
	}
	image := fields[idx]
	image = dockerfileExpandArgs(image, args)
	if strings.Contains(image, "$") {
		return ""
	}
	return image
}

func dockerfileExpandArgs(s string, args map[string]string) string {
	re := regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)
	return re.ReplaceAllStringFunc(s, func(m string) string {
		name := strings.TrimPrefix(m, "${")
		name = strings.TrimPrefix(name, "$")
		name = strings.TrimSuffix(name, "}")
		if v, ok := args[name]; ok {
			return v
		}
		return m
	})
}

func mirrorImageToHarbor(src, harborRegistry string) (string, error) {
	if err := ensureHarborProject("mirror-cache", harborRegistry); err != nil {
		return "", err
	}
	registry, repo, tag := parseImageReference(src)
	destRepo := fmt.Sprintf("mirror-cache/%s/%s", sanitizeRepoPart(registry), repo)
	destCopy := fmt.Sprintf("%s/%s:%s", harborCopyRegistry(), destRepo, tag)
	copyCmd := exec.Command(
		"docker", "run", "--rm",
		"quay.io/skopeo/stable:latest",
		"copy",
		"--dest-creds", "admin:Harbor12345",
		"--dest-tls-verify=false",
		"docker://"+src,
		"docker://"+destCopy,
	)
	if out, err := copyCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("skopeo copy %s -> %s failed: %v: %s", src, destCopy, err, strings.TrimSpace(string(out)))
	}
	return fmt.Sprintf("%s/%s:%s", harborRegistry, destRepo, tag), nil
}

func ensureHarborProject(project, _ string) error {
	body := fmt.Sprintf(`{"project_name":%q,"metadata":{"public":"true"}}`, project)
	tmp, err := os.CreateTemp("", "harbor-project-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	defer os.Remove(tmpPath)

	cmd := exec.Command(
		"curl", "-sk",
		"-u", "admin:Harbor12345",
		"-H", "Content-Type: application/json",
		"-o", "/tmp/harbor-project-create.out",
		"-w", "%{http_code}",
		"-d", "@"+tmpPath,
		harborAPIBase()+"/api/v2.0/projects",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("harbor project create request failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	code := strings.TrimSpace(string(out))
	if code == "201" || code == "409" {
		return nil
	}
	respBody, _ := os.ReadFile("/tmp/harbor-project-create.out")
	return fmt.Errorf("harbor project create failed: HTTP %s: %s", code, strings.TrimSpace(string(respBody)))
}

func harborAPIBase() string {
	return "https://127.0.0.1:8443"
}

func harborCopyRegistry() string {
	return "172.18.0.1:8443"
}

func parseImageReference(ref string) (string, string, string) {
	in := strings.TrimSpace(ref)
	if i := strings.Index(in, "@"); i >= 0 {
		digest := in[i+1:]
		in = in[:i]
		tag := "sha256-" + strings.ReplaceAll(strings.TrimPrefix(digest, "sha256:"), ":", "-")
		registry, repo, _ := parseImageReference(in)
		return registry, repo, tag
	}
	tag := "latest"
	lastSlash := strings.LastIndex(in, "/")
	lastColon := strings.LastIndex(in, ":")
	if lastColon > lastSlash {
		tag = in[lastColon+1:]
		in = in[:lastColon]
	}
	parts := strings.Split(in, "/")
	registry := "docker.io"
	repoParts := parts
	if len(parts) > 1 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":") || parts[0] == "localhost") {
		registry = parts[0]
		repoParts = parts[1:]
	}
	if registry == "docker.io" && len(repoParts) == 1 {
		repoParts = append([]string{"library"}, repoParts...)
	}
	return registry, strings.Join(repoParts, "/"), tag
}

func sanitizeRepoPart(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	re := regexp.MustCompile(`[^a-z0-9._-]+`)
	s = re.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "unknown"
	}
	return s
}

func withResolvedHost(rawURL, host string) string {
	u, err := neturl.Parse(rawURL)
	if err != nil || strings.TrimSpace(host) == "" {
		return rawURL
	}
	p := u.Port()
	if p != "" {
		u.Host = host + ":" + p
	} else {
		u.Host = host
	}
	return u.String()
}

func withResolvedPort(rawURL string, port int) string {
	if port <= 0 {
		return rawURL
	}
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	h := u.Hostname()
	if strings.TrimSpace(h) == "" {
		return rawURL
	}
	u.Host = fmt.Sprintf("%s:%d", h, port)
	return u.String()
}

func discoverConnectionStringEnvNames(in Input) ([]string, []string, error) {
	srcDir, cleanup, err := prepareSourceForMirror(in)
	if err != nil {
		return nil, nil, err
	}
	if cleanup != nil {
		defer cleanup()
	}

	sqlSet := map[string]bool{}
	redisSet := map[string]bool{}
	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		name := strings.ToLower(info.Name())
		if !strings.HasPrefix(name, "appsettings") || !strings.HasSuffix(name, ".json") {
			return nil
		}
		if info.Size() > 2*1024*1024 {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			return nil
		}
		cs, ok := doc["ConnectionStrings"].(map[string]any)
		if !ok {
			return nil
		}
		for key, raw := range cs {
			val, ok := raw.(string)
			if !ok {
				continue
			}
			envName := "ConnectionStrings__" + strings.ReplaceAll(strings.TrimSpace(key), ":", "__")
			if strings.TrimSpace(envName) == "" {
				continue
			}
			switch classifyConnectionStringKind(key, val) {
			case "redis":
				redisSet[envName] = true
			case "sql":
				sqlSet[envName] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	sqlNames := make([]string, 0, len(sqlSet))
	for k := range sqlSet {
		sqlNames = append(sqlNames, k)
	}
	redisNames := make([]string, 0, len(redisSet))
	for k := range redisSet {
		redisNames = append(redisNames, k)
	}
	sort.Strings(sqlNames)
	sort.Strings(redisNames)
	return sqlNames, redisNames, nil
}

func classifyConnectionStringKind(key, value string) string {
	k := strings.ToLower(strings.TrimSpace(key))
	v := strings.ToLower(strings.TrimSpace(value))
	joined := k + " " + v

	// Common .NET background job key: always treat as SQL even if appsettings value is placeholder.
	if strings.Contains(k, "hangfire") {
		return "sql"
	}
	if strings.Contains(joined, "redis") || strings.HasPrefix(v, "redis://") {
		return "redis"
	}
	if strings.Contains(joined, "host=") ||
		strings.Contains(joined, "server=") ||
		strings.Contains(joined, "database=") ||
		strings.Contains(joined, "username=") ||
		strings.Contains(joined, "user id=") ||
		strings.Contains(joined, "initial catalog=") ||
		strings.Contains(joined, "sslmode=") ||
		strings.HasPrefix(v, "postgres://") ||
		strings.HasPrefix(v, "postgresql://") {
		return "sql"
	}
	return ""
}

func handleZipDeploy(in Input, targets []AppTarget, taskRuns map[string]string, runID string) error {
	ns := in.Namespace
	workspace := deriveWorkspace(in)
	if len(targets) == 0 {
		return fmt.Errorf("no app target resolved")
	}
	for _, t := range targets {
		taskRunName := strings.TrimSpace(taskRuns[t.AppName])
		if taskRunName == "" {
			return fmt.Errorf("taskrun not found for app %s", t.AppName)
		}
		emitRunEvent(runID, workspace, t.AppName, "build", "running", "Waiting for TaskRun to complete", map[string]string{
			"taskrun":   taskRunName,
			"namespace": ns,
		})
		if err := waitForTaskRun(ns, taskRunName, 45*time.Minute); err != nil {
			captureAndArchiveTaskRunLogs(workspace, t.AppName, ns, taskRunName, 1200)
			emitRunEvent(runID, workspace, t.AppName, "build", "failed", err.Error(), map[string]string{
				"taskrun":   taskRunName,
				"namespace": ns,
			})
			return err
		}
		captureAndArchiveTaskRunLogs(workspace, t.AppName, ns, taskRunName, 1200)
		emitRunEvent(runID, workspace, t.AppName, "build", "succeeded", "TaskRun completed successfully", map[string]string{
			"taskrun":   taskRunName,
			"namespace": ns,
		})
	}

	clusterName := in.Workspace
	if clusterName == "" {
		clusterName = "ws-" + targets[0].AppName
	}
	kcfgDir := "/home/beko/kubeconfigs"
	if err := os.MkdirAll(kcfgDir, 0o755); err != nil {
		return err
	}
	kcfgPath := filepath.Join(kcfgDir, clusterName+".yaml")

	leadApp := targets[0].AppName
	emitRunEvent(runID, workspace, leadApp, "workspace", "running", "Ensuring workspace cluster", nil)
	if err := ensureKindCluster(clusterName, kcfgPath); err != nil {
		emitRunEvent(runID, workspace, leadApp, "workspace", "failed", err.Error(), nil)
		return err
	}
	emitRunEvent(runID, workspace, leadApp, "workspace", "succeeded", "Workspace cluster ready", nil)
	if err := ensureNamespaceWithKubeconfig(kcfgPath, clusterName); err != nil {
		emitRunEvent(runID, workspace, leadApp, "workspace", "failed", "Workspace namespace ensure failed", map[string]string{
			"namespace": clusterName,
		})
		return fmt.Errorf("ensure workspace namespace failed: %v", err)
	}

	emitRunEvent(runID, workspace, leadApp, "dependency", "running", "Applying dependencies", map[string]string{
		"type": in.Dependency.Type,
	})
	envRefs, err := ensureWorkspaceDependencies(kcfgPath, clusterName, leadApp, in.Dependency)
	if err != nil {
		emitRunEvent(runID, workspace, leadApp, "dependency", "failed", err.Error(), map[string]string{
			"type": in.Dependency.Type,
		})
		return err
	}
	emitRunEvent(runID, workspace, leadApp, "dependency", "succeeded", "Dependencies ready", map[string]string{
		"type": in.Dependency.Type,
	})
	if in.Dependency.Type == "sql" || in.Dependency.Type == "redis" || in.Dependency.Type == "both" {
		sqlEnvNames, redisEnvNames, derr := discoverConnectionStringEnvNames(in)
		if derr != nil {
			log.Printf("connection env discovery skipped: %v", derr)
		} else {
			secretName := leadApp + "-app-config"
			if in.Dependency.Type == "sql" || in.Dependency.Type == "both" {
				for _, envName := range sqlEnvNames {
					envRefs = appendEnvRef(envRefs, envName, secretName, "sql-conn")
				}
			}
			if in.Dependency.Type == "redis" || in.Dependency.Type == "both" {
				for _, envName := range redisEnvNames {
					envRefs = appendEnvRef(envRefs, envName, secretName, "redis-conn")
				}
			}
			if len(sqlEnvNames) > 0 || len(redisEnvNames) > 0 {
				emitRunEvent(runID, workspace, leadApp, "dependency", "succeeded", "ConnectionStrings keys auto-mapped from appsettings", map[string]string{
					"sql_keys":   strconv.Itoa(len(sqlEnvNames)),
					"redis_keys": strconv.Itoa(len(redisEnvNames)),
				})
			}
		}
	}
	if in.Migration.Enabled {
		projectName := harborProjectName(workspace)
		leadImage := fmt.Sprintf("%s/%s/%s:%s", in.Image.Registry, projectName, strings.ToLower(targets[0].Project), targets[0].Tag)
		emitRunEvent(runID, workspace, leadApp, "migration", "running", "Running migration job", nil)
		if err := runMigrationJob(kcfgPath, clusterName, leadApp, in.Migration, envRefs, leadImage); err != nil {
			emitRunEvent(runID, workspace, leadApp, "migration", "failed", err.Error(), nil)
			return err
		}
		emitRunEvent(runID, workspace, leadApp, "migration", "succeeded", "Migration completed", nil)
	}

	backendServiceURL := ""
	for _, t := range targets {
		if t.AppName == "backend" {
			backendServiceURL = fmt.Sprintf("http://%s.%s.svc.cluster.local", t.AppName, clusterName)
			break
		}
	}

	for _, t := range targets {
		projectName := harborProjectName(workspace)
		image := fmt.Sprintf("%s/%s/%s:%s", in.Image.Registry, projectName, strings.ToLower(t.Project), t.Tag)
		emitRunEvent(runID, workspace, t.AppName, "deploy", "running", "Applying deployment and service", map[string]string{
			"image": image,
		})
		var storage *ResolvedFileStorage
		if in.FileStorage.Enabled {
			emitRunEvent(runID, workspace, t.AppName, "storage", "running", "Preparing shared file storage", nil)
			storage, err = ensureSharedFileStorage(kcfgPath, clusterName, t.AppName, in.FileStorage)
			if err != nil {
				emitRunEvent(runID, workspace, t.AppName, "storage", "failed", err.Error(), nil)
				return err
			}
			emitRunEvent(runID, workspace, t.AppName, "storage", "succeeded", "Shared file storage ready", map[string]string{
				"pvc":        storage.PVCName,
				"mount_path": storage.MountPath,
				"sub_path":   storage.SubPath,
			})
		}
		appEnvRefs := append([]AppEnvSecretRef{}, envRefs...)
		if in.Dependency.Type == "sql" || in.Dependency.Type == "redis" || in.Dependency.Type == "both" {
			secretName := leadApp + "-app-config"
			profile := runtimeProfileForApp(in, t.AppName)
			for _, ref := range profileConnectionEnvRefs(profile, in.Dependency.Type, secretName) {
				appEnvRefs = appendEnvRef(appEnvRefs, ref.EnvName, ref.SecretName, ref.SecretKey)
			}
		}
		appEnvRefs = appendEnvValue(appEnvRefs, "PORT", strconv.Itoa(t.ContainerPort))
		for _, env := range in.ExtraEnv {
			if isConnectionEnvName(env.Name) {
				appEnvRefs = upsertEnvValue(appEnvRefs, env.Name, env.Value)
				continue
			}
			appEnvRefs = appendEnvValue(appEnvRefs, env.Name, env.Value)
		}
		if backendServiceURL != "" && t.AppName != "backend" {
			appEnvRefs = appendEnvValue(appEnvRefs, "BACKEND_BASE_URL", backendServiceURL)
			appEnvRefs = appendEnvValue(appEnvRefs, "BACKEND_URL", backendServiceURL)
			appEnvRefs = appendEnvValue(appEnvRefs, "API_BASE_URL", backendServiceURL)
			appEnvRefs = appendEnvValue(appEnvRefs, "VITE_API_BASE_URL", backendServiceURL)
			appEnvRefs = appendEnvValue(appEnvRefs, "NEXT_PUBLIC_API_URL", backendServiceURL)
		}
		if err := applyDeployment(kcfgPath, clusterName, t.AppName, image, t.ContainerPort, appEnvRefs, storage); err != nil {
			emitRunEvent(runID, workspace, t.AppName, "deploy", "failed", err.Error(), nil)
			return err
		}
		if err := waitForDeploymentReady(kcfgPath, clusterName, t.AppName, 5*time.Minute); err != nil {
			emitRunEvent(runID, workspace, t.AppName, "deploy", "failed", err.Error(), nil)
			return err
		}
		if err := verifyDeploymentRuntimePort(kcfgPath, clusterName, t.AppName, t.ContainerPort); err != nil {
			emitRunEvent(runID, workspace, t.AppName, "deploy", "failed", err.Error(), nil)
			return err
		}
		emitRunEvent(runID, workspace, t.AppName, "deploy", "succeeded", "Deployment applied", nil)

		port, err := getServiceNodePort(kcfgPath, clusterName, t.AppName)
		if err == nil {
			host := serverHostIP
			if host == "" {
				host = "127.0.0.1"
			}
			url := fmt.Sprintf("http://%s:%d", host, port)
			key := clusterName + "/" + t.AppName
			serverState.mu.Lock()
			serverState.endpoints[key] = url
			serverState.mu.Unlock()
			emitRunEvent(runID, workspace, t.AppName, "endpoint", "succeeded", "Application endpoint resolved", map[string]string{
				"url": url,
			})
		}
	}
	return nil
}

func waitForTaskRun(ns, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := kubectlCmd("-n", ns, "get", "taskrun", name, "-o", "jsonpath={.status.conditions[0].status}")
		out, _ := cmd.CombinedOutput()
		status := strings.TrimSpace(string(out))
		if status == "True" {
			return nil
		}
		if status == "False" {
			cmd = kubectlCmd("-n", ns, "get", "taskrun", name, "-o", "jsonpath={.status.conditions[0].message}")
			msg, _ := cmd.CombinedOutput()
			return fmt.Errorf("taskrun failed: %s", strings.TrimSpace(string(msg)))
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("taskrun timeout: %s", name)
}

func ensureKindCluster(name, kubeconfigPath string) error {
	cmd := exec.Command("kind", "get", "clusters")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kind get clusters: %v", err)
	}
	if !strings.Contains(string(out), name) {
		image := kindNodeImage()
		cmd = exec.Command("kind", "create", "cluster", "--name", name, "--image", image)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("kind create cluster: %v", err)
		}
	}

	if err := configureKindNode(name); err != nil {
		return err
	}

	cmd = exec.Command("kind", "get", "kubeconfig", "--name", name)
	out, err = cmd.Output()
	if err != nil {
		return fmt.Errorf("kind get kubeconfig: %v", err)
	}
	if err := os.WriteFile(kubeconfigPath, out, 0o600); err != nil {
		return err
	}
	if err := ensureMetricsServer(kubeconfigPath); err != nil {
		log.Printf("metrics-server setup failed for %s: %v", name, err)
	}
	return nil
}

func kindNodeImage() string {
	if v := strings.TrimSpace(os.Getenv("KIND_NODE_IMAGE")); v != "" {
		return v
	}
	return "kindest/node:v1.31.4"
}

func configureKindNode(clusterName string) error {
	node := clusterName + "-control-plane"
	// Ensure host mapping for Harbor name inside kind node.
	// We pin lenovo to the node's default gateway so the mapping survives host IP changes.
	// Rewrite any stale entry instead of only appending once.
	cmd := exec.Command("docker", "exec", node, "sh", "-c", `gw="$(ip route | awk '/default/ {print $3; exit}')"; [ -n "$gw" ] || gw="172.18.0.1"; tmp="$(mktemp)"; awk '$2 != "lenovo" { print }' /etc/hosts > "$tmp"; printf '%s %s\n' "$gw" lenovo >> "$tmp"; cat "$tmp" > /etc/hosts; rm -f "$tmp"`)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("configure hosts: %v", err)
	}

	// Configure containerd to trust Harbor (skip TLS verify for test)
	hostsToml := `server = "https://lenovo:8443"

[host."https://lenovo:8443"]
  capabilities = ["pull", "resolve"]
  skip_verify = true
`
	cmd = exec.Command("docker", "exec", node, "sh", "-c", "mkdir -p /etc/containerd/certs.d/lenovo:8443")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mkdir certs.d: %v", err)
	}
	cmd = exec.Command("docker", "exec", node, "sh", "-c", "cat > /etc/containerd/certs.d/lenovo:8443/hosts.toml <<'EOF'\n"+hostsToml+"EOF")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write hosts.toml: %v", err)
	}

	// Ensure containerd reads certs.d config (kind image doesn't always set config_path)
	cmd = exec.Command("docker", "exec", node, "sh", "-c", "grep -q 'config_path = \"/etc/containerd/certs.d\"' /etc/containerd/config.toml || printf '\\n[plugins.\"io.containerd.grpc.v1.cri\".registry]\\n  config_path = \"/etc/containerd/certs.d\"\\n' >> /etc/containerd/config.toml")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("set containerd config_path: %v", err)
	}

	cmd = exec.Command("docker", "exec", node, "sh", "-c", "systemctl restart containerd")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restart containerd: %v", err)
	}
	return nil
}

func applyDeployment(kubeconfig, namespace, app, image string, port int, envRefs []AppEnvSecretRef, storage *ResolvedFileStorage) error {
	if port == 0 {
		port = 8080
	}
	manifest := renderDeployment(namespace, app, image, port, envRefs, storage)
	return kubectlApplyWithKubeconfig(kubeconfig, manifest)
}

func renderDeployment(ns, app, image string, port int, envRefs []AppEnvSecretRef, storage *ResolvedFileStorage) string {
	tpl := `apiVersion: v1
kind: Namespace
metadata:
  name: {{.Namespace}}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{.App}}
  namespace: {{.Namespace}}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: {{.App}}
  template:
    metadata:
      labels:
        app: {{.App}}
    spec:
{{.VolumeBlock}}
      containers:
        - name: {{.App}}
          image: {{.Image}}
          ports:
            - containerPort: {{.Port}}
{{.EnvBlock}}
{{.VolumeMountBlock}}
---
apiVersion: v1
kind: Service
metadata:
  name: {{.App}}
  namespace: {{.Namespace}}
spec:
  type: NodePort
  selector:
    app: {{.App}}
  ports:
    - port: 80
      targetPort: {{.Port}}
`
	return mustRender(tpl, map[string]string{
		"Namespace":        ns,
		"App":              app,
		"Image":            image,
		"Port":             fmt.Sprintf("%d", port),
		"EnvBlock":         renderEnvFromSecretBlock(envRefs),
		"VolumeBlock":      renderFileStorageVolumeBlock(storage),
		"VolumeMountBlock": renderFileStorageMountBlock(storage),
	})
}

func renderEnvFromSecretBlock(envRefs []AppEnvSecretRef) string {
	if len(envRefs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("          env:\n")
	for _, ref := range envRefs {
		b.WriteString("            - name: " + ref.EnvName + "\n")
		if strings.TrimSpace(ref.Value) != "" {
			b.WriteString("              value: " + yamlQuote(ref.Value) + "\n")
			continue
		}
		b.WriteString("              valueFrom:\n")
		b.WriteString("                secretKeyRef:\n")
		b.WriteString("                  name: " + ref.SecretName + "\n")
		b.WriteString("                  key: " + ref.SecretKey + "\n")
	}
	return b.String()
}

func renderFileStorageVolumeBlock(storage *ResolvedFileStorage) string {
	if storage == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("      volumes:\n")
	b.WriteString("        - name: shared-storage\n")
	b.WriteString("          persistentVolumeClaim:\n")
	b.WriteString("            claimName: " + storage.PVCName + "\n")
	return b.String()
}

func renderFileStorageMountBlock(storage *ResolvedFileStorage) string {
	if storage == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("          volumeMounts:\n")
	b.WriteString("            - name: shared-storage\n")
	b.WriteString("              mountPath: " + yamlQuote(storage.MountPath) + "\n")
	if strings.TrimSpace(storage.SubPath) != "" {
		b.WriteString("              subPath: " + yamlQuote(storage.SubPath) + "\n")
	}
	return b.String()
}

func kubectlApplyWithKubeconfig(kubeconfig, manifest string) error {
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%v: %s", err, msg)
	}
	if len(out) > 0 {
		_, _ = os.Stdout.Write(out)
	}
	return nil
}

func ensureNamespaceWithKubeconfig(kubeconfig, namespace string) error {
	manifest := mustRender(`apiVersion: v1
kind: Namespace
metadata:
  name: {{.Namespace}}
`, map[string]string{
		"Namespace": namespace,
	})
	return kubectlApplyWithKubeconfig(kubeconfig, manifest)
}

func ensureSharedFileStorage(kubeconfig, namespace, app string, fs FileStorage) (*ResolvedFileStorage, error) {
	pvcName := sanitizeName(fs.PVCName)
	if pvcName == "" {
		pvcName = "shared-file-pvc"
	}
	if fs.NFS != nil {
		if err := kubectlApplyWithKubeconfig(kubeconfig, renderSharedFileStorageNFS(namespace, pvcName, *fs.NFS)); err != nil {
			return nil, fmt.Errorf("apply shared file storage NFS failed: %v", err)
		}
	} else if fs.SMB != nil {
		if err := kubectlApplyWithKubeconfig(kubeconfig, renderSharedFileStorageSMB(namespace, pvcName, *fs.SMB)); err != nil {
			return nil, fmt.Errorf("apply shared file storage SMB failed: %v", err)
		}
	} else if err := ensurePVCExists(kubeconfig, namespace, pvcName); err != nil {
		return nil, err
	}

	subPath, err := resolveFileStorageSubPath(namespace, app, fs.SubPath)
	if err != nil {
		return nil, err
	}
	if err := ensureFileStorageSubPath(kubeconfig, namespace, pvcName, subPath); err != nil {
		return nil, err
	}
	return &ResolvedFileStorage{
		PVCName:   pvcName,
		MountPath: fs.MountPath,
		SubPath:   subPath,
	}, nil
}

func ensurePVCExists(kubeconfig, namespace, pvcName string) error {
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace, "get", "pvc", pvcName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("shared file storage pvc %s not found in %s: %s", pvcName, namespace, strings.TrimSpace(string(out)))
	}
	return nil
}

func resolveFileStorageSubPath(workspace, app, raw string) (string, error) {
	if s, err := normalizeStorageSubPath(raw); err != nil {
		return "", err
	} else if s != "" {
		return s, nil
	}
	project := sanitizeName(strings.TrimPrefix(strings.TrimSpace(workspace), "ws-"))
	appName := sanitizeName(app)
	if project == "" {
		project = appName
	}
	if project == "" {
		project = "app"
	}
	if appName == "" || appName == project {
		return project, nil
	}
	return path.Join(project, appName), nil
}

func ensureFileStorageSubPath(kubeconfig, namespace, pvcName, subPath string) error {
	jobName := sanitizeName("fs-init-" + subPath + "-" + randSuffix())
	if len(jobName) > 63 {
		jobName = strings.Trim(sanitizeName(jobName[:63]), "-")
	}
	manifest := renderSharedFileStorageInitJob(namespace, jobName, pvcName, subPath)
	if err := kubectlApplyWithKubeconfig(kubeconfig, manifest); err != nil {
		return fmt.Errorf("apply shared storage init job failed: %v", err)
	}
	if err := waitForJobComplete(kubeconfig, namespace, jobName, 3*time.Minute); err != nil {
		logs, _ := getJobLogs(kubeconfig, namespace, jobName)
		if strings.TrimSpace(logs) != "" {
			return fmt.Errorf("shared storage init job failed: %v\n%s", err, logs)
		}
		return fmt.Errorf("shared storage init job failed: %v", err)
	}
	return nil
}

func renderSharedFileStorageInitJob(namespace, name, pvcName, subPath string) string {
	var b strings.Builder
	b.WriteString("apiVersion: batch/v1\n")
	b.WriteString("kind: Job\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: " + name + "\n")
	b.WriteString("  namespace: " + namespace + "\n")
	b.WriteString("spec:\n")
	b.WriteString("  ttlSecondsAfterFinished: 60\n")
	b.WriteString("  backoffLimit: 0\n")
	b.WriteString("  template:\n")
	b.WriteString("    spec:\n")
	b.WriteString("      restartPolicy: Never\n")
	b.WriteString("      volumes:\n")
	b.WriteString("        - name: shared-storage\n")
	b.WriteString("          persistentVolumeClaim:\n")
	b.WriteString("            claimName: " + pvcName + "\n")
	b.WriteString("      containers:\n")
	b.WriteString("        - name: init\n")
	b.WriteString("          image: lenovo:8443/library/alpine-git:2.45.2\n")
	b.WriteString("          command:\n")
	b.WriteString("            - /bin/sh\n")
	b.WriteString("            - -lc\n")
	b.WriteString("          args:\n")
	b.WriteString("            - " + yamlQuote("mkdir -p /shared/"+subPath+" && chmod 0777 /shared/"+subPath) + "\n")
	b.WriteString("          volumeMounts:\n")
	b.WriteString("            - name: shared-storage\n")
	b.WriteString("              mountPath: /shared\n")
	return b.String()
}

func renderSharedFileStorageNFS(namespace, pvcName string, cfg NFSConfig) string {
	pvName := sanitizeName(namespace + "-" + pvcName + "-pv")
	tpl := `apiVersion: v1
kind: PersistentVolume
metadata:
  name: {{.PVName}}
spec:
  capacity:
    storage: {{.Size}}
  storageClassName: ""
  accessModes:
    - ReadWriteMany
  persistentVolumeReclaimPolicy: Retain
  nfs:
    server: {{.Server}}
    path: {{.Path}}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: {{.PVCName}}
  namespace: {{.Namespace}}
spec:
  storageClassName: ""
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: {{.Size}}
  volumeName: {{.PVName}}
`
	return mustRender(tpl, map[string]string{
		"PVName":    pvName,
		"PVCName":   pvcName,
		"Namespace": namespace,
		"Size":      cfg.Size,
		"Server":    cfg.Server,
		"Path":      cfg.Path,
	})
}

func renderSharedFileStorageSMB(namespace, pvcName string, cfg SMBConfig) string {
	pvName := sanitizeName(namespace + "-" + pvcName + "-pv")
	secretName := cfg.SecretName
	if secretName == "" {
		secretName = pvcName + "-smb-cred"
	}
	volumeHandle := cfg.VolumeHandle
	if volumeHandle == "" {
		volumeHandle = pvcName + "-smb"
	}
	tpl := `apiVersion: v1
kind: Secret
metadata:
  name: {{.SecretName}}
  namespace: {{.Namespace}}
type: Opaque
stringData:
  username: {{.Username}}
  password: {{.Password}}
---
apiVersion: v1
kind: PersistentVolume
metadata:
  name: {{.PVName}}
spec:
  capacity:
    storage: {{.Size}}
  storageClassName: ""
  accessModes:
    - ReadWriteMany
  persistentVolumeReclaimPolicy: Retain
  csi:
    driver: smb.csi.k8s.io
    volumeHandle: {{.VolumeHandle}}
    volumeAttributes:
      source: "//{{.Server}}/{{.Share}}"
    nodeStageSecretRef:
      name: {{.SecretName}}
      namespace: {{.Namespace}}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: {{.PVCName}}
  namespace: {{.Namespace}}
spec:
  storageClassName: ""
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: {{.Size}}
  volumeName: {{.PVName}}
`
	return mustRender(tpl, map[string]string{
		"PVName":       pvName,
		"PVCName":      pvcName,
		"Namespace":    namespace,
		"SecretName":   secretName,
		"Username":     yamlQuote(cfg.Username),
		"Password":     yamlQuote(cfg.Password),
		"Size":         cfg.Size,
		"VolumeHandle": volumeHandle,
		"Server":       cfg.Server,
		"Share":        cfg.Share,
	})
}

func ensureWorkspaceDependencies(kubeconfig, namespace, app string, dep Dependency) ([]AppEnvSecretRef, error) {
	switch dep.Type {
	case "", "none":
		return nil, nil
	case "redis":
		depName := sanitizeName(dep.Redis.ServiceName)
		if depName == "" {
			depName = "redis"
		}
		if err := kubectlApplyWithKubeconfig(kubeconfig, renderRedisManifest(namespace, depName, dep.Redis.Image, dep.Redis.Port)); err != nil {
			return nil, fmt.Errorf("apply redis dependency failed: %v", err)
		}
		if err := waitForDeploymentReady(kubeconfig, namespace, depName, 5*time.Minute); err != nil {
			return nil, fmt.Errorf("redis dependency not ready: %v", err)
		}
		host := fmt.Sprintf("%s.%s.svc.cluster.local", depName, namespace)
		secretName := app + "-app-config"
		if err := kubectlApplyWithKubeconfig(kubeconfig, renderOpaqueSecret(namespace, secretName, redisSecretData(host, dep.Redis.Port))); err != nil {
			return nil, fmt.Errorf("apply redis app secret failed: %v", err)
		}
		return defaultRedisEnvRefs(secretName, dep.Redis), nil
	case "sql":
		depName := sanitizeName(dep.SQL.ServiceName)
		if depName == "" {
			depName = "sql"
		}
		authSecretName := depName + "-auth"
		if err := kubectlApplyWithKubeconfig(kubeconfig, renderOpaqueSecret(namespace, authSecretName, map[string]string{
			"db-password": dep.SQL.Password,
		})); err != nil {
			return nil, fmt.Errorf("apply sql auth secret failed: %v", err)
		}
		if err := kubectlApplyWithKubeconfig(kubeconfig, renderSQLManifest(namespace, depName, dep.SQL.Image, dep.SQL.Port, authSecretName, dep.SQL.Database, dep.SQL.Username)); err != nil {
			return nil, fmt.Errorf("apply sql dependency failed: %v", err)
		}
		if err := waitForDeploymentReady(kubeconfig, namespace, depName, 6*time.Minute); err != nil {
			return nil, fmt.Errorf("sql dependency not ready: %v", err)
		}
		host := fmt.Sprintf("%s.%s.svc.cluster.local", depName, namespace)
		secretName := app + "-app-config"
		if err := kubectlApplyWithKubeconfig(kubeconfig, renderOpaqueSecret(namespace, secretName, sqlSecretData(host, dep.SQL))); err != nil {
			return nil, fmt.Errorf("apply sql app secret failed: %v", err)
		}
		return defaultSQLEnvRefs(secretName, dep.SQL), nil
	case "both":
		redisName := sanitizeName(dep.Redis.ServiceName)
		if redisName == "" {
			redisName = "redis"
		}
		if err := kubectlApplyWithKubeconfig(kubeconfig, renderRedisManifest(namespace, redisName, dep.Redis.Image, dep.Redis.Port)); err != nil {
			return nil, fmt.Errorf("apply redis dependency failed: %v", err)
		}
		if err := waitForDeploymentReady(kubeconfig, namespace, redisName, 5*time.Minute); err != nil {
			return nil, fmt.Errorf("redis dependency not ready: %v", err)
		}
		redisHost := fmt.Sprintf("%s.%s.svc.cluster.local", redisName, namespace)

		sqlName := sanitizeName(dep.SQL.ServiceName)
		if sqlName == "" {
			sqlName = "sql"
		}
		authSecretName := sqlName + "-auth"
		if err := kubectlApplyWithKubeconfig(kubeconfig, renderOpaqueSecret(namespace, authSecretName, map[string]string{
			"db-password": dep.SQL.Password,
		})); err != nil {
			return nil, fmt.Errorf("apply sql auth secret failed: %v", err)
		}
		if err := kubectlApplyWithKubeconfig(kubeconfig, renderSQLManifest(namespace, sqlName, dep.SQL.Image, dep.SQL.Port, authSecretName, dep.SQL.Database, dep.SQL.Username)); err != nil {
			return nil, fmt.Errorf("apply sql dependency failed: %v", err)
		}
		if err := waitForDeploymentReady(kubeconfig, namespace, sqlName, 6*time.Minute); err != nil {
			return nil, fmt.Errorf("sql dependency not ready: %v", err)
		}
		sqlHost := fmt.Sprintf("%s.%s.svc.cluster.local", sqlName, namespace)
		secretName := app + "-app-config"
		secretData := redisSecretData(redisHost, dep.Redis.Port)
		for k, v := range sqlSecretData(sqlHost, dep.SQL) {
			secretData[k] = v
		}
		if err := kubectlApplyWithKubeconfig(kubeconfig, renderOpaqueSecret(namespace, secretName, secretData)); err != nil {
			return nil, fmt.Errorf("apply combined app secret failed: %v", err)
		}
		refs := defaultRedisEnvRefs(secretName, dep.Redis)
		for _, ref := range defaultSQLEnvRefs(secretName, dep.SQL) {
			refs = appendEnvRef(refs, ref.EnvName, ref.SecretName, ref.SecretKey)
		}
		return refs, nil
	default:
		return nil, fmt.Errorf("unsupported dependency.type: %s", dep.Type)
	}
}

func runMigrationJob(kubeconfig, namespace, app string, m Migration, envRefs []AppEnvSecretRef, defaultImage string) error {
	secretName := ""
	secretKey := ""
	for _, ref := range envRefs {
		if ref.EnvName == m.EnvName {
			secretName = ref.SecretName
			secretKey = ref.SecretKey
			break
		}
	}
	if secretName == "" || secretKey == "" {
		return fmt.Errorf("migration env %s secret reference not found", m.EnvName)
	}
	image := strings.TrimSpace(m.Image)
	if image == "" {
		image = strings.TrimSpace(defaultImage)
	}
	if image == "" {
		return fmt.Errorf("migration image is empty (set migration.image or ensure deploy image is resolved)")
	}

	jobName := sanitizeName(app + "-migrate-" + randSuffix())
	if len(jobName) > 63 {
		jobName = jobName[:63]
		jobName = strings.Trim(jobName, "-")
	}
	manifest := renderMigrationJob(namespace, jobName, image, m, secretName, secretKey)
	if err := kubectlApplyWithKubeconfig(kubeconfig, manifest); err != nil {
		return fmt.Errorf("apply migration job failed: %v", err)
	}
	if err := waitForJobComplete(kubeconfig, namespace, jobName, 10*time.Minute); err != nil {
		logs, _ := getJobLogs(kubeconfig, namespace, jobName)
		if strings.TrimSpace(logs) != "" {
			return fmt.Errorf("migration job failed: %v\n%s", err, logs)
		}
		return fmt.Errorf("migration job failed: %v", err)
	}
	return nil
}

func renderMigrationJob(namespace, name, image string, m Migration, secretName, secretKey string) string {
	var b strings.Builder
	b.WriteString("apiVersion: batch/v1\n")
	b.WriteString("kind: Job\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: " + name + "\n")
	b.WriteString("  namespace: " + namespace + "\n")
	b.WriteString("spec:\n")
	b.WriteString("  backoffLimit: 0\n")
	b.WriteString("  template:\n")
	b.WriteString("    spec:\n")
	b.WriteString("      restartPolicy: Never\n")
	b.WriteString("      containers:\n")
	b.WriteString("        - name: migrate\n")
	b.WriteString("          image: " + image + "\n")
	b.WriteString("          env:\n")
	b.WriteString("            - name: " + m.EnvName + "\n")
	b.WriteString("              valueFrom:\n")
	b.WriteString("                secretKeyRef:\n")
	b.WriteString("                  name: " + secretName + "\n")
	b.WriteString("                  key: " + secretKey + "\n")
	if len(m.Command) == 0 {
		b.WriteString("          command:\n")
		b.WriteString("            - /bin/sh\n")
		b.WriteString("            - -lc\n")
		b.WriteString("          args:\n")
		b.WriteString("            - " + yamlQuote(defaultMigrationAutoScript()) + "\n")
		return b.String()
	}
	if len(m.Command) > 0 {
		b.WriteString("          command:\n")
		for _, c := range m.Command {
			b.WriteString("            - " + yamlQuote(c) + "\n")
		}
	}
	if len(m.Args) > 0 {
		b.WriteString("          args:\n")
		for _, a := range m.Args {
			b.WriteString("            - " + yamlQuote(a) + "\n")
		}
	}
	return b.String()
}

func defaultMigrationAutoScript() string {
	return "set -e; for c in ./migrate ./migrate.sh /app/migrate /app/migrate.sh /workspace/migrate /workspace/migrate.sh; do if [ -x \"$c\" ]; then echo \"running migration via $c\"; exec \"$c\"; fi; done; echo \"no migration command found in image. Set migration.command or add executable /app/migrate(.sh)\"; exit 1"
}

func waitForJobComplete(kubeconfig, namespace, name string, timeout time.Duration) error {
	secs := int(timeout / time.Second)
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace, "wait", "--for=condition=complete", "job/"+name, "--timeout", fmt.Sprintf("%ds", secs))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func getJobLogs(kubeconfig, namespace, name string) (string, error) {
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace, "logs", "job/"+name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func waitForDeploymentReady(kubeconfig, namespace, name string, timeout time.Duration) error {
	secs := int(timeout / time.Second)
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace, "rollout", "status", "deployment/"+name, "--timeout", fmt.Sprintf("%ds", secs))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func getDeploymentLogs(kubeconfig, namespace, name string, tail int) (string, error) {
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace, "logs", "deployment/"+name, "--tail", strconv.Itoa(tail))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func inferListeningPorts(logs string) []int {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)uvicorn running on http://[a-z0-9\.\-:]*:(\d+)`),
		regexp.MustCompile(`(?i)url:\s*http://[a-z0-9\.\-:]*:(\d+)`),
		regexp.MustCompile(`(?i)listening on [a-z0-9\.\-:]*:(\d+)`),
		regexp.MustCompile(`(?i)running on http://[a-z0-9\.\-:]*:(\d+)`),
		regexp.MustCompile(`(?i)server started.*:(\d+)`),
	}
	seen := map[int]bool{}
	ports := make([]int, 0, 4)
	for _, re := range patterns {
		matches := re.FindAllStringSubmatch(logs, -1)
		for _, m := range matches {
			if len(m) < 2 {
				continue
			}
			p, err := strconv.Atoi(strings.TrimSpace(m[1]))
			if err != nil || p <= 0 || seen[p] {
				continue
			}
			seen[p] = true
			ports = append(ports, p)
		}
	}
	sort.Ints(ports)
	return ports
}

func verifyDeploymentRuntimePort(kubeconfig, namespace, app string, expectedPort int) error {
	logs, err := getDeploymentLogs(kubeconfig, namespace, app, 120)
	if err != nil {
		return nil
	}
	ports := inferListeningPorts(logs)
	if len(ports) == 0 {
		return nil
	}
	for _, p := range ports {
		if p == expectedPort {
			return nil
		}
	}
	found := make([]string, 0, len(ports))
	for _, p := range ports {
		found = append(found, strconv.Itoa(p))
	}
	return fmt.Errorf("runtime port mismatch for %s: declared container_port=%d but logs indicate %s", app, expectedPort, strings.Join(found, ", "))
}

func renderRedisManifest(namespace, name, image string, port int) string {
	tpl := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{.Name}}
  namespace: {{.Namespace}}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: {{.Name}}
  template:
    metadata:
      labels:
        app: {{.Name}}
    spec:
      containers:
        - name: {{.Name}}
          image: {{.Image}}
          ports:
            - containerPort: {{.Port}}
          readinessProbe:
            tcpSocket:
              port: {{.Port}}
            initialDelaySeconds: 3
            periodSeconds: 5
---
apiVersion: v1
kind: Service
metadata:
  name: {{.Name}}
  namespace: {{.Namespace}}
spec:
  type: NodePort
  selector:
    app: {{.Name}}
  ports:
    - port: {{.Port}}
      targetPort: {{.Port}}
`
	return mustRender(tpl, map[string]string{
		"Namespace": namespace,
		"Name":      name,
		"Image":     image,
		"Port":      fmt.Sprintf("%d", port),
	})
}

func renderSQLManifest(namespace, name, image string, port int, authSecretName, database, username string) string {
	tpl := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{.Name}}
  namespace: {{.Namespace}}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: {{.Name}}
  template:
    metadata:
      labels:
        app: {{.Name}}
    spec:
      containers:
        - name: {{.Name}}
          image: {{.Image}}
          env:
            - name: POSTGRES_DB
              value: {{.Database}}
            - name: POSTGRES_USER
              value: {{.Username}}
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: {{.AuthSecretName}}
                  key: db-password
          resources:
            requests:
              cpu: "250m"
              memory: "512Mi"
            limits:
              cpu: "1"
              memory: "1536Mi"
          ports:
            - containerPort: {{.Port}}
          readinessProbe:
            tcpSocket:
              port: {{.Port}}
            initialDelaySeconds: 20
            periodSeconds: 10
---
apiVersion: v1
kind: Service
metadata:
  name: {{.Name}}
  namespace: {{.Namespace}}
spec:
  type: NodePort
  selector:
    app: {{.Name}}
  ports:
    - port: {{.Port}}
      targetPort: {{.Port}}
`
	return mustRender(tpl, map[string]string{
		"Namespace":      namespace,
		"Name":           name,
		"Image":          image,
		"Port":           fmt.Sprintf("%d", port),
		"AuthSecretName": authSecretName,
		"Database":       yamlQuote(database),
		"Username":       yamlQuote(username),
	})
}

func renderOpaqueSecret(namespace, name string, data map[string]string) string {
	var b strings.Builder
	b.WriteString("apiVersion: v1\n")
	b.WriteString("kind: Secret\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: " + name + "\n")
	b.WriteString("  namespace: " + namespace + "\n")
	b.WriteString("type: Opaque\n")
	b.WriteString("stringData:\n")
	for k, v := range data {
		b.WriteString("  " + k + ": " + yamlQuote(v) + "\n")
	}
	return b.String()
}

func sanitizeName(in string) string {
	s := strings.ToLower(in)
	re := regexp.MustCompile(`[^a-z0-9-]+`)
	s = re.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "app"
	}
	return s
}

func getServiceNodePort(kubeconfig, namespace, app string) (int, error) {
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace, "get", "svc", app, "-o", "jsonpath={.spec.ports[0].nodePort}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("get service nodePort failed: %v", err)
	}
	portStr := strings.TrimSpace(string(out))
	if portStr == "" {
		return 0, fmt.Errorf("nodePort not found")
	}
	var port int
	_, err = fmt.Sscanf(portStr, "%d", &port)
	if err != nil {
		return 0, fmt.Errorf("invalid nodePort: %s", portStr)
	}
	return port, nil
}

func getDependencyAccessInfo(kubeconfig, namespace, app string) (map[string]string, error) {
	name := strings.ToLower(strings.TrimSpace(app))
	switch name {
	case "postgres", "sql":
		return getPostgresAccessInfo(kubeconfig, namespace, app)
	case "redis":
		// Current redis deployment does not configure auth; expose defaults.
		return map[string]string{
			"engine":   "redis",
			"username": "",
			"password": "",
			"database": "0",
		}, nil
	default:
		return nil, nil
	}
}

func addConnectionURL(access map[string]string, endpoint string) {
	if len(access) == 0 || endpoint == "" {
		return
	}
	parsed, err := neturl.Parse(endpoint)
	if err != nil {
		return
	}
	host := parsed.Hostname()
	port := parsed.Port()
	engine := strings.ToLower(strings.TrimSpace(access["engine"]))
	user := access["username"]
	pass := access["password"]
	db := access["database"]

	switch engine {
	case "postgres":
		if db == "" {
			db = "postgres"
		}
		cred := user
		if pass != "" {
			cred = neturl.QueryEscape(user) + ":" + neturl.QueryEscape(pass)
		}
		if cred != "" {
			access["url"] = fmt.Sprintf("postgresql://%s@%s:%s/%s?sslmode=disable", cred, host, port, db)
		} else {
			access["url"] = fmt.Sprintf("postgresql://%s:%s/%s?sslmode=disable", host, port, db)
		}
		access["public_url"] = fmt.Sprintf("postgresql://%s:%s/%s?sslmode=disable", host, port, db)
	case "redis":
		if db == "" {
			db = "0"
		}
		if pass != "" {
			access["url"] = fmt.Sprintf("redis://:%s@%s:%s/%s", neturl.QueryEscape(pass), host, port, db)
		} else {
			access["url"] = fmt.Sprintf("redis://%s:%s/%s", host, port, db)
		}
		access["public_url"] = fmt.Sprintf("redis://%s:%s/%s", host, port, db)
	}
}

func getPostgresAccessInfo(kubeconfig, namespace, app string) (map[string]string, error) {
	type envVar struct {
		Name      string `json:"name"`
		Value     string `json:"value"`
		ValueFrom *struct {
			SecretKeyRef *struct {
				Name string `json:"name"`
				Key  string `json:"key"`
			} `json:"secretKeyRef"`
		} `json:"valueFrom"`
	}
	type depResp struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Env []envVar `json:"env"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}

	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace, "get", "deployment", app, "-o", "json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("get postgres deployment failed: %v", err)
	}

	var dep depResp
	if err := json.Unmarshal(out, &dep); err != nil {
		return nil, err
	}
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return nil, fmt.Errorf("postgres deployment has no containers")
	}

	db := ""
	user := ""
	pass := ""
	passSecret := ""
	passKey := ""
	for _, ev := range dep.Spec.Template.Spec.Containers[0].Env {
		switch ev.Name {
		case "POSTGRES_DB":
			db = ev.Value
		case "POSTGRES_USER":
			user = ev.Value
		case "POSTGRES_PASSWORD":
			if ev.Value != "" {
				pass = ev.Value
			}
			if ev.ValueFrom != nil && ev.ValueFrom.SecretKeyRef != nil {
				passSecret = ev.ValueFrom.SecretKeyRef.Name
				passKey = ev.ValueFrom.SecretKeyRef.Key
			}
		}
	}
	if pass == "" && passSecret != "" && passKey != "" {
		if sec, err := getSecretStringData(kubeconfig, namespace, passSecret, passKey); err == nil {
			pass = sec
		}
	}

	return map[string]string{
		"engine":   "postgres",
		"username": user,
		"password": pass,
		"database": db,
	}, nil
}

func getSecretStringData(kubeconfig, namespace, secretName, key string) (string, error) {
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace, "get", "secret", secretName, "-o", "jsonpath={.data."+key+"}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("get secret failed: %v", err)
	}
	b64 := strings.TrimSpace(string(out))
	if b64 == "" {
		return "", fmt.Errorf("secret value is empty")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func listWorkspaces() ([]byte, error) {
	cmd := exec.Command("kind", "get", "clusters")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("kind get clusters: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var workspaces []map[string]any
	for _, line := range lines {
		name := strings.TrimSpace(line)
		if name == "" || !strings.HasPrefix(name, "ws-") {
			continue
		}
		kcfg := filepath.Join("/home/beko/kubeconfigs", name+".yaml")
		apps, _ := listWorkspaceApps(kcfg, name)
		workspaces = append(workspaces, map[string]any{
			"workspace": name,
			"apps":      apps,
		})
	}
	return json.Marshal(workspaces)
}

func listWorkspaceApps(kubeconfig, namespace string) ([]map[string]any, error) {
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace, "get", "svc", "-o", `jsonpath={range .items[*]}{.metadata.name}|{.spec.ports[0].nodePort}{"\n"}{end}`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var apps []map[string]any
	for _, l := range lines {
		parts := strings.Split(l, "|")
		if len(parts) != 2 {
			continue
		}
		name := parts[0]
		portStr := parts[1]
		var port int
		fmt.Sscanf(portStr, "%d", &port)
		apps = append(apps, map[string]any{
			"app":      name,
			"nodePort": port,
		})
	}
	return apps, nil
}

func deleteWorkspace(name string) error {
	cmd := exec.Command("kind", "delete", "cluster", "--name", name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kind delete cluster: %v", err)
	}
	kcfg := filepath.Join("/home/beko/kubeconfigs", name+".yaml")
	_ = os.Remove(kcfg)

	serverState.mu.Lock()
	for k := range serverState.endpoints {
		if strings.HasPrefix(k, name+"/") {
			delete(serverState.endpoints, k)
		}
	}
	serverState.mu.Unlock()
	return nil
}

func deleteApp(workspace, app string) error {
	kcfg := filepath.Join("/home/beko/kubeconfigs", workspace+".yaml")
	cmd := exec.Command("kubectl", "--kubeconfig", kcfg, "-n", workspace, "delete", "deployment", app)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("delete deployment: %v", err)
	}
	cmd = exec.Command("kubectl", "--kubeconfig", kcfg, "-n", workspace, "delete", "service", app)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("delete service: %v", err)
	}

	serverState.mu.Lock()
	delete(serverState.endpoints, workspace+"/"+app)
	serverState.mu.Unlock()
	return nil
}

func scaleApp(workspace, app, replicas string) error {
	kcfg := filepath.Join("/home/beko/kubeconfigs", workspace+".yaml")
	cmd := exec.Command("kubectl", "--kubeconfig", kcfg, "-n", workspace, "scale", "deployment", app, "--replicas", replicas)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scale deployment: %v", err)
	}
	return nil
}

func rolloutRestart(workspace, app string) error {
	kcfg := filepath.Join("/home/beko/kubeconfigs", workspace+".yaml")
	cmd := exec.Command("kubectl", "--kubeconfig", kcfg, "-n", workspace, "rollout", "restart", "deployment", app)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rollout restart: %v", err)
	}
	return nil
}

func rolloutRestartAll(workspace string) error {
	kcfg := filepath.Join("/home/beko/kubeconfigs", workspace+".yaml")
	cmd := exec.Command("kubectl", "--kubeconfig", kcfg, "-n", workspace, "rollout", "restart", "deployment", "-l", "app")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rollout restart all: %v", err)
	}
	return nil
}

func openAPISpec() string {
	return `{
  "openapi": "3.0.3",
  "info": {
    "title": "Tekton Runner API",
    "version": "1.0.0"
  },
  "paths": {
    "/healthz": {
      "get": {
        "summary": "Health check",
        "responses": { "200": { "description": "OK" } }
      }
    },
    "/run": {
      "post": {
        "summary": "Create TaskRun",
        "requestBody": {
          "required": true,
          "content": { "application/json": { "schema": { "$ref": "#/components/schemas/RunRequest" } } }
        },
        "responses": { "202": { "description": "Submitted" } }
      }
    },
    "/zip/upload": {
      "post": {
        "summary": "Upload a zip file to zip-server (/srv)",
        "requestBody": {
          "required": true,
          "content": {
            "multipart/form-data": {
              "schema": {
                "type": "object",
                "required": ["file"],
                "properties": {
                  "file": { "type": "string", "format": "binary" },
                  "filename": { "type": "string", "description": "Optional override filename (.zip)" }
                }
              }
            }
          }
        },
        "responses": {
          "200": { "description": "Uploaded" },
          "400": { "description": "Invalid request" },
          "401": { "description": "Unauthorized" },
          "500": { "description": "Upload failed" }
        }
      }
    },
    "/zip/delete": {
      "delete": {
        "summary": "Delete a zip file from zip-server (/srv)",
        "parameters": [
          { "name": "filename", "in": "query", "required": true, "schema": { "type": "string" }, "description": "Zip filename to delete" }
        ],
        "responses": {
          "200": { "description": "Deleted" },
          "400": { "description": "Invalid request" },
          "401": { "description": "Unauthorized" },
          "500": { "description": "Delete failed" }
        }
      },
      "post": {
        "summary": "Delete a zip file from zip-server (/srv) using form/query filename",
        "parameters": [
          { "name": "filename", "in": "query", "required": false, "schema": { "type": "string" } }
        ],
        "requestBody": {
          "required": false,
          "content": {
            "application/x-www-form-urlencoded": {
              "schema": {
                "type": "object",
                "properties": {
                  "filename": { "type": "string" }
                }
              }
            }
          }
        },
        "responses": {
          "200": { "description": "Deleted" },
          "400": { "description": "Invalid request" },
          "401": { "description": "Unauthorized" },
          "500": { "description": "Delete failed" }
        }
      }
    },
    "/taskrun/all": {
      "get": {
        "summary": "List all TaskRuns",
        "parameters": [
          { "name": "namespace", "in": "query", "required": false, "schema": { "type": "string" } }
        ],
        "responses": { "200": { "description": "Array of TaskRun statuses" } }
      }
    },
    "/taskrun/status": {
      "get": {
        "summary": "TaskRun status",
        "parameters": [
          { "name": "name", "in": "query", "required": true, "schema": { "type": "string" } },
          { "name": "namespace", "in": "query", "required": false, "schema": { "type": "string" } }
        ],
        "responses": { "200": { "description": "TaskRun status" } }
      }
    },
    "/taskrun/logs": {
      "get": {
        "summary": "TaskRun logs",
        "parameters": [
          { "name": "name", "in": "query", "required": true, "schema": { "type": "string" } },
          { "name": "namespace", "in": "query", "required": false, "schema": { "type": "string" } },
          { "name": "tail", "in": "query", "required": false, "schema": { "type": "integer" } }
        ],
        "responses": { "200": { "description": "Logs" } }
      }
    },
    "/run/logs": {
      "get": {
        "summary": "End-to-end timeline logs by workspace/app",
        "parameters": [
          { "name": "workspace", "in": "query", "required": true, "schema": { "type": "string" } },
          { "name": "app", "in": "query", "required": false, "schema": { "type": "string" } },
          { "name": "run_id", "in": "query", "required": false, "schema": { "type": "string" } },
          { "name": "limit", "in": "query", "required": false, "schema": { "type": "integer" } },
          { "name": "include_taskrun", "in": "query", "required": false, "schema": { "type": "boolean" } },
          { "name": "include_containers", "in": "query", "required": false, "schema": { "type": "boolean" } },
          { "name": "tail_raw", "in": "query", "required": false, "schema": { "type": "integer" } },
          { "name": "format", "in": "query", "required": false, "schema": { "type": "string", "enum": ["json","text"] } }
        ],
        "responses": { "200": { "description": "Timeline events" } }
      }
    },
    "/endpoint": {
      "get": {
        "summary": "Get app endpoint",
        "parameters": [
          { "name": "workspace", "in": "query", "required": true, "schema": { "type": "string" } },
          { "name": "app", "in": "query", "required": true, "schema": { "type": "string" } }
        ],
        "responses": { "200": { "description": "Endpoint" } }
      }
    },
    "/hostinfo": {
      "get": {
        "summary": "Get host IP",
        "responses": { "200": { "description": "Host info" } }
      }
    },
    "/external-map": {
      "get": {
        "summary": "List external port mappings",
        "responses": { "200": { "description": "Mappings" } }
      },
      "post": {
        "summary": "Set external port mapping",
        "requestBody": {
          "required": true,
          "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ExternalPortEntry" } } }
        },
        "responses": { "200": { "description": "Saved" }, "409": { "description": "Port conflict" } }
      }
    },
    "/workspaces": {
      "get": {
        "summary": "List workspaces",
        "responses": { "200": { "description": "Workspaces" } }
      }
    },
    "/workspace/status": {
      "get": {
        "summary": "Workspace status",
        "parameters": [
          { "name": "workspace", "in": "query", "required": true, "schema": { "type": "string" } }
        ],
        "responses": { "200": { "description": "Status" } }
      }
    },
    "/workspace/metrics": {
      "get": {
        "summary": "Workspace metrics",
        "parameters": [
          { "name": "workspace", "in": "query", "required": true, "schema": { "type": "string" } }
        ],
        "responses": { "200": { "description": "Metrics" } }
      }
    },
    "/cluster/metrics": {
      "get": {
        "summary": "Cluster metrics",
        "responses": { "200": { "description": "Metrics" } }
      }
    },
    "/workspace/delete": {
      "post": {
        "summary": "Delete workspace",
        "parameters": [
          { "name": "workspace", "in": "query", "required": true, "schema": { "type": "string" } }
        ],
        "responses": { "200": { "description": "Deleted" } }
      }
    },
    "/workspace/scale": {
      "post": {
        "summary": "Scale app",
        "parameters": [
          { "name": "workspace", "in": "query", "required": true, "schema": { "type": "string" } },
          { "name": "app", "in": "query", "required": true, "schema": { "type": "string" } },
          { "name": "replicas", "in": "query", "required": true, "schema": { "type": "integer" } }
        ],
        "responses": { "200": { "description": "Scaled" } }
      }
    },
    "/workspace/restart": {
      "post": {
        "summary": "Restart all apps in workspace",
        "parameters": [
          { "name": "workspace", "in": "query", "required": true, "schema": { "type": "string" } }
        ],
        "responses": { "200": { "description": "Restarted" } }
      }
    },
    "/app/delete": {
      "post": {
        "summary": "Delete app",
        "parameters": [
          { "name": "workspace", "in": "query", "required": true, "schema": { "type": "string" } },
          { "name": "app", "in": "query", "required": true, "schema": { "type": "string" } }
        ],
        "responses": { "200": { "description": "Deleted" } }
      }
    },
    "/app/restart": {
      "post": {
        "summary": "Restart app",
        "parameters": [
          { "name": "workspace", "in": "query", "required": true, "schema": { "type": "string" } },
          { "name": "app", "in": "query", "required": true, "schema": { "type": "string" } }
        ],
        "responses": { "200": { "description": "Restarted" } }
      }
    }
  },
  "components": {
    "schemas": {
      "RunRequest": {
        "type": "object",
        "properties": {
          "app_name": { "type": "string" },
          "apps": {
            "type": "array",
            "items": {
              "type": "object",
              "properties": {
                "app_name": { "type": "string" },
                "project": { "type": "string" },
                "tag": { "type": "string" },
                "container_port": { "type": "integer" },
                "context_sub_path": { "type": "string" }
              }
            }
          },
          "workspace": { "type": "string" },
          "source": {
            "type": "object",
            "properties": {
              "type": { "type": "string", "enum": ["git","zip"] },
              "repo_url": { "type": "string" },
              "revision": { "type": "string" },
              "git_username": { "type": "string" },
              "git_token": { "type": "string" },
              "pvc_name": { "type": "string" },
              "zip_url": { "type": "string" },
              "zip_username": { "type": "string" },
              "zip_password": { "type": "string" },
              "context_sub_path": { "type": "string" }
            }
          },
          "image": {
            "type": "object",
            "properties": {
              "project": { "type": "string" },
              "tag": { "type": "string" },
              "registry": { "type": "string" }
            }
          },
          "deploy": {
            "type": "object",
            "properties": {
              "container_port": { "type": "integer" }
            }
          },
          "dependency": {
            "type": "object",
            "properties": {
              "type": { "type": "string", "enum": ["none","redis","sql","both"] },
              "redis": {
                "type": "object",
                "properties": {
                  "image": { "type": "string" },
                  "service_name": { "type": "string" },
                  "port": { "type": "integer" },
                  "connection_env": { "type": "string" }
                }
              },
              "sql": {
                "type": "object",
                "properties": {
                  "image": { "type": "string" },
                  "service_name": { "type": "string" },
                  "port": { "type": "integer" },
                  "database": { "type": "string" },
                  "username": { "type": "string" },
                  "password": { "type": "string" },
                  "connection_env": { "type": "string" }
                }
              }
            }
          },
          "migration": {
            "type": "object",
            "properties": {
              "enabled": { "type": "boolean" },
              "image": { "type": "string" },
              "command": { "type": "array", "items": { "type": "string" } },
              "args": { "type": "array", "items": { "type": "string" } },
              "env_name": { "type": "string" }
            }
          }
        }
      },
      "ExternalPortEntry": {
        "type": "object",
        "properties": {
          "workspace": { "type": "string" },
          "app": { "type": "string" },
          "external_port": { "type": "integer" }
        }
      }
    }
  }
}`
}

func swaggerHTML() string {
	return `<!doctype html>
<html>
  <head>
    <meta charset="utf-8" />
    <title>Tekton Runner API Docs</title>
    <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" />
  </head>
  <body>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
    <script>
      window.ui = SwaggerUIBundle({
        url: "/openapi.json",
        dom_id: "#swagger-ui"
      });
    </script>
  </body>
</html>`
}

func (s *EventStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.events = []RunEvent{}
			return nil
		}
		return err
	}
	lines := strings.Split(string(data), "\n")
	events := make([]RunEvent, 0, len(lines))
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		var ev RunEvent
		if err := json.Unmarshal([]byte(ln), &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	if s.maxKeep > 0 && len(events) > s.maxKeep {
		events = events[len(events)-s.maxKeep:]
	}
	s.events = events
	return nil
}

func (s *EventStore) append(ev RunEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ev.Timestamp == "" {
		ev.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	s.events = append(s.events, ev)
	if s.maxKeep > 0 && len(s.events) > s.maxKeep {
		s.events = s.events[len(s.events)-s.maxKeep:]
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

func (s *EventStore) query(workspace, app, runID string, limit int) []RunEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RunEvent, 0, 128)
	for i := len(s.events) - 1; i >= 0; i-- {
		ev := s.events[i]
		if workspace != "" && ev.Workspace != workspace {
			continue
		}
		if app != "" && ev.App != app {
			continue
		}
		if runID != "" && ev.RunID != runID {
			continue
		}
		out = append(out, ev)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	// reverse to chronological order
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func parseBoolQuery(v string, def bool) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func formatEventBadge(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded", "success", "ok", "completed":
		return "[OK]"
	case "failed", "error":
		return "[FAIL]"
	case "running", "accepted", "submitted":
		return "[RUN]"
	default:
		return "[INFO]"
	}
}

func summarizeRunEvents(events []RunEvent) []RunEvent {
	if len(events) == 0 {
		return nil
	}
	lastByStage := map[string]RunEvent{}
	order := make([]string, 0, len(events))
	for _, ev := range events {
		if _, ok := lastByStage[ev.Stage]; !ok {
			order = append(order, ev.Stage)
		}
		lastByStage[ev.Stage] = ev
	}
	out := make([]RunEvent, 0, len(order))
	for _, stage := range order {
		out = append(out, lastByStage[stage])
	}
	return out
}

func trimLogBlock(s string) string {
	return strings.TrimRight(s, "\n")
}

func writeTextLogSection(b *strings.Builder, title string) {
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString("=== ")
	b.WriteString(title)
	b.WriteString(" ===\n")
}

type archivedRawLog struct {
	UpdatedAt string `json:"updated_at"`
	Workspace string `json:"workspace"`
	App       string `json:"app"`
	Namespace string `json:"namespace,omitempty"`
	TaskRun   string `json:"taskrun,omitempty"`
	Pod       string `json:"pod,omitempty"`
	Container string `json:"container,omitempty"`
	Logs      string `json:"logs"`
}

func safePathToken(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "unknown"
	}
	out := pathTokenRe.ReplaceAllString(v, "_")
	out = strings.Trim(out, "._-")
	if out == "" {
		return "unknown"
	}
	return out
}

func taskRunArchivePath(workspace, app, namespace, taskRun string) string {
	return filepath.Join(
		logArchiveRoot,
		safePathToken(workspace),
		safePathToken(app),
		"taskrun",
		safePathToken(namespace)+"__"+safePathToken(taskRun)+".json",
	)
}

func containerArchivePath(workspace, app, pod, container string) string {
	return filepath.Join(
		logArchiveRoot,
		safePathToken(workspace),
		safePathToken(app),
		"container",
		safePathToken(pod)+"__"+safePathToken(container)+".json",
	)
}

func writeArchiveFile(path string, rec archivedRawLog) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readArchiveFile(path string) (archivedRawLog, error) {
	var rec archivedRawLog
	b, err := os.ReadFile(path)
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return rec, err
	}
	return rec, nil
}

func archiveTaskRunLog(workspace, app string, block TaskRunLogBlock) error {
	if strings.TrimSpace(block.TaskRun) == "" {
		return fmt.Errorf("taskrun is empty")
	}
	if strings.TrimSpace(block.Logs) == "" {
		return fmt.Errorf("taskrun logs are empty")
	}
	rec := archivedRawLog{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Workspace: workspace,
		App:       app,
		Namespace: block.Namespace,
		TaskRun:   block.TaskRun,
		Logs:      block.Logs,
	}
	return writeArchiveFile(taskRunArchivePath(workspace, app, block.Namespace, block.TaskRun), rec)
}

func loadArchivedTaskRunLog(workspace, app, namespace, taskRun string) (TaskRunLogBlock, error) {
	rec, err := readArchiveFile(taskRunArchivePath(workspace, app, namespace, taskRun))
	if err != nil {
		return TaskRunLogBlock{}, err
	}
	return TaskRunLogBlock{
		TaskRun:   taskRun,
		Namespace: namespace,
		Logs:      rec.Logs,
		Source:    "archive",
		Collected: rec.UpdatedAt,
	}, nil
}

func archiveContainerLog(workspace, app string, block ContainerLogBlock) error {
	if strings.TrimSpace(block.Pod) == "" || strings.TrimSpace(block.Container) == "" {
		return fmt.Errorf("pod/container is empty")
	}
	if strings.TrimSpace(block.Logs) == "" {
		return fmt.Errorf("container logs are empty")
	}
	rec := archivedRawLog{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Workspace: workspace,
		App:       app,
		Pod:       block.Pod,
		Container: block.Container,
		Logs:      block.Logs,
	}
	return writeArchiveFile(containerArchivePath(workspace, app, block.Pod, block.Container), rec)
}

func loadArchivedContainerLog(workspace, app, pod, container string) (ContainerLogBlock, error) {
	rec, err := readArchiveFile(containerArchivePath(workspace, app, pod, container))
	if err != nil {
		return ContainerLogBlock{}, err
	}
	return ContainerLogBlock{
		Pod:       pod,
		Container: container,
		Logs:      rec.Logs,
		Source:    "archive",
		Collected: rec.UpdatedAt,
	}, nil
}

func loadArchivedContainerLogs(workspace, app string) ([]ContainerLogBlock, error) {
	dir := filepath.Join(logArchiveRoot, safePathToken(workspace), safePathToken(app), "container")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]ContainerLogBlock, 0, len(entries))
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		rec, err := readArchiveFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			continue
		}
		if strings.TrimSpace(rec.Logs) == "" {
			continue
		}
		out = append(out, ContainerLogBlock{
			Pod:       rec.Pod,
			Container: rec.Container,
			Logs:      rec.Logs,
			Source:    "archive",
			Collected: rec.UpdatedAt,
		})
		if len(out) >= 100 {
			break
		}
	}
	return out, nil
}

func captureAndArchiveTaskRunLogs(workspace, app, namespace, taskRun string, tail int) {
	if strings.TrimSpace(workspace) == "" || strings.TrimSpace(app) == "" || strings.TrimSpace(taskRun) == "" {
		return
	}
	if tail <= 0 {
		tail = 800
	}
	out, err := getTaskRunLogs(namespace, taskRun, strconv.Itoa(tail))
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return
	}
	_ = archiveTaskRunLog(workspace, app, TaskRunLogBlock{
		TaskRun:   taskRun,
		Namespace: namespace,
		Logs:      string(out),
		Source:    "live",
		Collected: time.Now().UTC().Format(time.RFC3339),
	})
}

func extractTaskRuns(events []RunEvent) []TaskRunLogBlock {
	seen := map[string]bool{}
	out := make([]TaskRunLogBlock, 0, 8)
	for _, ev := range events {
		if ev.Stage != "taskrun" {
			continue
		}
		if ev.Meta == nil {
			continue
		}
		name := strings.TrimSpace(ev.Meta["taskrun"])
		ns := strings.TrimSpace(ev.Meta["namespace"])
		if name == "" {
			continue
		}
		if ns == "" {
			ns = "tekton-pipelines"
		}
		key := ns + "/" + name
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, TaskRunLogBlock{TaskRun: name, Namespace: ns})
		if len(out) >= 20 {
			break
		}
	}
	return out
}

func getWorkspaceContainerLogs(workspace, app string, tail int) ([]ContainerLogBlock, error) {
	kcfg := filepath.Join("/home/beko/kubeconfigs", workspace+".yaml")
	if _, err := os.Stat(kcfg); err != nil {
		return nil, fmt.Errorf("kubeconfig not found for workspace %s", workspace)
	}
	args := []string{"--kubeconfig", kcfg, "-n", workspace, "get", "pods"}
	if strings.TrimSpace(app) != "" {
		args = append(args, "-l", "app="+app)
	}
	args = append(args, "-o", `jsonpath={range .items[*]}{.metadata.name}|{range .spec.containers[*]}{.name},{end}{"\n"}{end}`)
	cmd := exec.Command("kubectl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("get pods failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	blocks := make([]ContainerLogBlock, 0, 16)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		pod := strings.TrimSpace(parts[0])
		containerCSV := strings.Trim(parts[1], ",")
		if pod == "" || containerCSV == "" {
			continue
		}
		containers := strings.Split(containerCSV, ",")
		for _, c := range containers {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			logArgs := []string{"--kubeconfig", kcfg, "-n", workspace, "logs", pod, "-c", c, "--tail", strconv.Itoa(tail)}
			logCmd := exec.Command("kubectl", logArgs...)
			logOut, logErr := logCmd.CombinedOutput()
			block := ContainerLogBlock{
				Pod:       pod,
				Container: c,
				Logs:      string(logOut),
			}
			if logErr != nil {
				block.Error = strings.TrimSpace(string(logOut))
				block.Logs = ""
			}
			blocks = append(blocks, block)
			if len(blocks) >= 100 {
				return blocks, nil
			}
		}
	}
	return blocks, nil
}

var errPortConflict = fmt.Errorf("external port already in use")

func (s *ExternalPortStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.entries = []ExternalPortEntry{}
			return nil
		}
		return err
	}
	if len(data) == 0 {
		s.entries = []ExternalPortEntry{}
		return nil
	}
	var entries []ExternalPortEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}
	s.entries = entries
	return nil
}

func (s *ExternalPortStore) list() []ExternalPortEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ExternalPortEntry, len(s.entries))
	copy(out, s.entries)
	return out
}

func (s *ExternalPortStore) get(workspace, app string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.Workspace == workspace && e.App == app && e.ExternalPort > 0 {
			return e.ExternalPort, true
		}
	}
	return 0, false
}

func (s *ExternalPortStore) upsert(in ExternalPortEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.ExternalPort == in.ExternalPort && (e.Workspace != in.Workspace || e.App != in.App) {
			return errPortConflict
		}
	}
	updated := false
	for i, e := range s.entries {
		if e.Workspace == in.Workspace && e.App == in.App {
			s.entries[i] = in
			updated = true
			break
		}
	}
	if !updated {
		s.entries = append(s.entries, in)
	}
	b, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *ExternalPortStore) remove(workspace, app string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ExternalPortEntry, 0, len(s.entries))
	for _, e := range s.entries {
		if e.Workspace == workspace && e.App == app {
			continue
		}
		out = append(out, e)
	}
	s.entries = out
	b, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func forwardKey(workspace, app string) string {
	return workspace + "::" + app
}

func ensureForward(workspace, app string, externalPort int) error {
	if externalPort <= 0 {
		return fmt.Errorf("invalid external port")
	}
	key := forwardKey(workspace, app)

	// Existing workspaces can survive host reboots while their kind node loses
	// the registry host mapping/containerd tweaks needed for subsequent pulls.
	if err := configureKindNode(workspace); err != nil {
		return fmt.Errorf("prepare kind node failed: %v", err)
	}

	forwardMu.Lock()
	if fwd, ok := forwards[key]; ok && fwd != nil && fwd.Cmd != nil && fwd.Cmd.Process != nil {
		// Always recreate forward to avoid stale nodeIP/nodePort targets
		// after kind workspace recreations.
		_ = fwd.Cmd.Process.Kill()
		delete(forwards, key)
	}
	forwardMu.Unlock()

	kcfg := filepath.Join("/home/beko/kubeconfigs", workspace+".yaml")
	nodePort, err := getServiceNodePort(kcfg, workspace, app)
	if err != nil {
		return err
	}
	nodeIP, err := getNodeInternalIP(kcfg, workspace)
	if err != nil {
		return err
	}

	if _, err := exec.LookPath("socat"); err != nil {
		return fmt.Errorf("socat not found")
	}

	args := []string{
		fmt.Sprintf("TCP-LISTEN:%d,bind=0.0.0.0,fork,reuseaddr", externalPort),
		fmt.Sprintf("TCP:%s:%d", nodeIP, nodePort),
	}
	cmd := exec.Command("socat", args...)
	logPath := fmt.Sprintf("/tmp/socat-%s-%s.log", workspace, app)
	f, _ := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if f != nil {
		cmd.Stdout = f
		cmd.Stderr = f
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("socat start failed: %v", err)
	}
	forwardMu.Lock()
	forwards[key] = &Forward{Port: externalPort, Cmd: cmd}
	forwardMu.Unlock()
	return nil
}

func getNodeInternalIP(kubeconfig, workspace string) (string, error) {
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "get", "node", "-o", "jsonpath={.items[0].status.addresses[?(@.type==\"InternalIP\")].address}")
	out, err := cmd.Output()
	if err == nil {
		ip := strings.TrimSpace(string(out))
		if ip != "" {
			return ip, nil
		}
	}
	// Fallback to docker inspect on kind node
	node := workspace + "-control-plane"
	inspect := exec.Command("docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", node)
	insOut, insErr := inspect.Output()
	if insErr != nil {
		return "", fmt.Errorf("node ip not found")
	}
	ip := strings.TrimSpace(string(insOut))
	if ip == "" {
		return "", fmt.Errorf("node ip empty")
	}
	return ip, nil
}

func findUIDir() string {
	exe, err := os.Executable()
	if err == nil {
		if dir := filepath.Dir(exe); dir != "" {
			if fileExists(filepath.Join(dir, "ui", "index.html")) {
				return filepath.Join(dir, "ui")
			}
		}
	}
	if fileExists(filepath.Join("ui", "index.html")) {
		return "ui"
	}
	return ""
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func getWorkspaceStatus(workspace string) ([]byte, error) {
	kcfg := filepath.Join("/home/beko/kubeconfigs", workspace+".yaml")
	podsCmd := exec.Command("kubectl", "--kubeconfig", kcfg, "-n", workspace, "get", "pods", "-o", "jsonpath={range .items[*]}{.metadata.name}|{.status.phase}{\"\\n\"}{end}")
	podsOut, podsErr := podsCmd.CombinedOutput()
	if podsErr != nil {
		return nil, fmt.Errorf("get pods failed: %v", podsErr)
	}

	servicesCmd := exec.Command("kubectl", "--kubeconfig", kcfg, "-n", workspace, "get", "svc", "-o", `jsonpath={range .items[*]}{.metadata.name}|{.spec.ports[0].nodePort}{"\n"}{end}`)
	svcOut, svcErr := servicesCmd.CombinedOutput()
	if svcErr != nil {
		return nil, fmt.Errorf("get services failed: %v", svcErr)
	}

	var pods []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(podsOut)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 2 {
			continue
		}
		pods = append(pods, map[string]any{
			"name":  parts[0],
			"phase": parts[1],
		})
	}

	var svcs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(svcOut)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 2 {
			continue
		}
		portStr := parts[1]
		var port int
		fmt.Sscanf(portStr, "%d", &port)
		svcs = append(svcs, map[string]any{
			"name":     parts[0],
			"nodePort": port,
		})
	}

	out := map[string]any{
		"workspace": workspace,
		"pods":      pods,
		"services":  svcs,
	}
	return json.Marshal(out)
}

func getTaskRunStatus(namespace, name string) ([]byte, error) {
	cmd := kubectlCmd("-n", namespace, "get", "taskrun", name, "-o", "json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("get taskrun failed: %v", err)
	}
	var tr struct {
		Status struct {
			PodName        string `json:"podName"`
			StartTime      string `json:"startTime"`
			CompletionTime string `json:"completionTime"`
			Conditions     []struct {
				Status  string `json:"status"`
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal(out, &tr); err != nil {
		return nil, fmt.Errorf("parse taskrun: %v", err)
	}
	status := "Unknown"
	reason := ""
	message := ""
	if len(tr.Status.Conditions) > 0 {
		status = tr.Status.Conditions[0].Status
		reason = tr.Status.Conditions[0].Reason
		message = tr.Status.Conditions[0].Message
	}
	resp := TaskRunStatus{
		Name:           name,
		Namespace:      namespace,
		Status:         status,
		Reason:         reason,
		Message:        message,
		PodName:        tr.Status.PodName,
		StartTime:      tr.Status.StartTime,
		CompletionTime: tr.Status.CompletionTime,
	}
	return json.Marshal(resp)
}

func getAllTaskRuns(namespace string) ([]byte, error) {
	cmd := kubectlCmd("-n", namespace, "get", "taskrun", "-o", "json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list taskruns failed: %v", err)
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string            `json:"name"`
				Namespace         string            `json:"namespace"`
				CreationTimestamp string            `json:"creationTimestamp"`
				Labels            map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				PodName        string `json:"podName"`
				StartTime      string `json:"startTime"`
				CompletionTime string `json:"completionTime"`
				Conditions     []struct {
					Status  string `json:"status"`
					Reason  string `json:"reason"`
					Message string `json:"message"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("parse taskrun list: %v", err)
	}
	results := make([]TaskRunStatus, 0, len(list.Items))
	for _, item := range list.Items {
		status := "Unknown"
		reason := ""
		message := ""
		if len(item.Status.Conditions) > 0 {
			status = item.Status.Conditions[0].Status
			reason = item.Status.Conditions[0].Reason
			message = item.Status.Conditions[0].Message
		}
		results = append(results, TaskRunStatus{
			Name:           item.Metadata.Name,
			Namespace:      item.Metadata.Namespace,
			Status:         status,
			Reason:         reason,
			Message:        message,
			PodName:        item.Status.PodName,
			StartTime:      item.Status.StartTime,
			CompletionTime: item.Status.CompletionTime,
		})
	}
	sort.Slice(results, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, results[i].CompletionTime)
		tj, _ := time.Parse(time.RFC3339, results[j].CompletionTime)
		return ti.After(tj)
	})
	return json.Marshal(results)
}

func getTaskRunLogs(namespace, name, tail string) ([]byte, error) {
	statusBytes, err := getTaskRunStatus(namespace, name)
	if err != nil {
		return nil, err
	}
	var st TaskRunStatus
	if err := json.Unmarshal(statusBytes, &st); err != nil {
		return nil, err
	}
	if st.PodName == "" {
		return nil, fmt.Errorf("taskrun pod not created yet")
	}
	tailArg := "200"
	if strings.TrimSpace(tail) != "" {
		tailArg = strings.TrimSpace(tail)
	}
	cmd := kubectlCmd("-n", namespace, "logs", st.PodName, "--all-containers", "--tail", tailArg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("get logs failed: %v", err)
	}
	return out, nil
}

func getWorkspaceMetrics(workspace string) ([]byte, error) {
	kcfg := filepath.Join("/home/beko/kubeconfigs", workspace+".yaml")
	out := workspaceMetrics{
		Workspace: workspace,
		Node:      map[string]any{},
		Usage:     map[string]any{},
		Requests:  map[string]any{},
		Limits:    map[string]any{},
		Disk:      map[string]any{},
		Pods:      []map[string]any{},
		Errors:    map[string]string{},
		Raw:       map[string]string{},
	}

	nodeCmd := exec.Command("kubectl", "--kubeconfig", kcfg, "get", "nodes", "-o", "json")
	nodeOut, nodeErr := nodeCmd.CombinedOutput()
	if nodeErr != nil {
		return nil, fmt.Errorf("get nodes failed: %v", nodeErr)
	}
	var nodes struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Capacity    map[string]string `json:"capacity"`
				Allocatable map[string]string `json:"allocatable"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(nodeOut, &nodes); err != nil {
		return nil, fmt.Errorf("parse nodes: %v", err)
	}
	if len(nodes.Items) > 0 {
		n := nodes.Items[0]
		out.Node["name"] = n.Metadata.Name
		out.Node["capacity_raw"] = n.Status.Capacity
		out.Node["allocatable_raw"] = n.Status.Allocatable
		cpuCap := parseCPU(n.Status.Capacity["cpu"])
		memCap := parseMem(n.Status.Capacity["memory"])
		cpuAlloc := parseCPU(n.Status.Allocatable["cpu"])
		memAlloc := parseMem(n.Status.Allocatable["memory"])
		out.Node["capacity_mcpu"] = cpuCap
		out.Node["allocatable_mcpu"] = cpuAlloc
		out.Node["capacity_mem_mi"] = bytesToMi(memCap)
		out.Node["allocatable_mem_mi"] = bytesToMi(memAlloc)
	}

	topNodeCmd := exec.Command("kubectl", "--kubeconfig", kcfg, "top", "node", "--no-headers")
	topNodeOut, topNodeErr := topNodeCmd.CombinedOutput()
	if topNodeErr != nil {
		out.Errors["node_metrics"] = strings.TrimSpace(string(topNodeOut))
	} else {
		out.Raw["top_node"] = strings.TrimSpace(string(topNodeOut))
		fields := strings.Fields(string(topNodeOut))
		if len(fields) >= 5 {
			out.Usage["cpu_raw"] = fields[1]
			out.Usage["cpu_percent"] = fields[2]
			out.Usage["mem_raw"] = fields[3]
			out.Usage["mem_percent"] = fields[4]
			out.Usage["cpu_mcpu"] = parseCPU(fields[1])
			out.Usage["mem_mi"] = bytesToMi(parseMem(fields[3]))
		}
	}

	topPodsCmd := exec.Command("kubectl", "--kubeconfig", kcfg, "-n", workspace, "top", "pods", "--no-headers")
	topPodsOut, topPodsErr := topPodsCmd.CombinedOutput()
	if topPodsErr != nil {
		out.Errors["pod_metrics"] = strings.TrimSpace(string(topPodsOut))
	} else {
		out.Raw["top_pods"] = strings.TrimSpace(string(topPodsOut))
		var totalCPU int64
		var totalMem int64
		for _, line := range strings.Split(strings.TrimSpace(string(topPodsOut)), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			cpu := parseCPU(fields[1])
			mem := parseMem(fields[2])
			totalCPU += cpu
			totalMem += mem
			out.Pods = append(out.Pods, map[string]any{
				"name":     fields[0],
				"cpu_raw":  fields[1],
				"mem_raw":  fields[2],
				"cpu_mcpu": cpu,
				"mem_mi":   bytesToMi(mem),
			})
		}
		out.Usage["pods_cpu_mcpu"] = totalCPU
		out.Usage["pods_mem_mi"] = bytesToMi(totalMem)
	}

	reqCmd := exec.Command("kubectl", "--kubeconfig", kcfg, "-n", workspace, "get", "pods", "-o", "json")
	reqOut, reqErr := reqCmd.CombinedOutput()
	if reqErr != nil {
		out.Errors["requests_limits"] = strings.TrimSpace(string(reqOut))
	} else {
		var pods struct {
			Items []struct {
				Spec struct {
					Containers []struct {
						Resources struct {
							Requests map[string]string `json:"requests"`
							Limits   map[string]string `json:"limits"`
						} `json:"resources"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"items"`
		}
		if err := json.Unmarshal(reqOut, &pods); err == nil {
			var reqCPU, reqMem, limCPU, limMem int64
			for _, p := range pods.Items {
				for _, c := range p.Spec.Containers {
					reqCPU += parseCPU(c.Resources.Requests["cpu"])
					reqMem += parseMem(c.Resources.Requests["memory"])
					limCPU += parseCPU(c.Resources.Limits["cpu"])
					limMem += parseMem(c.Resources.Limits["memory"])
				}
			}
			out.Requests["cpu_mcpu"] = reqCPU
			out.Requests["mem_mi"] = bytesToMi(reqMem)
			out.Limits["cpu_mcpu"] = limCPU
			out.Limits["mem_mi"] = bytesToMi(limMem)
		}
	}

	nodeName := workspace + "-control-plane"
	dfCmd := exec.Command("docker", "exec", nodeName, "sh", "-c", "df -h / | tail -n 1")
	dfOut, dfErr := dfCmd.CombinedOutput()
	if dfErr != nil {
		out.Errors["disk"] = strings.TrimSpace(string(dfOut))
	} else {
		out.Raw["disk"] = strings.TrimSpace(string(dfOut))
		fields := strings.Fields(string(dfOut))
		if len(fields) >= 5 {
			out.Disk["size"] = fields[1]
			out.Disk["used"] = fields[2]
			out.Disk["avail"] = fields[3]
			out.Disk["use_percent"] = fields[4]
		}
	}

	duCmd := exec.Command("docker", "exec", nodeName, "sh", "-c", "du -sh /var/lib/containerd /var/lib/kubelet /var/lib/etcd 2>/dev/null | sort -h")
	duOut, duErr := duCmd.CombinedOutput()
	if duErr != nil {
		out.Errors["disk_usage"] = strings.TrimSpace(string(duOut))
	} else {
		out.Raw["disk_usage"] = strings.TrimSpace(string(duOut))
		var totalBytes int64
		for _, line := range strings.Split(strings.TrimSpace(string(duOut)), "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 1 {
				totalBytes += parseSize(parts[0])
			}
		}
		if totalBytes > 0 {
			out.Disk["workspace_total_bytes"] = totalBytes
			out.Disk["workspace_total"] = humanBytes(totalBytes)
		}
	}

	return json.Marshal(out)
}

func getClusterMetrics() ([]byte, error) {
	out := clusterMetrics{
		Clusters: []map[string]any{},
		Totals:   map[string]any{},
		Errors:   map[string]string{},
	}
	kindCmd := exec.Command("kind", "get", "clusters")
	kindOut, kindErr := kindCmd.CombinedOutput()
	if kindErr != nil {
		return nil, fmt.Errorf("kind get clusters failed: %v", kindErr)
	}
	var clusters []string
	for _, line := range strings.Split(strings.TrimSpace(string(kindOut)), "\n") {
		name := strings.TrimSpace(line)
		if name != "" {
			clusters = append(clusters, name)
		}
	}

	statsCmd := exec.Command("docker", "stats", "--no-stream", "--format", "{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}|{{.MemPerc}}")
	statsOut, statsErr := statsCmd.CombinedOutput()
	statsMap := map[string]map[string]string{}
	if statsErr != nil {
		out.Errors["docker_stats"] = strings.TrimSpace(string(statsOut))
	} else {
		for _, line := range strings.Split(strings.TrimSpace(string(statsOut)), "\n") {
			parts := strings.Split(line, "|")
			if len(parts) < 4 {
				continue
			}
			statsMap[parts[0]] = map[string]string{
				"cpu":    parts[1],
				"memory": parts[2],
				"mempct": parts[3],
			}
		}
	}

	sizeCmd := exec.Command("docker", "ps", "-s", "--format", "{{.Names}}|{{.Size}}")
	sizeOut, sizeErr := sizeCmd.CombinedOutput()
	sizeMap := map[string]string{}
	if sizeErr != nil {
		out.Errors["docker_sizes"] = strings.TrimSpace(string(sizeOut))
	} else {
		for _, line := range strings.Split(strings.TrimSpace(string(sizeOut)), "\n") {
			parts := strings.Split(line, "|")
			if len(parts) < 2 {
				continue
			}
			sizeMap[parts[0]] = parts[1]
		}
	}

	var totalCPU float64
	var totalSizeBytes int64
	for _, c := range clusters {
		node := c + "-control-plane"
		entry := map[string]any{
			"cluster": c,
			"node":    node,
		}
		if s, ok := statsMap[node]; ok {
			entry["cpu"] = s["cpu"]
			entry["memory"] = s["memory"]
			entry["mem_percent"] = s["mempct"]
			totalCPU += parsePercent(s["cpu"])
		}
		if sz, ok := sizeMap[node]; ok {
			entry["size"] = sz
			totalSizeBytes += parseSize(sz)
		}
		out.Clusters = append(out.Clusters, entry)
	}
	out.Totals["cpu_percent_sum"] = totalCPU
	out.Totals["size_bytes_sum"] = totalSizeBytes
	out.Totals["size_gb_sum"] = bytesToGB(totalSizeBytes)
	return json.Marshal(out)
}

func ensureMetricsServer(kubeconfigPath string) error {
	url := strings.TrimSpace(os.Getenv("METRICS_SERVER_URL"))
	if url == "" {
		url = "https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.8.0/components.yaml"
	}
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfigPath, "-n", "kube-system", "get", "deployment", "metrics-server")
	if err := cmd.Run(); err != nil {
		cmd = exec.Command("kubectl", "--kubeconfig", kubeconfigPath, "apply", "-f", url)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("apply metrics-server: %v", err)
		}
	}
	patch := `[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"},{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-preferred-address-types=InternalIP,Hostname,InternalDNS,ExternalDNS,ExternalIP"}]`
	cmd = exec.Command("kubectl", "--kubeconfig", kubeconfigPath, "-n", "kube-system", "patch", "deployment", "metrics-server", "--type=json", "-p", patch)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
	return nil
}

func parseCPU(q string) int64 {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0
	}
	if strings.HasSuffix(q, "m") {
		v, err := strconv.ParseInt(strings.TrimSuffix(q, "m"), 10, 64)
		if err != nil {
			return 0
		}
		return v
	}
	f, err := strconv.ParseFloat(q, 64)
	if err != nil {
		return 0
	}
	return int64(f * 1000)
}

func parseMem(q string) int64 {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0
	}
	units := map[string]int64{
		"Ki": 1024,
		"Mi": 1024 * 1024,
		"Gi": 1024 * 1024 * 1024,
		"Ti": 1024 * 1024 * 1024 * 1024,
		"Pi": 1024 * 1024 * 1024 * 1024 * 1024,
		"Ei": 1024 * 1024 * 1024 * 1024 * 1024 * 1024,
	}
	for u, mult := range units {
		if strings.HasSuffix(q, u) {
			v, err := strconv.ParseFloat(strings.TrimSuffix(q, u), 64)
			if err != nil {
				return 0
			}
			return int64(v * float64(mult))
		}
	}
	v, err := strconv.ParseFloat(q, 64)
	if err != nil {
		return 0
	}
	return int64(v)
}

func bytesToMi(b int64) int64 {
	if b == 0 {
		return 0
	}
	return b / (1024 * 1024)
}

func bytesToGB(b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(b) / (1024 * 1024 * 1024)
}

func parsePercent(s string) float64 {
	s = strings.TrimSpace(strings.TrimSuffix(s, "%"))
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func parseSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if idx := strings.Index(s, " "); idx >= 0 {
		s = s[:idx]
	}
	s = strings.TrimSpace(s)
	unit := s[len(s)-1:]
	value := s[:len(s)-1]
	mult := float64(1)
	switch unit {
	case "K":
		mult = 1024
	case "M":
		mult = 1024 * 1024
	case "G":
		mult = 1024 * 1024 * 1024
	case "T":
		mult = 1024 * 1024 * 1024 * 1024
	default:
		value = s
		mult = 1
	}
	v, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return int64(v * mult)
}

func humanBytes(b int64) string {
	if b <= 0 {
		return "0B"
	}
	units := []string{"B", "K", "M", "G", "T", "P"}
	v := float64(b)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if v >= 100 {
		return fmt.Sprintf("%.0f%s", v, units[i])
	}
	if v >= 10 {
		return fmt.Sprintf("%.1f%s", v, units[i])
	}
	return fmt.Sprintf("%.2f%s", v, units[i])
}

func setDefaults(in *Input) {
	if in.Namespace == "" {
		in.Namespace = "tekton-pipelines"
	}
	if in.Task == "" {
		in.Task = "build-and-push-generic"
	}
	if in.Source.Revision == "" {
		in.Source.Revision = "main"
	}
	if in.Image.Registry == "" {
		in.Image.Registry = "lenovo:8443"
	}
	in.RuntimeProfile = normalizeRuntimeProfile(in.RuntimeProfile)
	if in.RuntimeProfile == "" {
		in.RuntimeProfile = "auto"
	}
	for i := range in.Apps {
		in.Apps[i].RuntimeProfile = normalizeRuntimeProfile(in.Apps[i].RuntimeProfile)
	}
	if in.Image.Tag == "" {
		in.Image.Tag = "latest"
	}
	if in.Source.NFS != nil && in.Source.NFS.Size == "" {
		in.Source.NFS.Size = "50Gi"
	}
	if in.Source.SMB != nil && in.Source.SMB.Size == "" {
		in.Source.SMB.Size = "50Gi"
	}
	if in.FileStorage.Enabled {
		if strings.TrimSpace(in.FileStorage.PVCName) == "" {
			in.FileStorage.PVCName = "shared-file-pvc"
		}
		if strings.TrimSpace(in.FileStorage.MountPath) == "" {
			in.FileStorage.MountPath = "/app/storage"
		}
		if in.FileStorage.NFS == nil && in.FileStorage.SMB == nil {
			in.FileStorage.NFS = &NFSConfig{
				Server: "10.134.70.112",
				Path:   "/srv/nfs/shared",
				Size:   "1Gi",
			}
		}
		if in.FileStorage.NFS != nil && in.FileStorage.NFS.Size == "" {
			in.FileStorage.NFS.Size = "1Gi"
		}
		if in.FileStorage.SMB != nil && in.FileStorage.SMB.Size == "" {
			in.FileStorage.SMB.Size = "50Gi"
		}
	}
	if in.Deploy.ContainerPort == 0 {
		in.Deploy.ContainerPort = 8080
	}
	if in.Dependency.Type == "" {
		in.Dependency.Type = "none"
	}
	if in.Dependency.Redis.Image == "" {
		in.Dependency.Redis.Image = "lenovo:8443/library/redis:7-alpine"
	}
	if in.Dependency.Redis.ServiceName == "" {
		in.Dependency.Redis.ServiceName = "redis"
	}
	if in.Dependency.Redis.Port == 0 {
		in.Dependency.Redis.Port = 6379
	}
	if in.Dependency.Redis.ConnectionEnv == "" {
		in.Dependency.Redis.ConnectionEnv = "ConnectionStrings__Redis"
	}
	if in.Dependency.SQL.Image == "" {
		in.Dependency.SQL.Image = "lenovo:8443/library/postgres:16-alpine"
	}
	if in.Dependency.SQL.ServiceName == "" {
		in.Dependency.SQL.ServiceName = "postgres"
	}
	if in.Dependency.SQL.Port == 0 {
		in.Dependency.SQL.Port = 5432
	}
	if in.Dependency.SQL.Username == "" {
		in.Dependency.SQL.Username = "postgres"
	}
	if in.Dependency.SQL.ConnectionEnv == "" {
		in.Dependency.SQL.ConnectionEnv = "ConnectionStrings__DefaultConnection"
	}
	if in.Dependency.Type == "sql" || in.Dependency.Type == "both" {
		img := strings.ToLower(strings.TrimSpace(in.Dependency.SQL.Image))
		if img == "" || strings.Contains(img, "mssql") {
			in.Dependency.SQL.Image = "lenovo:8443/library/postgres:16-alpine"
		}
		if in.Dependency.SQL.ServiceName == "" || strings.EqualFold(in.Dependency.SQL.ServiceName, "sql") {
			in.Dependency.SQL.ServiceName = "postgres"
		}
		if in.Dependency.SQL.Port == 0 || in.Dependency.SQL.Port == 1433 {
			in.Dependency.SQL.Port = 5432
		}
		if in.Dependency.SQL.Username == "" || strings.EqualFold(in.Dependency.SQL.Username, "sa") {
			in.Dependency.SQL.Username = "postgres"
		}
	}
	if in.Migration.EnvName == "" {
		in.Migration.EnvName = "ConnectionStrings__DefaultConnection"
	}
}

func validate(in *Input) error {
	if err := validateRuntimeProfile(in.RuntimeProfile, "runtime_profile"); err != nil {
		return err
	}
	if in.Source.Type != "git" && in.Source.Type != "zip" {
		return fmt.Errorf("source.type must be git or zip")
	}
	if in.Source.Type == "git" {
		if in.Source.RepoURL == "" {
			return fmt.Errorf("source.repo_url is required for git")
		}
	}
	if in.Source.Type == "zip" {
		if in.Source.ZipURL == "" {
			return fmt.Errorf("source.zip_url is required for zip")
		}
	}
	if in.FileStorage.Enabled {
		if strings.TrimSpace(in.FileStorage.PVCName) == "" {
			return fmt.Errorf("file_storage.pvc_name is required when file_storage.enabled=true")
		}
		if !strings.HasPrefix(strings.TrimSpace(in.FileStorage.MountPath), "/") {
			return fmt.Errorf("file_storage.mount_path must be an absolute path")
		}
		if _, err := normalizeStorageSubPath(in.FileStorage.SubPath); err != nil {
			return err
		}
		if in.FileStorage.NFS != nil && (strings.TrimSpace(in.FileStorage.NFS.Server) == "" || strings.TrimSpace(in.FileStorage.NFS.Path) == "") {
			return fmt.Errorf("file_storage.nfs.server and file_storage.nfs.path are required")
		}
		if in.FileStorage.SMB != nil {
			if strings.TrimSpace(in.FileStorage.SMB.Server) == "" || strings.TrimSpace(in.FileStorage.SMB.Share) == "" {
				return fmt.Errorf("file_storage.smb.server and file_storage.smb.share are required")
			}
			if strings.TrimSpace(in.FileStorage.SMB.Username) == "" || strings.TrimSpace(in.FileStorage.SMB.Password) == "" {
				return fmt.Errorf("file_storage.smb.username and file_storage.smb.password are required")
			}
		}
	}
	if _, err := normalizeContextSubDir(in.Source.ContextSub); err != nil {
		return err
	}
	if len(in.Apps) == 0 {
		if strings.TrimSpace(in.AppName) == "" {
			return fmt.Errorf("app_name is required when apps is empty")
		}
		if strings.TrimSpace(in.Image.Project) == "" {
			return fmt.Errorf("image.project is required when apps is empty")
		}
	} else {
		for i, app := range in.Apps {
			if strings.TrimSpace(app.AppName) == "" {
				return fmt.Errorf("apps[%d].app_name is required", i)
			}
			if err := validateRuntimeProfile(app.RuntimeProfile, fmt.Sprintf("apps[%d].runtime_profile", i)); err != nil {
				return err
			}
			if _, err := normalizeContextSubDir(app.ContextSubDir); err != nil {
				return fmt.Errorf("apps[%d]: %w", i, err)
			}
		}
	}
	if _, err := resolveAppTargets(*in); err != nil {
		return err
	}
	if in.Workspace != "" && !strings.HasPrefix(in.Workspace, "ws-") {
		return fmt.Errorf("workspace must start with ws-")
	}
	if in.Dependency.Type != "none" && in.Dependency.Type != "redis" && in.Dependency.Type != "sql" && in.Dependency.Type != "both" {
		return fmt.Errorf("dependency.type must be none, redis, sql, or both")
	}
	if in.Dependency.Type == "sql" || in.Dependency.Type == "both" {
		if strings.TrimSpace(in.Dependency.SQL.Database) == "" {
			return fmt.Errorf("dependency.sql.database is required for sql dependency")
		}
		if strings.TrimSpace(in.Dependency.SQL.Username) == "" {
			return fmt.Errorf("dependency.sql.username is required for sql dependency")
		}
		if strings.TrimSpace(in.Dependency.SQL.Password) == "" {
			return fmt.Errorf("dependency.sql.password is required for sql dependency")
		}
	}
	if in.Migration.Enabled {
		if in.Dependency.Type != "sql" && in.Dependency.Type != "both" {
			return fmt.Errorf("migration.enabled requires dependency.type=sql or both")
		}
		if len(in.Migration.Command) == 0 && len(in.Migration.Args) > 0 {
			return fmt.Errorf("migration.args requires migration.command when migration.enabled=true")
		}
	}
	for i, env := range in.ExtraEnv {
		if strings.TrimSpace(env.Name) == "" {
			return fmt.Errorf("extra_env[%d].name is required", i)
		}
	}
	return nil
}

func renderGitSecret(in Input) string {
	tpl := `apiVersion: v1
kind: Secret
metadata:
  name: {{.GitSecret}}
  namespace: {{.Namespace}}
type: Opaque
stringData:
  username: {{.GitUsername}}
  token: {{.GitToken}}
`
	return mustRender(tpl, map[string]string{
		"GitSecret":   in.Source.GitSecret,
		"Namespace":   in.Namespace,
		"GitUsername": yamlQuote(in.Source.GitUsername),
		"GitToken":    yamlQuote(in.Source.GitToken),
	})
}

func renderNFS(in Input) (string, string) {
	id := randSuffix()
	pvName := "pv-nfs-" + id
	pvcName := in.Source.PVCName
	if pvcName == "" {
		pvcName = "pvc-nfs-" + id
		in.Source.PVCName = pvcName
	}

	pvTpl := `apiVersion: v1
kind: PersistentVolume
metadata:
  name: {{.PVName}}
spec:
  capacity:
    storage: {{.Size}}
  accessModes:
    - ReadWriteMany
  persistentVolumeReclaimPolicy: Retain
  nfs:
    server: {{.Server}}
    path: {{.Path}}
`
	pvcTpl := `apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: {{.PVCName}}
  namespace: {{.Namespace}}
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: {{.Size}}
  volumeName: {{.PVName}}
`

	pv := mustRender(pvTpl, map[string]string{
		"PVName": pvName,
		"Size":   in.Source.NFS.Size,
		"Server": in.Source.NFS.Server,
		"Path":   in.Source.NFS.Path,
	})
	pvc := mustRender(pvcTpl, map[string]string{
		"PVCName":   pvcName,
		"Namespace": in.Namespace,
		"Size":      in.Source.NFS.Size,
		"PVName":    pvName,
	})

	return pv, pvc
}

func renderSMB(in Input) (string, string, string) {
	id := randSuffix()
	pvName := "pv-smb-" + id
	pvcName := in.Source.PVCName
	if pvcName == "" {
		pvcName = "pvc-smb-" + id
		in.Source.PVCName = pvcName
	}
	secretName := in.Source.SMB.SecretName
	if secretName == "" {
		secretName = "smb-cred-" + id
		in.Source.SMB.SecretName = secretName
	}
	volHandle := in.Source.SMB.VolumeHandle
	if volHandle == "" {
		volHandle = "smb-" + id
		in.Source.SMB.VolumeHandle = volHandle
	}

	secretTpl := `apiVersion: v1
kind: Secret
metadata:
  name: {{.SecretName}}
  namespace: {{.Namespace}}
type: Opaque
stringData:
  username: {{.Username}}
  password: {{.Password}}
`

	pvTpl := `apiVersion: v1
kind: PersistentVolume
metadata:
  name: {{.PVName}}
spec:
  capacity:
    storage: {{.Size}}
  accessModes:
    - ReadWriteMany
  persistentVolumeReclaimPolicy: Retain
  csi:
    driver: smb.csi.k8s.io
    volumeHandle: {{.VolumeHandle}}
    volumeAttributes:
      source: "//{{.Server}}/{{.Share}}"
    nodeStageSecretRef:
      name: {{.SecretName}}
      namespace: {{.Namespace}}
`

	pvcTpl := `apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: {{.PVCName}}
  namespace: {{.Namespace}}
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: {{.Size}}
  volumeName: {{.PVName}}
`

	secret := mustRender(secretTpl, map[string]string{
		"SecretName": in.Source.SMB.SecretName,
		"Namespace":  in.Namespace,
		"Username":   yamlQuote(in.Source.SMB.Username),
		"Password":   yamlQuote(in.Source.SMB.Password),
	})

	pv := mustRender(pvTpl, map[string]string{
		"PVName":       pvName,
		"Size":         in.Source.SMB.Size,
		"VolumeHandle": in.Source.SMB.VolumeHandle,
		"Server":       in.Source.SMB.Server,
		"Share":        in.Source.SMB.Share,
		"SecretName":   in.Source.SMB.SecretName,
		"Namespace":    in.Namespace,
	})

	pvc := mustRender(pvcTpl, map[string]string{
		"PVCName":   pvcName,
		"Namespace": in.Namespace,
		"Size":      in.Source.SMB.Size,
		"PVName":    pvName,
	})

	return secret, pv, pvc
}

func renderTaskRun(in Input, target AppTarget) string {
	baseImageMap := `"{}"`
	if strings.TrimSpace(target.BaseImageMap) != "" {
		baseImageMap = yamlQuote(target.BaseImageMap)
	}
	ctx := RenderContext{
		Namespace:    in.Namespace,
		Task:         in.Task,
		SourceType:   in.Source.Type,
		RepoURL:      in.Source.RepoURL,
		Revision:     in.Source.Revision,
		Project:      harborProjectName(in.Workspace),
		Repo:         target.Project,
		Tag:          target.Tag,
		Registry:     in.Image.Registry,
		ZipURL:       in.Source.ZipURL,
		ZipUsername:  in.Source.ZipUsername,
		ZipPassword:  in.Source.ZipPassword,
		GitSecret:    in.Source.GitSecret,
		HasGit:       in.Source.Type == "git" && in.Source.GitUsername != "" && in.Source.GitToken != "",
		HasZip:       in.Source.Type == "zip",
		AppName:      target.AppName,
		ContextSub:   target.ContextSubDir,
		BaseImageMap: baseImageMap,
	}

	tpl := `apiVersion: tekton.dev/v1
kind: TaskRun
metadata:
  generateName: build-and-push-run-
  namespace: {{.Namespace}}
  labels:
    tekton-runner.app: {{.AppName}}
spec:
  serviceAccountName: build-bot
  taskRef:
    name: {{.Task}}
  params:
    - name: source-type
      value: {{.SourceType}}
{{- if eq .SourceType "git" }}
    - name: repo-url
      value: {{.RepoURL}}
    - name: revision
      value: {{.Revision}}
{{- end }}
{{- if eq .SourceType "zip" }}
    - name: zip-url
      value: {{.ZipURL}}
{{- if .ZipUsername }}
    - name: zip-username
      value: {{.ZipUsername}}
{{- end }}
{{- if .ZipPassword }}
    - name: zip-password
      value: {{.ZipPassword}}
{{- end }}
{{- end }}
    - name: project
      value: {{.Project}}
    - name: repo
      value: {{.Repo}}
    - name: registry
      value: {{.Registry}}
    - name: tag
      value: {{.Tag}}
    - name: base-image-map
      value: {{.BaseImageMap}}
{{- if .ContextSub }}
    - name: context-sub-path
      value: {{.ContextSub}}
{{- end }}
  workspaces:
    - name: source
      emptyDir: {}
{{- if .HasGit }}
    - name: git-credentials
      secret:
        secretName: {{.GitSecret}}
{{- end }}
`

	return mustRender(tpl, ctx)
}

func mustRender(tpl string, data any) string {
	t, err := template.New("tpl").Parse(tpl)
	if err != nil {
		fatal("parse template", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		fatal("render template", err)
	}
	return buf.String()
}

func randSuffix() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().Unix())
	}
	return hex.EncodeToString(b)
}

func yamlQuote(s string) string {
	return strconv.Quote(s)
}

func fatal(msg string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", msg, err)
	os.Exit(1)
}

func fatalMsg(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
