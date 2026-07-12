package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/client"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm/llmhttp"
	"shelley.exe.dev/models"
	"shelley.exe.dev/modelsources"
	"shelley.exe.dev/server"
	_ "shelley.exe.dev/server/notifications/channels" // register channel types
	"shelley.exe.dev/skills"
	"shelley.exe.dev/templates"
	"shelley.exe.dev/version"
)

type GlobalConfig struct {
	DBPath                string
	Debug                 bool
	Model                 string
	PredictableOnly       bool
	ConfigPath            string
	DefaultModel          string
	DisableLLMIntegration bool
	DisableGateway        bool
}

var discoverLLMIntegrations = modelsources.DiscoverLLMIntegrations

func main() {
	// Define global flags
	var global GlobalConfig
	defaultModelID := models.Default().ID
	flag.StringVar(&global.DBPath, "db", "shelley.db", "Path to SQLite database file")
	flag.BoolVar(&global.Debug, "debug", false, "Enable debug logging")
	flag.StringVar(&global.Model, "model", defaultModelID, "LLM model to use (use 'predictable' for testing)")
	flag.BoolVar(&global.PredictableOnly, "predictable-only", false, "Use only the predictable service, ignoring all other models")
	flag.StringVar(&global.ConfigPath, "config", "", "Path to shelley.json configuration file (optional)")
	flag.StringVar(&global.DefaultModel, "default-model", defaultModelID, "Default model for web UI")
	flag.BoolVar(&global.DisableLLMIntegration, "disable-llm-integration", false, "Ignore any discovered exe.dev llm integration")
	flag.BoolVar(&global.DisableGateway, "disable-gateway", false, "Ignore llm_gateway from shelley.json")

	// Custom usage function
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [global-flags] <command> [command-flags]\n\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "Global flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "\nCommands:\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  serve [flags]                 Start the web server\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  models [flags]                List the models the server would expose, without starting it\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  client [flags] <subcommand>   CLI client (chat, read, list, archive) (experimental)\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  skill <cat|ls|new> [name]     Read, list, or create skills\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  dtach <new|attach> ...        Persistent PTY sessions over a Unix socket\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  unpack-template <name> <dir>  Unpack a project template to a directory\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  version                       Print version information as JSON\n")
		fmt.Fprintf(flag.CommandLine.Output(), "\nUse '%s <command> -h' for command-specific help\n", os.Args[0])
	}

	// Parse all flags first
	flag.Parse()
	args := flag.Args()

	if len(args) == 0 {
		flag.Usage()
		os.Exit(1)
	}

	command := args[0]
	switch command {
	case "serve":
		runServe(global, args[1:])
	case "models":
		runModels(global, args[1:])
	case "client":
		client.Run(args[1:])
	case "skill":
		runSkill(args[1:])
	case "dtach":
		runDtach(args[1:])
	case "unpack-template":
		runUnpackTemplate(args[1:])
	case "version":
		runVersion()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		flag.Usage()
		os.Exit(1)
	}
}

func runSkill(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: shelley skill <cat|ls|new> [name]\n")
		os.Exit(1)
	}

	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	switch args[0] {
	case "cat":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: shelley skill cat SKILL_NAME\n")
			os.Exit(1)
		}
		content, err := skills.FindByName(args[1], wd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(content)

	case "ls":
		all := skills.ListAll(wd, "")
		for _, s := range all {
			desc := s.Description
			if s.When != "" {
				desc = "[when: " + s.When + "] " + desc
			}
			fmt.Printf("%s\t%s\n", s.Name, desc)
		}

	case "new":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: shelley skill new SKILL_NAME\n")
			os.Exit(1)
		}
		path, err := skills.CreateTemplate(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(path)

	default:
		fmt.Fprintf(os.Stderr, "Unknown skill subcommand: %s\nUsage: shelley skill <cat|ls|new> [name]\n", args[0])
		os.Exit(1)
	}
}

