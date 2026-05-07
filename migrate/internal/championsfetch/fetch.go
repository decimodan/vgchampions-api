package championsfetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/decimodan/vgchampions-api/migrate/internal/redisx"
)

// ExpandURL replaces {id} in template with escaped id (como encodeURIComponent).
func ExpandURL(template, id string) (string, error) {
	template = strings.TrimSpace(template)
	if !strings.Contains(template, "{id}") {
		return "", fmt.Errorf("la URL debe contener el marcador literal {id}")
	}
	return strings.ReplaceAll(template, "{id}", url.QueryEscape(id)), nil
}

// HTTPOptions controla el cliente Champions (véase fetch-tournament-data.mjs).
type HTTPOptions struct {
	Headers        http.Header
	TimeoutMs      int
	MaxRetries     int
	Min429WaitMS   int
	Max429WaitMS   int
	GapBetweenMS   int // entre GET details y standings
	Concurrency    int
	DelayBetweenMS int // mismo worker antes del siguiente trabajo
	NoProgress     bool
}

func defaultHTTPOpts() HTTPOptions {
	return HTTPOptions{
		TimeoutMs:      getenvIntEnv("CHAMPIONS_TIMEOUT_MS", 60000),
		MaxRetries:     getenvIntEnv("CHAMPIONS_MAX_RETRIES", 12),
		Min429WaitMS:   getenvIntEnv("CHAMPIONS_429_MIN_WAIT_MS", 10000),
		Max429WaitMS:   getenvIntEnv("CHAMPIONS_429_MAX_WAIT_MS", 300000),
		GapBetweenMS:   getenvIntEnv("CHAMPIONS_GAP_MS_BETWEEN_REQUESTS", 0),
		Concurrency:    maxInt(1, getenvIntEnv("CHAMPIONS_CONCURRENCY", 1)),
		DelayBetweenMS: getenvIntEnv("CHAMPIONS_DELAY_MS", 0),
	}
}

func getenvIntEnv(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ParseFetchHeaders parsea CHAMPIONS_FETCH_HEADERS_JSON.
func ParseFetchHeaders() (http.Header, error) {
	raw := strings.TrimSpace(os.Getenv("CHAMPIONS_FETCH_HEADERS_JSON"))
	if raw == "" {
		return nil, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("CHAMPIONS_FETCH_HEADERS_JSON: %w", err)
	}
	h := http.Header{}
	for k, v := range m {
		k = strings.TrimSpace(k)
		if k != "" && v != "" {
			h.Add(k, v)
		}
	}
	return h, nil
}

// FetchRedisStats cuentas de escritura Redis durante sync.
type FetchRedisStats struct {
	FetchedDetailSets   int
	FetchedStandingSets int
	SkippedWhole        int
	Failed              int
}

// ResultToRedis descarga champions API → Redis prefix:tournament:{id}:{details|standings}.
type ResultToRedis struct {
	Client            *redis.Client
	Prefix            string
	DetailsTemplate   string
	StandingsTemplate string
	DryURLsOnly       bool // solo imprimir URLs sin HTTP
	SkipExisting      bool
	Force             bool
	TTLSec            int
	Opts              HTTPOptions
	ErrListKey        string
	Prog              func(cur, total int, label string)
}

// Run procesa cada id concurrentemente según Concurrencidad.
func (r *ResultToRedis) Run(ctx context.Context, ids []string) (FetchRedisStats, error) {
	var out FetchRedisStats
	if len(ids) == 0 {
		return out, nil
	}
	cleanIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			cleanIDs = append(cleanIDs, id)
		}
	}
	if len(cleanIDs) == 0 {
		return out, nil
	}
	ids = cleanIDs

	detailsTpl := strings.TrimSpace(r.DetailsTemplate)
	stTpl := strings.TrimSpace(r.StandingsTemplate)

	if detailsTpl == "" || stTpl == "" {
		return out, fmt.Errorf(
			"CHAMPIONS_DETAILS_URL_TEMPLATE y CHAMPIONS_STANDINGS_URL_TEMPLATE (con {id}) son obligatorias",
		)
	}

	conc := maxInt(1, r.Opts.Concurrency)
	delay := r.Opts.DelayBetweenMS

	jobs := make(chan string, len(ids))
	for _, id := range ids {
		jobs <- id
	}
	close(jobs)

	var done atomic.Uint32
	all := uint32(len(ids))
	var wg sync.WaitGroup

	var mu sync.Mutex // stats + errors

	workerFn := func() {
		jobIdx := 0
		for id := range jobs {
			if delay > 0 && jobIdx > 0 {
				time.Sleep(time.Duration(delay) * time.Millisecond)
			}
			jobIdx++

			skipped, detailWrote, standingsWrote, err := r.processOne(ctx, id, detailsTpl, stTpl)

			mu.Lock()
			switch {
			case skipped:
				out.SkippedWhole++
			case err != nil:
				out.Failed++
				if r.ErrListKey != "" && r.Client != nil {
					payload := fmt.Sprintf(`{"ts":%q,"id":%q,"message":%q}`,
						time.Now().UTC().Format(time.RFC3339Nano), id, err.Error())
					_ = r.Client.RPush(ctx, r.ErrListKey, payload).Err()
				}
			default:
				if detailWrote {
					out.FetchedDetailSets++
				}
				if standingsWrote {
					out.FetchedStandingSets++
				}
			}
			mu.Unlock()

			cur := done.Add(1)
			if r.Prog != nil {
				r.Prog(int(cur), int(all), "Fetch API→Redis")
			}
		}
	}

	for w := 0; w < conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workerFn()
		}()
	}
	wg.Wait()
	if r.Prog != nil && all > 0 {
		r.Prog(int(all), int(all), "Fetch API→Redis")
	}
	return out, nil
}

