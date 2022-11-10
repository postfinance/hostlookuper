package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/peterbourgon/ff/v3"
	"github.com/postfinance/flash"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

const (
	metricsNamespace = "hostlookuper"
)

//nolint:gochecknoglobals // There is no other way than doing so. Values will be set on build.
var (
	version, date string
	commit        = "12345678"
)

func main() {

	fs := flag.NewFlagSet("hostlookuper", flag.ExitOnError)
	var (
		debug    = fs.Bool("debug", false, "enable verbose logging")
		interval = fs.Duration("interval", 5*time.Second, "interval between DNS checks. must be in Go time.ParseDuration format, e.g. 5s or 5m or 1h, etc")
		timeout  = fs.Duration("timeout", 5*time.Second, "maximum timeout for a DNS query. must be in Go time.ParseDuration format, e.g. 5s or 5m or 1h, etc")
		listen   = fs.String("listen", ":9090", "address on which hostlookuper listens. e.g. 0.0.0.0:9090")
		hostsVal = fs.String("hosts", "google.ch,ch.ch", "comma-separated list of hosts against which to perform DNS lookups")
	)

	err := ff.Parse(fs, os.Args[1:], ff.WithEnvVarPrefix("HOSTLOOKUPER"))
	if err != nil {
		fmt.Printf("unable to parse args/envs, exiting. error message: %v", err)

		os.Exit(2)
	}

	rand.Seed(time.Now().UnixNano())

	logger := flash.New(flash.WithoutCaller())
	logger.SetDebug(*debug)
	l := logger.Get()

	var hosts hosts = strings.Split(*hostsVal, ",")
	if err := hosts.isValid(); err != nil {
		l.Fatalw("parsing hosts failed",
			"val", hostsVal,
			"err", err,
		)
	}

	reg := prometheus.NewRegistry()
	errCounter := newErrCounter()
	totalCounter := newTotalCounter()
	latency := newLatency(*timeout)

	reg.MustRegister(errCounter, totalCounter, latency)

	for _, host := range hosts {
		look := newLookuper(host, l, errCounter, totalCounter, latency)

		go func() {
			look.start(*interval, *timeout)
		}()
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	l.Infow("starting server",
		"listen", listen,
		"interval", interval,
		"hosts", hostsVal,
		"version", version,
		"commit", commit,
		"date", date,
	)

	l.Fatal(http.ListenAndServe(*listen, mux))
}

type lookuper struct {
	host         string
	errCounter   *prometheus.CounterVec
	totalCounter *prometheus.CounterVec
	latency      *prometheus.HistogramVec
	l            *zap.SugaredLogger
}

func newLookuper(host string, log *zap.SugaredLogger, errCounter, totalCounter *prometheus.CounterVec,
	latency *prometheus.HistogramVec) *lookuper {
	return &lookuper{
		host:         host,
		l:            log,
		errCounter:   errCounter,
		totalCounter: totalCounter,
		latency:      latency,
	}
}

func (l lookuper) start(interval, timeout time.Duration) {
	//nolint:gosec // No need for a cryptographic secure random number since this is only used for a jitter.
	jitter := time.Duration(rand.Float64() * float64(500*time.Millisecond))

	l.l.Infow("start delayed",
		"host", l.host,
		"jitter", jitter,
	)

	time.Sleep(jitter)

	l.errCounter.WithLabelValues(l.host).Add(0.0)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		func() {
			l.l.Debugw("lookup host",
				"host", l.host,
			)

			start := time.Now()

			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			res, err := net.DefaultResolver.LookupHost(ctx, l.host)

			elapsed := time.Since(start)

			if err != nil {
				l.errCounter.WithLabelValues(l.host).Inc()

				l.l.Errorw("dns lookup failed",
					"host", l.host,
					"time", elapsed,
					"err", err,
				)

				return
			}

			l.latency.WithLabelValues(l.host).Observe(elapsed.Seconds())
			l.totalCounter.WithLabelValues(l.host).Inc()

			l.l.Infow("lookup result",
				"host", l.host,
				"time", elapsed,
				"result length", len(res),
			)
		}()
	}
}

type hosts []string

func (h hosts) isValid() error {
	for _, host := range h {
		if _, err := net.LookupHost(host); err != nil {
			return fmt.Errorf("host %s is not valid: %s", host, err)
		}
	}

	return nil
}

func newLatency(timeout time.Duration) *prometheus.HistogramVec {
	step := 0.5

	// standard buckets to measure normal operation
	buckets := []float64{.005, .01, .025, .05, .1, .25, .5}

	// Take the configured timeout into account to be more precise. Step the timeout seconds (adding +1 to catch measurements
	// for resolutions which are close to timeout) and create a bucket for each step.
	for s := 1.0; s <= timeout.Seconds()+1; s += step {
		buckets = append(buckets, s)
	}

	return prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "hostlookuper",
			Name:      "dns_lookup_duration_seconds",
			Help:      "How long it took for the lookup partitioned by hostname.",
			Buckets:   buckets,
		},
		[]string{"host"},
	)
}

func newErrCounter() *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "dns_lookup_errors",
		Help:      "Total number of dns lookup errors.",
	},
		[]string{"host"},
	)
}

func newTotalCounter() *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "dns_lookup_total",
		Help:      "Total number of dns lookups performed.",
	},
		[]string{"host"},
	)
}
