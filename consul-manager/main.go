package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// Config structures
type Service struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	VpsPort      int    `json:"vpsPort"`   // Port on VPS
	LocalPort    int    `json:"localPort"` // Local development port
	Enabled      bool   `json:"enabled"`   // Deprecated, use Mode
	Mode         string `json:"mode"`      // "off", "vps", "local"
	LocalPath    string `json:"localPath"`
	Cmd          string `json:"cmd"`          // Custom run command (e.g. "yarn start")
	SkipRegister bool   `json:"skipRegister"` // If true, tool won't try to register this service on Consul
	LocalRunning bool   `json:"localRunning"` // Read-only: true if process is alive
	VpsReached   bool   `json:"vpsReached"`   // Read-only: true if VPS port is open
	Rank         int    `json:"rank"`
}

type UISettings struct {
	Theme     string `json:"theme"`
	Language  string `json:"language"`
	ActiveTab string `json:"activeTab"`
	SetupPath string `json:"setupPath"`
	LogMode   string `json:"logMode"`
}

type Config struct {
	Services   []Service `json:"services"`
	ConsulPath string    `json:"consulPath"`
	ConsulArgs []string  `json:"consulArgs"`
	UISettings UISettings `json:"uiSettings"`
}

var (
	config       Config
	consulCmd    *exec.Cmd
	consulMutex  sync.Mutex
	logBuffer    []string
	logMutex     sync.Mutex
	wsClients    = make(map[*websocket.Conn]bool)
	wsClientsMux sync.Mutex
	upgrader     = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	
	runningProcs = make(map[string]*exec.Cmd)
	procMutex    sync.Mutex

	httpClient = &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     90 * time.Second,
			DisableKeepAlives:   false,
		},
		Timeout: 5 * time.Second, // Reduced from 10s for better responsiveness
	}
)

func main() {
	// Load config
	loadConfig()

	// Setup routes with gzip compression for API endpoints
	http.HandleFunc("/", serveStatic)
	http.HandleFunc("/api/services", gzipMiddleware(handleServices))
	http.HandleFunc("/api/services/mode", handleServiceMode)
	http.HandleFunc("/api/consul/status", gzipMiddleware(handleConsulStatus))
	http.HandleFunc("/api/consul/vps-status", gzipMiddleware(handleVPSConsulStatus))
	http.HandleFunc("/api/consul/services", gzipMiddleware(handleConsulServices))
	http.HandleFunc("/api/db/status", gzipMiddleware(handleDBStatus))
	http.HandleFunc("/api/services/reorder", handleReorderServices)
	http.HandleFunc("/api/consul/start", handleConsulStart)
	http.HandleFunc("/api/consul/stop", handleConsulStop)
	http.HandleFunc("/api/consul/restart", handleConsulRestart)
	http.HandleFunc("/api/config", gzipMiddleware(handleConfig))
	http.HandleFunc("/api/setup", handleSetup)
	http.HandleFunc("/api/logs/list", gzipMiddleware(handleListLogs))
	http.HandleFunc("/api/logs/view", gzipMiddleware(handleViewLog))
	http.HandleFunc("/api/status/all", gzipMiddleware(handleStatusAll)) // New: Batch endpoint
	http.HandleFunc("/api/settings", handleSettings)                     // New: Persistence
	http.HandleFunc("/api/helper/ports", gzipMiddleware(handleHelperPorts))
	http.HandleFunc("/api/helper/kill", handleHelperKill)
    http.HandleFunc("/api/helper/scan-active", handleHelperScanActive)
    http.HandleFunc("/api/consul/install", handleConsulInstall)
	http.HandleFunc("/ws/logs", handleWebSocket)

	// Try preferred port 9999 first
	preferredPort := 9999
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", preferredPort))
	if err != nil {
		fmt.Printf("⚠️ Port %d busy, choosing random port...\n", preferredPort)
		// Fallback to random port
		listener, err = net.Listen("tcp", ":0")
		if err != nil {
			log.Fatal(err)
		}
	}
	port := listener.Addr().(*net.TCPAddr).Port

	url := fmt.Sprintf("http://localhost:%d", port)
	fmt.Printf("\n🚀 Consul Manager running at: %s\n\n", url)

	// Auto open browser
	openBrowser(url)

	// Graceful Shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		fmt.Println("\n🔻 Shutting down Consul Manager...")

		// 1. Kill all running local services
		procMutex.Lock()
		for name, cmd := range runningProcs {
			if cmd.Process != nil {
				fmt.Printf("🛑 Killing service: %s (PID: %d)\n", name, cmd.Process.Pid)
				cmd.Process.Kill()
			}
		}
		procMutex.Unlock()

		// 2. Kill Consul Agent if we started it
		consulMutex.Lock()
		if consulCmd != nil && consulCmd.Process != nil {
			fmt.Printf("🛑 Killing Consul Agent (PID: %d)\n", consulCmd.Process.Pid)
			consulCmd.Process.Kill()
		}
		consulMutex.Unlock()

		fmt.Println("👋 Bye!")
		os.Exit(0)
	}()

	log.Fatal(http.Serve(listener, nil))
}