func runServe(global GlobalConfig, args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.String("port", "9000", "Port to listen on")
	portFile := fs.String("port-file", "", "Write the actual listening port to this file (useful with --port 0)")
	systemdActivation := fs.Bool("systemd-activation", false, "Use systemd socket activation (listen on fd from systemd)")
	requireHeader := fs.String("require-header", "", "Require this header on all API requests (e.g., X-Exedev-Userid)")
	socketPath := fs.String("socket", client.DefaultSocketPath(), "Path to Unix socket for local CLI client access (set to 'none' to disable)")
	banner := fs.String("banner", "", "If set, shows this text in a banner at the top of the UI (useful for marking demo instances)")
	fs.Parse(args)

	logger := setupLogging(global.Debug)

	database := setupDatabase(global.DBPath, logger)
	defer database.Close()

	// Set the database path for system prompt generation
	server.DBPath = global.DBPath

	// Build LLM configuration
	llmConfig := buildLLMConfig(global, logger, database)

	// Initialize LLM service manager (includes custom model support via database)
	llmManager := server.NewLLMServiceManager(llmConfig)

	// Log available models
	availableModels := llmManager.GetAvailableModels()
	logger.Info("Available models", "models", strings.Join(availableModels, ", "))

	toolSetConfig := setupToolSetConfig(llmManager, llmManager)

	// Create server
	svr := server.NewServer(database, llmManager, toolSetConfig, logger, global.PredictableOnly, llmConfig.DefaultModel, *requireHeader)
	svr.SetModelRefresher(llmConfig.RefreshBuiltModels)
	svr.Banner = *banner

	// Load notification channels from DB.
	svr.ReloadNotificationChannels()

	// Resolve socket path: "none" disables the Unix socket listener
	effectiveSocket := *socketPath
	if effectiveSocket == "none" {
		effectiveSocket = ""
	}

	var err error
	if *systemdActivation {
		listener, listenerErr := systemdListener()
		if listenerErr != nil {
			logger.Error("Failed to get systemd listener", "error", listenerErr)
			os.Exit(1)
		}
		logger.Info("Using systemd socket activation")
		err = svr.StartWithListeners(listener, effectiveSocket)
	} else {
		listener, listenerErr := net.Listen("tcp", ":"+*port)
		if listenerErr != nil {
			logger.Error("Failed to create listener", "error", listenerErr)
			os.Exit(1)
		}
		if *portFile != "" {
			actualPort := listener.Addr().(*net.TCPAddr).Port
			if writeErr := os.WriteFile(*portFile, []byte(fmt.Sprintf("%d\n", actualPort)), 0o644); writeErr != nil {
				logger.Error("Failed to write port file", "path", *portFile, "error", writeErr)
				os.Exit(1)
			}
		}
		err = svr.StartWithListeners(listener, effectiveSocket)
	}

	if err != nil {
		logger.Error("Server failed", "error", err)
		os.Exit(1)
	}
}

func setupLogging(debug bool) *slog.Logger {
	logLevel := slog.LevelInfo
	if debug {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)
	return logger
}

func setupDatabase(dbPath string, logger *slog.Logger) *db.DB {
	database, err := db.New(db.Config{DSN: dbPath})
	if err != nil {
		logger.Error("Failed to initialize database", "error", err)
		os.Exit(1)
	}

	// Run database migrations
	if err := database.Migrate(context.Background()); err != nil {
		logger.Error("Failed to run database migrations", "error", err)
		os.Exit(1)
	}
	logger.Debug("Database migrations completed successfully")

	// Truncate the WAL at startup. The -wal file can grow large during
	// normal operation and a PASSIVE auto-checkpoint never shrinks it.
	if err := database.Checkpoint(context.Background()); err != nil {
		logger.Warn("Failed to checkpoint WAL at startup", "error", err)
	}

	// agent_working is runtime-only state. If the previous process exited
	// while a loop was running, the column can be left TRUE for one or more
	// conversations. Clear them so the conversation list reflects reality.
	if err := database.ResetAllAgentWorking(context.Background()); err != nil {
		logger.Error("Failed to reset agent_working state", "error", err)
		os.Exit(1)
	}
	return database
}

// runUnpackTemplate unpacks a project template to a directory
func runUnpackTemplate(args []string) {
	fs := flag.NewFlagSet("unpack-template", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: shelley unpack-template <template-name> <directory>\n\n")
		fmt.Fprintf(fs.Output(), "Unpacks a project template to the specified directory.\n\n")
		fmt.Fprintf(fs.Output(), "Available templates:\n")
		names, err := templates.List()
		if err != nil {
			fmt.Fprintf(fs.Output(), "  (error listing templates: %v)\n", err)
		} else if len(names) == 0 {
			fmt.Fprintf(fs.Output(), "  (no templates available)\n")
		} else {
			for _, name := range names {
				fmt.Fprintf(fs.Output(), "  %s\n", name)
			}
		}
	}
	fs.Parse(args)

	if fs.NArg() < 2 {
		fs.Usage()
		os.Exit(1)
	}

	templateName := fs.Arg(0)
	destDir := fs.Arg(1)

	// Verify template exists
	names, err := templates.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing templates: %v\n", err)
		os.Exit(1)
	}
	found := false
	for _, name := range names {
		if name == templateName {
			found = true
			break
		}
	}
	if !found {
		fmt.Fprintf(os.Stderr, "Error: template %q not found\n", templateName)
		fmt.Fprintf(os.Stderr, "Available templates: %s\n", strings.Join(names, ", "))
		os.Exit(1)
	}

	// Create destination directory if it doesn't exist
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory %q: %v\n", destDir, err)
		os.Exit(1)
	}

	// Unpack the template
	if err := templates.Unpack(templateName, destDir); err != nil {
		fmt.Fprintf(os.Stderr, "Error unpacking template: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Template %q unpacked to %s\n", templateName, destDir)
}

