package main

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config is the full runtime configuration, sourced from the environment that
// /etc/init.d/dola2api exports from /opt/dola2api/dola2api.conf.
//
// Defaults are tuned for the router target (JCG-Q20 / MT7621 / 250MB RAM):
// memory is the primary budget, so concurrency is 1 and video delivery defaults
// to redirect (no local copy, no page cache).
type Config struct {
	Port     int
	APIKeys  []string
	AdminKey string

	Executor     string // protocol | sidecar
	SidecarURL   string
	SidecarToken string

	MaxConcurrency  int
	MaxPendingTasks int
	DailyLimit      int
	CooldownSec     int
	// AccountStrategy picks which session takes the next job:
	// least-used | round-robin | random.
	AccountStrategy string
	// SubmitAttempts bounds how many sessions one task may try before it is
	// reported as failed. 1 disables failover.
	SubmitAttempts int
	// SubmitInterval is the minimum gap between submits. The upstream refuses by
	// submit rate rather than by session, so this is the lever that matters.
	SubmitInterval time.Duration
	// ResetTZ is a fixed-offset location. OpenWrt ships no tzdata, so a named
	// zone that is not in the table below is a configuration error rather than
	// a silent fallback to UTC (the exact bug that shifted "today" by 8h in
	// wb-proxy).
	ResetTZ         *time.Location
	VideoTimeout    time.Duration
	RefVideoTimeout time.Duration

	EgressMode       string // relay | proxy | direct | none
	RelayURL         string
	RelayKey         string
	ProxyURL         string
	NoProxy          []string
	StrictProxy      bool
	EgressRotate     string // none | round-robin | random
	RelayMaxBody     int64
	FirstByteTimeout time.Duration
	SubmitDeadline   time.Duration

	// PollInterval is how often a running task asks for its video.
	PollInterval time.Duration
	// TaskTTL is how long a finished task stays readable before it is dropped,
	// which bounds the task map.
	TaskTTL time.Duration

	StoreMode      string // redirect | pipe | mirror
	MirrorMaxBytes int64
	MirrorTTL      time.Duration
	// MirrorDir holds mirrored videos. It must be on writable storage, and on a
	// router that means somewhere other than the overlay unless there is room.
	MirrorDir string

	// VideoModels is the set of model identifiers GET /v1/models reports and
	// POST /v1/videos accepts. Overridable because the upstream identifier is
	// not something this project can pin down from the outside.
	VideoModels []string
	// ImageModels and ChatModels are the ids GET /v1/models offers for those two
	// kinds. They are this service's own names; a client needs an id to select.
	ImageModels []string
	ChatModels  []string

	// ChatTimeout bounds one text turn. Chat does not go through the task pool,
	// so it needs its own deadline rather than the video one.
	ChatTimeout time.Duration
	// ChatConcurrency bounds simultaneous text turns, separately from
	// MaxConcurrency, which governs generation jobs.
	ChatConcurrency int
	// DeepThinkValue is the wire value for a reasoning request. It is a setting
	// because the value is uncertain: the upstream Python uses 1 and a
	// third-party client uses 3, and neither could be confirmed here.
	DeepThinkValue int

	// ImageAbility is the skill type used for image requests. It is
	// configurable because the value is evidence-based rather than confirmed
	// end to end: the upstream's skill table reports 3 as image generation, and
	// the value the upstream Python used (16) is SkillCodeAssistant, whose
	// branch had no callers. An operator can correct it without a rebuild.
	ImageAbility int

	// AccountsFile is the JSON list of upstream sessions. It is read at start
	// and re-read on SIGHUP, so a session can be added without a restart.
	AccountsFile string
	// DeviceFile holds the stable identifiers the web client sends with every write.
	// They are persisted because a device that presents a new identifier on every
	// request is not a device.
	DeviceFile string
	// SessionWarnDays is how long before a session expires the service starts
	// saying so. Nothing can refresh one: this account type logs in through a
	// browser, so replacing the cookie is a human step.
	SessionWarnDays int

	ReferenceImageMaxBytes int64
	ReferenceImageMaxCount int

	HeapSoftLimit  int64
	MaxConnections int
	MaxHeaderBytes int
	FreeOSAfterJob bool
}