func loadConfig() {
	file, err := os.ReadFile("config.json")
	if err != nil {
		log.Printf("Warning: config.json not found, using defaults")
		config = Config{
			Services: []Service{
				{Name: "setting", Host: "34.1.133.14", VpsPort: 8080, LocalPort: 8080, Enabled: true},
				{Name: "sampling", Host: "34.1.133.14", VpsPort: 8080, LocalPort: 8082, Enabled: true},
				{Name: "onboarding", Host: "34.1.133.14", VpsPort: 8080, LocalPort: 8081, Enabled: true},
				{Name: "oldmain", Host: "34.1.133.14", VpsPort: 8080, LocalPort: 8080, Enabled: true},
			},
			ConsulPath: "consul",
			ConsulArgs: []string{"agent", "-dev", "-ui", "-client", "0.0.0.0", "-log-level=INFO"},
		}
		return
	}
	if err := json.Unmarshal(file, &config); err != nil {
		log.Printf("Warning: failed to parse config.json: %v", err)
		return
	}

	// Backward compatibility: Migrate Enabled -> Mode
	for i := range config.Services {
		if config.Services[i].Mode == "" {
			if config.Services[i].Enabled {
				config.Services[i].Mode = "vps"
			} else {
				config.Services[i].Mode = "off"
			}
		}
	}
}

func saveConfig() {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		log.Printf("Error marshaling config: %v", err)
		return
	}
	if err := os.WriteFile("config.json", data, 0644); err != nil {
		log.Printf("Error writing config.json: %v", err)
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	}
	if cmd != nil {
		cmd.Start()
	}
}

// Gzip compression middleware
type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (w gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

func gzipMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check if client accepts gzip
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next(w, r)
			return
		}

		// Skip gzip for WebSocket connections
		if r.Header.Get("Upgrade") == "websocket" {
			next(w, r)
			return
		}

		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()

		gzw := gzipResponseWriter{Writer: gz, ResponseWriter: w}
		next(gzw, r)
	}
}

func serveStatic(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" {
		path = "/index.html"
	}

	filePath := "static" + path

	// Get file info for ETag and caching
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Set cache headers (1 hour for HTML, 1 day for CSS/JS)
	ext := filepath.Ext(path)
	if ext == ".css" || ext == ".js" {
		w.Header().Set("Cache-Control", "public, max-age=86400") // 1 day
	} else if ext == ".html" {
		w.Header().Set("Cache-Control", "public, max-age=3600") // 1 hour
	}

	// Generate ETag from file modification time
	etag := fmt.Sprintf(`"%x-%x"`, fileInfo.ModTime().Unix(), fileInfo.Size())
	w.Header().Set("ETag", etag)

	// Check If-None-Match for cache validation
	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// Gzip compression for text files
	if ext == ".html" || ext == ".css" || ext == ".js" {
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Vary", "Accept-Encoding")

			gz := gzip.NewWriter(w)
			defer gz.Close()

			file, err := os.Open(filePath)
			if err != nil {
				http.Error(w, "File not found", 404)
				return
			}
			defer file.Close()

			// Set content type
			if ext == ".html" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
			} else if ext == ".css" {
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
			} else if ext == ".js" {
				w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			}

			io.Copy(gz, file)
			return
		}
	}

	http.ServeFile(w, r, filePath)
}

func isPortOpen(host string, port int) bool {
	address := fmt.Sprintf("%s:%d", host, port)

	// Use context with timeout for better cancellation
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func handleServices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Create a wait group for parallel port checking
	var wg sync.WaitGroup

	// First, quickly check local process status with minimal lock time
	procMutex.Lock()
	localStatus := make(map[string]bool, len(config.Services))
	for _, s := range config.Services {
		_, ok := runningProcs[s.Name]
		localStatus[s.Name] = ok
	}
	procMutex.Unlock()

	// Now update services without holding the lock
	for i := range config.Services {
		s := &config.Services[i]
		s.LocalRunning = localStatus[s.Name]

		// Check VPS port reachability (in parallel)
		if s.Mode == "vps" {
			wg.Add(1)
			go func(svc *Service) {
				defer wg.Done()
				svc.VpsReached = isPortOpen(svc.Host, svc.VpsPort)
			}(s)
		} else {
			s.VpsReached = false
		}
	}

	wg.Wait()
	json.NewEncoder(w).Encode(config.Services)
}

func handleServiceMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}

	var req struct {
		Name string `json:"name"`
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", 400)
		return
	}

	for i := range config.Services {
		if config.Services[i].Name == req.Name {
			s := &config.Services[i]

			// Check dependency: Consul must be running for 'vps' or 'local'
			if (req.Mode == "vps" || req.Mode == "local") && !isConsulRunning() {
				broadcastError("CONSUL_REQUIRED", "Consul must be running to start services")
				http.Error(w, "Consul must be running to start services", 400)
				return
			}

			// Cleanup previous state
			if s.Mode == "local" {
				stopLocalService(s.Name)
			}
			// Always deregister to be clean
			deregisterService(s.Name)

			s.Mode = req.Mode
			// Sync legacy field
			s.Enabled = (req.Mode == "vps" || req.Mode == "local")

			switch req.Mode {
			case "vps":
				registerService(*s)
			case "local":
				startLocalService(*s)
			case "off":
				// Already deregistered
			}
			break
		}
	}

	saveConfig()

	// Notify WebSocket clients of service update
	broadcastServiceUpdate()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// Broadcast service update notification via WebSocket
func broadcastServiceUpdate() {
	msg := `{"type":"service_update"}`
	wsClientsMux.Lock()
	defer wsClientsMux.Unlock()

	for client := range wsClients {
		if err := client.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
			delete(wsClients, client)
			client.Close()
		}
	}
}