func (r *ResultToRedis) processOne(ctx context.Context, id, detailsTpl, stTpl string) (skippedWhole, wroteDetails, wroteStandings bool, err error) {
	kd := redisx.DetailsKey(r.Prefix, id)
	ks := redisx.StandingsKey(r.Prefix, id)

	detailsURL, err := ExpandURL(detailsTpl, id)
	if err != nil {
		return false, false, false, err
	}
	stURL, err := ExpandURL(stTpl, id)
	if err != nil {
		return false, false, false, err
	}

	if r.DryURLsOnly {
		fmt.Fprintf(os.Stdout, "dry-run sync %s\n  %s\n  %s\n", id, detailsURL, stURL)
		return false, false, false, nil
	}
	if r.Client == nil {
		return false, false, false, fmt.Errorf("Redis no configurado para fetch")
	}

	if r.SkipExisting && !r.Force {
		exD, e1 := r.Client.Exists(ctx, kd).Result()
		exS, e2 := r.Client.Exists(ctx, ks).Result()
		if e1 != nil {
			return false, false, false, e1
		}
		if e2 != nil {
			return false, false, false, e2
		}
		if exD == 1 && exS == 1 {
			return true, false, false, nil
		}
	}

	needDetails := r.Force
	needStandings := r.Force
	if !r.Force {
		nD, err := r.Client.Exists(ctx, kd).Result()
		if err != nil {
			return false, false, false, err
		}
		nS, err := r.Client.Exists(ctx, ks).Result()
		if err != nil {
			return false, false, false, err
		}
		needDetails = needDetails || nD == 0
		needStandings = needStandings || nS == 0
	}

	if !needDetails && !needStandings {
		return true, false, false, nil
	}

	hdr := cloneHeader(r.Opts.Headers)
	if hdr.Get("Accept") == "" {
		hdr.Set("Accept", "application/json")
	}

	var ttl time.Duration
	if r.TTLSec > 0 {
		ttl = time.Duration(r.TTLSec) * time.Second
	}

	if needDetails {
		body, err := fetchGETWithRetry(ctx, detailsURL, hdr, r.Opts)
		if err != nil {
			return false, false, false, err
		}
		bodyTrim := bytes.TrimSpace(body)
		if len(bodyTrim) > 0 && !json.Valid(bodyTrim) {
			return false, false, false, fmt.Errorf("details: respuesta no es JSON")
		}
		val := strings.TrimRight(string(bodyTrim), "\n")
		if err := r.Client.Set(ctx, kd, val, ttl).Err(); err != nil {
			return false, false, false, err
		}
		wroteDetails = true
	}

	if needDetails && needStandings && r.Opts.GapBetweenMS > 0 {
		select {
		case <-ctx.Done():
			return false, wroteDetails, false, ctx.Err()
		case <-time.After(time.Duration(r.Opts.GapBetweenMS) * time.Millisecond):
		}
	}

	if needStandings {
		body, err := fetchGETWithRetry(ctx, stURL, hdr, r.Opts)
		if err != nil {
			return false, wroteDetails, false, err
		}
		bodyTrim := bytes.TrimSpace(body)
		if len(bodyTrim) > 0 && !json.Valid(bodyTrim) {
			return false, wroteDetails, false, fmt.Errorf("standings: respuesta no es JSON")
		}
		val := strings.TrimRight(string(bodyTrim), "\n")
		if err := r.Client.Set(ctx, ks, val, ttl).Err(); err != nil {
			return false, wroteDetails, false, err
		}
		wroteStandings = true
	}

	return false, wroteDetails, wroteStandings, nil
}

