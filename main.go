package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type CheckResult struct {
	Status       string    `json:"status"`
	Timestamp    time.Time `json:"timestamp"`
	ActualResult []string  `json:"actual_result"`
	Server       string    `json:"server"`
}

type DNSCheck struct {
	Domain      string        `yaml:"domain"`
	Type        string        `yaml:"type"`
	Expected    string        `yaml:"expected"`
	Interval    time.Duration `yaml:"interval"`
	Status      string        `yaml:"-"`
	LastCheck   time.Time     `yaml:"-"`
	History     []CheckResult `json:"-"`
	historyLock sync.RWMutex
}

type Config struct {
	Global struct {
		DNSServer          string        `yaml:"dns_server"`
		SecondaryDNSServer string        `yaml:"secondary_dns_server"`
		DefaultInterval    time.Duration `yaml:"default_interval"`
		LogDir             string        `yaml:"log_dir"`
		Port               string        `yaml:"port"`
	} `yaml:"global"`
	Checks []DNSCheck `yaml:"checks"`
	mu     sync.RWMutex
}

func (c *Config) updateStatus(index int, result CheckResult) {
	c.mu.Lock()
	defer c.mu.Unlock()

	check := &c.Checks[index]
	check.Status = result.Status
	check.LastCheck = result.Timestamp

	// Update history
	check.historyLock.Lock()
	check.History = append(check.History, result)

	// Keep only last 30 days of history
	cutoff := time.Now().AddDate(0, 0, -30)
	var newHistory []CheckResult
	for _, hist := range check.History {
		if hist.Timestamp.After(cutoff) {
			newHistory = append(newHistory, hist)
		}
	}
	check.History = newHistory
	check.historyLock.Unlock()

	// Save to log file
	if c.Global.LogDir != "" {
		go saveCheckToLog(check, c.Global.LogDir)
	}
}

func saveCheckToLog(check *DNSCheck, logDir string) {
	filename := filepath.Join(logDir, fmt.Sprintf("%s-%s.log", check.Domain, check.Type))

	// Create log directory if it doesn't exist
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Printf("Error creating log directory: %v", err)
		return
	}

	// Open log file in append mode
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Error opening log file: %v", err)
		return
	}
	// Changed this line to handle Close() error
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("Error closing log file: %v", err)
		}
	}()

	// Add safety check for empty history
	check.historyLock.RLock()
	if len(check.History) == 0 {
		check.historyLock.RUnlock()
		log.Printf("No history entries to save for %s-%s", check.Domain, check.Type)
		return
	}
	result := check.History[len(check.History)-1]
	check.historyLock.RUnlock()

	logEntry := fmt.Sprintf("%s\t%s\t%s\t%v\n",
		result.Timestamp.Format(time.RFC3339),
		result.Status,
		result.Server,
		strings.Join(result.ActualResult, ","))

	if _, err := f.WriteString(logEntry); err != nil {
		log.Printf("Error writing to log file: %v", err)
	}
}
func loadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("error reading config file: %v", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("error parsing YAML: %v", err)
	}

	if config.Global.DefaultInterval == 0 {
		config.Global.DefaultInterval = 5 * time.Minute
	}
	if config.Global.LogDir == "" {
		config.Global.LogDir = "logs"
	}
	if config.Global.Port == "" {
		config.Global.Port = "8080"
	}

	if !strings.HasPrefix(config.Global.Port, ":") {
		config.Global.Port = ":" + config.Global.Port
	}

	for i := range config.Checks {
		if config.Checks[i].Interval == 0 {
			config.Checks[i].Interval = config.Global.DefaultInterval
		}
		config.Checks[i].Status = "PENDING"
		config.Checks[i].History = make([]CheckResult, 0)

		logFile := filepath.Join(config.Global.LogDir, fmt.Sprintf("%s-%s.log", config.Checks[i].Domain, config.Checks[i].Type))
		if _, err := os.Stat(logFile); err == nil {
			if err := loadHistoryFromLog(&config.Checks[i], logFile); err != nil {
				// Log the error but continue loading config
				log.Printf("Warning: Failed to load history for %s-%s: %v",
					config.Checks[i].Domain, config.Checks[i].Type, err)
			}
		}
	}

	return &config, nil
}