func handleReorderServices(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}

	var req []struct {
		Name string `json:"name"`
		Rank int    `json:"rank"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	// Update ranks
	rankMap := make(map[string]int)
	for _, item := range req {
		rankMap[item.Name] = item.Rank
	}

	for i := range config.Services {
		if rank, ok := rankMap[config.Services[i].Name]; ok {
			config.Services[i].Rank = rank
		}
	}

	saveConfig()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		w.Header().Set("Content-Type", "application/json")
		
		// Add helper metadata for UI
		wd, _ := os.Getwd()
		
		// Robust detection of project root
		parent := wd
		if strings.HasPrefix(filepath.Base(parent), "consul-manager") {
			parent = filepath.Dir(parent)
		}
		if filepath.Base(parent) == "tools" {
			parent = filepath.Dir(parent)
		}
		
		response := struct {
			Config
			DefaultScanPath string `json:"defaultScanPath"`
		}{
			Config:          config,
			DefaultScanPath: parent,
		}
		
		json.NewEncoder(w).Encode(response)
		return
	}
	if r.Method == "POST" {
		if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
			http.Error(w, "Invalid request body", 400)
			return
		}
		saveConfig()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
		return
	}
	http.Error(w, "Method not allowed", 405)
}

func registerService(s Service) {
	host := s.Host
	port := s.VpsPort
	if s.Mode == "local" {
		host = "127.0.0.1"
		port = s.LocalPort
	}

	payload := fmt.Sprintf(`{"ID": "%s", "Name": "%s", "Address": "%s", "Port": %d}`,
		s.Name, s.Name, host, port)
	
	req, _ := http.NewRequest("PUT", "http://localhost:8500/v1/agent/service/register",
		bytes.NewBufferString(payload))
    req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
    if err != nil {
        broadcastLog(fmt.Sprintf("❌ Register failed: %v", err))
        return
    }
    defer resp.Body.Close()
	
	broadcastLog(fmt.Sprintf("✅ Registered service: %s", s.Name))
}

func deregisterService(name string) {
	url := fmt.Sprintf("http://localhost:8500/v1/agent/service/deregister/%s", name)
	req, _ := http.NewRequest("PUT", url, nil)
	resp, err := httpClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
	}
	broadcastLog(fmt.Sprintf("❌ Deregistered service: %s", name))
}

// Helper function to re-register enabled services after Consul restart
func reregisterEnabledServices() {
	time.Sleep(3 * time.Second) // Wait for Consul to fully start
	broadcastLog("🔄 Re-registering enabled services...")
	for _, s := range config.Services {
		if s.Enabled {
			registerService(s)
		}
	}
}

// ===== Local Process Management =====

func isMatch(configName, consulSvcName string) bool {
	if configName == consulSvcName {
		return true
	}
	// Case for main-simp-backend -> main-*
	if configName == "main-simp-backend" && (consulSvcName == "main" || strings.HasPrefix(consulSvcName, "main-")) {
		return true
	}
	// General prefix matching (e.g. bioblock-abc matches bioblock)
	if strings.HasPrefix(consulSvcName, configName+"-") {
		return true
	}
	return false
}

func isServiceRegistered(name string) bool {
	resp, err := httpClient.Get("http://localhost:8500/v1/agent/services")
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	var services map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&services); err != nil {
		return false
	}

	for _, s := range services {
		if svc, ok := s.(map[string]interface{}); ok {
			consulSvcName := fmt.Sprintf("%v", svc["Service"])
			if isMatch(name, consulSvcName) {
				return true
			}
		}
	}
	return false
}

func startLocalService(s Service) {
	if s.LocalPath == "" {
		broadcastLog(fmt.Sprintf("❌ Service %s has no LocalPath configured", s.Name))
		return
	}

    // Strict Port Management: Kill ANY process on the target port before starting
    if s.LocalPort > 0 {
        pid := getPIDForPort(s.LocalPort)
        if pid != "" {
            broadcastLog(fmt.Sprintf("🔪 Port %d in use by PID %s. Killing before start...", s.LocalPort, pid))
            exec.Command("kill", "-9", pid).Run()
            time.Sleep(500 * time.Millisecond) // Give it a moment to die
        }
    }

	procMutex.Lock()
	if _, ok := runningProcs[s.Name]; ok {
		procMutex.Unlock()
		return
	}
	procMutex.Unlock()

	cmdStr := s.Cmd
	if cmdStr == "" {
		cmdStr = "./gradlew bootRun"
	}

	broadcastLog(fmt.Sprintf("⏳ Starting local service: %s (%s)", s.Name, cmdStr))
	
	cmd := exec.Command("sh", "-c", cmdStr)
	cmd.Dir = s.LocalPath

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		broadcastLog(fmt.Sprintf("❌ Failed to start %s: %v", s.Name, err))
		return
	}

	procMutex.Lock()
	runningProcs[s.Name] = cmd
	procMutex.Unlock()

	broadcastLog(fmt.Sprintf("🚀 START %s (PID: %d)", s.Name, cmd.Process.Pid))

	go streamServiceLogs(s.Name, stdout)
	go streamServiceLogs(s.Name, stderr)

	go func() {
		err := cmd.Wait()
		procMutex.Lock()
		delete(runningProcs, s.Name)
		procMutex.Unlock()
		
		if err != nil {
			broadcastLog(fmt.Sprintf("❌ ERROR: %s exited unexpectedly: %v", s.Name, err))
		} else {
			broadcastLog(fmt.Sprintf("🛑 STOP %s", s.Name))
		}
	}()
	
    // Wait for Consul registration
    go func() {
	    broadcastLog(fmt.Sprintf("🔍 Waiting for %s to register with Consul...", s.Name))
        
        healthy := false
        for i := 0; i < 60; i++ { // Try for 60 seconds
            if isServiceRegistered(s.Name) {
                healthy = true
                break
            }
            time.Sleep(1 * time.Second)
            
            // Check if process is still alive
            procMutex.Lock()
            _, isRunning := runningProcs[s.Name]
            procMutex.Unlock()
            if !isRunning {
                break
            }
        }

		if !healthy {
			broadcastLog(fmt.Sprintf("❌ %s failed to start within 60s. Check logs.", s.Name))
			return
		}

		if s.SkipRegister {
			broadcastLog(fmt.Sprintf("ℹ️ %s is ready! (Self-registration expected)", s.Name))
			return
		}

        // Final registration
        broadcastLog(fmt.Sprintf("🛡️ %s is ready! Registering with Consul...", s.Name))
        for _, curr := range config.Services {
            if curr.Name == s.Name && curr.Mode == "local" {
                registerService(curr)
                break
            }
        }
    }()
}

func stopLocalService(name string) {
	procMutex.Lock()
	cmd, ok := runningProcs[name]
	procMutex.Unlock()
	
    if ok && cmd.Process != nil {
        broadcastLog(fmt.Sprintf("🛑 Stopping local service: %s...", name))
		cmd.Process.Kill()
	}

    // Strict cleanup: Check port and kill if still running (or if not tracked)
    var servicePort int
    for _, s := range config.Services {
        if s.Name == name {
            servicePort = s.LocalPort
            break
        }
    }

    if servicePort > 0 {
        pid := getPIDForPort(servicePort)
        if pid != "" {
             broadcastLog(fmt.Sprintf("🧹 Cleaning up port %d (PID %s) for service %s...", servicePort, pid, name))
             exec.Command("kill", "-9", pid).Run()
        }
    }
}

func streamServiceLogs(name string, reader io.Reader) {
	// Ensure logs directory exists
	os.MkdirAll("logs", 0755)
	
	// Use O_TRUNC to overwrite file on restart
	logFile, err := os.OpenFile(fmt.Sprintf("logs/%s.log", name), os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		broadcastLog(fmt.Sprintf("❌ Failed to open log file for %s: %v", name, err))
	} else {
		defer logFile.Close()
	}

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		text := scanner.Text()
		msg := fmt.Sprintf("[%s] %s", name, text)
		broadcastLog(msg)
		
		if logFile != nil {
			timestamp := time.Now().Format("2006-01-02 15:04:05")
			logFile.WriteString(fmt.Sprintf("%s | %s\n", timestamp, text))
		}
	}
}

// broadcastLog sends a message to all connected WebSocket clients and saves to file
func broadcastLog(message string) {
	msg := message
	fmt.Println(msg)
	
	// Ensure logs directory exists
	os.MkdirAll("logs", 0755)
	
	// Save to general manager log
	f, err := os.OpenFile("logs/manager.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		timestamp := time.Now().Format("2006-01-02 15:04:05")
		f.WriteString(fmt.Sprintf("%s | %s\n", timestamp, msg))
		f.Close()
	}

	// Update in-memory buffer
	logMutex.Lock()
	logBuffer = append(logBuffer, msg)
	if len(logBuffer) > 1000 {
		logBuffer = logBuffer[len(logBuffer)-1000:]
	}
	logMutex.Unlock()

	// Send to all WS clients and remove failed ones
	wsClientsMux.Lock()
	defer wsClientsMux.Unlock()

	for client := range wsClients {
		if err := client.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
			// Remove failed connection
			delete(wsClients, client)
			client.Close()
		}
	}
}

// broadcastError sends a specific JSON error message to clients
func broadcastError(code, message string) {
	msg := fmt.Sprintf(`{"type":"error","code":"%s","message":"%s"}`, code, message)
	
	// Also log to console/file
	fmt.Printf("❌ ERROR [%s]: %s\n", code, message)
	broadcastLog(fmt.Sprintf("❌ %s", message))

	wsClientsMux.Lock()
	defer wsClientsMux.Unlock()

	for client := range wsClients {
		client.WriteMessage(websocket.TextMessage, []byte(msg))
	}
}


func handleConsulStatus(w http.ResponseWriter, r *http.Request) {
	consulMutex.Lock()
	running := consulCmd != nil && consulCmd.Process != nil
	consulMutex.Unlock()

	// Also check if Consul API is responding
	resp, err := http.Get("http://localhost:8500/v1/status/leader")
	apiRunning := err == nil && resp.StatusCode == 200

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{
		"running":    running || apiRunning,
		"apiRunning": apiRunning,
	})
}

func handleVPSConsulStatus(w http.ResponseWriter, r *http.Request) {
	// Check VPS Consul reusing global httpClient
	targetURL := "http://34.1.133.14:8500/v1/status/leader"
	resp, err := httpClient.Get(targetURL)
	
    running := false
    if err == nil {
        // Drain body to allow connection reuse
        io.Copy(io.Discard, resp.Body)
        resp.Body.Close()
        running = (resp.StatusCode == 200)
    }
	
	if !running {
		if err != nil {
			fmt.Printf("⚠️ VPS Check Failed: %v\n", err)
		} else if resp != nil {
			fmt.Printf("⚠️ VPS Check Status: %d\n", resp.StatusCode)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{
		"running": running,
	})
}

// Check VPS Database Status
func handleDBStatus(w http.ResponseWriter, r *http.Request) {
	timeout := 5 * time.Second
	params := r.URL.Query()
	host := params.Get("host")
	if host == "" {
		host = "34.1.133.14:3306"
	}

	conn, err := net.DialTimeout("tcp", host, timeout)
	running := err == nil
	if conn != nil {
		conn.Close()
	}

	if !running {
		// Log detailed error for debugging
		fmt.Printf("⚠️ DB Check Failed (%s): %v\n", host, err)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{
		"running": running,
	})
}

// Batch endpoint: Get all status in one call
func handleStatusAll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Use WaitGroup to fetch all in parallel
	var wg sync.WaitGroup
	result := make(map[string]interface{}, 4) // Pre-allocate with expected size
	var mu sync.Mutex

	// 1. Consul Local Status
	wg.Add(1)
	go func() {
		defer wg.Done()
		consulMutex.Lock()
		running := consulCmd != nil && consulCmd.Process != nil
		consulMutex.Unlock()

		resp, err := http.Get("http://localhost:8500/v1/status/leader")
		apiRunning := err == nil && resp != nil && resp.StatusCode == 200
		if resp != nil {
			resp.Body.Close()
		}

		mu.Lock()
		result["consulLocal"] = map[string]bool{
			"running":    running || apiRunning,
			"apiRunning": apiRunning,
		}
		mu.Unlock()
	}()

	// 2. Consul VPS Status
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := httpClient.Get("http://34.1.133.14:8500/v1/status/leader")
		running := false
		if err == nil && resp != nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			running = (resp.StatusCode == 200)
		}

		mu.Lock()
		result["consulVPS"] = map[string]bool{"running": running}
		mu.Unlock()
	}()

	// 3. DB Status
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, err := net.DialTimeout("tcp", "34.1.133.14:3306", 5*time.Second)
		running := err == nil
		if conn != nil {
			conn.Close()
		}

		mu.Lock()
		result["db"] = map[string]bool{"running": running}
		mu.Unlock()
	}()

	// 4. Registered Services
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := httpClient.Get("http://localhost:8500/v1/agent/services")
		if err != nil {
			mu.Lock()
			result["registeredServices"] = map[string]interface{}{}
			mu.Unlock()
			return
		}
		defer resp.Body.Close()

		var services map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&services)

		mu.Lock()
		result["registeredServices"] = services
		mu.Unlock()
	}()

	wg.Wait()
	json.NewEncoder(w).Encode(result)
}

// Get Registered Services from Consul Agent
func handleConsulServices(w http.ResponseWriter, r *http.Request) {
	resp, err := httpClient.Get("http://localhost:8500/v1/agent/services")
	if err != nil {
		// Return empty map if Consul is down
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{}"))
		return
	}
	defer resp.Body.Close()
	
	// Just proxy the response
	w.Header().Set("Content-Type", "application/json")
	io.Copy(w, resp.Body)
}

func handleConsulStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	startConsul()

	// Wait for Consul to be ready, then re-register enabled services
	go reregisterEnabledServices()
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func handleConsulStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	stopConsul()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func handleConsulRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	stopConsul()
	startConsul()

	// Wait for Consul to be ready, then re-register enabled services
	go reregisterEnabledServices()
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func startConsul() {
	consulMutex.Lock()
	defer consulMutex.Unlock()

	if consulCmd != nil && consulCmd.Process != nil {
		return
	}

	// Check if Consul executable exists and is valid
	if err := validateConsulBinary(config.ConsulPath); err != nil {
		broadcastError("CONSUL_BINARY_ERROR", fmt.Sprintf("Invalid Consul binary: %v", err))
		return
	}

	consulCmd = exec.Command(config.ConsulPath, config.ConsulArgs...)
	stdout, _ := consulCmd.StdoutPipe()
	stderr, _ := consulCmd.StderrPipe()

	consulCmd.Start()
	broadcastLog("🚀 Starting Consul agent...")

	go streamOutput(stdout)
	go streamOutput(stderr)
}

func stopConsul() {
	consulMutex.Lock()
	defer consulMutex.Unlock()

	if consulCmd != nil && consulCmd.Process != nil {
		consulCmd.Process.Kill()
		consulCmd.Wait()
		consulCmd = nil
		broadcastLog("🛑 Consul agent stopped")
	}
}

func streamOutput(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		broadcastLog(line)
	}
}



func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	wsClientsMux.Lock()
	wsClients[conn] = true
	wsClientsMux.Unlock()

	// Send buffered logs
	logMutex.Lock()
	for _, msg := range logBuffer {
		conn.WriteMessage(websocket.TextMessage, []byte(msg))
	}
	logMutex.Unlock()

	// Keep connection alive
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			wsClientsMux.Lock()
			delete(wsClients, conn)
			wsClientsMux.Unlock()
			break
		}
	}
}

// detectLocalPort scans for Spring Boot application-dev.yml and extracts server.port
func detectLocalPort(servicePath string) int {
	// Try common YAML config locations
	configPaths := []string{
		filepath.Join(servicePath, "src", "main", "resources", "config", "application-dev.yml"),
		filepath.Join(servicePath, "src", "main", "resources", "application-dev.yml"),
		filepath.Join(servicePath, "src", "main", "resources", "config", "application.yml"),
		filepath.Join(servicePath, "src", "main", "resources", "application.yml"),
	}

	for _, cfgPath := range configPaths {
		content, err := os.ReadFile(cfgPath)
		if err != nil {
			continue
		}

		// Simple regex to find server.port (handles "server:\n  port: XXXX" pattern)
		// Look for "server:" section then find "port:" value
		lines := strings.Split(string(content), "\n")
		inServerSection := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			
			// Check for server: section start
			if trimmed == "server:" {
				inServerSection = true
				continue
			}
			
			// If we hit another top-level key (no leading space), exit server section
			if inServerSection && len(line) > 0 && line[0] != ' ' && line[0] != '\t' && !strings.HasPrefix(line, "#") {
				inServerSection = false
			}
			
			// Look for port: within server section
			if inServerSection && strings.HasPrefix(trimmed, "port:") {
				portStr := strings.TrimSpace(strings.TrimPrefix(trimmed, "port:"))
				if port, err := strconv.Atoi(portStr); err == nil && port > 0 {
					return port
				}
			}
		}
	}
	return 0
}

func handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}

	var req struct {
		Path       string `json:"path"`
		ConsulPath string `json:"consulPath"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Fallback to default behavior if no body or invalid json
		exePath, _ := os.Getwd()
		req.Path = filepath.Dir(filepath.Dir(exePath))
	}

	// Update Consul Path if provided
	if req.ConsulPath != "" {
		config.ConsulPath = req.ConsulPath
		saveConfig() // Save early
		broadcastLog(fmt.Sprintf("⚙️ Updated Consul Path: %s", req.ConsulPath))
	}

	rootPath := req.Path
	if rootPath == "" {
		// Just saving settings?
		if req.ConsulPath != "" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"updated": 0,
			})
			return
		}
		// Fallback for scanning
		exePath, _ := os.Getwd()
		rootPath = filepath.Dir(filepath.Dir(exePath))
	}

	broadcastLog(fmt.Sprintf("🔍 Auto Setup: Scanning for services in %s", rootPath))

	updatedCount := 0
	procMutex.Lock()
	
	// 1. Check for main-simplified specifically
	mainSimpPath := filepath.Join(rootPath, "main-simplified")
	if info, err := os.Stat(mainSimpPath); err == nil && info.IsDir() {
		absMainPath, _ := filepath.Abs(mainSimpPath)
		broadcastLog(fmt.Sprintf("📦 Special Case: main-simplified folder detected at %s", absMainPath))
		
		// Auto-detect consul binary if it exists in main-simplified
		potentialConsul := filepath.Join(absMainPath, "consul")
		if info, err := os.Stat(potentialConsul); err == nil && !info.IsDir() {
			config.ConsulPath = potentialConsul
			broadcastLog(fmt.Sprintf("⚙️ Auto-detected Consul path: %s", potentialConsul))
		}
		
		foundFB := false
		foundBE := false
		
		for i := range config.Services {
			if config.Services[i].Name == "main-simp-frontend" {
				config.Services[i].LocalPath = absMainPath
				config.Services[i].Cmd = "npm start"
				config.Services[i].LocalPort = 9000
				foundFB = true
			}
			if config.Services[i].Name == "main-simp-backend" {
				config.Services[i].LocalPath = absMainPath
				config.Services[i].Cmd = "./gradlew bootRun"
				if detPort := detectLocalPort(absMainPath); detPort > 0 {
					config.Services[i].LocalPort = detPort
				}
				foundBE = true
			}
		}
		
		if !foundFB {
			config.Services = append(config.Services, Service{
				Name: "main-simp-frontend",
				Host: "127.0.0.1",
				VpsPort: 8080,
				LocalPort: 9000,
				Mode: "off",
				LocalPath: absMainPath,
				Cmd: "npm start",
				Rank: 20,
			})
			updatedCount++
		}
		if !foundBE {
			detPort := detectLocalPort(absMainPath)
			if detPort == 0 {
				detPort = 8080 // Fallback
			}
			config.Services = append(config.Services, Service{
				Name: "main-simp-backend",
				Host: "127.0.0.1",
				VpsPort: 8080,
				LocalPort: detPort,
				Mode: "off",
				LocalPath: absMainPath,
				Cmd: "./gradlew bootRun",
				Rank: 10,
			})
			updatedCount++
		}
	}

	// 2. Generic scan for other services
	knownServices := make(map[string]bool)
	for i := range config.Services {
		s := &config.Services[i]
		knownServices[s.Name] = true
		
		// Fix zeroed vpsPort for existing services
		if s.VpsPort == 0 {
			s.VpsPort = 8080
		}

		// Skip main-simp if already handled
		if s.Name == "main-simp-frontend" || s.Name == "main-simp-backend" {
			continue
		}

		// Check if folder exists in project root
		potentialPath := filepath.Join(rootPath, s.Name)
		if info, err := os.Stat(potentialPath); err == nil && info.IsDir() {
			absPath, _ := filepath.Abs(potentialPath)
			s.LocalPath = absPath
			s.SkipRegister = true // Default for Java/Spring services
			
			// Try to detect local port from Spring Boot config
			if detectedPort := detectLocalPort(absPath); detectedPort > 0 {
				s.LocalPort = detectedPort
				broadcastLog(fmt.Sprintf("✅ Updated: %s -> %s (port: %d)", s.Name, absPath, detectedPort))
			} else {
				broadcastLog(fmt.Sprintf("✅ Updated: %s -> %s", s.Name, absPath))
			}
			updatedCount++
		}
	}

	procMutex.Unlock()

	if updatedCount > 0 {
		saveConfig()
		broadcastLog(fmt.Sprintf("✨ Setup complete! Updated %d service settings.", updatedCount))
	} else {
		broadcastLog("✨ Setup check complete. No new directory changes detected.")
	}

    // Check for port conflicts (only for services with localPath)
    var conflicts []PortStatus
    for _, s := range config.Services {
        if s.LocalPath == "" || s.LocalPort == 0 {
            continue
        }
        p := s.LocalPort
        if p > 0 {
            pid := getPIDForPort(p)
            if pid != "" {
                 // Get process name
                cmd := exec.Command("ps", "-p", pid, "-o", "comm=")
                out, _ := cmd.Output()
                processName := strings.TrimSpace(string(out))
                
                conflicts = append(conflicts, PortStatus{
                    Service: s.Name,
                    Port:    p,
                    InUse:   true,
                    PID:     pid,
                    Process: processName,
                })
            }
        }
    }

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":       true,
		"updated":       updatedCount,
		"portConflicts": conflicts,
	})
}

