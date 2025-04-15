package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/alecthomas/kingpin/v2"
	porkbun "github.com/nikoraes/external-dns-porkbun-webhook/provider"
	"github.com/oklog/run"
	"github.com/prometheus/client_golang/prometheus"
	cversion "github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"
	webhook "sigs.k8s.io/external-dns/provider/webhook/api"
)

var (
	listenAddr        = kingpin.Flag("listen-address", "The address this plugin listens on").Default(":8888").Envar("LISTEN_ADDRESS").String()
	metricsListenAddr = kingpin.Flag("metrics-listen-address", "The address this plugin provides metrics on").Default(":8889").Envar("METRICS_LISTEN_ADDRESS").String()
	tlsConfig         = kingpin.Flag("tls-config", "Path to TLS config file.").Envar("TLS_CONFIG").Default("").String()

	domainFilter = kingpin.Flag("domain-filter", "Limit possible target zones by a domain suffix; specify multiple times for multiple domains").Required().Envar("DOMAIN_FILTER").Strings()
	dryRun       = kingpin.Flag("dry-run", "Run without connecting to Porkbun's API").Default("false").Envar("DRY_RUN").Bool()
	apiKey       = kingpin.Flag("api-key", "The api key to connect to Porkbun's API").Required().Envar("API_KEY").String()
	apiSecret    = kingpin.Flag("api-secret", "The api password to connect to Porkbun's API").Required().Envar("API_SECRET").String()
)

func main() {
	promslogConfig := &promslog.Config{}
	flag.AddFlags(kingpin.CommandLine, promslogConfig)
	kingpin.Version(version.Info())
	kingpin.Parse()

	// Ensure the logger respects the GO_LOG environment variable
	if os.Getenv("GO_LOG") != "" {
		level := new(promslog.Level)
		_ = level.Set(os.Getenv("GO_LOG"))
		promslogConfig.Level = level
	}

	var logger = promslog.New(promslogConfig)
	logger.Info("starting external-dns Porkbun webhook plugin", "version", version.Version, "revision", version.Revision)
	logger.Debug("configuration", "cdomain-filter", fmt.Sprintf("%s", *domainFilter), "api-key", *apiKey, "api-secret", *apiSecret)

	prometheus.DefaultRegisterer.MustRegister(cversion.NewCollector("external_dns_netcup"))

	metricsMux := buildMetricsServer(prometheus.DefaultGatherer, logger)
	metricsServer := http.Server{
		Handler:           metricsMux,
		ReadHeaderTimeout: 5 * time.Second}

	metricsFlags := web.FlagConfig{
		WebListenAddresses: &[]string{*metricsListenAddr},
		WebSystemdSocket:   new(bool),
		WebConfigFile:      tlsConfig,
	}

	webhookMux, err := buildWebhookServer(logger)
	if err != nil {
		logger.Error("Failed to create provider", "error", err.Error())
		os.Exit(1)
	}
	webhookServer := http.Server{
		Handler:           webhookMux,
		ReadHeaderTimeout: 5 * time.Second}

	webhookFlags := web.FlagConfig{
		WebListenAddresses: &[]string{*listenAddr},
		WebSystemdSocket:   new(bool),
		WebConfigFile:      tlsConfig,
	}

	var g run.Group

	// Run Metrics server
	{
		g.Add(func() error {
			logger.Info("Started external-dns-porkbun-webhook metrics server", "address", metricsListenAddr)
			return web.ListenAndServe(&metricsServer, &metricsFlags, logger)
		}, func(error) {
			ctxShutDown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = metricsServer.Shutdown(ctxShutDown)
		})
	}
	// Run webhook API server
	{
		g.Add(func() error {
			logger.Info("Started external-dns-porkbun-webhook webhook server", "address", listenAddr)
			return web.ListenAndServe(&webhookServer, &webhookFlags, logger)
		}, func(error) {
			ctxShutDown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = webhookServer.Shutdown(ctxShutDown)
		})
	}

	if err := g.Run(); err != nil {
		logger.Error("run server group error", "error", err.Error())
		os.Exit(1)
	}

}

func buildMetricsServer(registry prometheus.Gatherer, logger *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()

	var metricsPath = "/metrics"
	var rootPath = "/"

	// Add metricsPath
	mux.Handle(metricsPath, promhttp.HandlerFor(
		registry,
		promhttp.HandlerOpts{
			EnableOpenMetrics: true,
		}))

	// Add index
	landingConfig := web.LandingConfig{
		Name:        "external-dns-porkbun-webhook",
		Description: "external-dns webhook provider for Porkbun",
		Version:     version.Info(),
		Links: []web.LandingLinks{
			{
				Address: metricsPath,
				Text:    "Metrics",
			},
		},
	}
	landingPage, err := web.NewLandingPage(landingConfig)
	if err != nil {
		logger.Error("failed to create landing page", "error", err.Error())
	}
	mux.Handle(rootPath, landingPage)

	return mux
}

func buildWebhookServer(logger *slog.Logger) (*http.ServeMux, error) {
	mux := http.NewServeMux()

	var rootPath = "/"
	var healthzPath = "/healthz"
	var recordsPath = "/records"
	var adjustEndpointsPath = "/adjustendpoints"

	pbProvider, err := porkbun.NewPorkbunProvider(domainFilter, *apiKey, *apiSecret, *dryRun, logger)
	if err != nil {
		return nil, err
	}

	p := webhook.WebhookServer{
		Provider: pbProvider,
	}

	// Add healthzPath
	mux.HandleFunc(healthzPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(http.StatusText(http.StatusOK)))
	})

	// Add negotiatePath
	mux.HandleFunc(rootPath, p.NegotiateHandler)
	// Add adjustEndpointsPath
	mux.HandleFunc(adjustEndpointsPath, p.AdjustEndpointsHandler)
	// Add recordsPath
	mux.HandleFunc(recordsPath, p.RecordsHandler)

	return mux, nil
}
