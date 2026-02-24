package main

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"
)

type Input struct {
	Namespace  string     `json:"namespace"`
	Task       string     `json:"task"`
	AppName    string     `json:"app_name"`
	Apps       []AppSpec  `json:"apps"`
	Workspace  string     `json:"workspace"`
	Deploy     Deploy     `json:"deploy"`
	Source     Source     `json:"source"`
	Image      Image      `json:"image"`
	Dependency Dependency `json:"dependency"`
	Migration  Migration  `json:"migration"`
}

type AppSpec struct {
	AppName       string `json:"app_name"`
	Project       string `json:"project"`
	Tag           string `json:"tag"`
	ContainerPort int    `json:"container_port"`
	ContextSubDir string `json:"context_sub_path"`
}

type Source struct {
	Type        string     `json:"type"`
	RepoURL     string     `json:"repo_url"`
	Revision    string     `json:"revision"`
	GitUsername string     `json:"git_username"`
	GitToken    string     `json:"git_token"`
	GitSecret   string     `json:"git_secret"`
	LocalPath   string     `json:"local_path"`
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
}

type RenderContext struct {
	Namespace   string
	Task        string
	SourceType  string
	RepoURL     string
	Revision    string
	LocalPath   string
	Project     string
	Tag         string
	Registry    string
	ZipURL      string
	ZipUsername string
	ZipPassword string
	GitSecret   string
	PVCName     string
	HasGit      bool
	HasLocal    bool
	HasZip      bool
	AppName     string
	ContextSub  string
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
		if port == 0 {
			port = basePort
		}
		ctx, err := normalizeContextSubDir(app.ContextSubDir)
		if err != nil {
			return nil, fmt.Errorf("apps[%d]: %w", i, err)
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

	if len(taskRunByApp) > 0 && (in.Source.Type == "zip" || in.Source.Type == "git" || in.Source.Type == "local") {
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

		if len(taskRunByApp) > 0 && (in.Source.Type == "zip" || in.Source.Type == "git" || in.Source.Type == "local") {
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
		serverState.mu.Lock()
		if url, ok := serverState.endpoints[key]; ok {
			serverState.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(fmt.Sprintf(`{"endpoint":"%s"}`, url)))
			return
		}
		serverState.mu.Unlock()

		kcfgPath := filepath.Join("/home/beko/kubeconfigs", workspace+".yaml")
		port, err := getServiceNodePort(kcfgPath, workspace, app)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		host := serverHostIP
		if host == "" {
			host = "127.0.0.1"
		}
		url := fmt.Sprintf("http://%s:%d", host, port)
		serverState.mu.Lock()
		serverState.endpoints[key] = url
		serverState.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fmt.Sprintf(`{"endpoint":"%s"}`, url)))
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
		app := sanitizeName(r.URL.Query().Get("app"))
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
			b.WriteString("=== Timeline Events ===\n")
			for _, e := range events {
				fmt.Fprintf(&b, "%s | run=%s | ws=%s | app=%s | %s/%s | %s\n",
					e.Timestamp, e.RunID, e.Workspace, e.App, e.Stage, e.Status, e.Message)
			}
			if includeTaskRun {
				b.WriteString("\n=== TaskRun Logs ===\n")
				for _, tr := range taskrunLogs {
					fmt.Fprintf(&b, "\n--- taskrun=%s namespace=%s ---\n", tr.TaskRun, tr.Namespace)
					if tr.Error != "" {
						fmt.Fprintf(&b, "ERROR: %s\n", tr.Error)
						continue
					}
					b.WriteString(tr.Logs)
					if !strings.HasSuffix(tr.Logs, "\n") {
						b.WriteString("\n")
					}
				}
			}
			if includeContainers {
				b.WriteString("\n=== Workspace Container Logs ===\n")
				for _, c := range containerLogs {
					fmt.Fprintf(&b, "\n--- pod=%s container=%s ---\n", c.Pod, c.Container)
					if c.Error != "" {
						fmt.Fprintf(&b, "ERROR: %s\n", c.Error)
						continue
					}
					b.WriteString(c.Logs)
					if !strings.HasSuffix(c.Logs, "\n") {
						b.WriteString("\n")
					}
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

	http.HandleFunc("/app/status", func(w http.ResponseWriter, r *http.Request) {
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
		info, err := getAppStatus(workspace, app)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(info)
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
		"/run",
		"/endpoint",
		"/workspaces",
		"/workspace/delete",
		"/workspace/status",
		"/workspace/metrics",
		"/workspace/scale",
		"/workspace/restart",
		"/cluster/metrics",
		"/taskrun/status",
		"/taskrun/logs",
		"/taskrun/logs/stream",
		"/run/logs",
		"/pod/logs/stream",
		"/app/delete",
		"/app/status",
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
	if err := validate(in); err != nil {
		return nil, nil, err
	}
	targets, err := resolveAppTargets(*in)
	if err != nil {
		return nil, nil, err
	}

	manifests := make([]string, 0, 4)
	if in.Source.Type == "git" && in.Source.GitUsername != "" && in.Source.GitToken != "" {
		if in.Source.GitSecret == "" {
			in.Source.GitSecret = "git-cred-" + randSuffix()
		}
		manifests = append(manifests, renderGitSecret(*in))
	}

	if in.Source.Type == "local" {
		if in.Source.NFS != nil {
			pv, pvc := renderNFS(*in)
			manifests = append(manifests, pv, pvc)
		} else if in.Source.SMB != nil {
			secret, pv, pvc := renderSMB(*in)
			manifests = append(manifests, secret, pv, pvc)
		}
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

	basePort := in.Deploy.ContainerPort
	if basePort == 0 {
		basePort = 8080
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
			ContainerPort: basePort,
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

	seen := map[string]bool{}
	contexts := make([]string, 0, 4)
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
		if !seen[ctx] {
			seen[ctx] = true
			contexts = append(contexts, ctx)
		}
	}
	return contexts, nil
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
	if host == "" || host == "0.0.0.0" {
		return "127.0.0.1"
	}
	return host
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
	if in.Migration.Enabled {
		emitRunEvent(runID, workspace, leadApp, "migration", "running", "Running migration job", nil)
		if err := runMigrationJob(kcfgPath, clusterName, leadApp, in.Migration, envRefs); err != nil {
			emitRunEvent(runID, workspace, leadApp, "migration", "failed", err.Error(), nil)
			return err
		}
		emitRunEvent(runID, workspace, leadApp, "migration", "succeeded", "Migration completed", nil)
	}

	for _, t := range targets {
		image := fmt.Sprintf("%s/%s/%s:%s", in.Image.Registry, strings.ToLower(t.Project), strings.ToLower(t.Project), t.Tag)
		emitRunEvent(runID, workspace, t.AppName, "deploy", "running", "Applying deployment and service", map[string]string{
			"image": image,
		})
		if err := applyDeployment(kcfgPath, clusterName, t.AppName, image, t.ContainerPort, envRefs); err != nil {
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
	cmd := exec.Command("docker", "exec", node, "sh", "-c", `gw="$(ip route | awk '/default/ {print $3; exit}')"; [ -n "$gw" ] || gw="172.18.0.1"; if ! grep -qE "[[:space:]]lenovo$" /etc/hosts; then echo "$gw lenovo" >> /etc/hosts; fi`)
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

func applyDeployment(kubeconfig, namespace, app, image string, port int, envRefs []AppEnvSecretRef) error {
	if port == 0 {
		port = 8080
	}
	manifest := renderDeployment(namespace, app, image, port, envRefs)
	return kubectlApplyWithKubeconfig(kubeconfig, manifest)
}

func renderDeployment(ns, app, image string, port int, envRefs []AppEnvSecretRef) string {
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
      containers:
        - name: {{.App}}
          image: {{.Image}}
          ports:
            - containerPort: {{.Port}}
{{.EnvBlock}}
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
		"Namespace": ns,
		"App":       app,
		"Image":     image,
		"Port":      fmt.Sprintf("%d", port),
		"EnvBlock":  renderEnvFromSecretBlock(envRefs),
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
		b.WriteString("              valueFrom:\n")
		b.WriteString("                secretKeyRef:\n")
		b.WriteString("                  name: " + ref.SecretName + "\n")
		b.WriteString("                  key: " + ref.SecretKey + "\n")
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
		conn := fmt.Sprintf("%s.%s.svc.cluster.local:%d", depName, namespace, dep.Redis.Port)
		secretName := app + "-app-config"
		if err := kubectlApplyWithKubeconfig(kubeconfig, renderOpaqueSecret(namespace, secretName, map[string]string{
			"redis-conn": conn,
		})); err != nil {
			return nil, fmt.Errorf("apply redis app secret failed: %v", err)
		}
		return []AppEnvSecretRef{{
			EnvName:    dep.Redis.ConnectionEnv,
			SecretName: secretName,
			SecretKey:  "redis-conn",
		}}, nil
	case "sql":
		depName := sanitizeName(dep.SQL.ServiceName)
		if depName == "" {
			depName = "sql"
		}
		authSecretName := depName + "-auth"
		if err := kubectlApplyWithKubeconfig(kubeconfig, renderOpaqueSecret(namespace, authSecretName, map[string]string{
			"sa-password": dep.SQL.Password,
		})); err != nil {
			return nil, fmt.Errorf("apply sql auth secret failed: %v", err)
		}
		if err := kubectlApplyWithKubeconfig(kubeconfig, renderSQLManifest(namespace, depName, dep.SQL.Image, dep.SQL.Port, authSecretName)); err != nil {
			return nil, fmt.Errorf("apply sql dependency failed: %v", err)
		}
		if err := waitForDeploymentReady(kubeconfig, namespace, depName, 6*time.Minute); err != nil {
			return nil, fmt.Errorf("sql dependency not ready: %v", err)
		}
		conn := fmt.Sprintf("Server=tcp:%s.%s.svc.cluster.local,%d;Initial Catalog=%s;User ID=%s;Password=%s;Encrypt=False;TrustServerCertificate=True;", depName, namespace, dep.SQL.Port, dep.SQL.Database, dep.SQL.Username, dep.SQL.Password)
		secretName := app + "-app-config"
		if err := kubectlApplyWithKubeconfig(kubeconfig, renderOpaqueSecret(namespace, secretName, map[string]string{
			"sql-conn": conn,
		})); err != nil {
			return nil, fmt.Errorf("apply sql app secret failed: %v", err)
		}
		return []AppEnvSecretRef{{
			EnvName:    dep.SQL.ConnectionEnv,
			SecretName: secretName,
			SecretKey:  "sql-conn",
		}}, nil
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
		redisConn := fmt.Sprintf("%s.%s.svc.cluster.local:%d", redisName, namespace, dep.Redis.Port)

		sqlName := sanitizeName(dep.SQL.ServiceName)
		if sqlName == "" {
			sqlName = "sql"
		}
		authSecretName := sqlName + "-auth"
		if err := kubectlApplyWithKubeconfig(kubeconfig, renderOpaqueSecret(namespace, authSecretName, map[string]string{
			"sa-password": dep.SQL.Password,
		})); err != nil {
			return nil, fmt.Errorf("apply sql auth secret failed: %v", err)
		}
		if err := kubectlApplyWithKubeconfig(kubeconfig, renderSQLManifest(namespace, sqlName, dep.SQL.Image, dep.SQL.Port, authSecretName)); err != nil {
			return nil, fmt.Errorf("apply sql dependency failed: %v", err)
		}
		if err := waitForDeploymentReady(kubeconfig, namespace, sqlName, 6*time.Minute); err != nil {
			return nil, fmt.Errorf("sql dependency not ready: %v", err)
		}
		sqlConn := fmt.Sprintf("Server=tcp:%s.%s.svc.cluster.local,%d;Initial Catalog=%s;User ID=%s;Password=%s;Encrypt=False;TrustServerCertificate=True;", sqlName, namespace, dep.SQL.Port, dep.SQL.Database, dep.SQL.Username, dep.SQL.Password)
		secretName := app + "-app-config"
		if err := kubectlApplyWithKubeconfig(kubeconfig, renderOpaqueSecret(namespace, secretName, map[string]string{
			"redis-conn": redisConn,
			"sql-conn":   sqlConn,
		})); err != nil {
			return nil, fmt.Errorf("apply combined app secret failed: %v", err)
		}
		return []AppEnvSecretRef{
			{
				EnvName:    dep.Redis.ConnectionEnv,
				SecretName: secretName,
				SecretKey:  "redis-conn",
			},
			{
				EnvName:    dep.SQL.ConnectionEnv,
				SecretName: secretName,
				SecretKey:  "sql-conn",
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported dependency.type: %s", dep.Type)
	}
}

func runMigrationJob(kubeconfig, namespace, app string, m Migration, envRefs []AppEnvSecretRef) error {
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

	jobName := sanitizeName(app + "-migrate-" + randSuffix())
	if len(jobName) > 63 {
		jobName = jobName[:63]
		jobName = strings.Trim(jobName, "-")
	}
	manifest := renderMigrationJob(namespace, jobName, m, secretName, secretKey)
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

func renderMigrationJob(namespace, name string, m Migration, secretName, secretKey string) string {
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
	b.WriteString("          image: " + m.Image + "\n")
	b.WriteString("          env:\n")
	b.WriteString("            - name: " + m.EnvName + "\n")
	b.WriteString("              valueFrom:\n")
	b.WriteString("                secretKeyRef:\n")
	b.WriteString("                  name: " + secretName + "\n")
	b.WriteString("                  key: " + secretKey + "\n")
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
  type: ClusterIP
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

func renderSQLManifest(namespace, name, image string, port int, authSecretName string) string {
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
            - name: ACCEPT_EULA
              value: "Y"
            - name: MSSQL_PID
              value: "Developer"
            - name: MSSQL_MEMORY_LIMIT_MB
              value: "1024"
            - name: SA_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: {{.AuthSecretName}}
                  key: sa-password
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
  type: ClusterIP
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

func getAppStatus(workspace, app string) ([]byte, error) {
	kcfg := filepath.Join("/home/beko/kubeconfigs", workspace+".yaml")
	podsCmd := exec.Command("kubectl", "--kubeconfig", kcfg, "-n", workspace, "get", "pods", "-l", "app="+app, "-o", "jsonpath={range .items[*]}{.metadata.name}|{.status.phase}{\"\\n\"}{end}")
	podsOut, podsErr := podsCmd.CombinedOutput()
	if podsErr != nil {
		return nil, fmt.Errorf("get app pods failed: %v", podsErr)
	}
	svcCmd := exec.Command("kubectl", "--kubeconfig", kcfg, "-n", workspace, "get", "svc", app, "-o", "jsonpath={.spec.ports[0].nodePort}")
	svcOut, svcErr := svcCmd.CombinedOutput()
	if svcErr != nil {
		return nil, fmt.Errorf("get app service failed: %v", svcErr)
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

	nodePort := strings.TrimSpace(string(svcOut))
	out := map[string]any{
		"workspace": workspace,
		"app":       app,
		"nodePort":  nodePort,
		"pods":      pods,
	}
	return json.Marshal(out)
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
    "/app/status": {
      "get": {
        "summary": "App status",
        "parameters": [
          { "name": "workspace", "in": "query", "required": true, "schema": { "type": "string" } },
          { "name": "app", "in": "query", "required": true, "schema": { "type": "string" } }
        ],
        "responses": { "200": { "description": "Status" } }
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
              "type": { "type": "string", "enum": ["git","local","zip"] },
              "repo_url": { "type": "string" },
              "revision": { "type": "string" },
              "git_username": { "type": "string" },
              "git_token": { "type": "string" },
              "local_path": { "type": "string" },
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

	forwardMu.Lock()
	if fwd, ok := forwards[key]; ok && fwd != nil && fwd.Cmd != nil && fwd.Cmd.Process != nil {
		if fwd.Port == externalPort {
			forwardMu.Unlock()
			return nil
		}
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
	if in.Image.Tag == "" {
		in.Image.Tag = "latest"
	}
	if in.Source.NFS != nil && in.Source.NFS.Size == "" {
		in.Source.NFS.Size = "50Gi"
	}
	if in.Source.SMB != nil && in.Source.SMB.Size == "" {
		in.Source.SMB.Size = "50Gi"
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
		in.Dependency.SQL.Image = "lenovo:8443/library/mssql-server:2022-latest"
	}
	if in.Dependency.SQL.ServiceName == "" {
		in.Dependency.SQL.ServiceName = "sql"
	}
	if in.Dependency.SQL.Port == 0 {
		in.Dependency.SQL.Port = 1433
	}
	if in.Dependency.SQL.Username == "" {
		in.Dependency.SQL.Username = "sa"
	}
	if in.Dependency.SQL.ConnectionEnv == "" {
		in.Dependency.SQL.ConnectionEnv = "ConnectionStrings__DefaultConnection"
	}
	if in.Migration.EnvName == "" {
		in.Migration.EnvName = "ConnectionStrings__DefaultConnection"
	}
}

func validate(in *Input) error {
	if in.Source.Type != "git" && in.Source.Type != "local" && in.Source.Type != "zip" {
		return fmt.Errorf("source.type must be git, local, or zip")
	}
	if in.Source.Type == "git" {
		if in.Source.RepoURL == "" {
			return fmt.Errorf("source.repo_url is required for git")
		}
	}
	if in.Source.Type == "local" {
		if in.Source.LocalPath == "" {
			return fmt.Errorf("source.local_path is required for local")
		}
		if in.Source.PVCName == "" && in.Source.NFS == nil && in.Source.SMB == nil {
			return fmt.Errorf("source.pvc_name or source.nfs/source.smb is required for local")
		}
	}
	if in.Source.Type == "zip" {
		if in.Source.ZipURL == "" {
			return fmt.Errorf("source.zip_url is required for zip")
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
		if strings.TrimSpace(in.Migration.Image) == "" {
			return fmt.Errorf("migration.image is required when migration.enabled=true")
		}
		if len(in.Migration.Command) == 0 {
			return fmt.Errorf("migration.command is required when migration.enabled=true")
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
	ctx := RenderContext{
		Namespace:   in.Namespace,
		Task:        in.Task,
		SourceType:  in.Source.Type,
		RepoURL:     in.Source.RepoURL,
		Revision:    in.Source.Revision,
		LocalPath:   in.Source.LocalPath,
		Project:     target.Project,
		Tag:         target.Tag,
		Registry:    in.Image.Registry,
		ZipURL:      in.Source.ZipURL,
		ZipUsername: in.Source.ZipUsername,
		ZipPassword: in.Source.ZipPassword,
		GitSecret:   in.Source.GitSecret,
		PVCName:     in.Source.PVCName,
		HasGit:      in.Source.Type == "git" && in.Source.GitUsername != "" && in.Source.GitToken != "",
		HasLocal:    in.Source.Type == "local",
		HasZip:      in.Source.Type == "zip",
		AppName:     target.AppName,
		ContextSub:  target.ContextSubDir,
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
    - name: registry
      value: {{.Registry}}
    - name: tag
      value: {{.Tag}}
{{- if .ContextSub }}
    - name: context-sub-path
      value: {{.ContextSub}}
{{- end }}
{{- if eq .SourceType "local" }}
    - name: local-path
      value: {{.LocalPath}}
{{- end }}
  workspaces:
    - name: source
      emptyDir: {}
{{- if .HasGit }}
    - name: git-credentials
      secret:
        secretName: {{.GitSecret}}
{{- end }}
{{- if .HasLocal }}
    - name: local-source
      persistentVolumeClaim:
        claimName: {{.PVCName}}
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
