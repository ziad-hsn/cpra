package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math/rand"
	"net/http"
	"path/filepath"
	"time"
)

// Server represents the web dashboard server
type Server struct {
	templates *template.Template
	port      int
}

// NewServer creates a new dashboard server
func NewServer(port int) (*Server, error) {
	// Parse all templates
	templatesPath := filepath.Join("internal", "web", "templates")
	tmpl, err := template.ParseGlob(filepath.Join(templatesPath, "layouts", "*.html"))
	if err != nil {
		return nil, fmt.Errorf("failed to parse layout templates: %w", err)
	}
	
	tmpl, err = tmpl.ParseGlob(filepath.Join(templatesPath, "pages", "*.html"))
	if err != nil {
		return nil, fmt.Errorf("failed to parse page templates: %w", err)
	}
	
	return &Server{
		templates: tmpl,
		port:      port,
	}, nil
}

// Start starts the HTTP server
func (s *Server) Start() error {
	// Serve static files
	fs := http.FileServer(http.Dir("internal/web/static"))
	http.Handle("/static/", http.StripPrefix("/static/", fs))
	
	// Routes
	http.HandleFunc("/", s.handleDashboard)
	http.HandleFunc("/monitors", s.handleMonitors)
	http.HandleFunc("/alerts", s.handleAlerts)
	http.HandleFunc("/metrics", s.handleMetrics)
	http.HandleFunc("/api/events/dashboard", s.handleSSE)
	
	addr := fmt.Sprintf(":%d", s.port)
	log.Printf("🚀 CPRA Dashboard Preview Server starting on http://localhost%s", addr)
	log.Printf("📊 Dashboard: http://localhost%s/", addr)
	log.Printf("🔍 Monitors: http://localhost%s/monitors", addr)
	log.Printf("🚨 Alerts: http://localhost%s/alerts", addr)
	log.Printf("📈 Metrics: http://localhost%s/metrics", addr)
	
	return http.ListenAndServe(addr, nil)
}

// DashboardData holds data for the dashboard template
type DashboardData struct {
	CurrentPage      string
	SystemStatus     string
	SystemStatusText string
	UnreadAlerts     int
	TotalMonitors    int
	HealthyCount     int
	WarningCount     int
	ErrorCount       int
	AvgResponseTime  int
	HealthyTrend     int
	Monitors         []Monitor
	RecentAlerts     []Alert
}

// Monitor represents a monitor card
type Monitor struct {
	ID             string
	Name           string
	Type           string
	Status         string
	Endpoint       string
	Uptime         float64
	ResponseTime   int
	LastCheck      string
	LastCheckISO   string
	SparklinePoints string
	SparklineColor string
	ErrorMessage   string
}

// Alert represents an alert item
type Alert struct {
	ID           string
	Severity     string
	Title        string
	Message      string
	Timestamp    string
	TimestampISO string
	Acknowledged bool
}