// runVersion prints version information as JSON
func runVersion() {
	info := version.GetInfo()
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(info); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding version: %v\n", err)
		os.Exit(1)
	}
}

func setupToolSetConfig(llmProvider claudetool.LLMServiceProvider, llmManager server.LLMProvider) claudetool.ToolSetConfig {
	wd, err := os.Getwd()
	if err != nil {
		// Fallback to "/" if we can't get working directory
		wd = "/"
	}

	// Resolve the list of available models lazily, each time a ToolSet is
	// built. This lets newly-added custom models become visible to subagents
	// (and llm_one_shot) without restarting the server. See issue #195.
	buildAvailableModels := func() []claudetool.AvailableModel {
		var out []claudetool.AvailableModel
		for _, id := range llmManager.GetAvailableModels() {
			am := claudetool.AvailableModel{ID: id}
			if info := llmManager.GetModelInfo(id); info != nil && info.DisplayName != "" && info.DisplayName != id {
				am.DisplayName = info.DisplayName
			}
			out = append(out, am)
		}
		return out
	}

	return claudetool.ToolSetConfig{
		WorkingDir:           wd,
		LLMProvider:          llmProvider,
		EnableJITInstall:     claudetool.EnableBashToolJITInstall,
		EnableBrowser:        true,
		BuildAvailableModels: buildAvailableModels,
	}
}

// buildLLMConfig composes the set of built-in models the server should
// expose. Sources are evaluated in order; the first to claim a model ID
// wins:
//
//  1. Each discovered exe.dev "llm" integration (sorted by name; when
//     2+, subsequent integrations get an "@<name>" suffix on their
//     model IDs so the union of all served models is visible).
//  2. The exe.dev gateway from shelley.json's llm_gateway, if set and no
//     exe.dev LLM integration was discovered via reflection. Any non-empty
//     provider env var overrides the gateway's implicit credential for
//     that provider (legacy behavior).
//  3. Provider env vars (ANTHROPIC_API_KEY, ...) when no gateway is set.
//  4. Predictable (always available).
//
// Custom DB-backed models load on top of the returned set.
func buildLLMConfig(global GlobalConfig, logger *slog.Logger, database *db.DB) *server.LLMConfig {
	defaultModel, sources := buildLLMModelSources(context.Background(), global, logger)

	httpc := llmhttp.NewClient(nil)
	return &server.LLMConfig{
		Models:       modelsources.Build(models.All(), sources, httpc, logger),
		DefaultModel: defaultModel,
		DB:           database,
		HTTPC:        httpc,
		RefreshBuiltModels: func(ctx context.Context) ([]models.Built, error) {
			_, sources := buildLLMModelSources(ctx, global, logger)
			return modelsources.Build(models.All(), sources, httpc, logger), nil
		},
		Logger: logger,
	}
}