func loadHistoryFromLog(check *DNSCheck, logFile string) error {
	data, err := os.ReadFile(logFile)
	if err != nil {
		return fmt.Errorf("error reading history file %s: %v", logFile, err)
	}

	check.historyLock.Lock()
	defer check.historyLock.Unlock() // Make sure we always unlock

	lines := strings.Split(string(data), "\n")
	cutoff := time.Now().AddDate(0, 0, -30)

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 4 {
			continue
		}

		timestamp, err := time.Parse(time.RFC3339, parts[0])
		if err != nil {
			// Log the error but continue processing other lines
			log.Printf("Error parsing timestamp in log file %s: %v", logFile, err)
			continue
		}

		if timestamp.After(cutoff) {
			check.History = append(check.History, CheckResult{
				Status:       parts[1],
				Server:       parts[2],
				Timestamp:    timestamp,
				ActualResult: strings.Split(parts[3], ","),
			})
		}
	}
	return nil
}

func createResolver(dnsServer string) *net.Resolver {
	if dnsServer == "" {
		return net.DefaultResolver
	}

	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{}
			return d.DialContext(ctx, "udp", dnsServer+":53")
		},
	}
}

func performDNSCheck(check *DNSCheck, resolver *net.Resolver) (string, []string) {
	var records []string

	switch check.Type {
	case "A":
		ips, err := resolver.LookupIP(context.Background(), "ip4", check.Domain)
		if err != nil {
			return fmt.Sprintf("%s-%s-ERROR-%v", check.Domain, check.Type, err), nil
		}
		for _, ip := range ips {
			records = append(records, ip.String())
		}

	case "CNAME":
		cname, err := resolver.LookupCNAME(context.Background(), check.Domain)
		if err != nil {
			return fmt.Sprintf("%s-%s-ERROR-%v", check.Domain, check.Type, err), nil
		}
		records = append(records, cname)

	case "NS":
		ns, err := resolver.LookupNS(context.Background(), check.Domain)
		if err != nil {
			return fmt.Sprintf("%s-%s-ERROR-%v", check.Domain, check.Type, err), nil
		}
		for _, nsRecord := range ns {
			records = append(records, nsRecord.Host)
		}

	case "TXT":
		txtRecords, err := resolver.LookupTXT(context.Background(), check.Domain)
		if err != nil {
			return fmt.Sprintf("%s-%s-ERROR-%v", check.Domain, check.Type, err), nil
		}
		records = append(records, txtRecords...)

	case "MX":
		mxRecords, err := resolver.LookupMX(context.Background(), check.Domain)
		if err != nil {
			return fmt.Sprintf("%s-%s-ERROR-%v", check.Domain, check.Type, err), nil
		}
		for _, mx := range mxRecords {
			records = append(records, mx.Host)
		}

	default:
		return fmt.Sprintf("%s-%s-UNSUPPORTED", check.Domain, check.Type), nil
	}

	// Check if expected value is in records
	for _, record := range records {
		if strings.Contains(strings.ToLower(record), strings.ToLower(check.Expected)) {
			return fmt.Sprintf("%s-%s-PASS", check.Domain, check.Type), records
		}
	}

	return fmt.Sprintf("%s-%s-FAIL", check.Domain, check.Type), records
}