// handleDashboard renders the main dashboard
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	data := DashboardData{
		CurrentPage:      "dashboard",
		SystemStatus:     "healthy",
		SystemStatusText: "All Systems Operational",
		UnreadAlerts:     3,
		TotalMonitors:    12,
		HealthyCount:     8,
		WarningCount:     3,
		ErrorCount:       1,
		AvgResponseTime:  245,
		HealthyTrend:     5,
		Monitors:         generateMockMonitors(),
		RecentAlerts:     generateMockAlerts(),
	}
	
	if err := s.templates.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		log.Printf("Error rendering dashboard: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// handleMonitors renders the monitors page
func (s *Server) handleMonitors(w http.ResponseWriter, r *http.Request) {
	data := DashboardData{
		CurrentPage:      "monitors",
		SystemStatus:     "healthy",
		SystemStatusText: "All Systems Operational",
		UnreadAlerts:     3,
		TotalMonitors:    12,
		Monitors:         generateMockMonitors(),
	}
	
	if err := s.templates.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		log.Printf("Error rendering monitors: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// handleAlerts renders the alerts page
func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	data := DashboardData{
		CurrentPage:      "alerts",
		SystemStatus:     "healthy",
		SystemStatusText: "All Systems Operational",
		UnreadAlerts:     3,
		RecentAlerts:     generateMockAlerts(),
	}
	
	if err := s.templates.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		log.Printf("Error rendering alerts: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// handleMetrics renders the metrics page
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	data := DashboardData{
		CurrentPage:      "metrics",
		SystemStatus:     "healthy",
		SystemStatusText: "All Systems Operational",
		UnreadAlerts:     3,
	}
	
	if err := s.templates.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		log.Printf("Error rendering metrics: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// handleSSE handles Server-Sent Events for real-time updates
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}
	
	// Send initial connection message
	fmt.Fprintf(w, "event: connected\ndata: {\"message\": \"Connected to CPRA Dashboard\"}\n\n")
	flusher.Flush()
	
	// Simulate real-time updates every 5 seconds
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			// Send mock monitor update
			update := map[string]interface{}{
				"id":           "monitor-1",
				"name":         "Production API",
				"status":       randomStatus(),
				"uptime":       randomFloat(95.0, 100.0),
				"responseTime": randomInt(100, 500),
			}
			
			data, _ := json.Marshal(update)
			fmt.Fprintf(w, "event: monitor-update\ndata: %s\n\n", data)
			flusher.Flush()
			
			// Send mock stats update
			stats := map[string]interface{}{
				"healthy":     randomInt(5, 10),
				"warning":     randomInt(0, 5),
				"error":       randomInt(0, 3),
				"avgResponse": randomInt(200, 300),
			}
			
			statsData, _ := json.Marshal(stats)
			fmt.Fprintf(w, "event: stats-update\ndata: %s\n\n", statsData)
			flusher.Flush()
			
		case <-r.Context().Done():
			return
		}
	}
}

// Mock data generators

func generateMockMonitors() []Monitor {
	monitors := []Monitor{
		{
			ID:              "monitor-1",
			Name:            "Production API",
			Type:            "http",
			Status:          "healthy",
			Endpoint:        "https://api.prod.example.com/health",
			Uptime:          99.95,
			ResponseTime:    234,
			LastCheck:       "30 seconds ago",
			LastCheckISO:    time.Now().Add(-30 * time.Second).Format(time.RFC3339),
			SparklinePoints: "0,10 10,8 20,12 30,15 40,10 50,8 60,10 70,12 80,9 90,10 100,11",
			SparklineColor:  "#00D563",
		},
		{
			ID:              "monitor-2",
			Name:            "Database Primary",
			Type:            "tcp",
			Status:          "healthy",
			Endpoint:        "db-primary.internal:5432",
			Uptime:          99.99,
			ResponseTime:    45,
			LastCheck:       "15 seconds ago",
			LastCheckISO:    time.Now().Add(-15 * time.Second).Format(time.RFC3339),
			SparklinePoints: "0,5 10,4 20,6 30,5 40,4 50,5 60,6 70,5 80,4 90,5 100,6",
			SparklineColor:  "#00D563",
		},
		{
			ID:              "monitor-3",
			Name:            "Auth Service",
			Type:            "http",
			Status:          "warning",
			Endpoint:        "https://auth.example.com/status",
			Uptime:          98.50,
			ResponseTime:    1250,
			LastCheck:       "1 minute ago",
			LastCheckISO:    time.Now().Add(-1 * time.Minute).Format(time.RFC3339),
			SparklinePoints: "0,10 10,15 20,20 30,18 40,25 50,22 60,20 70,18 80,20 90,22 100,20",
			SparklineColor:  "#FFB020",
		},
		{
			ID:              "monitor-4",
			Name:            "Payment Gateway",
			Type:            "http",
			Status:          "critical",
			Endpoint:        "https://pay.example.com/ping",
			Uptime:          95.20,
			ResponseTime:    3450,
			LastCheck:       "2 minutes ago",
			LastCheckISO:    time.Now().Add(-2 * time.Minute).Format(time.RFC3339),
			SparklinePoints: "0,10 10,15 20,25 30,30 40,35 50,32 60,30 70,35 80,38 90,36 100,35",
			SparklineColor:  "#E50914",
			ErrorMessage:    "Connection timeout after 3000ms",
		},
		{
			ID:              "monitor-5",
			Name:            "CDN Edge Server",
			Type:            "icmp",
			Status:          "healthy",
			Endpoint:        "cdn-edge-01.example.com",
			Uptime:          99.98,
			ResponseTime:    12,
			LastCheck:       "10 seconds ago",
			LastCheckISO:    time.Now().Add(-10 * time.Second).Format(time.RFC3339),
			SparklinePoints: "0,2 10,3 20,2 30,3 40,2 50,3 60,2 70,3 80,2 90,3 100,2",
			SparklineColor:  "#00D563",
		},
		{
			ID:              "monitor-6",
			Name:            "Web Application",
			Type:            "http",
			Status:          "healthy",
			Endpoint:        "https://www.example.com",
			Uptime:          99.92,
			ResponseTime:    567,
			LastCheck:       "45 seconds ago",
			LastCheckISO:    time.Now().Add(-45 * time.Second).Format(time.RFC3339),
			SparklinePoints: "0,12 10,10 20,15 30,13 40,11 50,14 60,12 70,10 80,13 90,11 100,12",
			SparklineColor:  "#00D563",
		},
	}
	
	return monitors
}

func generateMockAlerts() []Alert {
	alerts := []Alert{
		{
			ID:           "alert-1",
			Severity:     "critical",
			Title:        "Payment Gateway Down",
			Message:      "Payment gateway is not responding. Connection timeout after 3000ms.",
			Timestamp:    "2 minutes ago",
			TimestampISO: time.Now().Add(-2 * time.Minute).Format(time.RFC3339),
			Acknowledged: false,
		},
		{
			ID:           "alert-2",
			Severity:     "warning",
			Title:        "Auth Service Degraded",
			Message:      "Auth service response time above threshold (1250ms > 1000ms).",
			Timestamp:    "5 minutes ago",
			TimestampISO: time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
			Acknowledged: false,
		},
		{
			ID:           "alert-3",
			Severity:     "info",
			Title:        "Scheduled Maintenance Complete",
			Message:      "Database maintenance completed successfully. All services restored.",
			Timestamp:    "15 minutes ago",
			TimestampISO: time.Now().Add(-15 * time.Minute).Format(time.RFC3339),
			Acknowledged: true,
		},
	}
	
	return alerts
}

// Helper functions

func randomStatus() string {
	statuses := []string{"healthy", "warning", "critical"}
	return statuses[rand.Intn(len(statuses))]
}

func randomInt(min, max int) int {
	return min + rand.Intn(max-min+1)
}

func randomFloat(min, max float64) float64 {
	return min + rand.Float64()*(max-min)
}