func cloneHeader(h http.Header) http.Header {
	if h == nil {
		return http.Header{}
	}
	out := http.Header{}
	for k, vv := range h {
		out[k] = append([]string(nil), vv...)
	}
	return out
}

func fetchGETWithRetry(ctx context.Context, reqURL string, hdr http.Header, opt HTTPOptions) ([]byte, error) {
	timeout := opt.TimeoutMs
	if timeout <= 0 {
		timeout = 60000
	}
	cli := &http.Client{Timeout: time.Duration(timeout) * time.Millisecond}
	max := opt.MaxRetries
	if max <= 0 {
		max = 12
	}
	min429 := opt.Min429WaitMS
	if min429 <= 0 {
		min429 = 10000
	}
	max429 := opt.Max429WaitMS
	if max429 <= 0 {
		max429 = 300000
	}

	var lastErr error
	for attempt := range max {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		for k, vals := range hdr {
			for _, v := range vals {
				req.Header.Add(k, v)
			}
		}

		res, err := cli.Do(req)
		if err != nil {
			lastErr = err
			sleepBackoff(attempt)
			continue
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()

		if res.StatusCode >= 200 && res.StatusCode < 300 {
			return body, nil
		}

		retryAfterMs := parseRetryAfterDuration(res.Header.Get("Retry-After"))
		lastErr = fmt.Errorf("HTTP %d %s: %s", res.StatusCode, res.Status, trim500(body))

		if res.StatusCode == 429 || res.StatusCode >= 500 {
			wait := calc429Wait(attempt, min429, max429, retryAfterMs)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
				continue
			}
		}
		return nil, lastErr
	}
	return nil, lastErr
}

func calc429Wait(attempt, min429Ms, max429Ms int, retryAfter time.Duration) time.Duration {
	expoMs := float64(min429Ms) * math.Pow(2, float64(attempt))
	useMs := expoMs
	if retryAfter > 0 {
		ram := float64(retryAfter.Milliseconds())
		if ram > useMs {
			useMs = ram
		}
	}
	if max429Ms > 0 && useMs > float64(max429Ms) {
		useMs = float64(max429Ms)
	}
	return time.Duration(math.Round(useMs)) * time.Millisecond
}

func trim500(b []byte) string {
	s := string(b)
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return s
}

func parseRetryAfterDuration(h string) time.Duration {
	if h == "" {
		return 0
	}
	sec, err := strconv.Atoi(strings.TrimSpace(h))
	if err != nil || sec < 0 {
		return 0
	}
	return time.Duration(sec) * time.Second
}

func sleepBackoff(attempt int) {
	if attempt <= 0 {
		time.Sleep(time.Second)
		return
	}
	shift := attempt
	if shift > 15 {
		shift = 15
	}
	ms := uint64(1000) << shift
	if ms > 30000 {
		ms = 30000
	}
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

// EnvFetch builds ResultToRedis desde variables de entorno (plantillas pueden ir vacías en dry sólo estadísticas previas sin HTTP).
func EnvFetch(detailsTpl, standingsTpl string, ttlSec int, dryURLsOnly bool) (*ResultToRedis, error) {
	h, err := ParseFetchHeaders()
	if err != nil {
		return nil, err
	}
	opt := defaultHTTPOpts()
	opt.Headers = h
	opt.NoProgress = false
	r := &ResultToRedis{
		DetailsTemplate:   detailsTpl,
		StandingsTemplate: standingsTpl,
		DryURLsOnly:       dryURLsOnly,
		TTLSec:            ttlSec,
		Opts:              opt,
	}
	return r, nil
}

func redisErrorsKey(prefix string) string {
	return fmt.Sprintf("%s:fetch-errors", prefix)
}

// RedisErrorsKey exporta nombre de lista de errores (RPUSH).
func RedisErrorsKey(prefix string) string { return redisErrorsKey(prefix) }
