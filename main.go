// main hostlookuper
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/VictoriaMetrics/metrics"
	"github.com/miekg/dns"
	"github.com/peterbourgon/ff/v3"
	"github.com/postfinance/flash"

	"go.uber.org/zap"
)

const (
	dnsDurationName    = "hostlookuper_dns_lookup_duration_seconds"
	dnsLookupTotalName = "hostlookuper_dns_lookup_total"
	dnsErrorsTotalName = "hostlookuper_dns_errors_total"
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

	// "default"
	// "tcp://9.9.9.9"
	// "udp://9.9.9.9"
	// "udp://dns.cluster.local"

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

	for _, host := range hosts {
		look := newLookuper(host, timeout, l)

		go func() {
			look.start(*interval)
		}()
	}

	http.HandleFunc("/metrics", func(w http.ResponseWriter, req *http.Request) {
		metrics.WritePrometheus(w, false)
	})

	l.Infow("starting server",
		"listen", listen,
		"interval", interval,
		"hosts", hostsVal,
		"version", version,
		"commit", commit,
		"date", date,
	)

	srv := &http.Server{
		ReadTimeout:       1 * time.Second,
		WriteTimeout:      1 * time.Second,
		IdleTimeout:       30 * time.Second,
		ReadHeaderTimeout: 2 * time.Second,
		Handler:           http.DefaultServeMux,
	}
	l.Fatal(srv.ListenAndServe())
}

type lookuper struct {
	host string
	l    *zap.SugaredLogger
	c    *dns.Client
}

func newLookuper(host string, timeout *time.Duration, log *zap.SugaredLogger) *lookuper {
	c := dns.Client{
		Net:     "udp",
		Timeout: *timeout,
	}

	return &lookuper{
		host: host,
		l:    log,
		c:    &c,
	}
}

func (l *lookuper) start(interval time.Duration) {
	//nolint:gosec // No need for a cryptographic secure random number since this is only used for a jitter.
	jitter := time.Duration(rand.Float64() * float64(500*time.Millisecond))

	l.l.Infow("start delayed",
		"host", l.host,
		"jitter", jitter,
	)

	time.Sleep(jitter)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		func() {
			l.l.Debugw("lookup host",
				"host", l.host,
			)

			m := new(dns.Msg)
			m.SetQuestion(fmt.Sprintf("%s.", l.host), dns.TypeA)
			msg, rtt, err := l.c.Exchange(m, "9.9.9.9:53")

			if err != nil {
				metrics.GetOrCreateCounter(fmt.Sprintf("%s{host=%q}", dnsLookupTotalName, l.host)).Inc()
				metrics.GetOrCreateCounter(fmt.Sprintf("%s{host=%q}", dnsErrorsTotalName, l.host)).Inc()

				l.l.Errorw("dns lookup failed",
					"host", l.host,
					"time", rtt,
					"err", err,
				)

				return
			}

			metrics.GetOrCreateHistogram(fmt.Sprintf("%s{host=%q}", dnsDurationName, l.host)).Update(rtt.Seconds())
			metrics.GetOrCreateCounter(fmt.Sprintf("%s{host=%q}", dnsLookupTotalName, l.host)).Inc()

			l.l.Infow("lookup result",
				"host", l.host,
				"time", rtt,
				"result length", len(msg.Answer),
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