// Addr returns the listen address.
func (c Config) Addr() string { return fmt.Sprintf(":%d", c.Port) }

// EgressActive reports whether any egress path is configured at all. It is a
// configuration-level answer; live health is tracked by the egress manager.
func (c Config) EgressActive() bool { return c.EgressMode != "none" }

// Defaults, spelled once so tests and docs cannot drift apart.
//
// Two of them come from measuring the live relay (spikes/RESULT.md):
//   - defaultRelayMaxBody: a 4MiB body passes through the relay, a 5MiB body is
//     rejected with FUNCTION_PAYLOAD_TOO_LARGE, so 4MiB is the safe ceiling.
//   - defaultFirstByte: the relay answers 504 once the upstream takes ~25s to
//     send its first byte (Vercel's documented Edge rule). Waiting the full 25s
//     would surface the relay's error page instead of our own, so our wait is
//     shorter and a relay 504 is treated as a retryable egress error.
const (
	defaultPort            = 8790
	defaultMaxConcurrency  = 1
	defaultMaxPending      = 20
	defaultDailyLimit      = 2
	defaultCooldownSec     = 1800
	defaultAccountStrategy = "least-used"
	defaultSubmitAttempts  = 3
	// defaultSubmitInterval spaces submits out; see SubmitInterval.
	defaultSubmitInterval  = 20 * time.Second
	defaultResetTZ         = "Asia/Tokyo"
	defaultVideoTimeout    = 300 * time.Second
	defaultRefVideoTimeout = 1800 * time.Second
	defaultEgressMode      = "relay"
	defaultRelayURL        = "https://vcrelay.dmean.dpdns.org/"
	defaultNoProxy         = "*.volces.com,*.byteimg.com,127.0.0.1,192.168.*"
	defaultEgressRotate    = "none"
	defaultPollInterval    = 10 * time.Second
	defaultTaskTTL         = 30 * time.Minute
	defaultRelayMaxBody    = 4 << 20
	defaultFirstByte       = 20 * time.Second
	defaultSubmitDeadline  = 30 * time.Second
	defaultStoreMode       = "redirect"
	// defaultVideoModel is what the upstream submit body carries. The web
	// client's own display tag for it is "Seedance 2.0".
	defaultImageModel = "dola-image"
	defaultChatModel  = "dola-chat"
	defaultVideoModel = "seedance_v2.0"
	// defaultMirrorDir is on tmpfs so a mirror never fills the overlay.
	defaultMirrorDir = "/tmp/dola2api-mirror"
	// defaultAccountsFile lives under the install root the init script uses, so
	// the operator edits one file next to the binary's config.
	defaultAccountsFile = "/opt/dola2api/accounts.json"
	// defaultSessionWarnDays is a week: long enough to replace a cookie without
	// rushing, short enough that the reminder is not routine noise.
	defaultSessionWarnDays = 7
	// defaultChatTimeout is short because a chat client is waiting and has its
	// own timeout, usually a minute or two.
	defaultChatTimeout = 60 * time.Second
	// defaultChatConcurrency is separate from MaxConcurrency: chat holds no task
	// slot, so a slow turn cannot block a generation.
	defaultChatConcurrency = 2
	// defaultDeepThinkValue is the value the upstream Python sends for a
	// reasoning request.
	defaultDeepThinkValue = 1
	defaultMirrorMaxBytes = 32 << 20
	defaultMirrorTTLHours = 24
	defaultRefImageBytes  = 4 << 20
	defaultRefImageCount  = 30
	defaultHeapSoftLimit  = 32 << 20
	defaultMaxConnections = 32
	defaultMaxHeaderBytes = 32 << 10
)

// fixedZoneMinutes maps the timezone names an operator may plausibly set to
// their fixed UTC offset in minutes. Half-hour zones are expressed in minutes.
var fixedZoneMinutes = map[string]int{
	"UTC":            0,
	"Asia/Tokyo":     9 * 60,
	"Asia/Seoul":     9 * 60,
	"Asia/Shanghai":  8 * 60,
	"Asia/Hong_Kong": 8 * 60,
	"Asia/Singapore": 8 * 60,
	"Asia/Taipei":    8 * 60,
	"Asia/Bangkok":   7 * 60,
	"Asia/Jakarta":   7 * 60,
	"Asia/Kolkata":   5*60 + 30,
}

