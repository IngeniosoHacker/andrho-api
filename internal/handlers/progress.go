package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/IngeniosoHacker/andrho-api/internal/progress"
)

const (
	progressCacheKey   = "cache:progress"
	progressStaleKey   = "cache:progress:stale"
	progressFreshFor   = 10 * time.Minute
	progressStaleFor   = 7 * 24 * time.Hour
	progressRecentSize = 10
)

// Only one request at a time refreshes from GitHub; the rest wait and read
// what it cached.
var progressFetch sync.Mutex

// In-process fallback when Redis isn't configured (local dev).
var progressMemo struct {
	sync.Mutex
	data []byte
	at   time.Time
}

// Progress handles GET /progress: AndRho's repos merged into one public
// timeline (see internal/progress). Served from Redis for progressFreshFor;
// when GitHub fails, the last good copy is served instead of an error.
func (h *Handler) Progress(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()

	serveCached := func() bool {
		cached := h.progressCached(ctx, progressCacheKey)
		if cached != nil {
			c.Header("Cache-Control", "public, max-age=300")
			c.Data(http.StatusOK, "application/json; charset=utf-8", cached)
		}
		return cached != nil
	}
	if serveCached() {
		return
	}
	progressFetch.Lock()
	defer progressFetch.Unlock()
	if serveCached() {
		return
	}

	p, err := progress.NewClient(h.Cfg.GitHubToken).Fetch(ctx, h.Cfg.GitHubRepos, progressRecentSize)
	if err != nil {
		log.Printf("progress: %v", err)
		if stale := h.progressCached(ctx, progressStaleKey); stale != nil {
			c.Data(http.StatusOK, "application/json; charset=utf-8", stale)
			return
		}
		respondError(c, http.StatusServiceUnavailable, "progress unavailable")
		return
	}

	body, _ := json.Marshal(p)
	h.progressStore(ctx, body)
	c.Header("Cache-Control", "public, max-age=300")
	c.Data(http.StatusOK, "application/json; charset=utf-8", body)
}

func (h *Handler) progressCached(ctx context.Context, key string) []byte {
	if h.Redis != nil {
		if b, err := h.Redis.Get(ctx, key).Bytes(); err == nil {
			return b
		}
		return nil
	}
	progressMemo.Lock()
	defer progressMemo.Unlock()
	if progressMemo.data == nil || (key == progressCacheKey && time.Since(progressMemo.at) > progressFreshFor) {
		return nil
	}
	return progressMemo.data
}

func (h *Handler) progressStore(ctx context.Context, body []byte) {
	if h.Redis != nil {
		h.Redis.Set(ctx, progressCacheKey, body, progressFreshFor)
		h.Redis.Set(ctx, progressStaleKey, body, progressStaleFor)
		return
	}
	progressMemo.Lock()
	progressMemo.data, progressMemo.at = body, time.Now()
	progressMemo.Unlock()
}