func monitorDNS(config *Config) {
	primaryResolver := createResolver(config.Global.DNSServer)
	var secondaryResolver *net.Resolver
	if config.Global.SecondaryDNSServer != "" {
		secondaryResolver = createResolver(config.Global.SecondaryDNSServer)
	}

	var wg sync.WaitGroup
	for i := range config.Checks {
		i := i // Create new variable for goroutine
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ticker := time.NewTicker(config.Checks[i].Interval)
			defer ticker.Stop()

			for {
				now := time.Now()
				// Check primary DNS server
				status, results := performDNSCheck(&config.Checks[i], primaryResolver) // removed serverName arg
				config.updateStatus(i, CheckResult{
					Status:       status,
					Timestamp:    now,
					ActualResult: results,
					Server:       config.Global.DNSServer, // we still use the server name from config
				})

				// Check secondary DNS server if configured
				if secondaryResolver != nil {
					status, results := performDNSCheck(&config.Checks[i], secondaryResolver) // removed serverName arg
					config.updateStatus(i, CheckResult{
						Status:       status,
						Timestamp:    now,
						ActualResult: results,
						Server:       config.Global.SecondaryDNSServer, // we still use the server name from config
					})
				}
				<-ticker.C
			}
		}(i)
	}
	wg.Wait()
}

const statusPageHTML = `
<!DOCTYPE html>
<html>
<head>
    <title>DNS Monitor Status</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 20px; }
        .status { margin: 20px 0; padding: 15px; border-radius: 4px; box-shadow: 0 1px 3px rgba(0,0,0,0.1); }
        .PASS { background-color: #dff0d8; color: #3c763d; border-left: 5px solid #3c763d; }
        .FAIL { background-color: #f2dede; color: #a94442; border-left: 5px solid #a94442; }
        .ERROR { background-color: #fcf8e3; color: #8a6d3b; border-left: 5px solid #8a6d3b; }
        .PENDING { background-color: #f5f5f5; color: #777; border-left: 5px solid #777; }
        .details { font-size: 0.9em; color: #666; margin: 5px 0; }
        .current-status { margin-top: 10px; font-size: 0.9em; }
        .result-detail { font-family: monospace; margin: 5px 0 5px 20px; padding: 5px; background: rgba(255,255,255,0.5); }
        .check-header { font-size: 1.1em; font-weight: bold; margin-bottom: 10px; }
    </style>
</head>
<body>
    <h1>DNS Monitor Status</h1>
    <p>
        Primary DNS Server: {{.Global.DNSServer}}
        {{if .Global.SecondaryDNSServer}}
        <br>Secondary DNS Server: {{.Global.SecondaryDNSServer}}
        {{end}}
    </p>
    {{range .Checks}}
    <div class="status {{if contains .Status "PASS"}}PASS{{else if contains .Status "FAIL"}}FAIL{{else if contains .Status "ERROR"}}ERROR{{else}}PENDING{{end}}">
        <div class="check-header">
            {{.Domain}} ({{.Type}})
        </div>
        <div class="details">
            Expected: {{.Expected}}<br>
            Check Interval: {{.Interval}}
        </div>
        <div class="current-status">
            <strong>Current Status:</strong>
            {{if .History}}
            {{with (lastCheck .History)}}
            <div class="result-detail">
                Time: {{.Timestamp.Format "2006-01-02 15:04:05"}}<br>
                Status: {{.Status}}<br>
                Server: {{.Server}}
                {{if .ActualResult}}
                <br>Results: {{range .ActualResult}}{{.}} {{end}}
                {{end}}
            </div>
            {{end}}
            {{else}}
            <div class="result-detail">No checks performed yet</div>
            {{end}}
        </div>
    </div>
    {{end}}
</body>
</html>
`

// Helper function to get the most recent check
func lastCheck(history []CheckResult) *CheckResult {
	if len(history) == 0 {
		return nil
	}
	return &history[len(history)-1]
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func main() {
	config, err := loadConfig("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Start DNS monitoring in background
	go monitorDNS(config)

	// Create template for status page
	tmpl := template.Must(template.New("status").Funcs(template.FuncMap{
		"contains":  contains,
		"lastCheck": lastCheck,
	}).Parse(statusPageHTML))

	// Setup HTTP handler
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		config.mu.RLock()
		err := tmpl.Execute(w, config)
		config.mu.RUnlock()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	// Start web server
	log.Printf("Starting server on port %s", config.Global.Port)
	if err := http.ListenAndServe(config.Global.Port, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