// LoadConfig reads configuration from getenv (os.Getenv in production, a map
// lookup in tests). Any value that is present but malformed is an error: a
// silently-defaulted port or timezone is worse than a service that refuses to
// start, because procd restarts it and the logread entry names the problem.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{}

	var err error
	if cfg.Port, err = envInt(getenv, "DOLA_PORT", defaultPort); err != nil {
		return Config{}, err
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return Config{}, fmt.Errorf("DOLA_PORT out of range: %d", cfg.Port)
	}

	cfg.APIKeys = envList(getenv, "DOLA_API_KEYS")
	cfg.AdminKey = strings.TrimSpace(getenv("DOLA_ADMIN_KEY"))

	if cfg.Executor, err = envChoice(getenv, "DOLA_EXECUTOR", "protocol", "protocol", "sidecar"); err != nil {
		return Config{}, err
	}
	cfg.SidecarURL = strings.TrimSpace(getenv("DOLA_SIDECAR_URL"))
	cfg.SidecarToken = strings.TrimSpace(getenv("DOLA_SIDECAR_TOKEN"))
	if cfg.Executor == "sidecar" && cfg.SidecarURL == "" {
		return Config{}, errors.New("DOLA_EXECUTOR=sidecar requires DOLA_SIDECAR_URL")
	}

	if cfg.MaxConcurrency, err = envInt(getenv, "DOLA_MAX_CONCURRENCY", defaultMaxConcurrency); err != nil {
		return Config{}, err
	}
	if cfg.MaxConcurrency < 1 {
		return Config{}, fmt.Errorf("DOLA_MAX_CONCURRENCY must be >= 1, got %d", cfg.MaxConcurrency)
	}
	if cfg.MaxPendingTasks, err = envInt(getenv, "DOLA_MAX_PENDING_TASKS", defaultMaxPending); err != nil {
		return Config{}, err
	}
	if cfg.MaxPendingTasks < 1 {
		return Config{}, fmt.Errorf("DOLA_MAX_PENDING_TASKS must be >= 1, got %d", cfg.MaxPendingTasks)
	}
	if cfg.DailyLimit, err = envInt(getenv, "DOLA_DAILY_LIMIT", defaultDailyLimit); err != nil {
		return Config{}, err
	}
	if cfg.CooldownSec, err = envInt(getenv, "DOLA_COOLDOWN_SEC", defaultCooldownSec); err != nil {
		return Config{}, err
	}
	if cfg.AccountStrategy, err = envChoice(getenv, "DOLA_ACCOUNT_STRATEGY", defaultAccountStrategy,
		"least-used", "round-robin", "random"); err != nil {
		return Config{}, err
	}
	if cfg.SubmitAttempts, err = envInt(getenv, "DOLA_SUBMIT_ATTEMPTS", defaultSubmitAttempts); err != nil {
		return Config{}, err
	}
	if cfg.SubmitAttempts < 1 {
		return Config{}, fmt.Errorf("DOLA_SUBMIT_ATTEMPTS must be >= 1, got %d", cfg.SubmitAttempts)
	}
	if cfg.SubmitInterval, err = envDuration(getenv, "DOLA_SUBMIT_INTERVAL", defaultSubmitInterval); err != nil {
		return Config{}, err
	}
	// Zero is meaningful: pace() reads it as "do not pace". Rejecting it would
	// make the documented way to turn pacing off a startup failure.
	if cfg.SubmitInterval < 0 {
		return Config{}, fmt.Errorf("DOLA_SUBMIT_INTERVAL must be >= 0, got %s", cfg.SubmitInterval)
	}
	if cfg.ResetTZ, err = parseFixedZone(envOr(getenv, "DOLA_LIMIT_RESET_TZ", defaultResetTZ)); err != nil {
		return Config{}, err
	}
	if cfg.VideoTimeout, err = envDuration(getenv, "DOLA_VIDEO_TIMEOUT", defaultVideoTimeout); err != nil {
		return Config{}, err
	}
	if cfg.RefVideoTimeout, err = envDuration(getenv, "DOLA_REFERENCE_VIDEO_TIMEOUT", defaultRefVideoTimeout); err != nil {
		return Config{}, err
	}

	if cfg.EgressMode, err = envChoice(getenv, "DOLA_EGRESS", defaultEgressMode, "relay", "proxy", "direct", "none"); err != nil {
		return Config{}, err
	}
	cfg.RelayURL = strings.TrimSpace(getenv("DOLA_RELAY_URL"))
	if cfg.RelayURL == "" && cfg.EgressMode == "relay" {
		cfg.RelayURL = defaultRelayURL
	}
	// A comma-separated list is allowed so DOLA_EGRESS_ROTATE has something to
	// rotate over; each entry is validated on its own.
	if cfg.RelayURL != "" {
		for _, entry := range strings.Split(cfg.RelayURL, ",") {
			if err := requireHTTPURL("DOLA_RELAY_URL", strings.TrimSpace(entry)); err != nil {
				return Config{}, err
			}
		}
	}
	cfg.RelayKey = strings.TrimSpace(getenv("DOLA_RELAY_KEY"))
	cfg.ProxyURL = strings.TrimSpace(getenv("DOLA_PROXY_URL"))
	if cfg.ProxyURL != "" {
		if err := requireHTTPURL("DOLA_PROXY_URL", cfg.ProxyURL); err != nil {
			return Config{}, err
		}
	}
	// Both configured and no explicit mode: refuse rather than pick one, since
	// the two paths differ in whether the router's real IP can leak upstream.
	if getenv("DOLA_EGRESS") == "" && cfg.RelayURL != "" && cfg.ProxyURL != "" {
		return Config{}, errors.New("both DOLA_RELAY_URL and DOLA_PROXY_URL are set; set DOLA_EGRESS=relay or =proxy explicitly")
	}
	if cfg.StrictProxy, err = envBool01(getenv, "DOLA_STRICT_PROXY", true); err != nil {
		return Config{}, err
	}
	if cfg.EgressRotate, err = envChoice(getenv, "DOLA_EGRESS_ROTATE", defaultEgressRotate, "none", "round-robin", "random"); err != nil {
		return Config{}, err
	}
	if cfg.RelayMaxBody, err = envInt64(getenv, "DOLA_RELAY_MAX_BODY", defaultRelayMaxBody); err != nil {
		return Config{}, err
	}
	cfg.NoProxy = envList(getenv, "DOLA_NO_PROXY")
	if len(cfg.NoProxy) == 0 {
		cfg.NoProxy = strings.Split(defaultNoProxy, ",")
	}
	if cfg.FirstByteTimeout, err = envDuration(getenv, "DOLA_EGRESS_FIRST_BYTE_TIMEOUT", defaultFirstByte); err != nil {
		return Config{}, err
	}
	if cfg.SubmitDeadline, err = envDuration(getenv, "DOLA_SUBMIT_DEADLINE", defaultSubmitDeadline); err != nil {
		return Config{}, err
	}
	if cfg.PollInterval, err = envDuration(getenv, "DOLA_POLL_INTERVAL", defaultPollInterval); err != nil {
		return Config{}, err
	}
	if cfg.PollInterval <= 0 {
		return Config{}, fmt.Errorf("DOLA_POLL_INTERVAL must be positive, got %s", cfg.PollInterval)
	}
	if cfg.TaskTTL, err = envDuration(getenv, "DOLA_TASK_TTL", defaultTaskTTL); err != nil {
		return Config{}, err
	}

	if cfg.StoreMode, err = envChoice(getenv, "DOLA_STORE_MODE", defaultStoreMode, "redirect", "pipe", "mirror"); err != nil {
		return Config{}, err
	}
	if cfg.MirrorMaxBytes, err = envInt64(getenv, "DOLA_MIRROR_MAX_BYTES", defaultMirrorMaxBytes); err != nil {
		return Config{}, err
	}
	mirrorTTLHours, err := envInt(getenv, "DOLA_MIRROR_TTL_HOURS", defaultMirrorTTLHours)
	if err != nil {
		return Config{}, err
	}
	cfg.MirrorTTL = time.Duration(mirrorTTLHours) * time.Hour
	cfg.MirrorDir = strings.TrimSpace(getenv("DOLA_MIRROR_DIR"))
	if cfg.MirrorDir == "" {
		cfg.MirrorDir = defaultMirrorDir
	}

	cfg.AccountsFile = strings.TrimSpace(getenv("DOLA_ACCOUNTS_FILE"))
	if cfg.AccountsFile == "" {
		cfg.AccountsFile = defaultAccountsFile
	}
	// Default the device file next to the account file, so both live where the
	// operator already looks.
	cfg.DeviceFile = strings.TrimSpace(getenv("DOLA_DEVICE_FILE"))
	if cfg.DeviceFile == "" {
		cfg.DeviceFile = filepath.Join(filepath.Dir(cfg.AccountsFile), "device.json")
	}
	if cfg.SessionWarnDays, err = envInt(getenv, "DOLA_SESSION_WARN_DAYS", defaultSessionWarnDays); err != nil {
		return Config{}, err
	}
	if cfg.SessionWarnDays < 0 {
		return Config{}, fmt.Errorf("DOLA_SESSION_WARN_DAYS must be >= 0, got %d", cfg.SessionWarnDays)
	}
	if cfg.ImageAbility, err = envInt(getenv, "DOLA_IMAGE_ABILITY", skillImage); err != nil {
		return Config{}, err
	}
	if cfg.ImageAbility < 0 {
		return Config{}, fmt.Errorf("DOLA_IMAGE_ABILITY must be >= 0, got %d", cfg.ImageAbility)
	}
	if cfg.ChatTimeout, err = envDuration(getenv, "DOLA_CHAT_TIMEOUT", defaultChatTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ChatTimeout <= 0 {
		return Config{}, fmt.Errorf("DOLA_CHAT_TIMEOUT must be positive, got %s", cfg.ChatTimeout)
	}
	if cfg.ChatConcurrency, err = envInt(getenv, "DOLA_CHAT_CONCURRENCY", defaultChatConcurrency); err != nil {
		return Config{}, err
	}
	if cfg.ChatConcurrency < 1 {
		return Config{}, fmt.Errorf("DOLA_CHAT_CONCURRENCY must be >= 1, got %d", cfg.ChatConcurrency)
	}
	if cfg.DeepThinkValue, err = envInt(getenv, "DOLA_DEEP_THINK_VALUE", defaultDeepThinkValue); err != nil {
		return Config{}, err
	}

	cfg.VideoModels = envList(getenv, "DOLA_VIDEO_MODELS")
	if len(cfg.VideoModels) == 0 {
		cfg.VideoModels = []string{defaultVideoModel}
	}
	cfg.ImageModels = envList(getenv, "DOLA_IMAGE_MODELS")
	if len(cfg.ImageModels) == 0 {
		cfg.ImageModels = []string{defaultImageModel}
	}
	cfg.ChatModels = envList(getenv, "DOLA_CHAT_MODELS")
	if len(cfg.ChatModels) == 0 {
		cfg.ChatModels = []string{defaultChatModel}
	}

	if cfg.ReferenceImageMaxBytes, err = envInt64(getenv, "DOLA_REFERENCE_IMAGE_MAX_BYTES", defaultRefImageBytes); err != nil {
		return Config{}, err
	}
	if cfg.ReferenceImageMaxCount, err = envInt(getenv, "DOLA_REFERENCE_IMAGE_MAX_COUNT", defaultRefImageCount); err != nil {
		return Config{}, err
	}

	if cfg.HeapSoftLimit, err = envInt64(getenv, "DOLA_HEAP_SOFT_LIMIT", defaultHeapSoftLimit); err != nil {
		return Config{}, err
	}
	if cfg.MaxConnections, err = envInt(getenv, "DOLA_MAX_CONNECTIONS", defaultMaxConnections); err != nil {
		return Config{}, err
	}
	if cfg.MaxConnections < 1 {
		return Config{}, fmt.Errorf("DOLA_MAX_CONNECTIONS must be >= 1, got %d", cfg.MaxConnections)
	}
	if cfg.MaxHeaderBytes, err = envInt(getenv, "DOLA_MAX_HEADER_BYTES", defaultMaxHeaderBytes); err != nil {
		return Config{}, err
	}
	if cfg.FreeOSAfterJob, err = envBool01(getenv, "DOLA_FREE_OS_AFTER_JOB", true); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// parseFixedZone accepts a name from fixedZoneMinutes or an explicit offset
// such as +09:00, -0500, or +8. Unknown names are rejected with the supported
// list, so a typo fails loudly at start instead of shifting the daily reset.
func parseFixedZone(raw string) (*time.Location, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("DOLA_LIMIT_RESET_TZ is empty")
	}
	if mins, ok := fixedZoneMinutes[raw]; ok {
		return time.FixedZone(raw, mins*60), nil
	}
	if mins, ok := parseOffsetMinutes(raw); ok {
		return time.FixedZone(raw, mins*60), nil
	}
	names := make([]string, 0, len(fixedZoneMinutes))
	for name := range fixedZoneMinutes {
		names = append(names, name)
	}
	sort.Strings(names)
	return nil, fmt.Errorf("DOLA_LIMIT_RESET_TZ %q is not a fixed offset this build knows (no tzdata on the router); supported: %s, or an explicit offset like +09:00",
		raw, strings.Join(names, ", "))
}

