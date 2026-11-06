package config

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"time"
)

type Config struct {
	Port              int
	MaxUploadBytes    int64
	MaxFiles          int
	MaxConcurrentJobs int
	JobTimeout        time.Duration
	MaxImagePixels    int64
}

func Load() (Config, error) {
	var errs []error
	read := func(name string, def, min, max int) int {
		raw := os.Getenv(name)
		if raw == "" {
			return def
		}
		v, err := strconv.Atoi(raw)
		if err != nil || v < min || v > max {
			errs = append(errs, fmt.Errorf("%s must be an integer between %d and %d, got %q", name, min, max, raw))
			return def
		}
		return v
	}

	cfg := Config{
		Port:              read("PORT", 8080, 1, 65535),
		MaxUploadBytes:    int64(read("MAX_UPLOAD_MB", 100, 1, 10240)) << 20,
		MaxFiles:          read("MAX_FILES", 50, 2, 1000),
		MaxConcurrentJobs: read("MAX_CONCURRENT_JOBS", runtime.NumCPU(), 1, 1024),
		JobTimeout:        time.Duration(read("JOB_TIMEOUT_SECONDS", 60, 1, 3600)) * time.Second,
		MaxImagePixels:    int64(read("MAX_IMAGE_MEGAPIXELS", 100, 1, 2000)) * 1_000_000,
	}
	if len(errs) > 0 {
		return Config{}, errs[0]
	}
	return cfg, nil
}