// Handle list logs in logs/ directory
func handleListLogs(w http.ResponseWriter, r *http.Request) {
	files, err := os.ReadDir("logs")
	if err != nil {
		http.Error(w, "Failed to read logs directory", 500)
		return
	}

	var filenames []string
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".log") {
			filenames = append(filenames, f.Name())
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(filenames)
}

// Handle view log content
func handleViewLog(w http.ResponseWriter, r *http.Request) {
	filename := r.URL.Query().Get("file")
	if filename == "" {
		http.Error(w, "File parameter is required", 400)
		return
	}

	// Basic security check: prevent directory traversal
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") || strings.Contains(filename, "\\") {
		http.Error(w, "Invalid filename", 400)
		return
	}

	content, err := os.ReadFile(filepath.Join("logs", filename))
	if err != nil {
		http.Error(w, "Failed to read log file", 404)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write(content)
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(config.UISettings)
		return
	}

	if r.Method == "POST" {
		var newSettings UISettings
		if err := json.NewDecoder(r.Body).Decode(&newSettings); err != nil {
			http.Error(w, "Invalid request body", 400)
			return
		}
		
		config.UISettings = newSettings
		saveConfig()
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
		return
	}

	http.Error(w, "Method not allowed", 405)
}

// ===== Helper API =====