// parseOffsetMinutes parses +09:00, -0500, +8, -5:30 style offsets.
func parseOffsetMinutes(raw string) (int, bool) {
	if len(raw) < 2 || (raw[0] != '+' && raw[0] != '-') {
		return 0, false
	}
	sign := 1
	if raw[0] == '-' {
		sign = -1
	}
	body := strings.ReplaceAll(raw[1:], ":", "")
	// Allow H, HH, HMM, HHMM.
	var hours, minutes int
	switch len(body) {
	case 1, 2:
		h, err := strconv.Atoi(body)
		if err != nil {
			return 0, false
		}
		hours = h
	case 3:
		h, err := strconv.Atoi(body[:1])
		if err != nil {
			return 0, false
		}
		m, err := strconv.Atoi(body[1:])
		if err != nil {
			return 0, false
		}
		hours, minutes = h, m
	case 4:
		h, err := strconv.Atoi(body[:2])
		if err != nil {
			return 0, false
		}
		m, err := strconv.Atoi(body[2:])
		if err != nil {
			return 0, false
		}
		hours, minutes = h, m
	default:
		return 0, false
	}
	if hours > 14 || minutes > 59 {
		return 0, false
	}
	return sign * (hours*60 + minutes), true
}

func envOr(getenv func(string) string, key, fallback string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return fallback
}