func buildLLMModelSources(ctx context.Context, global GlobalConfig, logger *slog.Logger) (string, []modelsources.Source) {
	configPath := global.ConfigPath
	defaultModel := global.DefaultModel
	anthropicKey := os.Getenv("ANTHROPIC_API_KEY")
	openAIKey := os.Getenv("OPENAI_API_KEY")
	geminiKey := os.Getenv("GEMINI_API_KEY")
	fireworksKey := os.Getenv("FIREWORKS_API_KEY")
	xaiKey := os.Getenv("XAI_API_KEY")

	var sources []modelsources.Source

	// 1. exe.dev LLM integrations.
	var integs []*modelsources.LLMIntegrationConfig
	llmIntegrationFound := false
	if !global.DisableLLMIntegration {
		discovered := discoverLLMIntegrations(ctx, nil, logger)
		llmIntegrationFound = discovered.Found
		integs = discovered.Integrations
	}
	for i, integ := range integs {
		suffix := ""
		if len(integs) > 1 && i > 0 {
			suffix = "@" + integ.Name
		}
		sources = append(sources, modelsources.LLMIntegration(integ, suffix))
	}

	var gateway string
	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err == nil {
			var cfg struct {
				LLMGateway   string `json:"llm_gateway"`
				DefaultModel string `json:"default_model"`
			}
			if err := json.Unmarshal(data, &cfg); err != nil {
				logger.Warn("Failed to parse config file", "path", configPath, "error", err)
			} else {
				gateway = strings.TrimSuffix(cfg.LLMGateway, "/")
				if cfg.DefaultModel != "" && defaultModel == "" {
					defaultModel = cfg.DefaultModel
					logger.Info("Using default model from config", "model", cfg.DefaultModel)
				}
			}
		} else if !os.IsNotExist(err) {
			logger.Warn("Failed to read config file", "path", configPath, "error", err)
		}
	}

	if global.DisableGateway {
		gateway = ""
	}

	// 2. Gateway (Anthropic, OpenAI, Fireworks, xAI). Per-provider env vars
	// override the gateway's implicit credential for those providers.
	if gateway != "" && llmIntegrationFound {
		logger.Info("Skipping LLM gateway because an exe.dev LLM integration was discovered")
		if geminiKey != "" || xaiKey != "" {
			sources = append(sources, modelsources.Env("", "", geminiKey, "", xaiKey))
		}
	} else if gateway != "" {
		logger.Info("Using LLM gateway", "gateway", gateway)
		sources = append(sources, modelsources.Gateway(gateway, anthropicKey, openAIKey, fireworksKey, xaiKey))
		// 2b. Gemini is not served by the gateway; let GEMINI_API_KEY,
		// when set, supply Gemini models alongside the gateway.
		if geminiKey != "" {
			sources = append(sources, modelsources.Env("", "", geminiKey, "", ""))
		}
	} else if anthropicKey != "" || openAIKey != "" || geminiKey != "" || fireworksKey != "" || xaiKey != "" {
		// 3. Env vars.
		sources = append(sources, modelsources.Env(anthropicKey, openAIKey, geminiKey, fireworksKey, xaiKey))
	}

	// 4. Predictable always available.
	sources = append(sources, modelsources.Predictable())
	return defaultModel, sources
}

// runModels prints the materialized list of built-in models the server
// would expose, without starting the server. Useful for confirming that
// integrations/gateway/env-var precedence and discovery are configured
// correctly. Does NOT include custom (DB-backed) models.
func runModels(global GlobalConfig, args []string) {
	fs := flag.NewFlagSet("models", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: shelley [global-flags] models\n\n")
		fmt.Fprintf(fs.Output(), "Prints the built-in models Shelley would expose with the current\n")
		fmt.Fprintf(fs.Output(), "configuration (--config, env vars), without starting the server.\n")
	}
	fs.Parse(args)
	if fs.NArg() > 0 {
		fs.Usage()
		os.Exit(2)
	}

	logger := setupLogging(global.Debug)
	llmCfg := buildLLMConfig(global, logger, nil)

	defaultID := llmCfg.DefaultModel
	if defaultID == "" {
		defaultID = models.Default().ID
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPROVIDER\tAPI TYPE\tBASE URL\tSOURCE\tDEFAULT")
	for _, m := range llmCfg.Models {
		mark := ""
		if m.ID == defaultID {
			mark = "*"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", m.ID, m.Provider, m.APIType, m.BaseURL, m.Source, mark)
	}
	tw.Flush()
	fmt.Printf("\n%d models\n", len(llmCfg.Models))
}

// systemdListener returns a net.Listener from systemd socket activation.
// Systemd passes file descriptors starting at fd 3, with LISTEN_FDS indicating the count.
func systemdListener() (net.Listener, error) {
	// Check LISTEN_PID matches our PID (optional but recommended)
	pidStr := os.Getenv("LISTEN_PID")
	if pidStr != "" {
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			return nil, fmt.Errorf("invalid LISTEN_PID: %w", err)
		}
		if pid != os.Getpid() {
			return nil, fmt.Errorf("LISTEN_PID %d does not match current PID %d", pid, os.Getpid())
		}
	}

	// Get the number of file descriptors passed
	fdsStr := os.Getenv("LISTEN_FDS")
	if fdsStr == "" {
		return nil, fmt.Errorf("LISTEN_FDS not set; not running under systemd socket activation")
	}
	nfds, err := strconv.Atoi(fdsStr)
	if err != nil {
		return nil, fmt.Errorf("invalid LISTEN_FDS: %w", err)
	}
	if nfds < 1 {
		return nil, fmt.Errorf("LISTEN_FDS=%d; expected at least 1", nfds)
	}

	// Systemd passes file descriptors starting at fd 3
	const listenFDsStart = 3
	fd := listenFDsStart

	// Create a file from the descriptor
	f := os.NewFile(uintptr(fd), "systemd-socket")
	if f == nil {
		return nil, fmt.Errorf("failed to create file from fd %d", fd)
	}

	// Create a listener from the file
	listener, err := net.FileListener(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to create listener from fd %d: %w", fd, err)
	}

	// Close the original file; the listener now owns the descriptor
	f.Close()

	return listener, nil
}