type PortStatus struct {
	Service string `json:"service"`
	Port    int    `json:"port"`
	InUse   bool   `json:"inUse"`
	PID     string `json:"pid"`
	Process string `json:"process"`
}

func handleHelperPorts(w http.ResponseWriter, r *http.Request) {
	var results []PortStatus
    
	// 1. Identify unique LOCAL ports to scan (only services with localPath configured)
	uniquePorts := make(map[int]bool)
	for _, s := range config.Services {
		// Only include services configured for local development (has localPath)
		if s.LocalPath == "" || s.LocalPort == 0 {
			continue // Skip - not configured for local
		}
		
		uniquePorts[s.LocalPort] = true
	}

	// 2. Scan ports (PID lookup)
	portPIDs := make(map[int]string)
	portProcs := make(map[int]string)
	
	for port := range uniquePorts {
		pid := getPIDForPort(port)
		if pid != "" {
			portPIDs[port] = pid
			// Get process name
			cmd := exec.Command("ps", "-p", pid, "-o", "comm=")
			out, _ := cmd.Output()
			portProcs[port] = strings.TrimSpace(string(out))
		}
	}

	// 3. Build result for services with localPath
	for _, s := range config.Services {
		// Only include services configured for local development
		if s.LocalPath == "" || s.LocalPort == 0 {
			continue
		}
		
		status := PortStatus{
			Service: s.Name,
			Port:    s.LocalPort,
			InUse:   false,
		}

		if pid, ok := portPIDs[s.LocalPort]; ok {
			status.InUse = true
			status.PID = pid
			status.Process = portProcs[s.LocalPort]
		}
		
		results = append(results, status)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func isConsulRunning() bool {
    // 1. Check process
    consulMutex.Lock()
    running := consulCmd != nil && consulCmd.Process != nil
    consulMutex.Unlock()

    if running {
        return true
    }

    // 2. Check API (fallback if running externally)
    resp, err := http.Get("http://localhost:8500/v1/status/leader")
    if err == nil && resp != nil && resp.StatusCode == 200 {
        resp.Body.Close()
        return true
    }
    return false
}

func handleConsulInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}

	broadcastLog("🛠️ Starting Consul Auto-Fix...")
	
	// 1. Detect OS/Arch
	osType := runtime.GOOS
	arch := runtime.GOARCH
	
	if osType != "darwin" {
		broadcastError("INSTALL_ERROR", "Auto-fix currently only supports macOS (darwin)")
		return
	}

	version := "1.15.4" // Stable version
	url := ""
	if arch == "arm64" {
		url = fmt.Sprintf("https://releases.hashicorp.com/consul/%s/consul_%s_darwin_arm64.zip", version, version)
	} else {
		url = fmt.Sprintf("https://releases.hashicorp.com/consul/%s/consul_%s_darwin_amd64.zip", version, version)
	}

	broadcastLog(fmt.Sprintf("⬇️ Downloading Consul %s for %s/%s...", version, osType, arch))
	broadcastLog(fmt.Sprintf("🔗 URL: %s", url))

	// 2. Download
	resp, err := http.Get(url)
	if err != nil {
		broadcastError("DOWNLOAD_ERROR", fmt.Sprintf("Download failed: %v", err))
		return
	}
	defer resp.Body.Close()

	// Create temp zip file
	tmpZip, err := os.CreateTemp("", "consul.zip")
	if err != nil {
		broadcastError("FS_ERROR", fmt.Sprintf("Failed to create temp file: %v", err))
		return
	}
	defer os.Remove(tmpZip.Name())

	_, err = io.Copy(tmpZip, resp.Body)
	if err != nil {
		broadcastError("DOWNLOAD_ERROR", fmt.Sprintf("Failed to write zip: %v", err))
		return
	}
	tmpZip.Close()

	// 3. Unzip and Install
	broadcastLog("📦 Extracting...")
	
	// Determine install path (current directory or configured path)
	installDir := filepath.Dir(config.ConsulPath)
	if installDir == "." || installDir == "" {
		wd, _ := os.Getwd()
		installDir = wd
	}
	
	targetPath := filepath.Join(installDir, "consul")
	
	if err := unzipAndInstall(tmpZip.Name(), installDir); err != nil {
		broadcastError("INSTALL_ERROR", fmt.Sprintf("Extraction failed: %v", err))
		return
	}

	// 4. Update Config
	config.ConsulPath = targetPath
	saveConfig()
	
	broadcastLog(fmt.Sprintf("✅ Consul installed successfully to: %s", targetPath))
	broadcastLog("🚀 You can now start Consul!")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func unzipAndInstall(src, dest string) error {
    r, err := zip.OpenReader(src)
    if err != nil {
        return err
    }
    defer r.Close()

    for _, f := range r.File {
        if f.Name != "consul" {
            continue
        }

        fpath := filepath.Join(dest, f.Name)
        
        // Check for ZipSlip (Directory traversal)
        if !strings.HasPrefix(fpath, filepath.Clean(dest)+string(os.PathSeparator)) {
            return fmt.Errorf("illegal file path: %s", fpath)
        }

        outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
        if err != nil {
            return err
        }

        rc, err := f.Open()
        if err != nil {
            outFile.Close()
            return err
        }

        _, err = io.Copy(outFile, rc)

        outFile.Close()
        rc.Close()
        
        // Ensure executable permissions
        os.Chmod(fpath, 0755)
        return nil
    }
    return fmt.Errorf("consul binary not found in zip")
}

func validateConsulBinary(path string) error {
    // 1. Check if file exists
    info, err := os.Stat(path)
    if err != nil {
        // Try looking in PATH
        resolved, err := exec.LookPath("consul")
        if err == nil {
            config.ConsulPath = resolved // Auto-fix path
            return nil
        }
        return fmt.Errorf("Binary not found at %s and not in PATH", path)
    }
    
    // 2. Check if executable
    if info.Mode()&0111 == 0 {
        return fmt.Errorf("File %s is not executable", path)
    }

    // 3. Check version/execution (catches OS mismatch)
    cmd := exec.Command(path, "-version")
    out, err := cmd.CombinedOutput()
    if err != nil {
        return fmt.Errorf("Failed to execute binary: %v. Output: %s", err, string(out))
    }
    
    // Optional: Log version
    lines := strings.Split(string(out), "\n")
    if len(lines) > 0 {
         broadcastLog(fmt.Sprintf("✓ Verified Consul Binary: %s", lines[0]))
    }
    
    return nil
}

func getPIDForPort(port int) string {
    // try lsof first (Mac/Linux)
    cmd := exec.Command("lsof", "-t", fmt.Sprintf("-i:%d", port))
    out, err := cmd.Output()
    if err == nil && len(out) > 0 {
        return strings.TrimSpace(string(out))
    }
    return ""
}

func handleHelperKill(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        http.Error(w, "Method not allowed", 405)
        return
    }

    var req struct {
        PID  string `json:"pid"`
        Port int    `json:"port"`
    }
    json.NewDecoder(r.Body).Decode(&req)

    targetPID := req.PID
    if targetPID == "" && req.Port > 0 {
        targetPID = getPIDForPort(req.Port)
    }

    if targetPID == "" {
        http.Error(w, "Process not found", 404)
        return
    }

    // Kill it
    // Force kill with -9 to ensure it dies
    if err := exec.Command("kill", "-9", targetPID).Run(); err != nil {
         broadcastError("KILL_FAIL", fmt.Sprintf("Failed to kill PID %s: %v", targetPID, err))
         http.Error(w, "Failed to kill process", 500)
         return
    }

    broadcastLog(fmt.Sprintf("🔪 Killed process %s (Port %d)", targetPID, req.Port))
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

type ScanResult struct {
    Port    int    `json:"port"`
    PID     string `json:"pid"`
    Process string `json:"process"`
    InUse   bool   `json:"inUse"`
}

func handleHelperScanActive(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        http.Error(w, "Method not allowed", 405)
        return
    }

    var req struct {
        Ports []int `json:"ports"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "Invalid request body", 400)
        return
    }

    var results []ScanResult
    for _, port := range req.Ports {
        res := ScanResult{Port: port, InUse: false}
        
        // Find PID and Process Name
        cmd := exec.Command("lsof", fmt.Sprintf("-i:%d", port))
        out, err := cmd.Output()
        
        if err == nil && len(out) > 0 {
            lines := strings.Split(strings.TrimSpace(string(out)), "\n")
            if len(lines) > 1 {
                // Header is first line, take second line
                // COMMAND PID USER ...
                fields := strings.Fields(lines[1])
                if len(fields) >= 2 {
                    res.InUse = true
                    res.Process = fields[0]
                    res.PID = fields[1]
                }
            }
        }
        results = append(results, res)
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(results)
}
