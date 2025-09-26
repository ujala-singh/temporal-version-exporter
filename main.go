package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	v1 "go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"
)

var (
	temporalAddr = flag.String("temporal-addr", getEnv("TEMPORAL_ADDR", "127.0.0.1:7236"), "Temporal frontend gRPC address")
	listenAddr   = flag.String("listen-addr", getEnv("LISTEN_ADDR", ":9090"), "metrics listen address")
	scrapeInt    = flag.Duration("scrape-interval", getEnvDuration("SCRAPE_INTERVAL", 30*time.Second), "how often to refresh version")
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
	}
	return fallback
}

var (
	versionGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "temporal_server_version_info",
			Help: "Temporal server version as a label (value will be 1). Label 'version' has the textual server version.",
		},
		[]string{"address", "version"},
	)
	unknownGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "temporal_server_version_unknown",
			Help: "Set to 1 if exporter could not determine version.",
		},
		[]string{"address"},
	)
)

func init() {
	prometheus.MustRegister(versionGauge)
	prometheus.MustRegister(unknownGauge)
}

func main() {
	flag.Parse()

	http.Handle("/metrics", promhttp.Handler())
	go func() {
		log.Printf("starting metrics server on %s\n", *listenAddr)
		if err := http.ListenAndServe(*listenAddr, nil); err != nil {
			log.Fatalf("metrics http server failed: %v", err)
		}
	}()

	for {
		if err := refresh(*temporalAddr); err != nil {
			log.Printf("refresh error: %v", err)
		}
		time.Sleep(*scrapeInt)
	}
}

func refresh(addr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, addr, grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		markUnknown(addr)
		return fmt.Errorf("grpc dial: %w", err)
	}
	defer conn.Close()

	client := v1.NewWorkflowServiceClient(conn)

	// Try GetSystemInfo (preferred); fallback to GetClusterInfo
	var version string

	sysResp, err := client.GetSystemInfo(ctx, &v1.GetSystemInfoRequest{})
	if err == nil && sysResp != nil {
		// Inspect the proto for likely fields. Different versions may expose different fields.
		// We'll try some common getters; otherwise fall back to string.
		version = extractVersionFromSystemInfo(sysResp.String())
	}

	if version == "" {
		clusResp, err2 := client.GetClusterInfo(ctx, &v1.GetClusterInfoRequest{})
		if err2 == nil && clusResp != nil {
			version = extractVersionFromClusterInfo(clusResp.String())
		}
	}

	// reset previous metrics for this address
	versionGauge.DeleteLabelValues(addr, "") // best-effort cleanup

	if version == "" {
		markUnknown(addr)
		log.Printf("version not found in responses")
		return nil
	}

	unknownGauge.DeleteLabelValues(addr)
	versionGauge.WithLabelValues(addr, version).Set(1)
	log.Printf("detected temporal version=%s at %s", version, addr)
	return nil
}

func markUnknown(addr string) {
	unknownGauge.WithLabelValues(addr).Set(1)
}

// very small best-effort version extraction; adapt to your environment
func extractVersionFromSystemInfo(s string) string {
	// Try to find tokens like "version: " or "build_version:" or "server_version:"
	for _, key := range []string{"server_version", "build_version", "version", "component_version"} {
		if v := scanAfterKey(s, key); v != "" {
			return v
		}
	}
	// last-resort: attempt to find a semver-like token
	parts := strings.Fields(s)
	for _, p := range parts {
		if looksLikeSemver(p) {
			return p
		}
	}
	return ""
}
func extractVersionFromClusterInfo(s string) string { return extractVersionFromSystemInfo(s) }

func scanAfterKey(s, key string) string {
	idx := strings.Index(strings.ToLower(s), strings.ToLower(key))
	if idx < 0 {
		return ""
	}
	rest := s[idx:]
	// naive split by non-alnum/dot/dash
	for _, token := range strings.FieldsFunc(rest, func(r rune) bool {
		return !(r == '.' || r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'))
	}) {
		if strings.Contains(strings.ToLower(token), strings.ToLower(key)) {
			continue
		}
		if looksLikeSemver(token) {
			return token
		}
		// if token contains digits and dots, return it (best-effort)
		if strings.IndexAny(token, "0123456789") >= 0 && strings.Contains(token, ".") {
			return token
		}
	}
	return ""
}

func looksLikeSemver(s string) bool {
	// super simple check: x.y.z or x.y
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	return true
}