// envList splits a comma-separated value, trimming whitespace and dropping
// empty entries so a trailing comma is not an error.
func envList(getenv func(string) string, key string) []string {
	raw := getenv(key)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func envInt(getenv func(string) string, key string, fallback int) (int, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", key, raw)
	}
	return n, nil
}

func envInt64(getenv func(string) string, key string, fallback int64) (int64, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", key, raw)
	}
	return n, nil
}

func envDuration(getenv func(string) string, key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	// Accept bare seconds as well as Go duration strings.
	if secs, err := strconv.Atoi(raw); err == nil {
		if secs <= 0 {
			return 0, fmt.Errorf("%s must be positive, got %q", key, raw)
		}
		return time.Duration(secs) * time.Second, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration (e.g. 25s) or seconds, got %q", key, raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %q", key, raw)
	}
	return d, nil
}

// envBool01 accepts only "0" or "1": a typo such as "yes" must not silently
// enable or disable a security-relevant switch like strict proxy mode.
func envBool01(getenv func(string) string, key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	switch raw {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be 0 or 1, got %q", key, raw)
	}
}

func envChoice(getenv func(string) string, key, fallback string, allowed ...string) (string, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	for _, a := range allowed {
		if raw == a {
			return raw, nil
		}
	}
	return "", fmt.Errorf("%s must be one of %s, got %q", key, strings.Join(allowed, "|"), raw)
}

func requireHTTPURL(key, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s is not a URL: %v", key, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s must be http(s), got scheme %q", key, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%s must include a host, got %q", key, raw)
	}
	return nil
}
